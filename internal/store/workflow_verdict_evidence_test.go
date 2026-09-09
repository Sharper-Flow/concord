package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Issue #816: record_verdict mints evaluation_evidence refs it never bound,
// so foldWorkflowCompleted clause 4 refused every agent-driven completion. A
// defaulted verdict is now born bound, and the complete action late-binds
// outstanding verdict refs for histories folded before the fix.

// seedItemAtAcceptance drives one break-fix-shaped item through the real
// action layer to its acceptance step, with a dispatched lane pinned as the
// executing actor (CD-0109). Verdicts are then recordable. When prebind is
// false the seeded evidence binds are skipped, so the verdict's own mint is
// the only bound evidence.

// seedComparisonObservation records the investigation artifact the operator
// question gate requires: a current Domain ref and a different work item. When
// the fixture Product holds no Domain registry yet, one is folded in first.
func seedComparisonObservation(t *testing.T, s *Store, workID string) {
	t.Helper()
	var domainRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT d.domain_id FROM domains d JOIN product_projects pp ON pp.product_id=d.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? AND d.status='current' LIMIT 1`, workID).Scan(&domainRef); err != nil {
		seedIssue31DomainRegistry(t, s)
		domainRef = "root"
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?)`, workID+"-compared", "task", "Investigation comparison", "needed", 0, "2026-09-09T00:00:00Z", "2026-09-09T00:00:00Z"); err != nil {
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'secondary')`, workID+"-compared", "project"); err != nil {
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,?)`, "obs:"+workID[len(workID)-12:]+"cmp0", workID, "investigation before question", `["`+domainRef+`","`+workID+`-compared"]`, `[]`, "2026-09-09T00:00:00Z"); err != nil {
		_ = leaveFold(context.Background(), tx)
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func seedItemAtAcceptance(t *testing.T, workID string, prebind bool) (*Store, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)

	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	setup := []Event{
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 2, "digest": workflowFixtureDefinition(t, 2).Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, ownerRef, 6, "design", "record_design"),
		workflowEventWithActor("contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 7, "resulting_version": 8, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
	}
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	reviewActor := workflowEvent("reviewer-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"})
	setup = append(setup, reviewActor)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID
	dispatch := Event{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: "sha256:" + strings.Repeat("e", 64)})}
	var preparedEvents []Event
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var err error
		preparedEvents, err = PrepareLaneActorDispatch(ctx, tx, dispatch, owner.PrincipalRef, owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(ctx, tx, preparedEvents)
		return err
	}); err != nil {
		t.Fatalf("lane actor dispatch: %v", err)
	}
	completed := Event{EventID: "completed-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	if prebind {
		if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:seeded-verification"}`), 0); err != nil {
			t.Fatalf("bind verification evidence: %v", err)
		}
		if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","immutable_subject_ref":"evidence:seeded-review"}`), 0); err != nil {
			t.Fatalf("bind review evidence: %v", err)
		}
	}
	if err := runVerdictAction(t, s, workID, "accept_worker_result", json.RawMessage(mustJSON(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1})), 0); err != nil {
		t.Fatalf("accept worker result: %v", err)
	}
	verdictReviewers[workID] = reviewer
	return s, owner
}

// verdictReviewers holds the non-executing reviewer actor each seeded item
// records, so verdict actions can be run by an agent that executed nothing.
var verdictReviewers = map[string]WorkflowActor{}

func verdictReviewer(t *testing.T, workID string) WorkflowActor {
	t.Helper()
	reviewer, ok := verdictReviewers[workID]
	if !ok {
		t.Fatalf("no reviewer recorded for %s", workID)
	}
	return reviewer
}

// actionEvidenceRefs supplies the immutable subject refs that the
// evidence-authority fold requires for evidence-producing actions.
func actionEvidenceRefs(action string, payload json.RawMessage) []string {
	if action != "bind_evidence" && action != "record_report" {
		return nil
	}
	var fields struct {
		ImmutableSubjectRef string `json:"immutable_subject_ref"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil || fields.ImmutableSubjectRef == "" {
		return nil
	}
	return []string{fields.ImmutableSubjectRef}
}

func runVerdictAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64) error {
	return runVerdictActionAs(t, s, workID, action, payload, version, WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent})
}

// runVerdictActionAs runs one action as a chosen actor. Verdict actions pass
// the reviewer: the owner executed the delivery step and the corrected
// independence law refuses a self-evaluation (#801).
func runVerdictActionAs(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor) error {
	t.Helper()
	if version == 0 {
		version = verdictItemVersion(t, s, workID)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		return err
	}
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-" + workID + "-" + fmt.Sprint(version), RequestID: "request:" + action + "-" + workID + "-" + fmt.Sprint(version), ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	}); err != nil {
		_ = leaveFold(context.Background(), tx)
		return err
	}
	_ = leaveFold(context.Background(), tx)
	return tx.Commit()
}

func verdictItemVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// A verdict recorded with no evaluation_evidence folds its own
// workflow.evidence_bound event for the minted ref, so completion passes
// clause 4 (issue #816).
func TestRecordVerdictDefaultEvidenceIsBornBound(t *testing.T) {
	const workID = "verdict-born-bound"
	s, _ := seedItemAtAcceptance(t, workID, true)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary"}`), verdictItemVersion(t, s, workID), verdictReviewer(t, workID)); err != nil {
		t.Fatalf("record_verdict with default evidence refused: %v", err)
	}
	var bound int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref') LIKE 'evidence:record_verdict-%'`, workID, WorkflowEvidenceBound).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound == 0 {
		t.Fatal("the minted evidence ref was not bound by the record_verdict operation")
	}
}

