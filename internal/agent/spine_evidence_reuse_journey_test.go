package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCriterion13ProducerFactsSurviveStageExitAndFreshSession(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.implement(t)
	f.proof = f.verify(t)
	producer := f.read(t)
	f.review(t, "ready_for_final_proof", f.proof)
	// Workflow engine contract §5.1: typed record actions hold the frontier.
	if step := f.read(t).Pinned.Step; step != producer.Pinned.Step || step != "implement" {
		t.Fatalf("Review moved the producing step to %q", step)
	}
	report := f.read(t).Pinned.Pin.Review.Accepted.ReportRef
	spineOK(t, f.action(t, "complete_step", map[string]any{"step_id": "implement", "note": "Implementation reviewed.", "reviewer": "concord-review", "result": "pass", "commit": f.proofCommit, "link": report}))
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	spineOK(t, f.action(t, "record_delivery", delivery))
	before := f.read(t)
	if before.Pinned.Step != "review" || before.Pinned.Pin.Review == nil || before.Pinned.Pin.Review.Accepted == nil {
		t.Fatal("producer delivery did not preserve Review for its accepting consumer")
	}
	spineOK(t, f.action(t, "record_delivery", delivery))
	before = f.read(t)
	if before.Pinned.Step != "review" {
		t.Fatalf("proof consumer = %q, want review", before.Pinned.Step)
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
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, f.work, f.proof).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("producer facts needed duplicate registration: bindings=%d err=%v", bindings, err)
	}
}
