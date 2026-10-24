package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// registryFixtureHash is the registry hash the return-route fixture's approved
// contract pins; seedIssue31DomainRegistry projects the registry under it.
func registryFixtureHash() string { return "sha256:" + strings.Repeat("b", 64) }

// registryRescannedHash is the hash a later Domain registry rescan publishes.
func registryRescannedHash() string { return "sha256:" + strings.Repeat("c", 64) }

// execStaleRegistryInFold applies raw projection statements under the fold
// guard, the seam every registry-seeding fixture uses for fold-only tables.
func execStaleRegistryInFold(t *testing.T, s *Store, statements ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// driftStaleRegistryFixture simulates the rescan: the Git home republishes the
// registry under a new content hash and every projected Domain carries the new
// hash. Domain identity and currency are unchanged, so only the pinned hash
// the fixture contract carries goes stale.
func driftStaleRegistryFixture(t *testing.T, s *Store) string {
	t.Helper()
	rescanned := registryRescannedHash()
	execStaleRegistryInFold(t, s,
		`UPDATE domain_registries SET content_hash='`+rescanned+`', scanned_commit_oid='rescan' WHERE product_id='product'`,
		`UPDATE domains SET registry_content_hash='`+rescanned+`', scanned_commit_oid='rescan' WHERE product_id='product'`,
	)
	return rescanned
}

// seedStaleRegistryWorkerAttempt leaves a completed, unaccepted worker attempt
// at the break_fix repair step: the in-flight state a registry rescan strands.
func seedStaleRegistryWorkerAttempt(t *testing.T, s *Store, workID string, owner WorkflowActor) (string, int64) {
	t.Helper()
	ctx := context.Background()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	identity := fmt.Sprintf("%s-%d", workID, version)
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0)+1 FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='repair'`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	startAction := "start_repair"
	if entry, entryErr := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID); entryErr != nil {
		t.Fatal(entryErr)
	} else if workflowDerivedStepStartActive(entry.Definition) {
		// The derived-start pin records the qualifying checkpoint's own
		// started event, so the fixture crafts that shape.
		startAction = "checkpoint_repair"
	}
	start := workflowEventWithActor("repin-start-"+identity, WorkflowActionStarted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "repair",
		"action_id": startAction, "attempt_epoch": epoch, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64),
		"idempotency_identity": "start:" + identity, "actor_ref": ownerRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + identity
	// The completion below resolves the attempt's original dispatch Project
	// through its authorization window, so the window opens before the
	// generic dispatch records the worker's own evidence.
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "repair", ownerRef)
	dispatch := workerDispatchEvent(workID, "repin-dispatch-"+identity, lane, map[string]any{"attempt_id": attemptID})
	dispatch.Actor = ownerRef
	dispatch.OccurredAt = time.Unix(30, 0).UTC()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatal(err)
	}
	completion := workerCompleteEventForLane(workID, "repin-completed-"+identity, attemptID, lane, time.Unix(31, 0).UTC(), workerFixtureDirectory(t, s, workID))
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{completion}}); err != nil {
		t.Fatal(err)
	}
	return attemptID, latestStepStartEpoch(t, s, workID, "repair")
}

// registryRepinSuccessorPayload is the fully supplied engineering
// successor contract whose architecture binding pins registryHash.
func registryRepinSuccessorPayload(contractVersion int64, registryHash, workID string, predecessor int64, domainModifies ...string) json.RawMessage {
	if domainModifies == nil {
		domainModifies = []string{}
	}
	fields := map[string]any{
		"contract_version":              contractVersion,
		"decision_domains":              []string{},
		"predecessor_contract_versions": []int64{predecessor},
		"premise":                       "re-pin the approved contract to the rescanned Domain registry",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"},
		"route_conventions": []string{},
		"spec_mandate":      []string{},
		"law_modifies":      []string{},
		"rigor_class":       "prototype_internal",
		"supersede_reason":  "the approved contract pins the pre-rescan Domain registry hash",
		"audit_evidence":    []string{"evidence:registry-rescan"},
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: registryHash, HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: domainModifies, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	}
	if len(domainModifies) != 0 {
		fields["law_change_reason"] = "keep the approved Domain authority under the rescanned registry"
		fields["law_change_removes_user_visible_behavior"] = false
		fields["law_change_touches_security_or_data_safety"] = false
	}
	return mustJSONValue(fields)
}

func activeStaleRegistryContract(t *testing.T, s *Store, workID string) (int64, int) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	return version, active
}

// TestStaleRegistryRescanAdmitsExactlyOneRePinRoute reproduces the state that
// admits exactly one re-pin route: a break_fix item at the repair step holds
// a completed, unaccepted worker attempt when a Domain registry rescan drifts
// the hash its approved contract pins. The attempt disposition records under
// the subject's own stale pin, so the enclosed item settles its attempt, and
// a supersede_contract whose successor pins the current
// registry hash is the one admissible re-pin (CD-0222 D7).
func TestStaleRegistryRescanAdmitsExactlyOneRePinRoute(t *testing.T) {
	const workID = "stale-registry-repin"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	attemptID, attemptEpoch := seedStaleRegistryWorkerAttempt(t, s, workID, owner)
	currentHash := driftStaleRegistryFixture(t, s)

	rejectPayload := mustJSONValue(map[string]any{
		"attempt_id": attemptID, "attempt_epoch": attemptEpoch,
		"diagnosis": "the delivered repair predates the registry rescan", "strategy": "re-pin the contract, then judge the completed result",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
	})

	// The repair executor cannot author its own verdict, so the held result is
	// judged by the coordinator actor the dispatch named as its reviewer.
	judge := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/repin-judge", SessionRef: "session/" + workID + "-judge", ActorClass: ActorAgent}

	// Attempt disposition records a fact about the attempt made under the
	// pinned contract, so the subject's own stale pin admits it: the enclosed
	// item settles its attempt before it self-heals through the re-pin.
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", rejectPayload, 0, judge); err != nil {
		t.Fatalf("reject_worker_result under the subject's own stale pin: %v", err)
	}
	var settled int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if settled != 1 {
		t.Fatalf("reject under the stale pin recorded %d completion events, want 1", settled)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.Disposition != "rejected" {
		t.Fatalf("correction pin under the stale pin = %#v, want the recorded rejection", pin.Correction)
	}

	staleSuccessor := registryRepinSuccessorPayload(2, registryFixtureHash(), workID, 1)
	err = runVerdictActionAs(t, s, workID, "supersede_contract", staleSuccessor, 0, owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStaleRequiresReview || failure.Detail != "architecture binding Domain registry hash is stale" {
		t.Fatalf("stale-hash successor error=%v, want stale_requires_review from the successor binding gate", err)
	}
	if version, active := activeStaleRegistryContract(t, s, workID); version != 1 || active != 1 {
		t.Fatalf("refused successor left contract version %d (active %d), want the original 1", version, active)
	}

	// The agent route resolves the action and runs the read-only preflight
	// before it executes, so both public gates must admit the re-pin too.
	if _, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil || action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("resolver under the subject's stale pin = %q approval %q err=%v, want the declared supersede_contract metadata", action.ID, action.Approval, err)
	}
	if err := issue1013Preflight(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, currentHash, workID, 1), owner); err != nil {
		t.Fatalf("preflight of the current-hash successor: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, currentHash, workID, 1), 0, owner); err != nil {
		t.Fatalf("current-hash successor supersede: %v", err)
	}
	if version, active := activeStaleRegistryContract(t, s, workID); version != 2 || active != 1 {
		t.Fatalf("active contracts after re-pin = version %d count %d, want exactly version 2", version, active)
	}
	var bindingHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT domain_registry_content_hash FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=2`, workID).Scan(&bindingHash); err != nil {
		t.Fatal(err)
	}
	if bindingHash != currentHash {
		t.Fatalf("successor binding pins %q, want the rescanned %q", bindingHash, currentHash)
	}
	if got := currentStep(t, s, workID); got != "repair" {
		t.Fatalf("step after re-pin = %q, want repair", got)
	}
	var attemptState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&attemptState); err != nil {
		t.Fatal(err)
	}
	// The attempt row keeps the report state; the disposition rides the
	// recorded rejection and the correction pin asserted above.
	if attemptState != "completed" {
		t.Fatalf("attempt state after the settled rejection = %q, want the untouched completed report", attemptState)
	}
	var verdictActions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id') IN ('accept_worker_result','reject_worker_result') AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&verdictActions); err != nil {
		t.Fatal(err)
	}
	if verdictActions != 1 {
		t.Fatalf("attempt disposition and re-pin recorded %d worker verdict actions, want the one rejection", verdictActions)
	}
}

