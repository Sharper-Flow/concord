package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGeneratedPayloadFixturesAreConsumedByGoValidator(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestWorkflowActionSchemaIsActionSpecificAndUsesPublicDispatchFields(t *testing.T) {
	t.Parallel()
	dispatch := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"dispatch_worker","idempotency_key":"dispatch-1","fields":{"lane_id":"research"}}`)
	if err := ValidatePayloadSchema("work_transition_action_public_input", dispatch); err != nil {
		t.Fatalf("public dispatch lane_id rejected: %v", err)
	}

	internalDispatch := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"dispatch_worker","idempotency_key":"dispatch-1","fields":{"attempt_id":"attempt-1","worker_packet":{"schema_version":"1.0","attempt_id":"attempt-1","lane_id":"verify","lane_version":1,"lane_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","work_id":"work-1","step_id":"execute","inputs":{"task":"verify the change"}}}}`)
	if err := ValidatePayloadSchema("work_transition_action_public_input", internalDispatch); err == nil {
		t.Fatal("public schema accepted adapter-owned dispatch fields")
	}
	if err := ValidatePayloadSchema("work_transition_action_input", internalDispatch); err != nil {
		t.Fatalf("core schema rejected adapter-owned dispatch fields: %v", err)
	}

	crossAction := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"bind_evidence","idempotency_key":"bind-1","fields":{"edge_id":"edge:wrong-action"}}`)
	if err := ValidatePayloadSchema("work_transition_action_public_input", crossAction); err == nil {
		t.Fatal("public schema accepted a field from another action")
	}
}

func TestRecoveryWorkflowActionSchemasRejectUnknownFieldsAtBoundary(t *testing.T) {
	t.Parallel()
	for _, action := range []struct {
		id     string
		fields string
	}{
		{id: "reject_worker_result", fields: `{"attempt_id":"attempt-1","attempt_epoch":1,"diagnosis":"bad result","strategy":"retry the worker","predicate_ids":["predicate-1"],"evidence_refs":["evidence-1"]}`},
		{id: "request_correction", fields: `{"diagnosis":"bad result","strategy":"retry the worker","predicate_ids":["predicate-1"],"evidence_refs":["evidence-1"]}`},
	} {
		payload := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"` + action.id + `","idempotency_key":"action-1","fields":` + action.fields + `}`)
		for _, schema := range []string{"work_transition_action_input", "work_transition_action_public_input"} {
			if err := ValidatePayloadSchema(schema, payload); err != nil {
				t.Fatalf("%s rejected by %s: %v", action.id, schema, err)
			}
		}

		var malformed map[string]any
		if err := json.Unmarshal(payload, &malformed); err != nil {
			t.Fatal(err)
		}
		malformedFields := malformed["fields"].(map[string]any)
		malformedFields["unexpected"] = true
		malformedPayload, err := json.Marshal(malformed)
		if err != nil {
			t.Fatal(err)
		}
		for _, schema := range []string{"work_transition_action_input", "work_transition_action_public_input"} {
			err := ValidatePayloadSchema(schema, malformedPayload)
			if err == nil {
				t.Fatalf("%s accepted by %s", action.id, schema)
			}
			if !strings.Contains(err.Error(), "fields.unexpected") {
				t.Fatalf("%s refusal from %s did not name fields.unexpected: %v", action.id, schema, err)
			}
		}
	}
}

func TestWorkerResultAcceptanceBindingIsPublicAndRequired(t *testing.T) {
	t.Parallel()
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

func TestWorkflowActionSchemaAcceptsWorkflowReferencePaths(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"checkpoint_context","idempotency_key":"checkpoint-1","fields":{"active_unit":"repair","hypothesis":"test","diagnosis":"test","strategy":"test","touched_refs":["internal/store/workflow_registry.go"],"evidence_refs":["artifact:test/output.json"],"pending_questions":[],"pending_decisions":[]}}`)
	if err := ValidatePayloadSchema("work_transition_action_input", input); err != nil {
		t.Fatalf("workflow reference path was rejected: %v", err)
	}
}
