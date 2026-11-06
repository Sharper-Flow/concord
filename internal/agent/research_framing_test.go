package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// Framing research holds the frame step. The step leaves only through
// complete_step, so an investigate action before completion is refused and
// the contract stays absent until the operator approves it.
func TestResearchFramingRequiresContractApprovalBeforeInvestigate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Framing gate research", "research", "workflow.research", "framing-gate-capture")

	action := func(actionID string, fields map[string]any, key string) agentcontract.Envelope {
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
		pin, err := store.ReadWorkPin(ctx, s, workID)
		if err != nil {
			t.Fatal(err)
		}
		return pin.Step
	}
	contractApprovals := func() int {
		t.Helper()
		var count int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, store.WorkflowContractApproved).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if step := currentStep(); step != "intake" {
		t.Fatalf("captured research step=%q, want intake", step)
	}
	for _, prelude := range []struct {
		id     string
		fields map[string]any
	}{
		{"record_intake", map[string]any{"outcome": "proceed"}},
		{"pick_type", map[string]any{"type": "research", "reason": "Investigate the research question."}},
		{"record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."}},
		{"complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."}},
	} {
		if response := action(prelude.id, prelude.fields, "framing-gate-"+prelude.id); response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s: %+v", prelude.id, response.Error)
		}
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("captured research step=%q, want frame", step)
	}
	framed := action("frame_research", map[string]any{}, "framing-gate-frame")
	if framed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("frame_research refused: %+v", framed.Error)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("frame_research advanced the step to %q with no approved contract", step)
	}
	if approvals := contractApprovals(); approvals != 0 {
		t.Fatalf("contract approvals after framing=%d, want 0", approvals)
	}

	investigated := action("record_finding", map[string]any{}, "framing-gate-finding")
	if investigated.Outcome != agentcontract.OutcomeError || investigated.Error == nil {
		t.Fatalf("record_finding before approval outcome=%q error=%+v, want refusal", investigated.Outcome, investigated.Error)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("refused record_finding moved the step to %q", step)
	}

	// The frame step records the research question as the contract premise:
	// contract approval refuses a missing, blank, or placeholder question.
	for i, fields := range []map[string]any{
		{"legislative_subjects": []string{}, "decision_domains": []string{}},
		{"premise": "   ", "legislative_subjects": []string{}, "decision_domains": []string{}},
		{"premise": "workflow premise", "legislative_subjects": []string{}, "decision_domains": []string{}},
	} {
		refused := action("approve_contract", fields, "framing-gate-unstated-question-"+strconv.Itoa(i))
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil {
			t.Fatalf("approve_contract without a stated question %d outcome=%q, want refusal", i, refused.Outcome)
		}
		if refused.Error.Kind != "invalid_input" || !strings.Contains(refused.Error.Message, "$.fields.premise") {
			t.Fatalf("approve_contract without a stated question %d refused as %s: %s, want an invalid premise", i, refused.Error.Kind, refused.Error.Message)
		}
	}
	if approvals := contractApprovals(); approvals != 0 {
		t.Fatalf("contract approvals without a stated question=%d, want 0", approvals)
	}
	stated := action("approve_contract", map[string]any{"premise": "Which retry bound keeps the dispatch route live?", "legislative_subjects": []string{}, "decision_domains": []string{"product"}}, "framing-gate-stated-question")
	if stated.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract with a stated question = %+v, want success", stated.Error)
	}
	if approvals := contractApprovals(); approvals != 1 {
		t.Fatalf("contract approvals with a stated question=%d, want 1", approvals)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("approval moved frontier to %q", step)
	}
	if completed := action("complete_step", map[string]any{"step_id": "frame", "note": "Research framed."}, "framing-gate-complete"); completed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("complete frame: %+v", completed.Error)
	}
	if step := currentStep(); step != "investigate" {
		t.Fatalf("completed frame frontier=%q", step)
	}
}
