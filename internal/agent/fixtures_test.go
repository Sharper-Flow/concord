package agent

import (
	"encoding/json"
	"os"
	"testing"
)

func TestGeneratedPayloadFixturesAreConsumedByGoValidator(t *testing.T) {
	data, err := os.ReadFile("../../contracts/agent-tool-surface.fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		ManifestDigest string `json:"manifest_digest"`
		Fixtures       []struct {
			InputSchema        string            `json:"input_schema"`
			InputValid         json.RawMessage   `json:"input_valid"`
			InputInvalidCases  []json.RawMessage `json:"input_invalid_cases"`
			ResultSchema       string            `json:"result_schema"`
			ResultValid        json.RawMessage   `json:"result_valid"`
			ResultInvalidCases []json.RawMessage `json:"result_invalid_cases"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.ManifestDigest != ManifestDigest || len(corpus.Fixtures) != len(ContractOperations) {
		t.Fatalf("fixture corpus drift: digest=%s fixtures=%d", corpus.ManifestDigest, len(corpus.Fixtures))
	}
	for _, fixture := range corpus.Fixtures {
		if err := ValidatePayloadSchema(fixture.InputSchema, fixture.InputValid); err != nil {
			t.Errorf("valid input %s rejected: %v", fixture.InputSchema, err)
		}
		for _, invalid := range fixture.InputInvalidCases {
			if err := ValidatePayloadSchema(fixture.InputSchema, invalid); err == nil {
				t.Errorf("invalid input %s accepted", fixture.InputSchema)
			}
		}
		if err := ValidatePayloadSchema(fixture.ResultSchema, fixture.ResultValid); err != nil {
			t.Errorf("valid result %s rejected: %v", fixture.ResultSchema, err)
		}
		for _, invalid := range fixture.ResultInvalidCases {
			if err := ValidatePayloadSchema(fixture.ResultSchema, invalid); err == nil {
				t.Errorf("invalid result %s accepted", fixture.ResultSchema)
			}
		}
	}
}

func TestWorkflowCompletionImpactVerdictIsPublicAndRequired(t *testing.T) {
	valid := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"complete","idempotency_key":"complete-1","fields":{"impact_verdict":"breaking"}}`)
	if err := ValidatePayloadSchema("work_transition_action_input", valid); err != nil {
		t.Fatalf("explicit completion impact verdict rejected: %v", err)
	}

	missing := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"complete","idempotency_key":"complete-1","fields":{}}`)
	if err := ValidatePayloadSchema("work_transition_action_input", missing); err == nil {
		t.Fatal("completion without impact verdict accepted")
	}

	invalid := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"complete","idempotency_key":"complete-1","fields":{"impact_verdict":"informational"}}`)
	if err := ValidatePayloadSchema("work_transition_action_input", invalid); err == nil {
		t.Fatal("completion with invalid impact verdict accepted")
	}
}

func TestWorkerResultAcceptanceBindingIsPublicAndRequired(t *testing.T) {
	valid := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"accept_worker_result","idempotency_key":"accept-1","fields":{"attempt_id":"attempt-1","attempt_epoch":1}}`)
	if err := ValidatePayloadSchema("work_transition_action_input", valid); err != nil {
		t.Fatalf("explicit worker result binding rejected: %v", err)
	}

	for name, input := range map[string]json.RawMessage{
		"missing attempt": json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"accept_worker_result","idempotency_key":"accept-1","fields":{"attempt_epoch":1}}`),
		"missing epoch":   json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"accept_worker_result","idempotency_key":"accept-1","fields":{"attempt_id":"attempt-1"}}`),
		"invalid epoch":   json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"accept_worker_result","idempotency_key":"accept-1","fields":{"attempt_id":"attempt-1","attempt_epoch":0}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePayloadSchema("work_transition_action_input", input); err == nil {
				t.Fatal("invalid worker result acceptance input was accepted")
			}
		})
	}
}