// TestStaleRegistryRescanLetsAcceptWorkerResultRecord is the sibling verdict
// route: the coordinator accepts the completed attempt under the subject's
// own stale pin, and the item still self-heals through the re-pin after the
// acceptance.
func TestStaleRegistryRescanLetsAcceptWorkerResultRecord(t *testing.T) {
	const workID = "stale-registry-repin-accept"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	attemptID, attemptEpoch := seedStaleRegistryWorkerAttempt(t, s, workID, owner)
	currentHash := driftStaleRegistryFixture(t, s)

	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/repin-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}), 0, acceptor); err != nil {
		t.Fatalf("accept_worker_result under the subject's own stale pin: %v", err)
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("accept under the stale pin recorded %d completion events, want 1", recorded)
	}

	if err := runVerdictActionAs(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, currentHash, workID, 1), 0, owner); err != nil {
		t.Fatalf("current-hash successor supersede after the acceptance: %v", err)
	}
	if version, active := activeStaleRegistryContract(t, s, workID); version != 2 || active != 1 {
		t.Fatalf("active contracts after re-pin = version %d count %d, want exactly version 2", version, active)
	}
}

// TestStaleRegistryRescanHoldsLateVerdictUntilRePin proves a stale contract
// pin strands a late verdict behind the staleness boundary: an item past its
// verification step with a missing verdict admits the late record_verdict
// recovery only through the boundary, so a verdict never records under a pin
// the rescan stranded. The recovery refuses on the owning transaction and on
// the admission inspection, the accepted successor law re-pins through the
// ordinary stale recovery, and the held receipt association records under the
// re-pinned contract afterwards. Research's complete step owns the late recovery.
func TestStaleRegistryRescanHoldsLateVerdictUntilRePin(t *testing.T) {
	const workID = "stale-registry-late-verdict"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.research", "complete")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	if got := currentStep(t, s, workID); got != "complete" {
		t.Fatalf("fixture step = %q, want complete", got)
	}
	f := workContextFixture{store: s, workID: workID, owner: owner}
	pack := seedResearchContextPack(t, f, "repin-research-proof")
	bindResearchPack(t, f, pack.PackID, 1, true)
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
	selection["product_wide_rationale"] = "The selected research supports the re-pinned research contract."
	if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
		t.Fatal(err)
	}
	view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 {
		t.Fatalf("selected research receipt: %+v err=%v", view, err)
	}
	// The rescan deletes the pinned law, so the contract reads stale.
	attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
	execStaleRegistryInFold(t, s,
		`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id='`+workID+`' AND contract_version=1`,
		`DELETE FROM law_domain_homes WHERE law_id='spec:one'`,
		`DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test')`,
		`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('project','workflow-law-locator','spec:two','product','root','sha256:`+strings.Repeat("b", 64)+`','test','')`,
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
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
	if version, active := activeStaleRegistryContract(t, s, workID); version != 2 || active != 1 {
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.contract_version')=2`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("verdicts recorded under the successor = %d, want 1", recorded)
	}
	if got := currentStep(t, s, workID); got != "complete" {
		t.Fatalf("step after the held verdict = %q, want complete", got)
	}
}

