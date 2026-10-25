package store

import (
	"strings"
	"testing"
)

func TestStartedWorkerRestartConflictScope(t *testing.T) {
	t.Parallel()
	f := seedCrossAttemptFixture(t, "started-worker-restart-scope")
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
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
	if err := guardWorkerDispatchRestart(t.Context(), j.s.db, j.workID, actor, "verify", held.WorktreeIdentity); !hasFailureKind(err, KindOperationConflict) || !strings.Contains(err.Error(), held.AttemptID) {
		t.Fatalf("supersession hid the live editing review: %v", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("restart refusal changed durable state")
	}
}
