package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
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
	if corpus.ManifestDigest != agentcontract.ManifestDigest || len(corpus.Fixtures) != len(agentcontract.ContractOperations) {
		t.Fatalf("fixture corpus drift: digest=%s fixtures=%d", corpus.ManifestDigest, len(corpus.Fixtures))
	}
	for _, fixture := range corpus.Fixtures {
		if err := payloadschema.Validate(fixture.InputSchema, fixture.InputValid); err != nil {
			t.Errorf("valid input %s rejected: %v", fixture.InputSchema, err)
		}
		for _, invalid := range fixture.InputInvalidCases {
			if err := payloadschema.Validate(fixture.InputSchema, invalid); err == nil {
				t.Errorf("invalid input %s accepted", fixture.InputSchema)
			}
		}
		if err := payloadschema.Validate(fixture.ResultSchema, fixture.ResultValid); err != nil {
			t.Errorf("valid result %s rejected: %v", fixture.ResultSchema, err)
		}
		for _, invalid := range fixture.ResultInvalidCases {
			if err := payloadschema.Validate(fixture.ResultSchema, invalid); err == nil {
				t.Errorf("invalid result %s accepted", fixture.ResultSchema)
			}
		}
	}
}

func TestWorkflowCompletionImpactVerdictIsPublicAndRequired(t *testing.T) {
	t.Parallel()
	valid := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"complete","idempotency_key":"complete-1","fields":{"impact_verdict":"breaking"}}`)
	if err := payloadschema.Validate("work_transition_action_input", valid); err != nil {
		t.Fatalf("explicit completion impact verdict rejected: %v", err)
	}

	missing := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"complete","idempotency_key":"complete-1","fields":{}}`)
	if err := payloadschema.Validate("work_transition_action_input", missing); err == nil {
		t.Fatal("completion without impact verdict accepted")
	}

	invalid := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"complete","idempotency_key":"complete-1","fields":{"impact_verdict":"informational"}}`)
	if err := payloadschema.Validate("work_transition_action_input", invalid); err == nil {
		t.Fatal("completion with invalid impact verdict accepted")
	}
}

func TestWorkflowActionSchemaIsActionSpecific(t *testing.T) {
	t.Parallel()
	valid := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"bind_evidence","idempotency_key":"bind-1","fields":{"evidence_kind":"verification","evidence_ref":"worktree_verify:one","producer_watermark":"request:one"}}`)
	for _, schema := range []string{"work_transition_action_public_input", "work_transition_action_input"} {
		if err := payloadschema.Validate(schema, valid); err != nil {
			t.Fatalf("%s rejected ordinary evidence binding: %v", schema, err)
		}
		missingWatermark := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"bind_evidence","idempotency_key":"bind-1","fields":{"evidence_kind":"verification","evidence_ref":"worktree_verify:one"}}`)
		if err := payloadschema.Validate(schema, missingWatermark); err == nil {
			t.Fatalf("%s accepted evidence without a producer watermark", schema)
		}
	}

	crossAction := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"bind_evidence","idempotency_key":"bind-1","fields":{"edge_id":"edge:wrong-action"}}`)
	if err := payloadschema.Validate("work_transition_action_public_input", crossAction); err == nil {
		t.Fatal("public schema accepted a field from another action")
	}
}

func TestWorkflowActionSchemasRejectUnknownFieldsAtBoundary(t *testing.T) {
	t.Parallel()
	for _, action := range []struct {
		id     string
		fields string
	}{
		{id: "bind_evidence", fields: `{"evidence_kind":"verification","evidence_ref":"worktree_verify:one","producer_watermark":"request:one"}`},
	} {
		payload := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"` + action.id + `","idempotency_key":"action-1","fields":` + action.fields + `}`)
		for _, schema := range []string{"work_transition_action_input", "work_transition_action_public_input"} {
			if err := payloadschema.Validate(schema, payload); err != nil {
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
			err := payloadschema.Validate(schema, malformedPayload)
			if err == nil {
				t.Fatalf("%s accepted by %s", action.id, schema)
			}
			if !strings.Contains(err.Error(), "fields.unexpected") {
				t.Fatalf("%s refusal from %s did not name fields.unexpected: %v", action.id, schema, err)
			}
		}
	}
}

func TestReviewStepRecordIsPublicAndRequired(t *testing.T) {
	t.Parallel()
	review := map[string]any{"step_id": "review", "note": "Review passed.", "reviewer": "reviewer:one", "result": "pass", "commit": strings.Repeat("a", 40), "link": "https://example.com/review"}
	input := map[string]any{"work_id": "work-1", "expected_version": 1, "action_id": "record_review", "idempotency_key": "review-1", "fields": review}
	for _, schema := range []string{"work_transition_action_input", "work_transition_action_public_input"} {
		if err := payloadschema.Validate(schema, retryJSON(input)); err != nil {
			t.Fatalf("%s rejected complete Review subject binding: %v", schema, err)
		}
	}
	for _, member := range []string{"step_id", "note", "reviewer", "result", "commit", "link"} {
		t.Run("missing "+member, func(t *testing.T) {
			var incomplete map[string]any
			if err := json.Unmarshal(retryJSON(review), &incomplete); err != nil {
				t.Fatal(err)
			}
			delete(incomplete, member)
			input["fields"] = incomplete
			for _, schema := range []string{"work_transition_action_input", "work_transition_action_public_input"} {
				if err := payloadschema.Validate(schema, retryJSON(input)); err == nil {
					t.Fatalf("%s admitted Review without %s", schema, member)
				}
			}
		})
	}
}

func TestWorkflowActionSchemaAcceptsWorkflowReferencePaths(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"work_id":"work-1","expected_version":1,"action_id":"bind_evidence","idempotency_key":"evidence-1","fields":{"evidence_ref":"artifact:test/output.json","producer_watermark":"request:test"}}`)
	if err := payloadschema.Validate("work_transition_action_input", input); err != nil {
		t.Fatalf("workflow reference path was rejected: %v", err)
	}
}