// seedStaleRegistryRescanPeer seeds an executing peer whose own contract pin
// stayed on the pre-rescan hash and whose footprint shares the subject's
// Domain write, so the subject's boundary check reads the peer's stale pin.
// The peer carries the durable execution-start fact (CD-0183 D2); a peer that
// has not started execution claims nothing and blocks nobody.
func seedStaleRegistryRescanPeer(t *testing.T, s *Store, peerID, approverRef string) {
	t.Helper()
	seedWork(t, s, peerID)
	setExecutionStartedForTesting(t, s, peerID, true)
	execStaleRegistryInFold(t, s,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('`+peerID+`',1,'peer contract','internal_sqlite','[]','[]','2026-09-27T00:00:00Z','`+approverRef+`','[]','[]',1,'prototype_internal')`,
		`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES('`+peerID+`',1,'product','`+registryFixtureHash()+`','root','`+registryFixtureHash()+`')`,
		`INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('`+peerID+`',1,'root')`,
		`INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+peerID+`',1,'root')`,
	)
	setWorkLifecycleForTesting(t, s, peerID, "in_progress")
}

// TestStaleRegistryOverlapRefusalBesideAPeerStalePin proves the boundary's
// answer beside a stranded peer is the subject's own: the unresolved-overlap
// refusal with its four closed recovery routes, never the peer's stale pin.
// supersede_contract is one of those routes, so the subject's recovery re-pin
// beside the peer is admitted, while the stale pin's marker keeps naming the
// peer whose own contract re-pins through it (CD-0222 D7).
func TestStaleRegistryOverlapRefusalBesideAPeerStalePin(t *testing.T) {
	const workID = "stale-registry-repin-peer"
	const peerID = "stale-registry-repin-peer-other"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	rescanned := driftStaleRegistryFixture(t, s)
	// The subject re-pins first: contract v2 pins the rescanned hash and keeps
	// the one shared Domain write the peer also claims.
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, rescanned, workID, 1, "root"), 0, owner); err != nil {
		t.Fatalf("subject re-pin: %v", err)
	}
	seedStaleRegistryRescanPeer(t, s, peerID, ownerRef)

	// supersede_contract is one of the four closed recovery routes the
	// unresolved-overlap refusal carries, so the resolver exposes the
	// recovery action beside the stranded peer.
	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil {
		t.Fatalf("resolver of the overlap recovery route beside a stale peer pin: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(3, rescanned, workID, 2, "root"), 0, owner); err != nil {
		t.Fatalf("supersede_contract beside a stale peer pin: %v", err)
	}
	if version, active := activeStaleRegistryContract(t, s, workID); version != 3 || active != 1 {
		t.Fatalf("subject contract after the recovery re-pin = version %d (active %d), want exactly version 3", version, active)
	}

	// The same structural rule holds the attempt disposition. The subject's
	// own stale pin would admit it, and the peer's stale pin never answers
	// for the subject: the refusal here is the unresolved overlap with its
	// four closed recovery routes, and the marker keeps the peer's recovery
	// route on the peer's own contract.
	attemptID, attemptEpoch := seedStaleRegistryWorkerAttempt(t, s, workID, owner)
	disposer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/peer-disposer", SessionRef: "session/" + workID + "-disposer", ActorClass: ActorAgent}
	err = runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attemptID, "attempt_epoch": attemptEpoch,
		"diagnosis": "the delivered repair predates the registry rescan", "strategy": "settle the attempt, then route the peer's recovery",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
	}), 0, disposer)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil {
		t.Fatalf("reject_worker_result beside a stale peer pin error=%v, want the unresolved-overlap refusal", err)
	}
	if len(failure.DomainOverlap.Overlaps) != 1 || failure.DomainOverlap.Overlaps[0].FromWorkID != workID || failure.DomainOverlap.Overlaps[0].ToWorkID != peerID {
		t.Fatalf("overlap refusal = %#v, want one overlap naming the pair", failure.DomainOverlap)
	}
	if got := failure.DomainOverlap.Overlaps[0].RecoveryActions; len(got) != 4 || got[0] != "wait" || got[1] != "resolve_overlap" || got[2] != "terminal_work" || got[3] != "supersede_contract" {
		t.Fatalf("overlap recovery actions = %v, want the four closed routes", got)
	}
	if failure.StaleDomainRegistryPin != nil && failure.StaleDomainRegistryPin.WorkID == workID {
		t.Fatalf("overlap refusal carries a stale-pin marker naming the subject: %#v", failure.StaleDomainRegistryPin)
	}
}

