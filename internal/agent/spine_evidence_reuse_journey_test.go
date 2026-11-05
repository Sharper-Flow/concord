package agent

import (
	"context"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestCriterion13ProducerFactsSurviveStageExitAndFreshSession(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.implement(t)
	f.proof = f.verify(t)
	f.review(t, "pass")
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	spineOK(t, f.action(t, "record_delivery", delivery))
	before := f.read(t)
	if before.Pinned.Step != "review" {
		t.Fatal("producer delivery did not reach the review step")
	}
	spineOK(t, f.action(t, "record_delivery", delivery))
	before = f.read(t)
	if before.Pinned.Step != "review" {
		t.Fatalf("proof consumer = %q, want review", before.Pinned.Step)
	}
	var beforePlan string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&beforePlan); err != nil {
		t.Fatal(err)
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
	var afterPlan string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&afterPlan); err != nil || afterPlan != beforePlan || after.Pinned.Step != before.Pinned.Step {
		t.Fatalf("fresh session lost the reviewed step or frontier: %v", err)
	}
	var bindings int
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, f.work, f.proof).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("producer facts needed duplicate registration: bindings=%d err=%v", bindings, err)
	}
}
