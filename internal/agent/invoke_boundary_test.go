package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// TestInvokeShapesUnsupportedOperationAsTypedEnvelope pins the boundary contract
// cmd/concord depends on: a dispatch refusal reaches the operator as a typed
// invalid_input envelope addressed to the request, never as a bare Go error.
// This shaping previously lived inside cmd/concord's invoke verb, where no test
// could reach it (issue #450).
func TestInvokeShapesUnsupportedOperationAsTypedEnvelope(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "not_a_contract_operation", Input: json.RawMessage(`{}`)}

	response, err := Invoke(context.Background(), s, service, mustMarshalInvoke(t, request, env))
	if err != nil {
		t.Fatalf("dispatch refusal must not surface as a transport error: %v", err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("response=%+v, want an invalid_input error outcome", response)
	}
	if response.Error.RecoveryAction.Kind != "restart_query" {
		t.Fatalf("recovery action=%q, want restart_query", response.Error.RecoveryAction.Kind)
	}
	if response.RequestID != env.RequestID || response.Tool != request.Tool || response.Operation != request.Operation {
		t.Fatalf("envelope is not addressed to the refused request: %+v", response)
	}
}

// TestInvokeReturnsErrorOnlyForUndecodableInput proves the one case that still
// travels as a Go error: a payload that cannot be decoded yields no request to
// address, so no envelope can be built for it.
func TestInvokeReturnsErrorOnlyForUndecodableInput(t *testing.T) {
	s, service, _, _ := mutationDispatchFixture(t, []Capability{"work_transition"})

	response, err := Invoke(context.Background(), s, service, []byte(`{"tool":`))
	if err == nil {
		t.Fatalf("undecodable input must surface as an error, got response=%+v", response)
	}
	if response.Outcome != "" {
		t.Fatalf("undecodable input must yield a zero envelope, got %+v", response)
	}
}
