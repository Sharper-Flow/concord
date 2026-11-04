package store

import (
	"strings"
	"testing"
)

// removeFixturePremiseConfirmation leaves the dead-end state of CON-1701:
// every plan step complete, the delivery assertion recorded, and the active
// contract's premise confirmation missing.
func removeFixturePremiseConfirmation(t testing.TB, s *Store, workID string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_premise_confirmations WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
}

// TestPlanCompleteUnconfirmedItemHasConfirmationRoute reproduces CON-1701 and
// holds its end state: an item whose required steps are all complete but whose
// active contract is unconfirmed keeps the completion refusal, gains one
// admitted route to record the missing confirmation, and then completes.
func TestPlanCompleteUnconfirmedItemHasConfirmationRoute(t *testing.T) {
	t.Parallel()
	const id = "confirm-after-complete"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	s := route.store
	completeWorkflowPlanFixture(t, s, id)
	removeFixturePremiseConfirmation(t, s, id)

	plan, err := readWorkflowPlanTx(t.Context(), s.db, id)
	if err != nil || plan == nil || !WorkflowStepPlanComplete(*plan) {
		t.Fatalf("fixture plan is not complete: plan=%v err=%v", plan, err)
	}
	if frontier := WorkflowStepPlanFrontier(*plan); len(frontier) != 0 {
		t.Fatalf("completed plan still has a frontier: %v", frontier)
	}
	var confirmed int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=?`, id).Scan(&confirmed); err != nil || confirmed != 0 {
		t.Fatalf("fixture premise confirmation count=%d err=%v", confirmed, err)
	}

	// The gate stays: completion refuses the missing confirmation.
	completeErr := f.run(t.Context(), "complete", "complete-unconfirmed", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil)
	if !hasFailureKind(completeErr, KindMissingEvidence) || !strings.Contains(errDetail(completeErr), "the active contract premise is not confirmed") {
		t.Fatalf("unconfirmed completion refusal=%v, want the premise-confirmation gate", completeErr)
	}

	// The route: the completed plan admits confirm_premise on its declaring
	// step. Before the fix this is the dead end: no frontier step declares it.
	confirmErr := f.run(t.Context(), "confirm_premise", "confirm-after-complete", f.version(t.Context(), t), nil, nil)
	if confirmErr != nil {
		t.Fatalf("plan-complete premise confirmation refused: %v", confirmErr)
	}

	var active int64
	if err := s.db.QueryRow(`SELECT contract_version FROM workflow_premise_confirmations WHERE work_id=?`, id).Scan(&active); err != nil || active != 1 {
		t.Fatalf("recorded confirmation version=%d err=%v", active, err)
	}
	// The route closes once the confirmation is recorded: availability tracks
	// the missing confirmation, not the completed plan.
	repeatErr := f.run(t.Context(), "confirm_premise", "confirm-repeat", f.version(t.Context(), t), nil, nil)
	if !hasFailureKind(repeatErr, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(repeatErr), "action is not declared on a frontier step") {
		t.Fatalf("repeated plan-complete confirmation refusal=%v, want the frontier bound", repeatErr)
	}
	if err := f.run(t.Context(), "complete", "complete-confirmed", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil); err != nil {
		t.Fatalf("completion after confirmation refused: %v", err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("lifecycle=%q err=%v, want completed", lifecycle, err)
	}
}

// TestConfirmPremiseStaysFrontierBoundBeforePlanComplete pins the route's
// scope: before the plan completes, confirm_premise remains declared only on a
// frontier step that declares it, and a terminal instance admits nothing.
func TestConfirmPremiseStaysFrontierBoundBeforePlanComplete(t *testing.T) {
	t.Parallel()
	const id = "confirm-midplan"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "implement")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}

	err := f.run(t.Context(), "confirm_premise", "confirm-midplan", f.version(t.Context(), t), nil, nil)
	if !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(err), "action is not declared on a frontier step") {
		t.Fatalf("mid-plan confirmation refusal=%v, want the frontier bound", err)
	}
}

func errDetail(err error) string {
	if failure, ok := err.(*Failure); ok {
		return failure.Detail
	}
	return err.Error()
}
