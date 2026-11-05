package agent

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestCaptureOpensIntakeForEveryWorkKind(t *testing.T) {
	for _, kind := range []string{"task", "bug", "decision", "research", "other"} {
		t.Run(kind, func(t *testing.T) {
			s, service, grant, scopeVersion := captureTaskFixture(t)
			input := fmt.Sprintf(`{"title":"Need","kind":%q,"project_ids":["project-1"],"human_key":"ZZ-7000","remote_issue_uuid":"fixture-intake-%s","url":"https://linear.app/example/issue/ZZ-7000","idempotency_key":"capture-intake"}`, kind, kind)
			response := dispatchCapture(t, s, service, grant, scopeVersion, "capture-intake", input)
			if response.Outcome != agentcontract.OutcomeOK || response.ChangedRefs == nil || len(*response.ChangedRefs) != 2 {
				t.Fatalf("capture response = %+v", response)
			}
			changed := (*response.ChangedRefs)[0]
			var state, actor, capturedKind string
			var workflowType, stepPlan *string
			var version int64
			err := s.DatabaseForTesting().QueryRow(`SELECT i.instance_state, i.execution_actor_ref, i.workflow_type, i.step_plan, w.version, w.kind FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE i.work_id=?`, changed.ID).Scan(&state, &actor, &workflowType, &stepPlan, &version, &capturedKind)
			if err != nil {
				t.Fatal(err)
			}
			if state != "intake" || actor == "" || workflowType != nil || stepPlan != nil || version < 1 || changed.Version != strconv.FormatInt(version, 10) {
				t.Fatalf("intake state=%q actor=%q type=%v plan=%v version=%d changed=%+v", state, actor, workflowType, stepPlan, version, changed)
			}
			if capturedKind != kind {
				t.Fatalf("captured kind=%q, want %q", capturedKind, kind)
			}
			replay := dispatchCapture(t, s, service, grant, scopeVersion, "capture-intake", input)
			if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || replay.ChangedRefs == nil || len(*replay.ChangedRefs) != 2 || (*replay.ChangedRefs)[0].ID != changed.ID {
				t.Fatalf("capture replay = %+v", replay)
			}
		})
	}
}
