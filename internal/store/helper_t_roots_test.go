package store

import "testing"

// tSeedReviewRoute seeds the current break_fix return-route fixture at its
// repair step with a real repository, so Review and terminal delivery observe
// real candidate identities.
func tSeedReviewRoute(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	f := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	reviewSubjectClaim(t, f.store, workID)
	return f
}
