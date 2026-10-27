package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// TestInvokeShapesUnsupportedOperationAsTypedEnvelope pins the boundary contract
// cmd/concord depends on: a dispatch refusal reaches the operator as a typed
// invalid_input envelope addressed to the request, never as a bare Go error.
// The shaping lives in this package so a test can reach it (issue #450).
func TestInvokeShapesUnsupportedOperationAsTypedEnvelope(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
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

func TestInvokePreEffectInputCorrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{"work_id": "work-holder", "statement": strings.Repeat("x", 832), "idempotency_key": "input-correction"}
	invoke := func() Envelope {
		t.Helper()
		raw, _ := json.Marshal(input)
		response, err := Invoke(ctx, s, service, mustMarshalInvoke(t, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: raw}, env))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(response); err != nil {
			t.Fatalf("refusal must serialize: %v", err)
		}
		return response
	}
	refused := invoke()
	assertInputCorrection(t, refused, nil, "$.statement")
	for _, fragment := range []string{"maxLength", "$.statement", "832", "512"} {
		if !strings.Contains(refused.Error.Message, fragment) {
			t.Errorf("missing %q in %q", fragment, refused.Error.Message)
		}
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_observations WHERE work_id='work-holder'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refusal wrote observations: count=%d err=%v", count, err)
	}
	input["statement"] = "A corrected bounded observation."
	corrected := invoke()
	if corrected.Outcome != OutcomeOK || corrected.Replayed {
		t.Fatalf("correcting only statement under unchanged authority failed: %+v", corrected)
	}
	if replay := invoke(); replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("committed correction must keep idempotency: %+v", replay)
	}
}

func assertInputCorrection(t *testing.T, response Envelope, err error, diagnostic string) {
	t.Helper()
	if err != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" || response.Error.EffectState != EffectNone || response.Error.RetrySafe || response.Error.RecoveryAction.Kind != "correct_request" {
		t.Fatalf("expected pre-effect input correction: response=%+v err=%v", response.Error, err)
	}
	if !strings.Contains(response.Error.Message, diagnostic) {
		t.Fatalf("input refusal %q does not name %q", response.Error.Message, diagnostic)
	}
	if _, err := response.Encode(); err != nil {
		t.Fatalf("input correction cannot serialize: %v", err)
	}
}

// TestInvokeReturnsErrorOnlyForUndecodableInput proves the one case that still
// travels as a Go error: a payload that cannot be decoded yields no request to
// address, so no envelope can be built for it.
func TestInvokeReturnsErrorOnlyForUndecodableInput(t *testing.T) {
	t.Parallel()
	s, service, _, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})

	response, err := Invoke(context.Background(), s, service, []byte(`{"tool":`))
	if err == nil {
		t.Fatalf("undecodable input must surface as an error, got response=%+v", response)
	}
	if response.Outcome != "" {
		t.Fatalf("undecodable input must yield a zero envelope, got %+v", response)
	}
}