// TestStaleRegistryRePinRefusesWithoutRegistry proves a Product with no
// current Domain registry has no pin to re-pin against, so the recovery route
// stays refused.
func TestStaleRegistryRePinRefusesWithoutRegistry(t *testing.T) {
	const workID = "stale-registry-repin-no-registry"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	execStaleRegistryInFold(t, s,
		`DELETE FROM domains WHERE product_id='product'`,
		`DELETE FROM domain_registries WHERE product_id='product'`,
	)
	err := runVerdictActionAs(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, registryRescannedHash(), workID, 1), 0, owner)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnknownScope {
		t.Fatalf("supersede without a registry error=%v, want unknown_scope", err)
	}
}

// seedStaleRegistryCompleteStepAttempt leaves a completed, undisposed worker
// attempt on an implementation instance parked at its pinned complete-declaring
// step. Its authorization belongs to the preceding confirmation checkpoint:
// the complete step declares no attempt-opening action. The dispatch and
// report may still arrive after the instance advances past that checkpoint.
func seedStaleRegistryCompleteStepAttempt(t *testing.T, s *Store, workID string) string {
	t.Helper()
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + workID
	dispatch := workerDispatchEvent(workID, "repin-dispatch-"+workID, lane, map[string]any{"attempt_id": attemptID})
	dispatch.OccurredAt = time.Unix(30, 0).UTC()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatal(err)
	}
	completion := workerCompleteEventForLane(workID, "repin-completed-"+workID, attemptID, lane, time.Unix(31, 0).UTC(), workerFixtureDirectory(t, s, workID))
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completion}}); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

