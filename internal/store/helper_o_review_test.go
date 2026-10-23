package store

import "testing"

func oSeedReviewRoute(t *testing.T, workID, definitionRef, step string) workflowReturnRouteFixture {
	t.Helper()
	f := seedWorkflowReturnRouteFixture(t, workID, definitionRef, step)
	dir, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	return f
}

func oDispatchJobBoundAttempt(t *testing.T, s *Store, workID, stepID, attemptID string, correction *WorkflowCorrectionContext, actor WorkflowActor, version int64, key string, job *WorkerJobBinding) {
	t.Helper()
	if job.Digest == "" {
		recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	}
	review, err := loadWorkflowReviewState(t.Context(), s.db, workID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if workflowAdmitReview(review, job.JobID) != nil {
		acceptCurrentReviewForTest(t, workContextFixture{store: s, workID: workID, owner: actor}, workerFixtureDirectory(t, s, workID), "continue")
	}
	dispatchJobBoundAttempt(t, s, workID, stepID, attemptID, correction, actor, version, key, job)
}
