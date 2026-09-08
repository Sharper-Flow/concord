package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The session remains the executing actor after an in-session delivery.
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
	// No lane or attempt window exists for this delivery.
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
	var evidenceRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='verification' ORDER BY seq DESC LIMIT 1`, workID, WorkflowEvidenceBound).Scan(&evidenceRef); err != nil {
		return err
	}
	return runOperatorVerdictWithEvidence(t, s, workID, owner, operator, evidenceRef)
}

func runOperatorVerdictWithEvidence(t *testing.T, s *Store, workID string, owner, operator WorkflowActor, evidenceRef string) error {
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
	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{evidenceRef}})
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

func operatorVerdictExitCheck(t *testing.T, s *Store, workID string) error {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return requireOperatorVerdictExit(context.Background(), tx, workID)
}

func TestOperatorVerdictAfterAcceptedWorkerResultPassesDistinctness(t *testing.T) {
	const workID = "operator-verdict-accepted-worker"
	s, owner := seedItemAtAcceptance(t, workID, true)
	operator := operatorVerdictActor(t, workID)

	if err := runOperatorVerdictWithEvidence(t, s, workID, owner, operator, "attempt:"+workID); err != nil {
		t.Fatalf("operator verdict after accepted worker result refused: %v", err)
	}
	operatorRef, _ := WorkflowActorRef(operator)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := workflowCompletionActorDistinct(context.Background(), tx, workID, operatorRef, "", false); err != nil {
		t.Fatalf("completion distinctness refused the operator verdict: %v", err)
	}
}

func TestOperatorVerdictConditionAcceptsOnlyDeliveryOrAcceptedWorkerResult(t *testing.T) {
	noExit := openTemp(t)
	seedWork(t, noExit, "operator-verdict-no-exit")
	seedWorkflowLaw(t, noExit)
	if err := operatorVerdictExitCheck(t, noExit, "operator-verdict-no-exit"); err == nil {
		t.Fatal("operator verdict condition accepted a workflow without an allowed exit")
	}

	accepted, _ := seedItemAtAcceptance(t, "operator-verdict-accepted-exit", true)
	if err := operatorVerdictExitCheck(t, accepted, "operator-verdict-accepted-exit"); err != nil {
		t.Fatalf("operator verdict condition refused an accepted worker result: %v", err)
	}

	delivered, _ := seedDeliveredItemAtAcceptance(t, "operator-verdict-delivery-exit")
	if err := operatorVerdictExitCheck(t, delivered, "operator-verdict-delivery-exit"); err != nil {
		t.Fatalf("operator verdict condition refused a delivery exit: %v", err)
	}
}

func TestAgentVerdictAfterAcceptedWorkerResultStillRequiresDistinctActor(t *testing.T) {
	const workID = "agent-verdict-accepted-worker"
	s, _ := seedItemAtAcceptance(t, workID, true)

	var laneRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&laneRef); err != nil {
		t.Fatal(err)
	}
	var lane WorkflowActor
	if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, laneRef).Scan(&lane.PrincipalRef, &lane.ClientRef, &lane.AgentRef, &lane.SessionRef, &lane.ActorClass); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"predicate_id":"predicate:primary","verdict_kind":"ok","evaluation_evidence":["attempt:` + workID + `"]}`)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, lane); err == nil || !strings.Contains(err.Error(), "evaluate") {
		t.Fatalf("worker agent verdict err=%v, want the distinct-actor refusal", err)
	}
}

func TestOperatorVerdictAfterDeliveryPassesDistinctness(t *testing.T) {
	testOperatorVerdictAfterDelivery(t, seedDeliveredItemAtAcceptance)
}

func TestOperatorVerdictAfterLanePassesDistinctness(t *testing.T) {
	testOperatorVerdictAfterDelivery(t, func(t *testing.T, id string) (*Store, WorkflowActor) {
		return seedItemAtAcceptance(t, id, true)
	})
}