// TestStaleRegistryRescanReleasesTheEnclosedCompleteStepAttempt reproduces
// the POKE-79 shape: a workflow.implementation instance at its pinned
// complete-declaring step holds a completed, undisposed review attempt when a
// Domain registry rescan strands the contract's pin, and complete-step
// correction requires no unsettled attempt. The attempt disposition is a fact
// about the attempt made under the pinned contract, so the subject's own
// stale pin no longer decides its admission; record_verdict keeps the
// refusal, so terminal completion still requires the re-pin. The step that
// declares the disposition still owns its own admission, so the released
// disposition reaches the step gate, not the marker.
func TestStaleRegistryRescanReleasesTheEnclosedCompleteStepAttempt(t *testing.T) {
	const workID = "stale-registry-complete-attempt"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	ownerRef, actorErr := WorkflowActorRef(owner)
	if actorErr != nil {
		t.Fatal(actorErr)
	}
	recordWorkerFixtureDispatchAuthorization(t, s, workID, "attempt:"+workID, "acceptance", ownerRef)
	if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, "release", ownerRef); err != nil {
		t.Fatal(err)
	}
	if got := currentStep(t, s, workID); got != "release" {
		t.Fatalf("fixture step = %q, want release", got)
	}
	attemptID := seedStaleRegistryCompleteStepAttempt(t, s, workID)
	driftStaleRegistryFixture(t, s)

	lateVerdict := mustJSONValue(map[string]any{
		"contract_version": 1,
		"verdicts":         []map[string]any{{"predicate_id": "predicate:return-route", "receipt_refs": []string{"evidence:return-route-verification"}}},
	})
	// The action executor cannot author its own verdict, so the held verdict
	// is judged by the reviewer actor the dispatch named.
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/complete-judge", SessionRef: "session/" + workID + "-judge", ActorClass: ActorAgent}
	err := runVerdictActionAs(t, s, workID, "record_verdict", lateVerdict, 0, reviewer)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindStaleRequiresReview || failure.Detail != "workflow Domain registry pin is stale" {
		t.Fatalf("late record_verdict under the subject's own stale pin error=%v, want stale_requires_review naming the stale pin", err)
	}

	// The attempt disposition reaches the step gate instead of the marker:
	// the subject's own stale pin no longer refuses it.
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/complete-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	err = runVerdictActionAs(t, s, workID, "accept_worker_evidence", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": int64(1)}), 0, acceptor)
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("accept_worker_evidence under the subject's own stale pin error=%v, want the declaring step's own refusal, not a stale-pin marker", err)
	}

	// The complete-step correction route the item would take waits for the
	// settled attempt: the reserved route convention is declared, the
	// successor pins the rescanned hash, and the unsettled attempt still
	// refuses the correction. The stale-hash successor refusal itself stays
	// exactly as tested today on the non-complete-step route.
	rescanned := registryRescannedHash()
	completeCorrection := mustJSONValue(map[string]any{
		"contract_version": 2, "predecessor_contract_versions": []int64{1},
		"decision_domains": []string{},
		"premise":          "re-pin the approved contract to the rescanned Domain registry",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification", "review", "artifact"},
		"route_conventions": []string{"complete_step_correction"},
		"spec_mandate":      []string{}, "law_modifies": []string{},
		"rigor_class":      "prototype_internal",
		"supersede_reason": "the approved contract pins the pre-rescan Domain registry hash",
		"audit_evidence":   []string{"evidence:registry-rescan"},
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: rescanned, HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	err = runVerdictActionAs(t, s, workID, "supersede_contract", completeCorrection, 0, owner)
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("complete-step correction with the attempt unsettled error=%v, want the complete-step state gate's invalid_operation refusal", err)
	}
	if version, active := activeStaleRegistryContract(t, s, workID); version != 1 || active != 1 {
		t.Fatalf("refused correction left contract version %d (active %d), want the original 1", version, active)
	}
}

