package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// derivedRevisionConflictLaw is the committed spec:one revision whose record
// declares the conflicts_with relation against the legislated fixture law:
// the declarer is whichever record carries the relation.
const derivedRevisionConflictLaw = "---\n" +
	`{"id":"spec:one","title":"Fixture folder law spec:one","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[],"law_relations":[{"kind":"conflicts_with","target_id":"spec:legislated"}]}` + "\n" +
	"---\n\nSynthetic fixture law body.\n"

// derivedRevisionLandedLaw is the committed spec:one revision after the
// derived record came into conformance: the same stable ID and home, the
// conflict withdrawn, and a new whole-file digest.
const derivedRevisionLandedLaw = "---\n" +
	`{"id":"spec:one","title":"Fixture folder law spec:one","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[]}` + "\n" +
	"---\n\nSynthetic fixture law body, brought into conformance.\n"

// TestDerivedRevisionCompletesOnlyAfterItLands holds CD-0159 D4 from the
// contract approval through completion. A research contract mandates both
// ends of a conflicts_with relation and revises the derived end, which the
// tier admits as a revision line. Completion re-checks the conflict without
// the amendment allowance, so it refuses while the committed law folder still
// carries the conflict. The derived record declares the conflict on its
// committed file, so the landing commit edits only derived law: a new content
// digest for the derived law and no conflict relation, and the same contract
// completes with no further approval.
func TestDerivedRevisionCompletesOnlyAfterItLands(t *testing.T) {
	t.Parallel()
	const workID = "derived-revision-lands"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedDerivedLawExtension(t, s)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	commitFixtureLawFile(t, home, "spec:one", derivedRevisionConflictLaw)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/researcher", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	version := seedDerivedRevisionWorkflow(t, s, workID, owner)
	approval := json.RawMessage(`{"premise":"Bring the derived law into conformance with the legislated one.","spec_mandate":["spec:legislated","spec:one"],"law_modifies":["spec:one"],"law_change_reason":"Bring the derived record into conformance with its legislated mandate.","required_evidence":["artifact"],"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["report_recorded"]}}]}`)
	if err := runDerivedLawApproval(t, s, workID, version, owner, approval); err != nil {
		t.Fatalf("the derived revision was not admitted at approval: %v", err)
	}
	for _, step := range []struct {
		action  string
		payload string
	}{
		{"bind_evidence", `{"evidence_kind":"artifact","immutable_subject_ref":"spec:legislated"}`},
		{"bind_evidence", `{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}`},
		{"record_finding", `{}`},
		{"record_report", `{"evidence_kind":"artifact","immutable_subject_ref":"evidence:research-report"}`},
		{"record_conclusion", `{}`},
	} {
		if err := runVerdictActionAs(t, s, workID, step.action, json.RawMessage(step.payload), 0, owner); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
	}
	f := workContextFixture{store: s, workID: workID, owner: owner}
	pack := seedResearchContextPack(t, f, "derived-lands-proof")
	bindResearchPack(t, f, pack.PackID, 1, true)
	// The selection's child Domain carries no product-wide rationale.
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
	if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
		t.Fatal(err)
	}
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 {
		t.Fatalf("selected research proof: %+v err=%v", view, err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", mustJSONValue(map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{view.ResearchFindings[0].ViewID}}}}), 0, owner); err != nil {
		t.Fatalf("research receipt association: %v", err)
	}
	ownerRef, _ := WorkflowActorRef(owner)
	premiseVersion := readWorkVersion(t, s, workID)
	premise := workflowEventWithActor("derived-lands-premise", WorkflowPremiseConfirmed, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "contract_version": 1, "confirming_actor_ref": ownerRef, "confirmation_authority": "coordinator"})
	premise.PayloadVersion = 2
	premiseCompleted := workflowActionCompletedFixture("derived-lands-premise-done", workID, ownerRef, premiseVersion+1, "conclude", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{premise, premiseCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatalf("premise confirmation: %v", err)
	}
	bindCompletionClosure(t, s, workID, owner)
	complete := func() error {
		t.Helper()
		return runVerdictActionAs(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, owner)
	}
	err = complete()
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindRelationConflict || failure.Clause != 3 || !strings.Contains(failure.Detail, "unresolved explicit conflict: spec:legislated and spec:one") {
		t.Fatalf("completion before the revision landed did not meet the clause 3 conflict refusal: %v", err)
	}

	// The landing commit withdraws the conflict on the committed derived
	// record: same stable ID and home, new whole-file digest.
	commitFixtureLawFile(t, home, "spec:one", derivedRevisionLandedLaw)
	if err := complete(); err != nil {
		t.Fatalf("completion after the revision landed was refused: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
	var contracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if contracts != 1 {
		t.Fatalf("the landed revision needed %d contracts, want the one approval", contracts)
	}
}
