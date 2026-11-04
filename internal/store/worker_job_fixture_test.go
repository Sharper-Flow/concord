package store

import (
	"testing"
)

func seedChecksPlanFixture(t *testing.T, workID, step string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanFix, step, []string{"verification", "review", "artifact"})
}

func passExitCode() *int { value := 0; return &value }