func testOperatorVerdictAfterDelivery(t *testing.T, seed func(*testing.T, string) (*Store, WorkflowActor)) {
	t.Helper()
	const workID = "operator-verdict-delivery"
	s, owner := seed(t, workID)
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

func TestOperatorCompleteAfterDeliveryPassesDistinctness(t *testing.T) {
	testOperatorCompleteAfterDelivery(t, seedDeliveredItemAtAcceptance)
}

func TestOperatorCompleteAfterLanePassesDistinctness(t *testing.T) {
	testOperatorCompleteAfterDelivery(t, func(t *testing.T, id string) (*Store, WorkflowActor) {
		return seedItemAtAcceptance(t, id, true)
	})
}

func testOperatorCompleteAfterDelivery(t *testing.T, seed func(*testing.T, string) (*Store, WorkflowActor)) {
	t.Helper()
	const workID = "operator-complete-delivery"
	s, owner := seed(t, workID)
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

// Completion still requires a recorded verdict after an accepted worker
// result, even though the operator identity is an allowed verdict actor.
func TestOperatorCompleteConditionBinds(t *testing.T) {
	const workID = "operator-complete-lane"
	s, owner := seedItemAtAcceptance(t, workID, true)
	operator := operatorVerdictActor(t, workID)

	// Advance past acceptance so complete is the declared action.
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRef, _ := WorkflowActorRef(operator)
	operatorRecorded := workflowEvent("operator-cond-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
	premise := workflowEventWithActor("operator-cond-premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	premiseCompleted := workflowActionCompletedFixture("operator-cond-premise-done-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
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
	if err == nil || !strings.Contains(err.Error(), "workflow verdict is missing") {
		t.Fatalf("operator complete without a verdict err=%v, want the verdict refusal", err)
	}
}

func TestOperatorEvaluationRequiresDeliveryExit(t *testing.T) {
	for _, completedWorker := range []bool{false, true} {
		name := "no_delivery"
		if completedWorker {
			name = "completed_worker_not_accepted"
		}
		t.Run(name, func(t *testing.T) {
			const workID = "operator-undelivered"
			var s *Store
			if completedWorker {
				s, _, _, _ = seedCompletedWorkerAtExecution(t, workID)
			} else {
				s = openTemp(t)
				seedWork(t, s, workID)
			}
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := requireOperatorVerdictExit(context.Background(), tx, workID); err == nil || !strings.Contains(err.Error(), "requires a record_delivery or accept_worker_result exit") {
				t.Fatalf("operator evaluation without a delivery exit err=%v", err)
			}
		})
	}
}

// #909: a host restart mints a new session identity, so the completing
// action can be the first place that tuple appears. The complete path must
// land the guard's actor-recording events instead of dropping them; before
// the fix this refused with "workflow actor reference is not recorded".
func TestCompleteRecordsAFirstSeenActorTuple(t *testing.T) {
	const workID = "complete-first-seen-actor"
	s, owner := seedDeliveredItemAtAcceptance(t, workID)
	operator := operatorVerdictActor(t, workID)

	if err := runOperatorVerdict(t, s, workID, owner, operator); err != nil {
		t.Fatalf("operator verdict after delivery refused: %v", err)
	}
	operatorRef, _ := WorkflowActorRef(operator)
	premiseVersion := verdictItemVersion(t, s, workID)
	operatorRecorded := workflowEvent("first-seen-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": premiseVersion, "resulting_version": premiseVersion + 1, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
	premise := workflowEventWithActor("first-seen-premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": premiseVersion + 1, "resulting_version": premiseVersion + 2, "contract_version": 1, "confirming_actor_ref": operatorRef})
	premiseCompleted := workflowActionCompletedFixture("first-seen-premise-done-"+workID, workID, operatorRef, premiseVersion+2, "acceptance", "confirm_premise")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{operatorRecorded, premise, premiseCompleted}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): premiseVersion}}); err != nil {
		t.Fatalf("premise confirmation: %v", err)
	}

	// The restarted session: same principal and client, new agent and
	// session identity, never recorded on this work item before.
	restarted := WorkflowActor{PrincipalRef: owner.PrincipalRef, ClientRef: owner.ClientRef, AgentRef: "agent/owner-restarted", SessionRef: "session/" + workID + "-restarted", ActorClass: ActorAgent}
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
		WorkID: workID, ExpectedVersion: completeVersion, ActionID: "complete", Payload: payload, Actor: restarted,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + workID, IdempotencyIdentity: "first-seen-complete-" + workID, OperationID: "first-seen-complete-" + workID,
		PrincipalRef: restarted.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "first-seen-complete-" + workID, RequestID: "request:first-seen-complete-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(13, 0).UTC(),
	})
	if err != nil {
		_ = leaveFold(context.Background(), tx)
		t.Fatalf("complete from a first-seen actor tuple refused: %v", err)
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
	restartedRef, _ := WorkflowActorRef(restarted)
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, restartedRef).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("restarted actor recordings=%d, want 1", recorded)
	}
}

