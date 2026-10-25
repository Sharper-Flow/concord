package store

import (
	"strings"
	"testing"
)

func TestStartedWorkerRestartConflictScope(t *testing.T) {
	t.Parallel()
	f := seedCrossAttemptFixture(t, "started-worker-restart-scope")
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	actor := mustActorRef(t, f.dispatcher)
	// Job-less assignment identity survives claim changes and new contracts.
	if err := guardWorkerDispatchRestart(t.Context(), f.s.db, f.workID, actor, workerRestartAssignment{stepID: f.step, laneID: f.researchLane.ID}, "separate-claim"); !hasFailureKind(err, KindOperationConflict) {
		t.Fatalf("a different claim hid the same outstanding research assignment: %v", err)
	}
	if err := guardWorkerDispatchRestart(t.Context(), f.s.db, f.workID, actor, workerRestartAssignment{stepID: "different-step", laneID: f.researchLane.ID}, ""); err != nil {
		t.Fatalf("a different job-less assignment was not independent: %v", err)
	}
	refuse := func(attempt string, lane LaneDefinition, held string) {
		t.Helper()
		before := walledResearchReadCounters(t, f)
		packet := joinPacketFor(t, f.s, f.workID, f.step, attempt, lane.ID, lane.Digest)
		_, err := dispatchJoinAttempt(t.Context(), t, f.s, f.workID, readWorkVersion(t, f.s, f.workID), f.dispatcher, packet)
		if !hasFailureKind(err, KindOperationConflict) || !strings.Contains(err.Error(), held+" has no terminal outcome") {
			t.Fatalf("dispatch over %s = %v, want its unresolved-effect refusal", held, err)
		}
		if after := walledResearchReadCounters(t, f); after != before {
			t.Fatalf("refused dispatch changed durable state: before %+v, after %+v", before, after)
		}
	}
	refuse("attempt:research-retry", f.researchLane, f.attemptR)
	// Read-only research does not prevent a different lane. Its earlier row
	// must not hide the editing Review that a subsequent restart encounters.
	f.dispatch(t, f.attemptV, f.reviewLane, f.dispatcher)
	refuse("attempt:review-retry", f.reviewLane, f.attemptV)
	f.completeReport(t, f.attemptR, f.researchLane, 30)
	refuse("attempt:research-after-review", f.researchLane, f.attemptV)
}

func TestStartedReviewSurvivesSupersession(t *testing.T) {
	t.Parallel()
	j, _ := descendantReviewFixture(t, "started-review-supersession")
	held := workerDiffDispatch(t, j, "held-review")
	reviewSubjectSupersedeContract(t, j.s, j.workID)
	before := j.effects(t)
	actor := mustActorRef(t, j.fixture.owner)
	// A new contract and a different target lane do not stop an editing
	// worker on the same claim, even when its old packet is now stale.
	if err := guardWorkerDispatchRestart(t.Context(), j.s.db, j.workID, actor, workerRestartAssignment{stepID: currentStep(t, j.s, j.workID), laneID: "verify"}, held.WorktreeIdentity); !hasFailureKind(err, KindOperationConflict) || !strings.Contains(err.Error(), held.AttemptID) {
		t.Fatalf("supersession hid the live editing review: %v", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("restart refusal changed durable state")
	}
	assignment := workerRestartAssignment{stepID: currentStep(t, j.s, j.workID), laneID: "verify"}
	if err := guardWorkerDispatchRestart(t.Context(), j.s.db, j.workID, actor, assignment, ""); !hasFailureKind(err, KindOperationConflict) {
		t.Fatalf("missing claim identity hid a live editor: %v", err)
	}
	if err := guardWorkerDispatchRestart(t.Context(), j.s.db, j.workID, actor, assignment, "separate-claim"); err != nil {
		t.Fatalf("editing scope leaked across distinct prevalidated claims: %v", err)
	}
	if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worker_attempts SET lane_digest=? WHERE work_id=? AND attempt_id=?; DELETE FROM write_guard`, "sha256:"+strings.Repeat("0", 64), j.workID, held.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := guardWorkerDispatchRestart(t.Context(), j.s.db, j.workID, actor, assignment, "separate-claim"); !hasFailureKind(err, KindOperationConflict) {
		t.Fatalf("unknown lane authority established independence: %v", err)
	}
}

func TestReadOnlyRestartUsesJobIdentityAcrossClaimsAndLanes(t *testing.T) {
	t.Parallel()
	j, _ := descendantReviewFixture(t, "readonly-restart-job-identity")
	const attempt = "verify-original"
	job := WorkerJobBinding{JobID: "job:readonly-original"}
	recordWorkerJobRevisionForTest(t, j.s, j.workID, j.fixture.owner, &job)
	if err := descendantDispatch(t, j, "verify", attempt, job, nil); err != nil {
		t.Fatalf("dispatch the read-only assignment: %v", err)
	}
	j.laneDispatch(t, attempt, "verification", &job)
	actor := mustActorRef(t, j.fixture.owner)
	assignment := workerRestartAssignment{jobID: job.JobID, stepID: "different-step", laneID: "research"}
	t.Run("same_assignment_across_claims_and_lanes", func(t *testing.T) {
		before := j.effects(t)
		if err := guardWorkerDispatchRestart(t.Context(), j.s.db, j.workID, actor, assignment, "separate-claim"); !hasFailureKind(err, KindOperationConflict) || !strings.Contains(err.Error(), attempt) {
			t.Fatalf("a claim, step or lane change hid the same read-only job: %v", err)
		}
		if after := j.effects(t); after != before {
			t.Fatal("same-assignment refusal changed durable progress")
		}
	})
	t.Run("different_assignment_on_same_lane_and_claim", func(t *testing.T) {
		nextJob := WorkerJobBinding{JobID: "job:readonly-independent"}
		recordWorkerJobRevisionForTest(t, j.s, j.workID, j.fixture.owner, &nextJob)
		if err := descendantDispatch(t, j, "verify", "verify-independent", nextJob, nil); err != nil {
			t.Fatalf("a different read-only assignment was over-refused: %v", err)
		}
		j.laneDispatch(t, "verify-independent", "verification", &nextJob)
	})
}
