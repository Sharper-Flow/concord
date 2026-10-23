package store

import (
	"strings"
	"testing"
)

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

// tDispatchJobBoundAttempt dispatches a job-bound attempt. When the
// execution loop refuses the assignment for want of an accepted Review on the
// current candidate (CD-0242 D5), Main first accepts a real continue Review
// through the dispatch, terminal, and acceptance owners, and the dispatch
// is requested again.
func tDispatchJobBoundAttempt(t *testing.T, s *Store, workID, attemptID string, correction *WorkflowCorrectionContext, actor WorkflowActor, key string, job *WorkerJobBinding) {
	t.Helper()
	const stepID = "repair"
	if job.Digest == "" {
		recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	}
	packet := func() map[string]any {
		packet := dispatchWorkerPacket(t, s, workID, stepID, attemptID)
		packet["schema_version"] = WorkerPacketSchemaVersion
		if correction != nil {
			packet["inputs"].(map[string]any)["correction"] = correction
		}
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
		return packet
	}
	err := dispatchJobPacketForTest(t, s, workID, attemptID, actor, key, packet())
	if err != nil && strings.Contains(err.Error(), "requires an accepted Review on the current candidate") {
		acceptCurrentReviewForTest(t, workContextFixture{store: s, workID: workID, owner: actor}, workerFixtureDirectory(t, s, workID), "continue")
		err = dispatchJobPacketForTest(t, s, workID, attemptID, actor, key+"-reviewed", packet())
	}
	if err != nil {
		t.Fatalf("dispatch job-bound attempt %s: %v", attemptID, err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attemptID, job)
}