// TestStaleRegistryRescanSequencesAroundAPeerStalePin proves the one rule at
// the overlap seams: admission under a stale pin belongs to the marker's
// named subject only, recording a resolution matches its D7-exempt
// invocation, and a recorded resolution is evaluated before the peer pin
// check so a resolved overlap never vetoes.
func TestStaleRegistryRescanSequencesAroundAPeerStalePin(t *testing.T) {
	const workID = "stale-overlap-sequence"
	const peerID = "stale-overlap-sequence-peer"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	driftStaleRegistryFixture(t, s)
	// The subject's fixture contract writes no Domain, so the pair would not
	// derive an overlap at all. One shared Domain write makes the peer a real
	// overlap peer, the shape the boundary refuses on.
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+workID+`',1,'root')`)
	seedStaleRegistryRescanPeer(t, s, peerID, ownerRef)
	attemptID, attemptEpoch := seedStaleRegistryWorkerAttempt(t, s, workID, owner)
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/sequence-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	accept := mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch})

	// The subject's own stale pin no longer decides the attempt disposition,
	// and the peer's stale pin never answers for the subject: the refusal
	// that remains is the unresolved overlap, and the peer's marker keeps the
	// peer's own re-pin route.
	err = runVerdictActionAs(t, s, workID, "accept_worker_result", accept, 0, acceptor)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil {
		t.Fatalf("accept_worker_result beside a stale peer pin error=%v, want the unresolved-overlap refusal", err)
	}

	// Recording while the declarer's own pin is stale still refuses: the
	// marker names the subject, and the subject's recovery route is its own
	// re-pin.
	resolution := func(fromVersion, toVersion int64) WorkflowDomainOverlapResolutionRequest {
		return WorkflowDomainOverlapResolutionRequest{
			EventID: "stale-overlap-resolution-" + workID, FromWorkID: workID, ToWorkID: peerID,
			FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
			FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith,
			Reason: "the delivered repair is independent of the peer's stranded claim",
			Actor:  owner.PrincipalRef, OccurredAt: time.Unix(40, 0).UTC(),
		}
	}
	subjectVersion := readWorkVersion(t, s, workID)
	peerVersion := readWorkVersion(t, s, peerID)
	err = s.Transact(ctx, func(transaction *Transaction) error {
		_, txErr := ResolveWorkflowDomainOverlapTx(ctx, transaction, resolution(subjectVersion, peerVersion))
		return txErr
	})
	if !errors.As(err, &failure) || failure.Kind != KindStaleRequiresReview || failure.StaleDomainRegistryPin == nil || failure.StaleDomainRegistryPin.WorkID != workID {
		t.Fatalf("recording under the declarer's own stale pin error=%v, want stale_requires_review naming the declarer", err)
	}

	// The re-pin cures the subject's own pin; the peer stays stale.
	currentHash := registryRescannedHash()
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, currentHash, workID, 1, "root"), 0, owner); err != nil {
		t.Fatalf("subject re-pin beside the stale peer footprint: %v", err)
	}
	// A successor closes the prior result's eligibility interval. Keep that
	// boundary, then exercise overlap sequencing with a current result.
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", accept, 0, acceptor); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("predecessor result stayed eligible after supersession: %v", err)
	}
	attemptID, attemptEpoch = seedStaleRegistryWorkerAttempt(t, s, workID, owner)
	accept = mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch})

	// With no recorded resolution the unresolved overlap still vetoes the
	// subject's admission, and the peer's stale pin never becomes the
	// subject's answer.
	err = runVerdictActionAs(t, s, workID, "accept_worker_result", accept, 0, acceptor)
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil {
		t.Fatalf("accept_worker_result with an unresolved overlap beside the stale peer error=%v, want the unresolved-overlap refusal", err)
	}

	// Recording succeeds while a peer is stale, matching the invocation
	// exemption the D7 boundary already grants resolve_overlap.
	subjectVersion = readWorkVersion(t, s, workID)
	err = s.Transact(ctx, func(transaction *Transaction) error {
		request := resolution(subjectVersion, peerVersion)
		request.FromContractVersion, request.ToContractVersion = 2, 1
		_, txErr := ResolveWorkflowDomainOverlapTx(ctx, transaction, request)
		return txErr
	})
	if err != nil {
		t.Fatalf("recording beside the stale peer pin: %v", err)
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions WHERE product_id='product' AND from_work_id=? AND to_work_id=? AND invalidated_seq IS NULL`, workID, peerID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("recorded resolutions for the pair = %d, want 1", recorded)
	}

	// A recorded resolution is evaluated before the peer pin check, so the
	// resolved overlap never vetoes the subject's admission.
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", accept, 0, acceptor); err != nil {
		t.Fatalf("accept_worker_result under the recorded resolution: %v", err)
	}
}
