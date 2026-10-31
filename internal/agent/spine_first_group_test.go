package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSpineFirstGroupResumeDoesNotRestoreFirstEntry(t *testing.T) {
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	f.verify(t)
	if f.read(t).Pinned.Pin.Review.Accepted != nil {
		t.Fatal("first entry installed an unsolicited baseline Review")
	}
	f.implement(t)
	before := f.read(t).Pinned.Pin
	if before.Review.Candidate != nil || before.Review.Accepted != nil {
		t.Fatalf("report manufactured Review: %+v", before.Review)
	}
	assertHeld := func() {
		t.Helper()
		refused := f.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:first-group", "delivery_state": "asserted"})
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "missing_evidence" {
			t.Fatalf("a progressed execution reopened its first group: %+v", refused)
		}
		after := f.read(t).Pinned.Pin
		if after.Version != before.Version || after.Lifecycle != before.Lifecycle || !reflect.DeepEqual(after.Review, before.Review) || !reflect.DeepEqual(after.WorkContext, before.WorkContext) {
			t.Fatal("refused delivery changed work context, Review, or lifecycle")
		}
	}
	assertHeld()
	f.administrativeRecovery(t)
	before = f.read(t).Pinned.Pin
	assertHeld()
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
	assertHeld()
}
