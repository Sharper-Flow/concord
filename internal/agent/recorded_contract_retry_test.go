package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestRecordedContractCallRetry(t *testing.T) {
	subjects := []string{"Verified assistant text is not a valid reference."}
	input := map[string]any{
		"work_id": "work-example", "expected_version": 10,
		"action_id": "approve_contract", "idempotency_key": "request-example",
		"fields": map[string]any{
			"premise":              "Repair first-assistant-output detection without changing timeout recovery.",
			"decision_domains":     []string{},
			"legislative_subjects": subjects,
		},
	}
	validate := func() error {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", raw, false)
	}
	if err := validate(); err == nil || !strings.Contains(err.Error(), "$.fields.legislative_subjects[0]") {
		t.Fatalf("invalid legislative subject did not produce its refusal: %v", err)
	} else {
		t.Logf("recorded payload refused: %v", err)
	}
	input["fields"].(map[string]any)["legislative_subjects"] = []string{"first-assistant-output"}
	if err := validate(); err != nil {
		t.Fatalf("corrected identifiers refused: %v", err)
	}
	t.Log("corrected identifiers accepted with decision_domains unchanged; no live workflow mutation issued")
}

func TestContractStructuralRefusalsDeliverCompleteGuidance(t *testing.T) {
	for _, tc := range []struct{ name, input, field string }{
		{"missing outer", `{"work_id":"work-example","action_id":"approve_contract","idempotency_key":"request-example","fields":{}}`, "expected_version"},
		{"unknown outer", `{"work_id":"work-example","expected_version":1,"action_id":"approve_contract","idempotency_key":"request-example","extra":true,"fields":{}}`, "extra"},
		{"long unknown", `{"work_id":"work-example","expected_version":1,"action_id":"approve_contract","idempotency_key":"request-example","` + strings.Repeat("x", 1000) + `":true,"fields":{}}`, "member name sha256:"},
		{"missing fields", `{"work_id":"work-example","expected_version":1,"action_id":"approve_contract","idempotency_key":"request-example"}`, "fields"},
		{"invalid subject", `{"work_id":"work-example","expected_version":1,"action_id":"approve_contract","idempotency_key":"request-example","fields":{"premise":"A bounded change.","decision_domains":[],"legislative_subjects":["bad subject"]}}`, "legislative_subjects"},
		{"many missing", `{"work_id":"work-example","expected_version":1,"title":"Updated plan","kind":"task","reason":"Revise the plan","idempotency_key":"request-example","contract":{"decision_domains":[]}}`, "contract_version"},
		{"unknown nested", `{"work_id":"work-example","expected_version":1,"action_id":"approve_contract","idempotency_key":"request-example","fields":{"premise":"A bounded change.","decision_domains":[],"extra":true,"legislative_subjects":["example"]}}`, "extra"},
		{"extended unknown", `{"work_id":"work-example","expected_version":1,"title":"Updated plan","kind":"task","reason":"Revise the plan","idempotency_key":"request-example","contract":{"contract_version":2,"premise":"A bounded change.","decision_domains":[],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"production_internal","extra":true,"legislative_subjects":["example"]}}`, "extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, operation := "concord_work_transition", "workflow_action"
			if tc.name == "many missing" || tc.name == "extended unknown" {
				tool, operation = "concord_work_define", "revise_intent"
			}
			out, err := Dispatch(context.Background(), nil, nil, InvokeRequest{Tool: tool, Operation: operation, Input: json.RawMessage(tc.input)}, CallEnvelope{RequestID: "request-example"})
			if err != nil || out.Error == nil {
				t.Fatalf("missing typed refusal: %+v, %v", out, err)
			}
			message := out.Error.Message
			if out.Error.Kind != "invalid_input" || out.Error.RetrySafe || out.Error.EffectState != agentcontract.EffectNone || out.Error.RecoveryAction.Kind != "correct_request" {
				t.Fatalf("typed refusal changed: %+v", out.Error)
			}
			if !strings.Contains(message, tc.field) || !strings.Contains(message, "Accepted shape:") || !strings.Contains(message, "example:") || strings.Contains(message, "[truncated]") || len(message) > 1000 {
				t.Fatalf("incomplete guidance (%d bytes): %s", len(message), message)
			}
			if strings.Contains(message, "unknown property $.fields.decision_domains") {
				t.Fatalf("branch-local field restriction leaked: %s", message)
			}
			if _, err := out.Encode(); err != nil {
				t.Fatalf("refusal is undeliverable: %v", err)
			}
		})
	}
}
