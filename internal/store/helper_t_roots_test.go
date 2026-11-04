package store

import "testing"

// tSeedReviewRoute seeds the break-fix return route at its repair step.
func tSeedReviewRoute(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	f := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	return f
}
