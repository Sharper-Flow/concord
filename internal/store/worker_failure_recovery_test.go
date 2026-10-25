package store

import (
	"context"
	"testing"
	"time"
)

// On the current definitions a live failed terminal derives the
// attempt's failure record in the same transaction (CD-0198 D5), so the
// engine's hold-mode record_worker_failure recovery is never offered beside
// it, and the pinned definition digest stays unchanged.
func TestFailedWorkerRecoveryIsDerivedOnTheCurrentDefinition(t *testing.T) {
	t.Parallel()
	const workID = "worker-failure-recovery-current"
	f, attemptID, epoch := seedCurrentRepairWorker(t, workID)
	s, owner := f.store, f.owner
	defer s.Close()
	entry := mustBuiltinDefinition(t, "workflow.break_fix")
	event := s2WorkerFailedAttributedEvent(workID, attemptID, "failed-"+workID, preferredModelForLane(laneForID(t)), WorkerFailureWorkerError, "worker failed", time.Unix(30, 0).UTC())
	if err := s2WorkerFailedLive(t, s, event); err != nil {
		t.Fatalf("live failed terminal: %v", err)
	}
	if records := s2DerivedFailureRecords(t, s, workID, attemptID); len(records) != 1 {
		t.Fatalf("derived failure records = %d, want one from the terminal", len(records))
	}

	if resolved, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "record_worker_failure"); err == nil || resolved.Definition.Ref != "" {
		t.Fatalf("the derived failure left a hold-mode recovery resolvable: definition=%#v error=%v", resolved.Definition, err)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_worker_failure") {
		t.Fatal("work pin advertised a recovery beside the derived failure record")
	}
	payload := mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch})
	if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "record_worker_failure", Payload: payload, Actor: owner,
	}); err == nil {
		t.Fatal("recovery preflight admitted a second failure record")
	}

	registered, ok := BuiltinWorkflowRegistry().Current(entry.Definition.Ref)
	if !ok || registered.Digest != entry.Digest {
		t.Fatalf("pinned definition changed: before=%s after=%s", entry.Digest, registered.Digest)
	}
}

func TestFailedWorkerRecoveryRefusesWithoutTheCurrentFailedAttempt(t *testing.T) {
	t.Parallel()
	const workID = "worker-failure-recovery-no-failure"
	f, attemptID, epoch := seedCurrentRepairWorker(t, workID)
	s, owner := f.store, f.owner
	defer s.Close()

	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "record_worker_failure"); err == nil {
		t.Fatal("recovery action resolved without a failed attempt")
	}
	payload := mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch})
	err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "record_worker_failure", Payload: payload, Actor: owner,
	})
	if err == nil {
		t.Fatal("recovery preflight admitted an active attempt")
	}
}

// seedCurrentRepairWorker seeds current workflow.break_fix work at repair with
// the fixture-approved contract and a claimed worktree, then dispatches one
// implement attempt on a ready worker-job revision through the real
// dispatch_worker authorization and lane dispatch. It returns the fixture,
// the attempt, and the attempt's authorized epoch.
func seedCurrentRepairWorker(t *testing.T, workID string) (workContextFixture, string, int64) {
	t.Helper()
	f := seedWorkContextFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.break_fix"))
	reviewSubjectClaim(t, f.store, workID)
	attemptID := "attempt:" + workID
	job := reviewSubjectReadyRevision(t, f)
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), workID)
	if err != nil || view == nil {
		t.Fatalf("admitted view = %+v err = %v, want a present view", view, err)
	}
	if err := dispatchWorkContextAttempt(t, f, workContextDispatchPacket(t, f, attemptID, job, view)); err != nil {
		t.Fatalf("implement dispatch %s refused: %v", attemptID, err)
	}
	if err := ApplyOperation(context.Background(), f.store, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attemptID, laneForID(t), job)}}); err != nil {
		t.Fatal(err)
	}
	return f, attemptID, reviewSubjectWindow(t, f.store, workID, attemptID).AttemptEpoch
}
