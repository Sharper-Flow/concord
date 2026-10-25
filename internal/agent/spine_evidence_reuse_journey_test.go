package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCriterion13ProducerFactsSurviveStageExitAndFreshSession(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	f.completeWorker(t)
	f.proof = f.verify(t)
	f.recordJob(t)
	f.review(t, "ready_for_final_proof", f.proof)
	before := f.read(t)
	if before.Pinned.Pin.Review == nil || before.Pinned.Pin.Review.Accepted == nil {
		t.Fatal("producer Review did not reach its accepting consumer")
	}
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	spineOK(t, f.action(t, "record_delivery", delivery))
	if got := f.read(t).Pinned.Step; got == before.Pinned.Step {
		t.Fatal("accepted producer facts did not authorize the execution exit")
	}
	spineOK(t, f.action(t, "checkpoint_refine", map[string]any{}))
	spineOK(t, f.action(t, "record_delivery", delivery))
	spineOK(t, f.action(t, "record_delivery", delivery))
	before = f.read(t)
	if before.Pinned.Step != "acceptance" {
		t.Fatalf("proof consumer = %q, want acceptance", before.Pinned.Step)
	}
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
	f.env.SessionRef, f.env.AgentRef = "session-proof-consumer", "agent-evaluator"
	after := f.read(t)
	if !reflect.DeepEqual(before.Pinned.Pin.Review, after.Pinned.Pin.Review) {
		t.Fatal("fresh session lost the accepted producer Review")
	}
	verdict := map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}
	spineOK(t, f.action(t, "record_verdict", verdict))
	var bindings int
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, f.work, f.proof).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("producer facts needed duplicate registration: bindings=%d err=%v", bindings, err)
	}
	if err := os.WriteFile(filepath.Join(f.path, "pkg/value.go"), []byte("package spine\nfunc Value() int { return 2 + 5 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "changed candidate")
	before = f.read(t)
	refused := f.action(t, "record_verdict", verdict)
	after = f.read(t)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "stale_requires_review" || refused.Error.EffectState != EffectNone || before.Pinned.Pin.Version != after.Pinned.Pin.Version || !reflect.DeepEqual(before.Pinned.Pin.Review, after.Pinned.Pin.Review) {
		t.Fatalf("changed candidate reused the original pass or changed its attribution: %+v", refused.Error)
	}
}
