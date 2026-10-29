package store

import "testing"

// tSeedReviewRoute seeds the current break_fix return-route fixture at its
// repair step on a real claimed checkout whose branch matches the claimed
// worktree entry, so Review and terminal delivery observe real candidate
// identities.
func tSeedReviewRoute(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	f := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	dir, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	return f
}
