package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// CON-883 packet admission. The pinned continuity projection carries the
// owning reader's latest context checkpoint, and the dispatch_worker guard
// refuses a packet that does not carry that checkpoint byte-for-byte,
// mirroring the correction and work-context admissions above it.

// recordFixtureCheckpoint records one checkpoint_context action with the
// given strategy text, through the same action route a coordinator uses.
func recordFixtureCheckpoint(t *testing.T, f workContextFixture, strategy string) {
	t.Helper()
	if err := runVerdictActionAs(t, f.store, f.workID, "checkpoint_context", json.RawMessage(mustJSONValue(map[string]any{
		"active_unit": "the bounded unit", "hypothesis": "the bounded hypothesis", "diagnosis": "the bounded diagnosis", "strategy": strategy,
		"touched_refs": []string{"internal/store/work_context.go"}, "evidence_refs": []string{"evidence:checkpoint"}, "pending_questions": []string{}, "pending_decisions": []string{},
	})), 0, f.owner); err != nil {
		t.Fatalf("checkpoint_context refused: %v", err)
	}
}

// checkpointDispatchPacket builds the closed implement-lane packet the
// repair step dispatches, carrying the given checkpoint member (nil omits
// it). The job revision is recorded before the packet inputs are read
// because the recording advances the work version the binding must carry.
func checkpointDispatchPacket(t *testing.T, f workContextFixture, attemptID string, checkpoint any) map[string]any {
	t.Helper()
	job := seedReadyWorkerJob(t, f)
	laneVersion, laneDigest := implementLaneIdentity()
	task, binding := recordedPacketInputs(t, f.store, f.workID, "implement")
	inputs := map[string]any{
		"task":        task,
		"binding":     binding,
		"worker_job":  recordedPacketJobForTest(t, f.store, f.workID, job),
		"constraints": []string{"do-not-modify-product-truth"},
	}
	if checkpoint != nil {
		inputs["checkpoint"] = checkpoint
	}
	return map[string]any{
		"schema_version": "1.1",
		"attempt_id":     attemptID,
		"lane_id":        "implement",
		"lane_version":   laneVersion,
		"lane_digest":    laneDigest,
		"work_id":        f.workID,
		"step_id":        "repair",
		"inputs":         inputs,
	}
}

func dispatchCheckpointAttempt(t *testing.T, f workContextFixture, packet map[string]any) error {
	t.Helper()
	return dispatchBindingAttempt(t, f.store, cd0059DispatchSeed{workID: f.workID, ownerActor: f.owner}, packet["attempt_id"].(string), packet)
}

// The continuity snapshot pins the shared reader's exact latest checkpoint
// once: the envelope's byte budget admits one copy of a max-size
// checkpoint, and the work pin embeds inside that same envelope.
func TestContinuityPinsLatestCheckpointOnce(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-pin-view")
	defer fixture.store.Close()
	snapshot, err := ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestCheckpoint != nil {
		t.Fatal("continuity pinned a checkpoint before any record existed")
	}
	recordFixtureCheckpoint(t, fixture, "follow the recorded strategy")
	snapshot, err = ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestCheckpoint == nil || snapshot.LatestCheckpoint.Strategy != "follow the recorded strategy" {
		t.Fatalf("continuity left the latest checkpoint absent or stale: %+v", snapshot.LatestCheckpoint)
	}
	if snapshot.WorkPin == nil {
		t.Fatal("continuity pinned no work pin")
	}
	pinJSON, err := json.Marshal(snapshot.WorkPin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pinJSON, []byte(`"checkpoint"`)) {
		t.Fatalf("the pin duplicates the checkpoint the snapshot already carries:\n%s", pinJSON)
	}
}

