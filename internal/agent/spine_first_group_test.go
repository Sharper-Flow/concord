package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// First entry without Review applies only where no architecture is recorded;
// the break-fix family has none.
func TestSpineFirstGroupFailedProofPreservesUnreviewedCandidate(t *testing.T) {
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	f.verify(t)
	f.prepareControl(t)
	failed := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"go", "test", "./missing"}, "idempotency_key": "first-group-failed-proof",
	}))
	var proof store.WorktreeVerifyResult
	if err := json.Unmarshal(failed.Result, &proof); err != nil || proof.ExitCode == 0 {
		t.Fatalf("fixture did not capture a failed proof: %s (%v)", failed.Result, err)
	}
	before := f.read(t).Pinned.Pin
	if before.Review.Accepted != nil {
		t.Fatal("failure or revision manufactured a Review")
	}
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\nfunc Value() int { return 3 + 4 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "finish the relaunched assignment")
	f.verify(t)
	after := f.read(t).Pinned.Pin
	if after.Review.Accepted != nil || after.Review.Candidate == nil || after.Review.Candidate.Commit != gitRun(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("same-assignment relaunch lost its result or required Review: %+v", after.Review)
	}
}

func TestSpineFirstGroupResumeDoesNotRestoreFirstEntry(t *testing.T) {
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	f.verify(t)
	f.prepareControl(t)
	if f.read(t).Pinned.Pin.Review.Accepted != nil {
		t.Fatal("first entry installed an unsolicited baseline Review")
	}
	f.implement(t)
	before := f.read(t).Pinned.Pin
	if before.Review.Candidate == nil || before.Review.Accepted != nil {
		t.Fatalf("first result did not produce the unreviewed integrated candidate: %+v", before.Review)
	}
	assertHeld := func() {
		t.Helper()
		refused := f.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:first-group", "delivery_state": "asserted"})
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "missing_evidence" {
			t.Fatalf("a progressed execution reopened its first group: %+v", refused)
		}
		after := f.read(t).Pinned.Pin
		if after.Version != before.Version || after.Lifecycle != before.Lifecycle || !reflect.DeepEqual(after.Review, before.Review) {
			t.Fatal("refused group opening changed the candidate, disposition, findings, or lifecycle")
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