// #911: the premise confirmation is the last step where record_verdict is
// declared, so it refuses while any approved predicate lacks a verdict
// instead of letting the item wedge at complete.
func TestConfirmPremiseRequiresEveryPredicateVerdict(t *testing.T) {
	const workID = "confirm-all-predicates"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/"+workID)
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/"+workID+"-operator")
	version := int64(2)
	events := []Event{
		workflowEvent("confirm-all-executor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/" + workID, "actor_class": "agent"}),
		workflowEvent("confirm-all-operator", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/" + workID + "-operator", "actor_class": "operator"}),
		workflowEvent("confirm-all-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": version + 2, "resulting_version": version + 3, "ref": workflowFixtureRef, "version": 1, "digest": workflowFixtureDigest(t), "work_kind": workflowFixtureWorkKind}),
		workflowEventWithActor("confirm-all-contract", WorkflowContractApproved, workID, executor, map[string]any{"work_id": workID, "expected_version": version + 3, "resulting_version": version + 4, "contract_version": 1, "premise": "deliver both required end states", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:confirm-all", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "outcome_predicates": []map[string]any{{"predicate_id": "predicate:present", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "surface:one", "subjects": []string{"subject:one"}}}, {"predicate_id": "predicate:absent", "ordinal": 1, "outcome_kind": "absent", "outcome_payload": map[string]any{"kind": "absent", "surface": "surface:two", "subjects": []string{"subject:two"}, "distinguish_from": []string{"archived"}}}}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("confirm-all-start", WorkflowActionStarted, workID, executor, map[string]any{"work_id": workID, "expected_version": version + 4, "resulting_version": version + 5, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "confirm-all-operation", "actor_ref": executor}),
		workflowEvent("confirm-all-verification", WorkflowEvidenceBound, workID, map[string]any{"work_id": workID, "expected_version": version + 5, "resulting_version": version + 6, "evidence_kind": "verification", "immutable_subject_ref": "evidence:confirm-all-verification", "producer_id": "principal/verify", "producer_run_ref": "confirm-all-verification", "producer_watermark": "request/confirm-all-verification", "observed_at": "2026-09-07T00:00:00Z"}),
		workflowEvent("confirm-all-review", WorkflowEvidenceBound, workID, map[string]any{"work_id": workID, "expected_version": version + 6, "resulting_version": version + 7, "evidence_kind": "review", "immutable_subject_ref": "evidence:confirm-all-review", "producer_id": "principal/review", "producer_run_ref": "confirm-all-review", "producer_watermark": "request/confirm-all-review", "observed_at": "2026-09-07T00:00:00Z"}),
		workflowEventWithActor("confirm-all-one-verdict", WorkflowVerdictRecorded, workID, operator, map[string]any{"work_id": workID, "expected_version": version + 7, "resulting_version": version + 8, "contract_version": 1, "predicate_id": "predicate:present", "verdict_kind": "ok", "verdict_actor_ref": operator, "evaluation_evidence": []string{"evidence:confirm-all-verification"}, "incomparable_with_approved": false}),
	}
	seedWorkflowAuthority(t, s, "confirm-all-verification", workID, "principal/verify", "request/confirm-all-verification", []string{"evidence:confirm-all-verification"})
	seedWorkflowAuthority(t, s, "confirm-all-review", workID, "principal/review", "request/confirm-all-review", []string{"evidence:confirm-all-review"})
	events[3].PayloadVersion = 3
	events[7].PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	missing, err := missingPredicateVerdicts(context.Background(), tx, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "predicate:absent" {
		t.Fatalf("missing predicates=%v, want [predicate:absent]", missing)
	}
	_ = leaveFold(context.Background(), tx)
}
