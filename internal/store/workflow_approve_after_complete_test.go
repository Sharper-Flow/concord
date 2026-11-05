package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// removeFixtureWorkflowContract leaves the CON-1714 dead end: every plan step
// complete, the delivery assertion recorded, and no workflow contract at all.
// The contract's dependent rows go first: the premise confirmation, then the
// architecture binding's detail rows, then the binding itself; every foreign
// key on the chain restricts the deletion otherwise.
func removeFixtureWorkflowContract(t testing.TB, s *Store, workID string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_premise_confirmations WHERE work_id=?; DELETE FROM workflow_contract_affected_domains WHERE work_id=?; DELETE FROM workflow_contract_domain_modifications WHERE work_id=?; DELETE FROM workflow_contract_domain_relation_modifications WHERE work_id=?; DELETE FROM workflow_contract_law_additions WHERE work_id=?; DELETE FROM workflow_contract_verification_obligations WHERE work_id=?; DELETE FROM workflow_contract_law_modifications WHERE work_id=?; DELETE FROM workflow_law_addition_reservations WHERE owner_work_id=?; DELETE FROM workflow_architecture_bindings WHERE work_id=?; DELETE FROM workflow_contracts WHERE work_id=?; DELETE FROM write_guard`, workID, workID, workID, workID, workID, workID, workID, workID, workID, workID); err != nil {
		t.Fatal(err)
	}
}

// TestPlanCompleteContractlessItemHasApprovalRoute reproduces CON-1714 and
// holds its end state: an item whose required steps are all complete but that
// carries no workflow contract keeps the completion refusal, gains one
// admitted route to approve the initial contract, then confirms the premise
// and completes.
func TestPlanCompleteContractlessItemHasApprovalRoute(t *testing.T) {
	t.Parallel()
	const id = "approve-after-complete"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanRemoval, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	s := route.store
	completeWorkflowPlanFixture(t, s, id)
	removeFixtureWorkflowContract(t, s, id)

	plan, err := readWorkflowPlanTx(t.Context(), s.db, id)
	if err != nil || plan == nil || !WorkflowStepPlanComplete(*plan) {
		t.Fatalf("fixture plan is not complete: plan=%v err=%v", plan, err)
	}
	if frontier := WorkflowStepPlanFrontier(*plan); len(frontier) != 0 {
		t.Fatalf("completed plan still has a frontier: %v", frontier)
	}
	var contracts int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, id).Scan(&contracts); err != nil || contracts != 0 {
		t.Fatalf("fixture contract count=%d err=%v, want none", contracts, err)
	}

	// The gate stays: completion refuses the missing contract.
	completeErr := f.run(t.Context(), "complete", "complete-contractless", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil)
	if !hasFailureKind(completeErr, KindInvariantViolation) || !strings.Contains(errDetail(completeErr), "approved workflow contract is missing or ambiguous") {
		t.Fatalf("contractless completion refusal=%v, want the contract gate", completeErr)
	}

	// The route: the completed plan admits approve_contract on its declaring
	// step. Before the fix this is the dead end: no frontier step declares it.
	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "approve_contract") {
		t.Fatalf("completed-plan pin intents omit approve_contract: %v", pin.NextValidIntents)
	}
	approval := json.RawMessage(`{"premise":"The removed fixture family keeps a covering check.","decision_domains":[],"legislative_subjects":[],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
	if approveErr := f.run(t.Context(), "approve_contract", "approve-after-complete", f.version(t.Context(), t), approval, nil); approveErr != nil {
		t.Fatalf("plan-complete contract approval refused: %v", approveErr)
	}

	var version int64
	if err := s.db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, id).Scan(&version); err != nil || version != 1 {
		t.Fatalf("approved contract version=%d err=%v, want 1", version, err)
	}

	// The route closes once the contract exists: a repeated approval is
	// refused the way any off-frontier action is.
	repeatErr := f.run(t.Context(), "approve_contract", "approve-repeat", f.version(t.Context(), t), approval, nil)
	if !hasFailureKind(repeatErr, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(repeatErr), "action is not declared on a frontier step") {
		t.Fatalf("repeated plan-complete approval refusal=%v, want the frontier bound", repeatErr)
	}

	// The premise confirms on the same completed-plan route, and the item
	// completes through the normal gate.
	if confirmErr := f.run(t.Context(), "confirm_premise", "confirm-after-approve", f.version(t.Context(), t), nil, nil); confirmErr != nil {
		t.Fatalf("plan-complete premise confirmation refused: %v", confirmErr)
	}
	if err := f.run(t.Context(), "complete", "complete-contracted", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil); err != nil {
		t.Fatalf("completion after approval refused: %v", err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("lifecycle=%q err=%v, want completed", lifecycle, err)
	}
}

// TestPlanCompleteDeliverylessItemHasDeliveryRoute reproduces the recurring
// trap where recording the final review closes the step that admits
// record_delivery: the completed plan keeps the first delivery recording
// admissible on that step, and the owner records delivery without inserting
// an extra step.
func TestPlanCompleteDeliverylessItemHasDeliveryRoute(t *testing.T) {
	t.Parallel()
	const id = "deliver-after-complete"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanRemoval, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	s := route.store
	completeWorkflowPlanFixture(t, s, id)
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_delivery_state WHERE work_id=?; DELETE FROM write_guard`, id); err != nil {
		t.Fatal(err)
	}

	// The gate stays: completion refuses the missing delivery assertion.
	completeErr := f.run(t.Context(), "complete", "complete-deliveryless", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil)
	if !hasFailureKind(completeErr, KindMissingEvidence) || !strings.Contains(errDetail(completeErr), "delivery assertion is missing") {
		t.Fatalf("deliveryless completion refusal=%v, want the delivery gate", completeErr)
	}

	// The pin advertises the same route admission serves.
	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("completed-plan pin intents omit record_delivery: %v", pin.NextValidIntents)
	}

	// The route: the completed plan admits the first delivery recording on
	// the review step that closed with the final review.
	delivery := json.RawMessage(`{"delivery_artifact":"https://example.test/delivery/approve-after-complete","delivery_state":"asserted"}`)
	if deliveryErr := f.run(t.Context(), "record_delivery", "deliver-after-complete", f.version(t.Context(), t), delivery, nil); deliveryErr != nil {
		t.Fatalf("plan-complete delivery recording refused: %v", deliveryErr)
	}
	var delivered int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_delivery_state WHERE work_id=?`, id).Scan(&delivered); err != nil || delivered != 1 {
		t.Fatalf("delivery assertion count=%d err=%v, want 1", delivered, err)
	}
	if err := f.run(t.Context(), "complete", "complete-delivered", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil); err != nil {
		t.Fatalf("completion after delivery refused: %v", err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("lifecycle=%q err=%v, want completed", lifecycle, err)
	}
}

// TestApproveContractStaysFrontierBoundBeforePlanComplete pins the approval
// route's scope: before the plan completes, approve_contract is admitted only
// on a frontier step that declares it.
func TestApproveContractStaysFrontierBoundBeforePlanComplete(t *testing.T) {
	t.Parallel()
	const id = "approve-midplan"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanRemoval, "implement")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	removeFixtureWorkflowContract(t, route.store, id)

	err := f.run(t.Context(), "approve_contract", "approve-midplan", f.version(t.Context(), t), json.RawMessage(`{"premise":"The removed fixture family keeps a covering check.","decision_domains":[],"legislative_subjects":[],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`), nil)
	if !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(err), "action is not declared on a frontier step") {
		t.Fatalf("mid-plan approval refusal=%v, want the frontier bound", err)
	}
}
