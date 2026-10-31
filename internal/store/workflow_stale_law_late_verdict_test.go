package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestStaleLawHoldsLateVerdictUntilRePin proves a stale contract
// pin strands a late verdict behind the staleness boundary: an item past its
// verification step with a missing verdict admits the late record_verdict
// recovery only through the boundary, so a verdict never records under a
// retained pin to a law already deleted before the owning start. The recovery
// refuses on the owning transaction and on the admission inspection, the
// accepted successor law re-pins through the
// ordinary stale recovery, and the held receipt association records under the
// re-pinned contract afterwards. Research's complete step owns the late recovery.
func TestStaleLawHoldsLateVerdictUntilRePin(t *testing.T) {
	const workID = "stale-law-late-verdict"
	// The replacement predates this item's owning start. Its retained contract
	// still pins the deleted law, so this tests stale-pin recovery, not attribution
	// of a post-start deletion to an uncompleted peer. The committed cutover
	// retires spec:one and enacts spec:two before the initializer reads the
	// law folder, so the owning start inventory carries the successor law.
	fixture := seedWorkflowReturnRouteFixtureWithSource(t, workID, currentFixtureDefinition(t, "workflow.research"), "complete", []string{"verification"}, []string{"verification", "review", "artifact"}, func(s *Store) {
		home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
		removeCommittedFixtureLaw(t, home, "spec:one")
		commitFixtureLawFile(t, home, "spec:two", fixtureLawFolderLaw("spec:two"))
	})
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	var startBaseline string
	if err := s.DatabaseForTesting().QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&startBaseline); err != nil {
		t.Fatal(err)
	}
	var baseline workflowLawBaseline
	if err := json.Unmarshal([]byte(startBaseline), &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Laws) != 1 || baseline.Laws[0].LawID != "project/spec:two" {
		t.Fatalf("owning start inventory=%+v, want the already-delivered successor law", baseline.Laws)
	}
	if got := currentStep(t, s, workID); got != "complete" {
		t.Fatalf("fixture step = %q, want complete", got)
	}
	f := workContextFixture{store: s, workID: workID, owner: owner}
	pack := seedResearchContextPack(t, f, "repin-research-proof")
	bindResearchPack(t, f, pack.PackID, 1, true)
	// The selection's child Domain carries no product-wide rationale.
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
	if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
		t.Fatal(err)
	}
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 {
		t.Fatalf("selected research receipt: %+v err=%v", view, err)
	}
	// Retain the historical deleted pin without changing the current source or
	// the owning-start inventory. Both action surfaces must refuse that pin.
	attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
	execInFold(t, s,
		`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id='`+workID+`' AND contract_version=1`,
	)

	// The current declaration accepts criterion-to-receipt associations only.
	lateVerdict := func(contractVersion int64, receipt string) json.RawMessage {
		return mustJSONValue(map[string]any{
			"contract_version": contractVersion,
			"verdicts":         []map[string]any{{"predicate_id": "predicate:return-route", "receipt_refs": []string{receipt}}},
		})
	}
	// The non-worker coordinator associates the research producer's receipt.
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/late-verdict-judge", SessionRef: "session/" + workID + "-judge", ActorClass: ActorAgent}

	err = runVerdictActionAs(t, s, workID, "record_verdict", lateVerdict(1, view.ResearchFindings[0].ViewID), 0, reviewer)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStaleLawRevision || failure.Detail != "workflow contract pins a deleted law" {
		t.Fatalf("late record_verdict under a stale pin error=%v, want the stale-law refusal naming the deleted revision", err)
	}
	if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: workID, ActionID: "record_verdict", Payload: lateVerdict(1, view.ResearchFindings[0].ViewID), Actor: owner}); !errors.As(err, &failure) || failure.Kind != KindStaleLawRevision {
		t.Fatalf("admission inspection error=%v, want the stale-law refusal", err)
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Fatalf("refused late verdict recorded %d verdicts, want 0", recorded)
	}

	// The ordinary stale-law re-pin: the successor mandates the accepted
	// successor law and carries no Domain write authority.
	successor := mustJSONValue(map[string]any{
		"contract_version": 2, "predecessor_contract_versions": []int64{1},
		"decision_domains": []string{},
		"premise":          "re-pin the approved contract to the accepted successor law",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "outcome",
			"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{"spec:two"}, "law_modifies": []string{},
		"rigor_class":      "prototype_internal",
		"supersede_reason": "the approved contract pins a deleted law",
		"audit_evidence":   []string{"evidence:law-rescan"},
	})
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", successor, 0, owner); err != nil {
		t.Fatalf("successor-law supersede: %v", err)
	}
	if version, active := activeContractState(t, s, workID); version != 2 || active != 1 {
		t.Fatalf("active contracts after re-pin = version %d count %d, want exactly version 2", version, active)
	}
	if got := currentStep(t, s, workID); got != "complete" {
		t.Fatalf("step after re-pin = %q, want the held complete step", got)
	}
	// The successor's mandate law binds the way every mandate binds: one
	// durable evidence reference naming the law.
	version := readWorkVersion(t, s, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	seedWorkflowAuthority(t, s, "law-rescan-"+workID, workID, owner.PrincipalRef, "request/law-rescan-"+workID, []string{"spec:two"})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("law-rescan-binding-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "approval",
		"immutable_subject_ref": "spec:two", "producer_id": owner.PrincipalRef, "producer_run_ref": "law-rescan-" + workID,
		"producer_watermark": "request/law-rescan-" + workID, "observed_at": "2026-10-11T00:00:00Z",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}

	// A late receipt association records under the successor without advancing
	// the held completion step.
	view, err = readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 {
		t.Fatalf("selected research receipt: %+v err=%v", view, err)
	}
	if err := workflowCriterionReceipt(context.Background(), s.DatabaseForTesting(), workID, "predicate:return-route", view.ResearchFindings[0].ViewID, ""); err != nil {
		t.Fatalf("re-pinned research receipt does not qualify: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", lateVerdict(2, view.ResearchFindings[0].ViewID), 0, reviewer); err != nil {
		t.Fatalf("late record_verdict after the re-pin: %v", err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_predicate_verdicts WHERE work_id=? AND contract_version=2`, workID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("verdicts recorded under the successor = %d, want 1", recorded)
	}
	if got := currentStep(t, s, workID); got != "complete" {
		t.Fatalf("step after the held verdict = %q, want complete", got)
	}
	var finalBaseline string
	var noEnactment bool
	if err := s.DatabaseForTesting().QueryRow(`SELECT baseline_json,enactment_json IS NULL AND enactment_effect_seq IS NULL FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&finalBaseline, &noEnactment); err != nil {
		t.Fatal(err)
	}
	if finalBaseline != startBaseline || !noEnactment {
		t.Fatal("stale-pin recovery rewrote its owning start or invented enactment attribution")
	}
}
