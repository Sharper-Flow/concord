package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// The guard table is the declared inventory of action-specific guards in the
// workflow action dispatcher. These tests bind the declaration to the actions
// that carry guards, the same way workflow_event_shape_test.go binds the
// declared event shapes to the dispatcher's control flow. A new guard is a
// deliberate declaration: adding one without extending this inventory, or
// extending the inventory without a guard, fails here first.

// guardedActions is the closed set of actions that carry an action-specific
// guard in applyWorkflowActionRawTx. Two guards are deliberately absent,
// because each applies to every action and the dispatcher therefore calls it
// directly: guardOperatorPremiseActor, since an operator actor is valid nowhere
// except premise confirmation, and guardRecordedActorTuple, since any declared
// action is some session's possible first action (issue #740).
var guardedActions = map[string]workflowActionGuardPhase{
	"supersede_contract":     guardPhaseRecovery,
	"complete":               guardPhaseBoundary,
	"link_successor":         guardPhasePostValidation,
	"cross_context_boundary": guardPhaseClaim,
	"record_delivery":        guardPhaseClaim,
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

func seedMandateRecoveryItem(t *testing.T, workID string) *Store {
	t.Helper()
	s, _ := seedItemAtAcceptance(t, workID)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,'spec:one',?)`, workID, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	return s
}

func recoveryBindPayload() json.RawMessage {
	return json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}`)
}

func TestBindEvidenceAdmittedPastBindingStepWhileMandateUnbound(t *testing.T) {
	const workID = "mandate-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("late mandate binding refused: %v", err)
	}
}

func TestBindEvidenceRefusedPastBindingStepOnceMandateBound(t *testing.T) {
	const workID = "mandate-recovery-narrow"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("initial mandate binding refused: %v", err)
	}
	err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0)
	if err == nil {
		t.Fatal("late binding passed after the mandate was bound")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("late binding error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

func TestRecordVerdictPassesAfterRecoveryBind(t *testing.T) {
	const workID = "mandate-recovery-verdict"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(), 0); err != nil {
		t.Fatalf("mandate recovery binding refused: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","evaluation_evidence":["spec:one"]}`), 0); err != nil {
		t.Fatalf("verdict after mandate recovery binding refused: %v", err)
	}
}
