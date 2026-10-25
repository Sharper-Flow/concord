package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The evaluation authority owner keeps independent evaluation and completion
// recording separate. Under consequence_v1 there is no operator substitution
// for verdicts, completion, or repair requests: the independent evaluator
// invokes record_verdict under its own authenticated identity, the coordinator
// completes without an operator actor, and a correction request is a repair
// request, not an evaluation. Every retained gate — executor refusal, worker
// refusal, evidence binding, model distinctness, operator-typed premise
// confirmation — must hold. New-policy owners are exercised through the
// synthetic definition value (fixture shape plus the discriminator); no
// unreleased builtin is registered, so builtin-only end-to-end pins wait for
// the promotion slice.

// routinePolicyDefinition copies a registered fixture definition and declares
// consequence_v1 on the copy. The registry keeps the legacy version; only the
// pure and transactional owners under test see the declared policy.
func routinePolicyDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	definition := workflowFixtureDefinition(t, 2).Definition
	definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	return definition
}

// routineDefinitionOnPin copies the instance's own pinned definition and
// declares consequence_v1 on the copy, so admission derivations see a shape
// that matches the seeded instance.
func routineDefinitionOnPin(t *testing.T, s *Store, workID string) WorkflowDefinition {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s v%d is not registered", ref, version)
	}
	definition := registered.Definition
	definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	return definition
}

