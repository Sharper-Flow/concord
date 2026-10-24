package store

import (
	"slices"
	"testing"
)

func TestCurrentReviewRoutesStayInsideExecution(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(ref, func(t *testing.T) {
			entry := mustBuiltinDefinition(t, ref)
			target := "execution"
			checkpoint := "acceptance"
			if ref == "workflow.break_fix" {
				target, checkpoint = "repair", "verify"
			}
			for _, stepID := range []string{target, "refine", checkpoint} {
				step := workflowStep(entry.Definition, stepID)
				for _, action := range []string{"record_worker_job", "dispatch_worker", "accept_worker_result"} {
					if step == nil || !slices.Contains(step.Actions, action) {
						t.Errorf("%s cannot %s for its current Review", stepID, action)
					}
				}
				if got := workflowUnhealthyVerdictRouteTarget(entry.Definition, stepID); got != target {
					t.Errorf("%s correction target = %q, want %q", stepID, got, target)
				}
			}
			delivery := workflowStep(entry.Definition, "delivery")
			for _, action := range []string{"record_worker_job", "dispatch_worker", "accept_worker_result"} {
				if slices.Contains(delivery.Actions, action) {
					t.Errorf("delivery unexpectedly opens a Review through %s", action)
				}
			}
		})
	}
}

func TestExecutionReviewCorrectionRequiresItsOwningActions(t *testing.T) {
	for _, missing := range []string{"dispatch_worker", "accept_worker_result"} {
		t.Run(missing, func(t *testing.T) {
			definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
			step := workflowStep(definition, "execution")
			step.Actions = slices.DeleteFunc(step.Actions, func(action string) bool { return action == missing })
			if err := validateWorkflowRecoveryRoutes(definition); err == nil {
				t.Fatalf("execution correction admitted without %s", missing)
			}
		})
	}
}
