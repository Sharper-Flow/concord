package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestSpineFirstGroupResumeDoesNotRestoreFirstEntry(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	if f.read(t).Pinned.Pin.Review.Accepted != nil {
		t.Fatal("first entry installed an unsolicited baseline Review")
	}
	f.completeWorker(t)
	f.recordJob(t)
	before := f.read(t).Pinned.Pin
	if before.Review.Candidate == nil || before.Review.Accepted != nil {
		t.Fatalf("first result did not produce the unreviewed integrated candidate: %+v", before.Review)
	}
	assertHeld := func(attempt string) {
		t.Helper()
		lane, ok := builtinLane("implement")
		if !ok {
			t.Fatal("implementation lane unavailable")
		}
		packet := bindPacketToRecordedState(t, f.s, map[string]any{
			"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attempt,
			"lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
			"work_id": f.work, "step_id": f.read(t).Pinned.Step, "inputs": map[string]any{},
		})
		refused := f.action(t, "dispatch_worker", map[string]any{"attempt_id": attempt, "worker_packet": packet})
		if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "missing_evidence" {
			t.Fatalf("a progressed execution reopened its first group: %+v", refused)
		}
		after := f.read(t).Pinned.Pin
		if after.Version != before.Version || after.Lifecycle != before.Lifecycle || !reflect.DeepEqual(after.Review, before.Review) {
			t.Fatal("refused group opening changed the candidate, disposition, findings, or lifecycle")
		}
	}
	assertHeld("attempt:second-before-resume")
	f.administrativeRecovery(t)
	before = f.read(t).Pinned.Pin
	assertHeld("attempt:second-after-recovery")
	database := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.s, f.service = reopened, NewService(reopened)
	f.service.Now = time.Now
	resolveProjectAuthority(f.service, store.ProjectResolution{ProjectID: "project-1"})
	f.env.SessionRef = "session-spine-first-group-resumed"
	assertHeld("attempt:second-after-resume")
}

func TestSpineFirstGroupUnknownLaunchCannotAuthorizeAnotherAttempt(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	lane, ok := builtinLane("implement")
	if !ok {
		t.Fatal("implementation lane unavailable")
	}
	authorize := func(attempt string) Envelope {
		packet := bindPacketToRecordedState(t, f.s, map[string]any{
			"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attempt,
			"lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
			"work_id": f.work, "step_id": f.read(t).Pinned.Step, "inputs": map[string]any{},
		})
		return f.action(t, "dispatch_worker", map[string]any{"attempt_id": attempt, "worker_packet": packet})
	}
	spineOK(t, authorize("attempt:initial-unknown"))
	before := f.read(t).Pinned.Pin
	refused := authorize("attempt:initial-duplicate")
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "operation_conflict" {
		t.Fatalf("unknown-effect first launch: outcome=%s error=%+v", refused.Outcome, refused.Error)
	}
	after := f.read(t).Pinned.Pin
	if after.Version != before.Version || !reflect.DeepEqual(after.Attempt, before.Attempt) || !reflect.DeepEqual(after.Review, before.Review) {
		t.Fatal("refused duplicate launch changed execution state")
	}
}
