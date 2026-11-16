package store

import (
	"slices"
	"testing"
)

func TestCoordinatorReviewActionExists(t *testing.T) {
	plan := currentWorkflowPlanFixture(t, WorkflowStepPlanFix)
	for _, step := range plan.Steps {
		if slices.Contains(WorkflowStepPlanTemplateActions(plan.Type, step.ID), "record_review") {
			return
		}
	}
	t.Fatal("coordinator cannot record a review attempt")
}