// Issue #842: the born-bind mint hardcoded the review kind, so a contract
// requiring verification failed completion clause 1 unless the operator
// carried evidence_kind on the complete envelope. A defaulted verdict now
// mints every contract-required kind, so clause 1 passes by construction
// with no envelope kind.
func TestRecordVerdictDefaultEvidenceSatisfiesContractRequiredKinds(t *testing.T) {
	const workID = "verdict-born-bound-kinds"
	s, _ := seedItemAtAcceptance(t, workID, false)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary"}`), verdictItemVersion(t, s, workID), verdictReviewer(t, workID)); err != nil {
		t.Fatalf("record_verdict with default evidence refused: %v", err)
	}
	var verification int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='verification'`, workID, WorkflowEvidenceBound).Scan(&verification); err != nil {
		t.Fatal(err)
	}
	if verification == 0 {
		t.Fatal("the defaulted verdict minted no verification-kind evidence")
	}
	operatorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/"+workID)
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRecorded := workflowEvent("operator-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/" + workID, "actor_class": "operator"})
	premise := workflowEventWithActor("premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	confirmCompleted := workflowActionCompletedFixture("premise-completed-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, confirmCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), verdictItemVersion(t, s, workID)); err != nil {
		t.Fatalf("completion clause 1 refused a verdict born bound under the contract kinds: %v", err)
	}
	var instanceState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" {
		t.Fatalf("instance_state=%s, want completed", instanceState)
	}
}

// A history folded before the fix holds verdicts whose refs were never
// bound. The complete action late-binds them, so clause 4 passes instead of
// stranding the item (the live state of work-0ce535bc9d03c043ef8dddb1).
func TestLateBindEvidenceAtCompleteStepUnblocksClauseFour(t *testing.T) {
	const workID = "verdict-late-bind"
	s, _ := seedItemAtAcceptance(t, workID, true)
	// The pre-fix fold: a verdict pinned to a minted, never-bound ref. The
	// verdict actor is the reviewer, an agent that executed no step, as the
	// corrected independence law requires (#801).
	reviewerRef, err := WorkflowActorRef(verdictReviewer(t, workID))
	if err != nil {
		t.Fatal(err)
	}
	seedVersion := verdictItemVersion(t, s, workID)
	verdict := workflowEventWithActor("verdict-pre-fix-"+workID, WorkflowVerdictRecorded, workID, reviewerRef, map[string]any{"work_id": workID, "expected_version": seedVersion, "resulting_version": seedVersion + 1, "contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "evaluation_evidence": []string{"evidence:workflow-pre-fix-unbound"}, "incomparable_with_approved": false})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): seedVersion}}); err != nil {
		t.Fatal(err)
	}
	operatorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/"+workID)
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRecorded := workflowEvent("operator-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/" + workID, "actor_class": "operator"})
	premise := workflowEventWithActor("premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	confirmCompleted := workflowActionCompletedFixture("premise-completed-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, confirmCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), verdictItemVersion(t, s, workID)); err != nil {
		t.Fatalf("late-bound verdict evidence still blocks completion: %v", err)
	}
	var instanceState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" {
		t.Fatalf("instance_state=%s, want completed", instanceState)
	}
	var bound int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')='evidence:workflow-pre-fix-unbound'`, workID, WorkflowEvidenceBound).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound == 0 {
		t.Fatal("complete did not bind the outstanding verdict evidence ref")
	}
}

// Explicit evaluation_evidence keeps the pre-fix contract: the refs must
// already be bound when the verdict is recorded.
func TestRecordVerdictExplicitEvidenceMustBePreBound(t *testing.T) {
	const workID = "verdict-explicit-unbound"
	s, _ := seedItemAtAcceptance(t, workID, true)
	err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","evaluation_evidence":["evidence:never-bound"]}`), verdictItemVersion(t, s, workID), verdictReviewer(t, workID))
	if err == nil {
		t.Fatal("a verdict naming unbound explicit evidence must be refused")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("explicit unbound evidence failure = %v, want %s", err, KindMissingEvidence)
	}
}
