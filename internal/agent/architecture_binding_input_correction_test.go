package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestInvokeResearchArchitectureBindingCorrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Research request correction", "research", "workflow.research", "binding-capture")
	action := func(actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"work_id": workID, "expected_version": workVersion(t, s, workID), "action_id": actionID, "fields": fields, "idempotency_key": key})
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:" + key
		response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if framed := action("frame_research", map[string]any{}, "binding-frame"); framed.Outcome != OutcomeOK {
		t.Fatalf("frame research: %+v", framed.Error)
	}
	snapshot := func() [3]int64 {
		t.Helper()
		state := [3]int64{workVersion(t, s, workID)}
		if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&state[1]); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&state[2]); err != nil {
			t.Fatal(err)
		}
		return state
	}
	fields := map[string]any{
		"premise":            "Which retry bound preserves the existing dispatch route?",
		"decision_domains":   []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:answer", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
		"architecture_binding": map[string]any{
			"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64), "home_domain_id": "root", "affected_domain_ids": []string{"root"},
			"domain_modifies": []string{}, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{},
		},
	}
	before := snapshot()
	if before[2] != 0 {
		t.Fatal("research already has a contract")
	}
	refused := action("approve_contract", fields, "binding-approve")
	if after := snapshot(); after != before {
		t.Fatalf("refusal mutated version/events/contracts: %v -> %v", before, after)
	}
	assertInputCorrection(t, refused, nil, "architecture_binding")
	if !strings.Contains(refused.Error.Message, "non-Product-changing approval cannot carry architecture_binding") {
		t.Fatalf("refusal lost the non-Product-changing boundary: %+v", refused.Error)
	}
	if refused.RequestID != env.RequestID || refused.Tool != "concord_work_transition" || refused.Operation != "workflow_action" {
		t.Fatalf("refusal lost request identity: %+v", refused)
	}
	delete(fields, "architecture_binding")
	if corrected := action("approve_contract", fields, "binding-approve"); corrected.Outcome != OutcomeOK {
		t.Fatalf("correcting only architecture_binding under unchanged authority failed: %+v", corrected.Error)
	}
	var workflowRef, step, premise string
	if err := s.DatabaseForTesting().QueryRow(`SELECT i.definition_ref,i.current_step,c.premise FROM workflow_instances i JOIN workflow_contracts c ON c.work_id=i.work_id WHERE i.work_id=?`, workID).Scan(&workflowRef, &step, &premise); err != nil {
		t.Fatal(err)
	}
	if workflowRef != "workflow.research" || step != "investigate" || premise != fields["premise"] || snapshot()[2] != 1 {
		t.Fatalf("correction changed research scope: workflow=%s step=%s premise=%s", workflowRef, step, premise)
	}
}
