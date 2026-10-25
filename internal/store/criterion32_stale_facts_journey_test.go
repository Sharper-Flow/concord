package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestCriterion32ReconcilesResourceViewAndClaimBeforeUsingCandidateProof(t *testing.T) {
	j := journeyThroughA(t, "criterion32-resource-freshness", true)
	job := reviewSubjectReadyRevision(t, j.fixture)
	const attempt = "attempt-criterion32-reconciled"
	prepare := func() *WorkerDispatchPreparation {
		t.Helper()
		proof, err := PrepareWorkerDispatchFreshness(t.Context(), j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
		if err != nil {
			t.Fatal(err)
		}
		return proof
	}
	dispatch := func(proof *WorkerDispatchPreparation) error {
		t.Helper()
		packet := joinPacketFor(t, j.s, j.workID, currentStep(t, j.s, j.workID), attempt, "review", registeredLaneIdentity(t, "review"))
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, j.s, j.workID, job, "review")
		return dispatchBindingAttemptCtx(t, j.s, cd0059DispatchSeed{workID: j.workID, ownerActor: j.fixture.owner}, attempt, packet, WithWorkerDispatchPreparation(context.Background(), proof))
	}
	refuse := func(proof *WorkerDispatchPreparation, diagnostic string) {
		t.Helper()
		before := j.effects(t)
		err := dispatch(proof)
		if !hasFailureKind(err, KindProjectionConflict) || !strings.Contains(err.Error(), diagnostic) {
			t.Fatalf("stale resource fact reached use: %v, want %q", err, diagnostic)
		}
		if after := j.effects(t); after != before {
			t.Fatalf("stale-resource refusal changed durable progress: %s -> %s", before, after)
		}
	}
	viewProof := prepare()
	// Isolate the resource-view dependency from work-version concurrency. The
	// current-row change occurs after the native observation, before admission.
	if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_context_state SET source_frontier=source_frontier+1 WHERE work_id=?; DELETE FROM write_guard`, j.workID); err != nil {
		t.Fatal(err)
	}
	refuse(viewProof, "the work context changed under the dispatch subject observation")
	claimProof := prepare()
	// Replacing the owner of the same active path leaves the Git subject and
	// resource view unchanged; the exact claim tuple must still be reconciled.
	if _, err := j.s.db.Exec(`UPDATE worktree_entries SET claim_op_id='criterion32-replacement-claim' WHERE set_id=?`, WorktreeSetID(j.workID)); err != nil {
		t.Fatal(err)
	}
	refuse(claimProof, "the claimed worktree changed under the dispatch subject observation")
	if err := dispatch(prepare()); err != nil {
		t.Fatalf("fresh resource view and reconciled claim refused: %v", err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	j.b, _ = j.completeCorrectiveReview(t, attempt, job, filepath.Join("internal", "store", job.JobID, "review.txt"), "corrected candidate\n", "criterion32-b")
	if err := j.acceptEvidence(t, attempt); err != nil {
		t.Fatalf("integrate the reconciled worker result: %v", err)
	}
	j.requireAcceptedReview(t, attempt, j.b)
	before := j.effects(t)
	err := j.action(t, "record_verdict", criterionAssociationPayload(j.runA.OperationRef, "predicate:work-context"), j.fixture.owner)
	if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "final candidate") {
		t.Fatalf("original candidate's receipt satisfied changed candidate: %v", err)
	}
	if after := j.effects(t); after != before {
		t.Fatalf("stale receipt changed progress: %s -> %s", before, after)
	}
	j.replayProducer(t, "observe-refine-a", j.a, j.b)
	j.requireRecordsUnchanged(t, j.snapshotA)
	if len(j.reviewReceipts) != 1 {
		t.Fatalf("changed candidate has no fresh receipt: %v", j.reviewReceipts)
	}
	if err := j.action(t, "record_verdict", criterionAssociationPayload(j.reviewReceipts[0], "predicate:work-context"), j.fixture.owner); err != nil {
		t.Fatalf("fresh proof did not satisfy changed candidate: %v", err)
	}
	j.requireRecordsUnchanged(t, j.snapshotA)
}