// A dispatch whose packet carries the reader's exact latest checkpoint
// admits.
func TestDispatchAdmitsPacketConsumingLatestCheckpoint(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-dispatch-admit")
	defer fixture.store.Close()
	recordFixtureCheckpoint(t, fixture, "follow the recorded strategy")
	checkpoint, err := readLatestContextCheckpointTx(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatchCheckpointAttempt(t, fixture, checkpointDispatchPacket(t, fixture, "attempt-ckpt-admit", checkpoint)); err != nil {
		t.Fatalf("dispatch refused a packet consuming the latest checkpoint: %v", err)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 1 {
		t.Fatalf("dispatch_worker started events = %d, want 1", got)
	}
}

// A checkpoint that exists must ride the packet: a packet built without the
// member refuses.
func TestDispatchRefusesPacketMissingCheckpoint(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-dispatch-missing")
	defer fixture.store.Close()
	recordFixtureCheckpoint(t, fixture, "follow the recorded strategy")
	err := dispatchCheckpointAttempt(t, fixture, checkpointDispatchPacket(t, fixture, "attempt-ckpt-missing", nil))
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not carry the latest context checkpoint") {
		t.Fatalf("missing checkpoint error = %v, want %s refusing the packet without the latest checkpoint", err, KindInvalidPayload)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 0 {
		t.Fatalf("dispatch_worker started events = %d, want 0 on refusal", got)
	}
}

// A tampered strategy refuses with the same admission: the packet must carry
// the reader's bytes, not a close copy.
func TestDispatchRefusesTamperedCheckpointPacket(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-dispatch-tampered")
	defer fixture.store.Close()
	recordFixtureCheckpoint(t, fixture, "follow the recorded strategy")
	checkpoint, err := readLatestContextCheckpointTx(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	tampered := *checkpoint
	tampered.Strategy = "the tampered strategy the core never recorded"
	err = dispatchCheckpointAttempt(t, fixture, checkpointDispatchPacket(t, fixture, "attempt-ckpt-tampered", &tampered))
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not carry the latest context checkpoint") {
		t.Fatalf("tampered checkpoint error = %v, want %s refusing the tampered checkpoint", err, KindInvalidPayload)
	}
}

// A packet may not carry a checkpoint the work item does not hold.
func TestDispatchRefusesCheckpointWithoutRecord(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-dispatch-without-record")
	defer fixture.store.Close()
	fabricated := map[string]any{
		"checkpoint_id": "fabricated:context-checkpoint", "work_version": 1, "sequence": 1, "step_id": "repair", "attempt_epoch": 1,
		"active_unit": "the bounded unit", "hypothesis": "the bounded hypothesis", "diagnosis": "the bounded diagnosis", "strategy": "the bounded strategy",
		"touched_refs": []string{"internal/store/work_context.go"}, "evidence_refs": []string{"evidence:checkpoint"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}
	err := dispatchCheckpointAttempt(t, fixture, checkpointDispatchPacket(t, fixture, "attempt-ckpt-without-record", fabricated))
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet carries a context checkpoint without a latest context checkpoint") {
		t.Fatalf("fabricated checkpoint error = %v, want %s refusing the checkpoint without a record", err, KindInvalidPayload)
	}
}

// A checkpoint recorded after the packet was built refuses. The recording
// advances the work version, so an untouched prepared packet is caught first
// by the binding admission's work-version comparison; the checkpoint
// admission owns the packet whose binding was rebuilt at the current version
// while its checkpoint member still names the superseded checkpoint.
func TestDispatchRefusesStaleCheckpointPacket(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-dispatch-stale")
	defer fixture.store.Close()
	recordFixtureCheckpoint(t, fixture, "the first strategy stands")
	stale, err := readLatestContextCheckpointTx(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	packet := checkpointDispatchPacket(t, fixture, "attempt-ckpt-stale", stale)
	recordFixtureCheckpoint(t, fixture, "the second strategy supersedes it")
	// The untouched packet: the binding admission refuses the superseded
	// work version before any checkpoint comparison runs.
	err = dispatchCheckpointAttempt(t, fixture, packet)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "binding records work_version") {
		t.Fatalf("stale untouched packet error = %v, want the binding admission refusing the superseded work version", err)
	}
	// The rebound packet: every other member is current, so the refusal
	// belongs to the checkpoint admission alone.
	rebound := bindPacketToRecordedState(t, fixture.store, packet)
	err = dispatchCheckpointAttempt(t, fixture, rebound)
	if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "worker packet does not carry the latest context checkpoint") {
		t.Fatalf("stale rebound packet error = %v, want %s refusing the superseded checkpoint", err, KindInvalidPayload)
	}
	if got := dispatchStartedCount(t, fixture.store, fixture.workID); got != 0 {
		t.Fatalf("dispatch_worker started events = %d, want 0 on refusals", got)
	}
}
