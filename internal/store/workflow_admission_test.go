package store

import (
	"strings"
	"testing"
)

// The post-rejection review admission family's decision table: the pure
// workflowAdmit is total and action-scoped over the folded state space —
// outstanding debt hides only the advances toward delivery, so dispatch,
// recovery, and continuity actions keep their route — and every refusal
// carries the typed fresh-review failure. The decision echoes the ready
// settling review the calling guard binds the accept's attempt identity
// against. The loader never folds a no_ship ready review, and the table
// keeps the answer defined for one anyway, so the pure function stays total.

func TestWorkflowAdmitDecisionTable(t *testing.T) {
	const refineStep = "refine"
	const gateStep = "delivery"
	definition := WorkflowDefinition{Ref: "workflow.break_fix", Version: 1,
		StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{
			{ID: refineStep, Actions: []string{"start_refine", "record_delivery", "dispatch_worker", "accept_worker_result", "checkpoint_context"}},
			{ID: gateStep, Actions: []string{"record_delivery", "request_correction", "checkpoint_context"}},
		}}}
	// Each case names the step, the debt, and the ready review the loader
	// folded; the want columns give the per-action admission the rule owns.
	cases := []struct {
		name     string
		state    WorkflowAdmissionState
		want     map[string]bool
		refusals map[string]bool // action IDs whose refusal must carry the typed fresh-review failure
	}{
		{
			name:  "no debt at the refine step admits every action",
			state: WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtNone},
			want:  map[string]bool{"record_delivery": true, "accept_worker_result": true, "dispatch_worker": true, "checkpoint_context": true},
		},
		{
			name:  "debt at the refine step hides both advances",
			state: WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding},
			want:  map[string]bool{"record_delivery": false, "accept_worker_result": false, "dispatch_worker": true, "checkpoint_context": true},
			refusals: map[string]bool{"record_delivery": true, "accept_worker_result": true},
		},
		{
			name:  "debt at the gate hides only the delivery exit",
			state: WorkflowAdmissionState{Step: gateStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding},
			want:  map[string]bool{"record_delivery": false, "request_correction": true, "checkpoint_context": true},
			refusals: map[string]bool{"record_delivery": true},
		},
		{
			name:  "debt outside the review shape admits everything",
			state: WorkflowAdmissionState{Step: gateStep, CorrectionWorkflow: false, ReviewStep: false, ReviewDebt: ReviewDebtOutstanding},
			want:  map[string]bool{"record_delivery": true, "request_correction": true},
		},
		{
			name:  "a ready settling review stands behind the accept",
			state: WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding, ReadyReviewAttemptID: "attempt:ready-ship", ReadyReviewVerdict: "ship"},
			want:  map[string]bool{"record_delivery": false, "accept_worker_result": false, "dispatch_worker": true},
			refusals: map[string]bool{"record_delivery": true, "accept_worker_result": true},
		},
		{
			name:  "the loader never folds a no_ship ready review but the answer stays defined",
			state: WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding, ReadyReviewAttemptID: "attempt:ready-no-ship", ReadyReviewVerdict: "no_ship"},
			want:  map[string]bool{"record_delivery": false, "accept_worker_result": false, "dispatch_worker": true},
			refusals: map[string]bool{"record_delivery": true, "accept_worker_result": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for actionID, wantAdmitted := range tc.want {
				decision := workflowAdmit(definition, tc.state, actionID)
				if decision.Admitted != wantAdmitted {
					t.Fatalf("action %s: admitted = %v, want %v", actionID, decision.Admitted, wantAdmitted)
				}
				if decision.ReadyReviewAttemptID != tc.state.ReadyReviewAttemptID {
					t.Fatalf("action %s: ready review = %q, want %q", actionID, decision.ReadyReviewAttemptID, tc.state.ReadyReviewAttemptID)
				}
				if tc.refusals[actionID] {
					if decision.Failure == nil || decision.Failure.Kind != KindInvalidOperation || !strings.Contains(decision.Failure.Detail, "fresh accepted review") {
						t.Fatalf("action %s: refusal = %v, want the typed fresh-review failure", actionID, decision.Failure)
					}
				} else if decision.Failure != nil {
					t.Fatalf("action %s: admitted decision carries a failure: %v", actionID, decision.Failure)
				}
			}
		})
	}
	// The verdict rule owner stays load-bearing: only ship, or the absent
	// verdict of the pre-CD-0197 reports, settles.
	for verdict, want := range map[string]bool{"": true, "ship": true, "no_ship": false, "nonsense": false} {
		if workflowReviewSettlesDebt(verdict) != want {
			t.Fatalf("workflowReviewSettlesDebt(%q) = %v, want %v", verdict, !want, want)
		}
	}
}
