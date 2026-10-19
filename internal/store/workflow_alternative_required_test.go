package store

import "testing"

func TestWorkflowRequiredFieldIsExemptOnlyInItsForbiddenAlternative(t *testing.T) {
	definition := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{
		ID: "record_verdict",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			actionRefField("predicate_id", false),
			actionEnumField("verdict_kind", true, "ok", "outcome_mismatch", "insufficient_evidence"),
			actionItemArrayField("verdicts", false, 1, 8, "workflow_verdict_batch_entry"),
		}},
	}}}
	for _, tc := range []struct {
		name   string
		fields map[string]any
		admit  bool
	}{
		{"single", map[string]any{"predicate_id": "predicate:sample", "verdict_kind": "ok"}, true},
		{"single missing required field", map[string]any{"predicate_id": "predicate:sample"}, false},
		{"batch", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:sample", "verdict_kind": "ok"}}}, true},
		{"forbidden field beside batch", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:sample", "verdict_kind": "ok"}}, "verdict_kind": "ok"}, false},
		{"null batch", map[string]any{"verdicts": nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkflowActionPayload(definition, "record_verdict", mustJSONValue(tc.fields))
			if (err == nil) != tc.admit {
				t.Fatalf("admission = %v, want admitted=%v", err, tc.admit)
			}
		})
	}
}
