package store

import (
	"context"
	"testing"
)

// A rejection is Main's progression (CD-0242 D5): no dispatched worker may
// reject, so the later executor's former standing is gone. The surviving
// obligation is that a Main rejection of an earlier attempt, recorded after a
// later dispatch replaced the instance executor, replays unchanged.
func TestRecordedRejectionKeepsOriginalExecutor(t *testing.T) {
	const workID = "recorded-rejection-original-executor"
	f := seedCrossAttemptFixture(t, workID)
	f.buildTwoAttemptHistory(t)
	epoch := crossAttemptDispatchEpoch(t, f.s, workID, f.attemptR)
	original := crossAttemptDispatchedLaneActor(t, f.s, workID, f.attemptR)
	later := crossAttemptDispatchedLaneActor(t, f.s, workID, f.attemptV)
	var executor string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&executor); err != nil {
		t.Fatal(err)
	}
	if original == later || executor != later {
		t.Fatalf("executor witness = %q/%q/%q, want distinct dispatch actors and the later executor", original, later, executor)
	}
	mainRef := crossAttemptOwnerRef(t, f.fixture.owner)
	event := f.forgedRejectCompletionEvent(t, "recorded-original-executor-rejection", f.attemptR, epoch, mainRef, 100)
	if err := f.applyWitnessEvent(t, event); err != nil {
		t.Fatalf("Main rejects the earlier result after a later dispatch: %v", err)
	}
	version := readWorkVersion(t, f.s, workID)
	payload := crossAttemptEventPayload(t, f.s, workID, WorkflowActionCompleted, f.attemptR)
	if err := RebuildFromLog(context.Background(), f.s); err != nil {
		t.Fatalf("replay must keep the target's original executor: %v", err)
	}
	if got := readWorkVersion(t, f.s, workID); got != version {
		t.Fatalf("replayed rejection version = %d, want %d", got, version)
	}
	if got := crossAttemptEventPayload(t, f.s, workID, WorkflowActionCompleted, f.attemptR); string(got) != string(payload) {
		t.Fatal("replay changed the recorded rejection payload")
	}
}
