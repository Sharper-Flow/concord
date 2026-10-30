package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The session remains the executing actor after an in-session delivery.
func seedDeliveredItemAtAcceptance(t *testing.T, workID string) *Store {
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
	setup := []Event{
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": workflowFixtureDefinition(t, 2).Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, ownerRef, 6, "design", "record_design"),
		workflowEventWithActor("contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 7, "resulting_version": 8, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef, "execution_model": "model:fixture"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	// Evidence binds while the step is still execution: bind_evidence is
	// declared there and the delivery exit leaves it.
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:operator-verdict"}`)); err != nil {
		t.Fatalf("bind verification evidence: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","immutable_subject_ref":"evidence:operator-review"}`)); err != nil {
		t.Fatalf("bind review evidence: %v", err)
	}
	// No lane or attempt window exists for this delivery.
	delivery := workflowActionCompletedFixture("delivery-"+workID, workID, ownerRef, readWorkVersion(t, s, workID), "execution", "record_delivery")
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{delivery}}); err != nil {
		t.Fatalf("record delivery exit: %v", err)
	}
	return s
}

func operatorVerdictActor(t *testing.T, workID string) WorkflowActor {
	t.Helper()
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	if _, err := WorkflowActorRef(operator); err != nil {
		t.Fatal(err)
	}
	return operator
}

func setWorkflowStepForOperatorVerdictTest(t *testing.T, s *Store, workID, stepID string) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_instances SET current_step=? WHERE work_id=?`, stepID, workID); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// #909: a host restart mints a new session identity, so the completing
// action can be the first place that tuple appears. The complete path lands
// the guard's actor-recording events instead of dropping them.
func TestCompleteRecordsAFirstSeenActorTuple(t *testing.T) {
	t.Parallel()
	const workID = "complete-first-seen-actor"
	f := qCurrentAcceptance(t, workID)
	ctx := t.Context()
	if err := f.run(ctx, "confirm_premise", "confirm", f.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirm the premise: %v", err)
	}
	bindCompletionClosure(t, f.store, workID, f.owner)
	// The restarted session: same principal and client, new agent and
	// session identity, never recorded on this work item before.
	restarted := WorkflowActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef, AgentRef: "agent/owner-restarted", SessionRef: "session/" + workID + "-restarted", ActorClass: ActorAgent}
	restartedFixture := f
	restartedFixture.owner = restarted
	if err := restartedFixture.run(ctx, "complete", "complete", f.version(ctx, t), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatalf("complete from a first-seen actor tuple refused: %v", err)
	}
	var state string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
	restartedRef, _ := WorkflowActorRef(restarted)
	var recorded int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, restartedRef).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("restarted actor recordings=%d, want 1", recorded)
	}
}

// #911: the premise confirmation is the last step where record_verdict is
// declared, so it refuses while any approved criterion lacks an associated
// passing receipt instead of letting the item wedge at complete.
func TestConfirmPremiseRequiresEveryPredicateVerdict(t *testing.T) {
	t.Parallel()
	const workID = "confirm-all-predicates"
	present := map[string]any{"predicate_id": "predicate:present", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "surface:one", "subjects": []string{"subject:one"}}}
	absent := map[string]any{"predicate_id": "predicate:absent", "ordinal": 1, "outcome_kind": "absent", "outcome_payload": map[string]any{"kind": "absent", "surface": "surface:two", "subjects": []string{"subject:two"}, "distinguish_from": []string{"archived"}}}
	f := qBuildAcceptancePredicates(t, workID, "", false, nil, present, absent)
	s := f.store
	if err := runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload(f.receipt, "predicate:present"), 0, f.owner); err != nil {
		t.Fatalf("associate one criterion: %v", err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := missingPredicateVerdicts(context.Background(), tx, workID)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "predicate:absent" {
		t.Fatalf("missing predicates=%v, want [predicate:absent]", missing)
	}
	version := readWorkVersion(t, s, workID)
	err = f.run(t.Context(), "confirm_premise", "confirm", version, nil, nil)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("confirm_premise with an unassociated criterion = %v, want the missing-verdict refusal", err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("the refused confirmation moved the item")
	}
}
