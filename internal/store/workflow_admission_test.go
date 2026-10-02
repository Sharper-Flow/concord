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
// review the calling guard binds the accept's attempt identity against, and
// ReadyReviewSettles reports whether that ready verdict settles the debt.
// The loader folds a ready no_ship review like any other, and the table
// keeps the answer defined for it, so the pure function stays total.

func TestWorkflowAdmitDecisionTable(t *testing.T) {
	const refineStep = "refine"
	const gateStep = "delivery"
	definition := WorkflowDefinition{Ref: "workflow.break_fix", Version: 1,
		ActionDefinitions: []WorkflowActionDefinition{
			{ID: "record_delivery", ExecutionMode: ActionAdvance},
			{ID: "accept_worker_result", ExecutionMode: ActionAdvance},
			{ID: "dispatch_worker", ExecutionMode: ActionHold},
			{ID: "checkpoint_context", ExecutionMode: ActionHold},
			{ID: "start_refine", ExecutionMode: ActionHold},
		},
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
			name:     "debt at the refine step hides both advances",
			state:    WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding},
			want:     map[string]bool{"record_delivery": false, "accept_worker_result": false, "dispatch_worker": true, "checkpoint_context": true},
			refusals: map[string]bool{"record_delivery": true, "accept_worker_result": true},
		},
		{
			name:     "debt at the gate hides only the delivery exit",
			state:    WorkflowAdmissionState{Step: gateStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding, CorrectionRequestRecovery: true},
			want:     map[string]bool{"record_delivery": false, "request_correction": true, "checkpoint_context": true},
			refusals: map[string]bool{"record_delivery": true},
		},
		{
			name:  "debt outside the review shape admits everything",
			state: WorkflowAdmissionState{Step: gateStep, CorrectionWorkflow: false, ReviewStep: false, ReviewDebt: ReviewDebtOutstanding, CorrectionRequestRecovery: true},
			want:  map[string]bool{"record_delivery": true, "request_correction": true},
		},
		{
			name:     "a ready settling review stands behind the accept",
			state:    WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding, ReadyReviewAttemptID: "attempt:ready-ship", ReadyReviewVerdict: "ship", ReadyReviewSettles: true},
			want:     map[string]bool{"record_delivery": false, "accept_worker_result": false, "dispatch_worker": true},
			refusals: map[string]bool{"record_delivery": true, "accept_worker_result": true},
		},
		{
			name:     "a ready no_ship review folds and the answer stays defined",
			state:    WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding, ReadyReviewAttemptID: "attempt:ready-no-ship", ReadyReviewVerdict: "no_ship", ReadyReviewSettles: false},
			want:     map[string]bool{"record_delivery": false, "accept_worker_result": false, "dispatch_worker": true},
			refusals: map[string]bool{"record_delivery": true, "accept_worker_result": true},
		},
		{
			name:  "the gate's corrective return refuses without its folded route",
			state: WorkflowAdmissionState{Step: gateStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding, CorrectionRequestRecovery: false, CorrectionRequestMissing: workflowCorrectionMissingGateReview},
			want:  map[string]bool{"record_delivery": false, "request_correction": false},
		},
		{
			name:  "the dispatch hold refuses only the advancing exits",
			state: WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtNone, DispatchHold: true},
			want:  map[string]bool{"dispatch_worker": true, "checkpoint_context": true, "record_delivery": false, "accept_worker_result": true},
		},
		{
			name:  "an undeclared action refuses on step legality",
			state: WorkflowAdmissionState{Step: refineStep, CorrectionWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtNone},
			want:  map[string]bool{"record_verdict": false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for actionID, wantAdmitted := range tc.want {
				decision := workflowAdmit(definition, tc.state, actionID)
				if decision.Admitted != wantAdmitted {
					t.Fatalf("action %s: admitted = %v, want %v", actionID, decision.Admitted, wantAdmitted)
				}
				if decision.ReadyReviewAttemptID != tc.state.ReadyReviewAttemptID || decision.ReadyReviewSettles != tc.state.ReadyReviewSettles {
					t.Fatalf("action %s: ready review = %q (settles %v), want %q (%v)", actionID, decision.ReadyReviewAttemptID, decision.ReadyReviewSettles, tc.state.ReadyReviewAttemptID, tc.state.ReadyReviewSettles)
				}
				switch {
				case tc.refusals[actionID]:
					if decision.Failure == nil || decision.Failure.Kind != KindInvalidOperation || !strings.Contains(decision.Failure.Detail, "fresh accepted review") {
						t.Fatalf("action %s: refusal = %v, want the typed fresh-review failure", actionID, decision.Failure)
					}
				case wantAdmitted && decision.Failure != nil:
					t.Fatalf("action %s: admitted decision carries a failure: %v", actionID, decision.Failure)
				case !wantAdmitted && decision.Failure == nil:
					t.Fatalf("action %s: refused decision carries no failure", actionID)
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