// seedRoutineActors records an independent reviewer, an operator, and a
// non-executing coordinator on a delivered seed, and confirms the premise with
// the operator identity the legacy clause 5 requirement keeps demanding.
func seedRoutineActors(t *testing.T, s *Store, workID string) (reviewer WorkflowActor, operator WorkflowActor, coordinator WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	reviewer = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/independent-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	operator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	coordinator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
	refs := make([]string, 3)
	for index, actor := range []WorkflowActor{reviewer, operator, coordinator} {
		ref, err := WorkflowActorRef(actor)
		if err != nil {
			t.Fatal(err)
		}
		refs[index] = ref
	}
	version := readWorkVersion(t, s, workID)
	setup := []Event{
		workflowEventWithActor("routine-reviewer-"+workID, WorkflowActorRecorded, workID, refs[0], map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": refs[0], "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("routine-operator-"+workID, WorkflowActorRecorded, workID, refs[1], map[string]any{"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "actor_ref": refs[1], "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"}),
		workflowEventWithActor("routine-coordinator-"+workID, WorkflowActorRecorded, workID, refs[2], map[string]any{"work_id": workID, "expected_version": version + 2, "resulting_version": version + 3, "actor_ref": refs[2], "principal_ref": coordinator.PrincipalRef, "client_ref": coordinator.ClientRef, "agent_ref": coordinator.AgentRef, "session_ref": coordinator.SessionRef, "actor_class": "agent"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES(?,1,?,'2026-01-01T00:00:00Z'); DELETE FROM write_guard`, workID, refs[1]); err != nil {
		t.Fatal(err)
	}
	return reviewer, operator, coordinator
}

// foldRoutineVerdict folds one ok verdict from the independent evaluator so
// the completion clause 4 gate has its witness.
func foldRoutineVerdict(t *testing.T, s *Store, workID string, reviewer WorkflowActor) {
	t.Helper()
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	event := workflowEventWithActor("routine-verdict-"+workID, WorkflowVerdictRecorded, workID, reviewerRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "evaluation_evidence": []string{"evidence:operator-verdict"}, "incomparable_with_approved": false})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("fold independent verdict: %v", err)
	}
}

func TestRoutineEvaluation(t *testing.T) {
	t.Parallel()

	t.Run("new policy refuses operator-substituted verdicts", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-verdict-substitution"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		operator := operatorVerdictActor(t, workID)
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:operator-verdict"]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperatorActor: &operator, OperationID: "routine-verdict-" + workID}, "actor:owner", fields, "routine-verdict-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "accepts no operator-signing substitute") {
			t.Fatalf("consequence_v1 verdict with an operator actor returned %v, want the no-substitution refusal", err)
		}
	})

	t.Run("new policy refuses operator-substituted completion", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-completion-substitution"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		_, operator, coordinator := seedRoutineActors(t, s, workID)
		setWorkflowStepForOperatorVerdictTest(t, s, workID, "release")
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execution := WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: json.RawMessage(`{"impact_verdict":"non-breaking"}`), Actor: coordinator, OperatorActor: &operator,
			OperationID: "routine-complete-" + workID, Now: time.Unix(11, 0).UTC(),
		}
		_, err = applyCompleteWorkflowActionTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), RegisteredDefinition{Definition: routinePolicyDefinition(t)}, execution, ownerRefForTest(t, coordinator), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "consequence_v1 takes no operator substitution") {
			t.Fatalf("consequence_v1 completion with an operator actor returned %v, want the no-substitution refusal", err)
		}
	})

	t.Run("worker authority refuses evaluation", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-worker-verdict"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		reviewerRef := ownerRefForTest(t, reviewer)
		lane := BuiltinLaneDefinitions()[0]
		dispatch := workerDispatchEvent(workID, "routine-dispatch-"+workID, lane, map[string]any{"attempt_id": "attempt:" + workID, "lane_actor_ref": reviewerRef})
		dispatch.Actor = reviewerRef
		dispatch.OccurredAt = time.Unix(9, 30).UTC()
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
			t.Fatalf("seed worker dispatch: %v", err)
		}
		var authority WorkflowEvaluationAuthority
		err := s.readSnapshot(context.Background(), func(q queryer) error {
			var innerErr error
			authority, innerErr = workflowEvaluationAuthority(context.Background(), q, workID, reviewerRef)
			return innerErr
		})
		if err != nil {
			t.Fatal(err)
		}
		if authority != WorkflowWorkerEvaluator {
			t.Fatalf("dispatched reviewer authority = %q, want worker", authority)
		}
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"predicate_id": json.RawMessage(`"predicate:primary"`), "verdict_kind": json.RawMessage(`"ok"`), "evaluation_evidence": json.RawMessage(`["evidence:operator-verdict"]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperationID: "routine-worker-" + workID}, reviewerRef, fields, "routine-worker-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized {
			t.Fatalf("worker-authority verdict returned %v, want the unauthorized refusal", err)
		}
	})

	t.Run("new policy admits repair request without operator identity", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-correction"
		fixture := prepareVerdictCorrectionFixture(t, workID)
		g := routineGuardContext(t, fixture.store, workID, "request_correction", nil, true)
		if err := guardOperatorPremiseActor(g); err != nil {
			t.Fatalf("consequence_v1 repair request without an operator identity refused: %v", err)
		}
	})

	t.Run("new policy rejects supplied operator identity on substitution actions", func(t *testing.T) {
		t.Parallel()
		for _, actionID := range []string{"record_verdict", "complete", "request_correction"} {
			const workID = "routine-new-operator-claim"
			fixture := prepareVerdictCorrectionFixture(t, workID)
			g := routineGuardContext(t, fixture.store, workID, actionID, &fixture.operator, true)
			err := guardOperatorPremiseActor(g)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "consequence_v1 takes no operator substitution") {
				t.Fatalf("consequence_v1 %s with a supplied operator identity returned %v, want the no-substitution refusal", actionID, err)
			}
		}
	})

	t.Run("premise confirmation keeps its operator identity under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-premise"
		fixture := prepareVerdictCorrectionFixture(t, workID)
		g := routineGuardContext(t, fixture.store, workID, "confirm_premise", &fixture.operator, true)
		if err := guardOperatorPremiseActor(g); err != nil {
			t.Fatalf("consequence_v1 premise confirmation with the verified operator identity refused: %v", err)
		}
	})

	t.Run("unbound evidence refuses under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-unbound-evidence"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _, _ := seedRoutineActors(t, s, workID)
		reviewerRef := ownerRefForTest(t, reviewer)
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		fields := map[string]json.RawMessage{"verdicts": json.RawMessage(`[{"predicate_id":"predicate:primary","receipt_refs":["evidence:not-bound"]}]`)}
		_, err = workflowRecordVerdictEvents(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: workID, OperationID: "routine-unbound-" + workID}, reviewerRef, fields, "routine-unbound-"+workID, version)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("unbound evaluation evidence returned %v, want the missing-evidence refusal", err)
		}
	})
	t.Run("seam admits the executing coordinator under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-executing-completion"
		s, owner := seedDeliveredItemAtAcceptance(t, workID)
		seedRoutineActors(t, s, workID)
		coordinatorRef := ownerRefForTest(t, owner)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := workflowCompletionCallerAuthority(context.Background(), tx, workID, coordinatorRef); err != nil {
			t.Fatalf("consequence_v1 completion authority refused the initiating coordinator: %v", err)
		}
	})

	t.Run("seam refuses worker completion authority under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-worker-completion"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		_, _, coordinator := seedRoutineActors(t, s, workID)
		coordinatorRef := ownerRefForTest(t, coordinator)
		lane := BuiltinLaneDefinitions()[0]
		dispatch := workerDispatchEvent(workID, "routine-completion-dispatch-"+workID, lane, map[string]any{"attempt_id": "attempt:" + workID + "-completion", "lane_actor_ref": coordinatorRef})
		dispatch.Actor = coordinatorRef
		dispatch.OccurredAt = time.Unix(9, 45).UTC()
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
			t.Fatalf("seed completion-worker dispatch: %v", err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = workflowCompletionCallerAuthority(context.Background(), tx, workID, coordinatorRef)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "a worker cannot accept results, associate criterion proof, or complete work") {
			t.Fatalf("worker-authority completion returned %v, want the worker refusal", err)
		}
	})

	t.Run("new policy keeps verdict gates for coordinator completion", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-completion-gates"
		s, _ := seedDeliveredItemAtAcceptance(t, workID)
		_, _, coordinator := seedRoutineActors(t, s, workID)
		setWorkflowStepForOperatorVerdictTest(t, s, workID, "release")
		version := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execution := WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: json.RawMessage(`{"impact_verdict":"non-breaking"}`), Actor: coordinator,
			OperationID: "routine-complete-" + workID, Now: time.Unix(11, 0).UTC(),
		}
		_, err = applyCompleteWorkflowActionTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), RegisteredDefinition{Definition: workflowFixtureDefinition(t, 2).Definition}, execution, ownerRefForTest(t, coordinator), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict is missing") {
			t.Fatalf("completion without a verdict returned %v, want the missing-verdict clause refusal", err)
		}
	})
}

// routineGuardContext builds a direct guard invocation over one seeded store:
// the entry carries the instance's pinned definition with consequence_v1
// declared when requested, and the current step comes from the instance.
func routineGuardContext(t *testing.T, s *Store, workID, actionID string, operator *WorkflowActor, newPolicy bool) *workflowActionGuardContext {
	t.Helper()
	ctx := context.Background()
	var ref string
	var version int64
	var currentStep string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version, &currentStep); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s v%d is not registered", ref, version)
	}
	definition := registered.Definition
	if newPolicy {
		definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The guard reads one read-mostly snapshot; rolling the transaction back
	// releases the pool's sole connection for the next fixture.
	t.Cleanup(func() { _ = tx.Rollback() })
	return &workflowActionGuardContext{
		ctx: ctx, tx: tx, currentStep: currentStep,
		entry:    RegisteredDefinition{Definition: definition},
		request:  WorkflowActionExecutionRequest{WorkID: workID, ActionID: actionID, OperatorActor: operator},
		actorRef: ownerRefForTest(t, owner),
	}
}

func ownerRefForTest(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
