package store

import "testing"

func TestWorkflowVerdictBatchExcludesScalarFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		admit  bool
	}{
		{"batch", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:sample", "receipt_refs": []string{"worktree_verify:sample"}}}}, true},
		{"scalar", map[string]any{"predicate_id": "predicate:sample", "verdict_kind": "ok"}, false},
		{"missing batch", map[string]any{}, false},
		{"forbidden field beside batch", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:sample", "receipt_refs": []string{"worktree_verify:sample"}}}, "verdict_kind": "ok"}, false},
		{"null batch", map[string]any{"verdicts": nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateWorkflowPlanActionPayload("record_verdict", mustJSONValue(tc.fields))
			if (err == nil) != tc.admit {
				t.Fatalf("admission = %v, want admitted=%v", err, tc.admit)
			}
		})
	}
}
