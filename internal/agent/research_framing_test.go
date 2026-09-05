package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Framing research holds the frame step. The step leaves only through
// approve_contract, so an investigate action before approval is refused and
// the contract stays absent until the operator approves it.
func TestResearchFramingRequiresContractApprovalBeforeInvestigate(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Framing gate research", "research", "workflow.research", "framing-gate-capture")

	action := func(actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		version := agentCompositionWorkVersion(t, s, workID)
		raw, err := json.Marshal(map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "fields": fields, "idempotency_key": key})
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	currentStep := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	contractApprovals := func() int {
		t.Helper()
		var count int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, store.WorkflowContractApproved).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if step := currentStep(); step != "frame" {
		t.Fatalf("captured research step=%q, want frame", step)
	}
	framed := action("frame_research", map[string]any{"premise": "Which surface owns the framing gate?"}, "framing-gate-frame")
	if framed.Outcome != OutcomeOK {
		t.Fatalf("frame_research refused: %+v", framed.Error)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("frame_research advanced the step to %q with no approved contract", step)
	}
	if approvals := contractApprovals(); approvals != 0 {
		t.Fatalf("contract approvals after framing=%d, want 0", approvals)
	}

	investigated := action("record_finding", map[string]any{"summary": "premature finding"}, "framing-gate-finding")
	if investigated.Outcome != OutcomeError || investigated.Error == nil {
		t.Fatalf("record_finding before approval outcome=%q error=%+v, want refusal", investigated.Outcome, investigated.Error)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("refused record_finding moved the step to %q", step)
	}
}
