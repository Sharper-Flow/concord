package store

import (
	"context"
	"strings"
	"testing"
)

// These are query-level tests over the existing history fixture, not native
// dispatch or admission evidence. The small composition changes the producer
// and review steps; rejection, repair, and settlement keep their shared owner.
func TestSmallReviewDebtUsesProducerAndReviewSteps(t *testing.T) {
	t.Parallel()
	registered, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	db, appendEvent := correctionHistoryFixture(t)
	ctx := context.Background()
	authorize := func(attempt, step, class string) int64 {
		appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "step_id": step, "worker_attempt_id": attempt})
		return appendEvent(WorkerDispatched, map[string]any{"attempt_id": attempt, "capability_class": class})
	}
	complete := func(attempt, verdict string) int64 {
		return appendEvent(WorkerCompleted, map[string]any{"attempt_id": attempt, "review": map[string]any{"verdict": verdict}})
	}
	dispose := func(attempt, step, action string) int64 {
		return appendEvent(WorkflowActionCompleted, map[string]any{"action_id": action, "step_id": step, "worker_attempt_id": attempt})
	}
	assertDebt := func(want bool, frontier int64) {
		t.Helper()
		got, err := workflowPostRejectionReviewOutstanding(ctx, db, "work-history", definition, "test")
		if err != nil || got != want {
			t.Fatalf("review debt = %v, %v; want %v", got, err, want)
		}
		gotFrontier, err := workflowPostRejectionFrontier(ctx, db, "work-history", definition, "test")
		if err != nil || gotFrontier != frontier {
			t.Fatalf("review frontier = %d, %v; want %d", gotFrontier, err, frontier)
		}
	}

	authorize("review-rejected", "acceptance", "review")
	complete("review-rejected", "no_ship")
	rejected := dispose("review-rejected", "acceptance", "reject_worker_result")
	assertDebt(true, rejected)

	// A review dispatched before a repair cannot cover it even when the
	// review completes and is accepted after that repair.
	authorize("review-old", "acceptance", "review")
	authorize("repair", "execution", "implementation")
	repaired := complete("repair", "")
	dispose("repair", "execution", "accept_worker_result")
	assertDebt(true, repaired)
	complete("review-old", "ship")
	dispose("review-old", "acceptance", "accept_worker_evidence")
	assertDebt(true, repaired)

	authorize("review-no-ship", "acceptance", "review")
	complete("review-no-ship", "no_ship")
	noShip := dispose("review-no-ship", "acceptance", "accept_worker_evidence")
	assertDebt(true, noShip)

	// A ship report accepted at a different step is not this composition's
	// review disposition.
	authorize("review-wrong-step", "planning", "review")
	complete("review-wrong-step", "ship")
	dispose("review-wrong-step", "planning", "accept_worker_evidence")
	assertDebt(true, noShip)

	authorize("review-fresh", "acceptance", "review")
	complete("review-fresh", "ship")
	dispose("review-fresh", "acceptance", "accept_worker_evidence")
	assertDebt(false, noShip)

	// Later producer activity makes the previous review stale again.
	later := authorize("repair-later", "execution", "implementation")
	assertDebt(true, later)
}

func TestSmallReviewDebtAdmissionKeepsRepairAndReviewRoutes(t *testing.T) {
	t.Parallel()
	registered, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	for _, step := range []string{"acceptance", "release"} {
		state := WorkflowAdmissionState{
			Step: step, RefinementWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding,
			ActiveContracts: 1, ConfirmationAuthority: WorkflowConfirmationCoordinator, CorrectionRequestRecovery: true,
		}
		action := "confirm_premise"
		if step == "release" {
			action = "complete"
		}
		decision := workflowAdmit(definition, state, action)
		if decision.Admitted || decision.Failure == nil || !strings.Contains(decision.Failure.Error(), "fresh accepted review") {
			t.Fatalf("%s at %s bypassed review debt: %+v", action, step, decision)
		}
		for _, recovery := range []string{"checkpoint_context", "request_correction"} {
			if decision := workflowAdmit(definition, state, recovery); !decision.Admitted {
				t.Fatalf("debt must preserve %s at %s: %+v", recovery, step, decision)
			}
		}
		if step == "acceptance" {
			for _, recovery := range []string{"dispatch_worker", "accept_worker_evidence"} {
				if decision := workflowAdmit(definition, state, recovery); !decision.Admitted {
					t.Fatalf("debt must preserve %s at acceptance: %+v", recovery, decision)
				}
			}
		}
		state.ReviewDebt = ReviewDebtNone
		if decision := workflowAdmit(definition, state, action); !decision.Admitted {
			t.Fatalf("settled debt must release %s at %s: %+v", action, step, decision)
		}
	}
	for step, want := range map[string]bool{"execution": false, "acceptance": true, "release": true} {
		if got := workflowPostRejectionReviewStep(definition, step); got != want {
			t.Fatalf("review-debt gate at %s = %v; want %v", step, got, want)
		}
	}
	state := WorkflowAdmissionState{Step: "execution", RefinementWorkflow: true, ReviewDebt: ReviewDebtOutstanding, ActiveContracts: 1}
	state.Delivery.Started = true
	if decision := workflowAdmit(definition, state, "record_delivery"); !decision.Admitted {
		t.Fatalf("repair must be able to return to the review step under debt: %+v", decision)
	}
}

func TestSmallReviewDebtRequiresAnExplicitShipVerdict(t *testing.T) {
	t.Parallel()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	full, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		definition WorkflowDefinition
		step       string
		wantDebt   bool
	}{
		{"small", small.Definition, "acceptance", true},
		{"released full", full.Definition, "refine", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			roles := workflowReviewProofRoles(test.definition)
			for verdict, want := range map[string]bool{"": !test.wantDebt, "ship": true, "no_ship": false} {
				if got := workflowReviewSettlesDebt(roles, verdict); got != want {
					t.Fatalf("settlement of %q = %v; want %v", verdict, got, want)
				}
			}
			db, appendEvent := correctionHistoryFixture(t)
			appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "reject_worker_result", "step_id": test.step})
			appendEvent(WorkerDispatched, map[string]any{"attempt_id": "review", "capability_class": "review"})
			appendEvent(WorkerCompleted, map[string]any{"attempt_id": "review"})
			appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_evidence", "step_id": test.step, "worker_attempt_id": "review"})
			debt, err := workflowPostRejectionReviewOutstanding(context.Background(), db, "work-history", test.definition, "test")
			if err != nil || debt != test.wantDebt {
				t.Fatalf("debt = %v, %v; want %v", debt, err, test.wantDebt)
			}
		})
	}
}
