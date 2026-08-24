package store

import (
	"testing"
)

// The guard table is the declared inventory of action-specific guards in the
// workflow action dispatcher. These tests bind the declaration to the actions
// that carry guards, the same way workflow_event_shape_test.go binds the
// declared event shapes to the dispatcher's control flow. A new guard is a
// deliberate declaration: adding one without extending this inventory, or
// extending the inventory without a guard, fails here first.

// guardedActions is the closed set of actions that carry an action-specific
// guard in applyWorkflowActionRawTx. confirm_premise's operator-actor
// constraint is deliberately absent: it applies to every action (an operator
// actor is valid nowhere except premise confirmation), so it lives in
// guardOperatorPremiseActor rather than the per-action table.
var guardedActions = map[string]workflowActionGuardPhase{
	"supersede_contract":     guardPhaseRecovery,
	"complete":               guardPhaseBoundary,
	"link_successor":         guardPhasePostValidation,
	"record_verdict":         guardPhaseActor,
	"cross_context_boundary": guardPhaseClaim,
}

func TestEveryGuardTableEntryIsARegisteredAction(t *testing.T) {
	for actionID := range workflowActionGuards {
		if _, ok := builtinActionPolicies[actionID]; !ok {
			t.Errorf("guard table names %q, which is not a registered action", actionID)
		}
	}
}

func TestGuardTableMatchesTheGuardedActionInventory(t *testing.T) {
	if len(workflowActionGuards) != len(guardedActions) {
		t.Fatalf("guard table has %d entries, want %d", len(workflowActionGuards), len(guardedActions))
	}
	for actionID, phase := range guardedActions {
		guard, ok := workflowActionGuards[actionID]
		if !ok {
			t.Errorf("%q carries a guard in the dispatcher but is absent from workflowActionGuards", actionID)
			continue
		}
		if guard.phase != phase {
			t.Errorf("%q is declared in phase %d, want %d", actionID, guard.phase, phase)
		}
		if guard.run == nil {
			t.Errorf("%q declares a nil guard function", actionID)
		}
	}
	for actionID := range workflowActionGuards {
		if _, ok := guardedActions[actionID]; !ok {
			t.Errorf("workflowActionGuards declares %q, which the guarded-action inventory does not list", actionID)
		}
	}
}

func TestOperatorActorIsRejectedOutsidePremiseConfirmation(t *testing.T) {
	g := &workflowActionGuardContext{
		request: WorkflowActionExecutionRequest{
			ActionID: "record_proposal",
			OperatorActor: &WorkflowActor{
				ActorClass: ActorOperator,
			},
		},
	}
	err := guardOperatorPremiseActor(g)
	if err == nil {
		t.Fatal("operator actor on a non-premise action passed, want unauthorized")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("operator actor outside premise confirmation returned %v, want KindUnauthorized", err)
	}
}
