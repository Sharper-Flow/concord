package store

import (
	"strings"
	"testing"
)

// TestPlanCompleteItemStillBindsEvidence holds the engine contract (§4.2):
// ordinary bind_evidence stays available after the workflow passes a step
// declaring it, under one active contract. A completed plan has no frontier
// step, yet evidence that exists only after publication (merged commit, CI
// run) must still bind through actual dispatch, repeatedly, without blocking
// completion.
func TestPlanCompleteItemStillBindsEvidence(t *testing.T) {
	t.Parallel()
	const id = "bind-after-complete"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "review")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	s := route.store
	completeWorkflowPlanFixture(t, s, id)

	plan, err := readWorkflowPlanTx(t.Context(), s.db, id)
	if err != nil || plan == nil || !WorkflowStepPlanComplete(*plan) {
		t.Fatalf("fixture plan is not complete: plan=%v err=%v", plan, err)
	}
	if frontier := WorkflowStepPlanFrontier(*plan); len(frontier) != 0 {
		t.Fatalf("completed plan still has a frontier: %v", frontier)
	}
	if _, err := activeWorkflowContractVersion(t.Context(), s.db, id, "workflow_action"); err != nil {
		t.Fatalf("fixture holds no active contract: %v", err)
	}
	var seeded int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, id).Scan(&seeded); err != nil {
		t.Fatal(err)
	}

	bind := func(label, watermark string) error {
		t.Helper()
		seedWorkflowAuthority(t, s, "bind-after-producer-"+label, id, "principal/bind-after", "request/"+label, []string{"evidence:" + id + "-" + label})
		payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "evidence:" + id + "-" + label, "producer_id": "principal/bind-after", "producer_run_ref": "bind-after-producer-" + label, "producer_watermark": watermark})
		return f.run(t.Context(), "bind_evidence", label, f.version(t.Context(), t), payload, []string{"evidence:" + id + "-" + label})
	}
	if err := bind("postmerge", "request/postmerge"); err != nil {
		t.Fatalf("plan-complete evidence binding refused: %v", err)
	}
	// Prior bindings do not prevent binding fresh independent evidence.
	if err := bind("second-run", "request/second-run"); err != nil {
		t.Fatalf("second plan-complete evidence binding refused: %v", err)
	}
	var bindings int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, id).Scan(&bindings); err != nil || bindings != seeded+2 {
		t.Fatalf("evidence bindings=%d seeded=%d err=%v, want %d", bindings, seeded, err, seeded+2)
	}
	// The completed plan still completes afterwards.
	if err := f.run(t.Context(), "complete", "bind-after-complete-finish", f.version(t.Context(), t), mustJSONValue(map[string]any{"impact_verdict": "non-breaking"}), nil); err != nil {
		t.Fatalf("completion after plan-complete bindings refused: %v", err)
	}
}

// TestPlanCompletePinListsEvidenceBinding holds the pin/write parity of the
// completed-plan route: the pin a driving agent reads names bind_evidence
// among the completed plan's intents while an active contract stands.
func TestPlanCompletePinListsEvidenceBinding(t *testing.T) {
	t.Parallel()
	const id = "bind-after-complete-pin"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "review")
	s := route.store
	completeWorkflowPlanFixture(t, s, id)

	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		names := make([]string, 0, len(pin.NextValidIntents))
		for _, intent := range pin.NextValidIntents {
			names = append(names, intent.ActionID)
		}
		t.Fatalf("completed-plan pin intents=%v, want bind_evidence listed", strings.Join(names, ","))
	}
}

// TestEvidenceBindingStaysFrontierBoundBeforeItsStep holds the route's scope:
// before the plan reaches a step declaring bind_evidence, the action stays
// refused (§4.2 opens the route only once a declaring step is reached).
func TestEvidenceBindingStaysFrontierBoundBeforeItsStep(t *testing.T) {
	t.Parallel()
	const id = "bind-before-declaring-step"
	route := seedWorkflowReturnRouteFixture(t, id, WorkflowStepPlanChange, "plan")
	f := acceptanceRecoveryFixture{store: route.store, workID: id, owner: route.owner}
	seedWorkflowAuthority(t, route.store, "bind-before-producer", id, "principal/bind-before", "request/bind-before", []string{"evidence:bind-before"})
	payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "evidence:bind-before", "producer_id": "principal/bind-before", "producer_run_ref": "bind-before-producer", "producer_watermark": "request/bind-before"})
	err := f.run(t.Context(), "bind_evidence", "bind-before", f.version(t.Context(), t), payload, []string{"evidence:bind-before"})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(err), "action is not declared on a frontier step") {
		t.Fatalf("pre-declaring-step binding refusal=%v, want the frontier bound", err)
	}
}
