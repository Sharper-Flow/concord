package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// PendingWorkerLaunchRefusalFixture returns the live launch owner's refusal.
func PendingWorkerLaunchRefusalFixture(t *testing.T) *Failure {
	const workID = "retry-wall-remedy"
	f := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:pending-launch"}
	recordWorkerJobRevisionForTest(t, s, workID, f.owner, job)
	first := "attempt:" + workID + ":1"
	firstPacket := dispatchWorkerPacket(t, s, workID, "repair", first)
	firstPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, first, f.owner, "pending-launch-first", firstPacket); err != nil {
		t.Fatal(err)
	}
	second := "attempt:" + workID + ":2"
	packet := dispatchWorkerPacket(t, s, workID, "repair", second)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	err := dispatchJobPacketForTest(t, s, workID, second, f.owner, "pending-launch-second", packet)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("dispatch beside a pending worker did not refuse: %v", err)
	}
	return failure
}

// BootstrapOriginRefusalFixture drives the real bootstrap origin gate against
// a path no active Concord worktree occupies and returns its genuine refusal.
func BootstrapOriginRefusalFixture(t *testing.T) *Failure {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "bootstrap-remedy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.ValidateBootstrapOrigin(context.Background(), "concord", filepath.Join(t.TempDir(), "not-an-active-worktree"), nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("origin gate did not refuse: %v", err)
	}
	return failure
}
