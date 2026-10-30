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
				if step == nil || !slices.Contains(step.Actions, "record_review") {
					t.Errorf("%s cannot record its current Review", stepID)
				}
				if got := workflowUnhealthyVerdictRouteTarget(entry.Definition, stepID); got != target {
					t.Errorf("%s correction target = %q, want %q", stepID, got, target)
				}
			}
			delivery := workflowStep(entry.Definition, "delivery")
			if slices.Contains(delivery.Actions, "record_review") {
				t.Error("delivery unexpectedly opens a Review")
			}
		})
	}
}

func TestExecutionReviewCorrectionRequiresItsOwningActions(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	step := workflowStep(definition, "execution")
	step.Actions = slices.DeleteFunc(step.Actions, func(action string) bool { return action == "record_review" })
	if err := validateWorkflowRecoveryRoutes(definition); err == nil {
		t.Fatal("execution correction admitted without record_review")
	}
}
