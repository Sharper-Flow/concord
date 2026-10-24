package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshalInvoke(t *testing.T, request InvokeRequest, env CallEnvelope) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{env, request.Tool, request.Operation, request.Input})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWorkflowActionErrorEnvelopeHasNoFallbackEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "unknown-action", "fields": map[string]any{}, "idempotency_key": "boundary-unknown-action"}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: mustJSON(t, input)}
	decoded, decodedEnv, err := DecodeInvokeRequest(mustMarshalInvoke(t, request, env))
	if err != nil {
		t.Fatal(err)
	}
	before := workflowReplayEffectCounts(t, s)
	response, err := Invoke(context.Background(), s, service, mustMarshalInvoke(t, decoded, decodedEnv))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.RecoveryAction.Kind == "" {
		t.Fatalf("unknown action response=%+v", response)
	}
	if response.Error.Kind != "invalid_input" {
		t.Fatalf("unknown action error=%+v, want invalid_input", response.Error)
	}
	if after := workflowReplayEffectCounts(t, s); after != before {
		t.Fatalf("unknown action changed durable state: before=%+v after=%+v", before, after)
	}
	if after, err := workVersionForWorkflow(t, s); err != nil || after != version {
		t.Fatalf("unknown action changed work version: got=%d want=%d err=%v", after, version, err)
	}
}
