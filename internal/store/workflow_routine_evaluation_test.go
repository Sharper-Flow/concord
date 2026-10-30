package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// routinePolicyDefinition copies a registered fixture definition.
func routinePolicyDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	return workflowFixtureDefinition(t, 2).Definition
}

// routineDefinitionOnPin copies the instance's own pinned definition.
func routineDefinitionOnPin(t *testing.T, s *Store, workID string) WorkflowDefinition {
	t.Helper()
	var ref string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("pinned definition %s is not registered", ref)
	}
	return registered.Definition
}

// seedRoutineActors confirms the delivered premise under a coordinator.
func seedRoutineActors(t *testing.T, s *Store, workID string) (reviewer WorkflowActor, coordinator WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	reviewer = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/independent-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
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
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES(?,1,?,'2026-01-01T00:00:00Z'); DELETE FROM write_guard`, workID, refs[2]); err != nil {
		t.Fatal(err)
	}
	return reviewer, coordinator
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

	t.Run("new policy admits repair request without operator identity", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-correction"
		fixture := prepareVerdictCorrectionFixture(t, workID)
		g := routineGuardContext(t, fixture.store, workID, "request_correction")
		if err := guardCorrectionRecovery(g); err != nil {
			t.Fatalf("consequence_v1 repair request without an operator identity refused: %v", err)
		}
	})

	t.Run("unbound evidence refuses under new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-unbound-evidence"
		s := seedDeliveredItemAtAcceptance(t, workID)
		reviewer, _ := seedRoutineActors(t, s, workID)
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
	t.Run("new policy keeps verdict gates for coordinator completion", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-new-completion-gates"
		f := qBuildAcceptance(t, workID, "", false)
		s := f.store
		_, coordinator := seedRoutineActors(t, s, workID)
		setWorkflowStepForOperatorVerdictTest(t, s, workID, "release")
		bindCompletionClosure(t, s, workID, coordinator)
		version := readWorkVersion(t, s, workID)
		ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execution := WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "complete", Payload: json.RawMessage(`{"impact_verdict":"non-breaking"}`), Actor: coordinator,
			OperationID: "routine-complete-" + workID, Now: time.Unix(11, 0).UTC(),
		}
		_, err = applyCompleteWorkflowActionTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), execution, ownerRefForTest(t, coordinator), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict is missing") {
			t.Fatalf("completion without a verdict returned %v, want the missing-verdict clause refusal", err)
		}
	})
}

// routineGuardContext builds a direct guard invocation over one seeded store.
func routineGuardContext(t *testing.T, s *Store, workID, actionID string) *workflowActionGuardContext {
	t.Helper()
	ctx := context.Background()
	var ref string
	var currentStep string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &currentStep); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("pinned definition %s is not registered", ref)
	}
	definition := registered.Definition
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
		request:  WorkflowActionExecutionRequest{WorkID: workID, ActionID: actionID},
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
