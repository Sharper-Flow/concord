package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// CD-0116: after an in-session record_delivery the session is the pinned
// executing actor, so CD-0013 D5 refuses its verdict and no distinct evaluator
// exists. The operator's signed identity is that evaluator, and the path is
// conditioned on the delivery exit.
func seedDeliveredItemAtAcceptance(t *testing.T, workID string) (*Store, WorkflowActor) {
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
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	// Evidence binds while the step is still execution: bind_evidence is
	// declared there and the delivery exit leaves it.
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:operator-verdict"}`), 0); err != nil {
		t.Fatalf("bind verification evidence: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","immutable_subject_ref":"evidence:operator-review"}`), 0); err != nil {
		t.Fatalf("bind review evidence: %v", err)
	}
	// The in-session delivery exit (CD-0112): no lane, no attempt window.
	delivery := workflowActionCompletedFixture("delivery-"+workID, workID, ownerRef, verdictItemVersion(t, s, workID), "execution", "record_delivery")
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{delivery}}); err != nil {
		t.Fatalf("record delivery exit: %v", err)
	}
	return s, owner
}

func operatorVerdictActor(t *testing.T, workID string) WorkflowActor {
	t.Helper()
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	if _, err := WorkflowActorRef(operator); err != nil {
		t.Fatal(err)
	}
	return operator
}

func runOperatorVerdict(t *testing.T, s *Store, workID string, owner, operator WorkflowActor) error {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{"evidence:operator-verdict"}})
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "record_verdict", Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + workID, IdempotencyIdentity: "operator-verdict-" + workID, OperationID: "operator-verdict-" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "operator-verdict-" + workID, RequestID: "request:operator-verdict-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
	})
	if err != nil {
		_ = leaveFold(context.Background(), tx)
		return err
	}
	_ = leaveFold(context.Background(), tx)
	return tx.Commit()
}

// The wedge, pinned: the delivering session cannot evaluate itself, and the
// operator identity passes D5 by construction after a record_delivery exit.
func TestOperatorVerdictAfterDeliveryPassesDistinctness(t *testing.T) {
	const workID = "operator-verdict-delivery"
	s, owner := seedDeliveredItemAtAcceptance(t, workID)
	operator := operatorVerdictActor(t, workID)

	// The delivering session is still refused.
	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{"evidence:operator-verdict"}})
	if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner); err == nil || !strings.Contains(err.Error(), "evaluate its own delivery") {
		t.Fatalf("delivering session verdict err=%v, want the self-evaluation refusal", err)
	}

	// The operator's signed verdict is accepted and stamped as the verdict actor.
	if err := runOperatorVerdict(t, s, workID, owner, operator); err != nil {
		t.Fatalf("operator verdict after delivery refused: %v", err)
	}
	operatorRef, _ := WorkflowActorRef(operator)
	var recorded string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.verdict_actor_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != operatorRef {
		t.Fatalf("verdict actor=%q, want the operator %q", recorded, operatorRef)
	}

	// The completion-time distinctness accepts the operator verdict too.
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := workflowCompletionActorDistinct(context.Background(), tx, workID, operatorRef, "", false); err != nil {
		t.Fatalf("completion distinctness refused the operator verdict: %v", err)
	}
}

// The condition binds: without a record_delivery exit the operator identity is
// refused, so the lane route's distinct evaluator stays the only verdict path.
func TestOperatorVerdictConditionBinds(t *testing.T) {
	const workID = "operator-verdict-lane"
	s, owner := seedItemAtAcceptance(t, workID, true)
	operator := operatorVerdictActor(t, workID)

	err := runOperatorVerdict(t, s, workID, owner, operator)
	if err == nil || !strings.Contains(err.Error(), "only after an in-session record_delivery exit") {
		t.Fatalf("operator verdict without a delivery exit err=%v, want the condition refusal", err)
	}
}

// Completion is the verdict's terminal act: after an in-session delivery the
// lease-holding session cannot submit complete, and the operator identity
// completes under the same delivery-exit condition (CD-0116 D1).
func TestOperatorCompleteAfterDeliveryPassesDistinctness(t *testing.T) {
	const workID = "operator-complete-delivery"
	s, owner := seedDeliveredItemAtAcceptance(t, workID)
	operator := operatorVerdictActor(t, workID)

	if err := runOperatorVerdict(t, s, workID, owner, operator); err != nil {
		t.Fatalf("operator verdict after delivery refused: %v", err)
	}
	operatorRef, _ := WorkflowActorRef(operator)
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRecorded := workflowEvent("operator-complete-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
	premise := workflowEventWithActor("operator-complete-premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	premiseCompleted := workflowActionCompletedFixture("operator-complete-premise-done-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, premiseCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatalf("premise confirmation: %v", err)
	}

	completeVersion := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"impact_verdict": "non-breaking"})
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: completeVersion, ActionID: "complete", Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64) + workID, IdempotencyIdentity: "operator-complete-" + workID, OperationID: "operator-complete-" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "operator-complete-" + workID, RequestID: "request:operator-complete-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(11, 0).UTC(),
	})
	if err != nil {
		_ = leaveFold(context.Background(), tx)
		t.Fatalf("operator complete after delivery refused: %v", err)
	}
	_ = leaveFold(context.Background(), tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
}

// The condition binds for completion too: a lane-executed exit refuses the
// operator identity on complete.
func TestOperatorCompleteConditionBinds(t *testing.T) {
	const workID = "operator-complete-lane"
	s, owner := seedItemAtAcceptance(t, workID, true)
	operator := operatorVerdictActor(t, workID)

	// Advance past acceptance so complete is the declared action, then the
	// condition refuses the operator identity on the lane-executed item.
	reviewer := verdictReviewers[workID]
	reviewerRef, rerr := WorkflowActorRef(reviewer)
	if rerr != nil {
		t.Fatal(rerr)
	}
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRef, _ := WorkflowActorRef(operator)
	operatorRecorded := workflowEvent("operator-cond-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
	premise := workflowEventWithActor("operator-cond-premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	premiseCompleted := workflowActionCompletedFixture("operator-cond-premise-done-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
	_ = reviewerRef
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, premiseCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatalf("premise confirmation: %v", err)
	}
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"impact_verdict": "non-breaking"})
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64) + workID, IdempotencyIdentity: "operator-complete-cond-" + workID, OperationID: "operator-complete-cond-" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "operator-complete-cond-" + workID, RequestID: "request:operator-complete-cond-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(11, 0).UTC(),
	})
	_ = leaveFold(context.Background(), tx)
	if err == nil || !strings.Contains(err.Error(), "only after an in-session record_delivery exit") {
		t.Fatalf("operator complete without a delivery exit err=%v, want the condition refusal", err)
	}
}
