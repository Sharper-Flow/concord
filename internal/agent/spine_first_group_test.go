package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSpineFirstGroupResumeDoesNotRestoreFirstEntry(t *testing.T) {
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	f.verify(t)
	f.implement(t)
	before := f.read(t).Pinned.Pin
	var planJSON string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&planJSON); err != nil {
		t.Fatal(err)
	}
	var plan store.WorkflowStepPlan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.ID == "implement" && len(step.Attempts) != 0 {
			t.Fatalf("first entry manufactured an implementation review: %+v", step.Attempts)
		}
	}
	assertHeld := func() {
		t.Helper()
		refused := f.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:first-group", "delivery_state": "asserted"})
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_transition" {
			t.Fatalf("a progressed implementation admitted delivery: %+v", refused.Error)
		}
		after := f.read(t).Pinned.Pin
		var afterPlan string
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&afterPlan); err != nil {
			t.Fatal(err)
		}
		if after.Version != before.Version || after.Lifecycle != before.Lifecycle || afterPlan != planJSON || !reflect.DeepEqual(after.WorkContext, before.WorkContext) {
			t.Fatal("refused delivery changed work context, step plan, or lifecycle")
		}
	}
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
