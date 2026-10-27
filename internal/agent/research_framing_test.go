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
// approve_contract, so an investigate action before approval is refused and
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
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, store.WorkflowContractApproved).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if step := currentStep(); step != "frame" {
		t.Fatalf("captured research step=%q, want frame", step)
	}
	framed := action("frame_research", map[string]any{}, "framing-gate-frame")
	if framed.Outcome != OutcomeOK {
		t.Fatalf("frame_research refused: %+v", framed.Error)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("frame_research advanced the step to %q with no approved contract", step)
	}
	if approvals := contractApprovals(); approvals != 0 {
		t.Fatalf("contract approvals after framing=%d, want 0", approvals)
	}

	investigated := action("record_finding", map[string]any{}, "framing-gate-finding")
	if investigated.Outcome != OutcomeError || investigated.Error == nil {
		t.Fatalf("record_finding before approval outcome=%q error=%+v, want refusal", investigated.Outcome, investigated.Error)
	}
	if step := currentStep(); step != "frame" {
		t.Fatalf("refused record_finding moved the step to %q", step)
	}

	// The frame step records the research question as the contract premise:
	// approval refuses a missing, blank, or placeholder question, and a stated
	// one reaches the operator approval boundary.
	predicates := []map[string]any{{"predicate_id": "predicate:answer", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}}
	for i, fields := range []map[string]any{
		{"outcome_predicates": predicates, "decision_domains": []string{}},
		{"premise": "   ", "outcome_predicates": predicates, "decision_domains": []string{}},
		{"premise": "workflow premise", "outcome_predicates": predicates, "decision_domains": []string{}},
	} {
		refused := action("approve_contract", fields, "framing-gate-unstated-question-"+strconv.Itoa(i))
		if refused.Outcome != OutcomeError || refused.Error == nil {
			t.Fatalf("approve_contract without a stated question %d outcome=%q, want refusal", i, refused.Outcome)
		}
		if refused.Error.Kind != "invalid_input" || !strings.Contains(refused.Error.Message, "$.fields.premise") {
			t.Fatalf("approve_contract without a stated question %d refused as %s: %s, want an invalid premise", i, refused.Error.Kind, refused.Error.Message)
		}
	}
	if approvals := contractApprovals(); approvals != 0 {
		t.Fatalf("contract approvals without a stated question=%d, want 0", approvals)
	}
	stated := action("approve_contract", map[string]any{"premise": "Which retry bound keeps the dispatch route live?", "outcome_predicates": predicates, "decision_domains": []string{"product"}}, "framing-gate-stated-question")
	if stated.Error == nil || stated.Error.Kind != "approval_required" {
		t.Fatalf("approve_contract with a stated question = %+v, want the operator approval boundary", stated.Error)
	}
}
