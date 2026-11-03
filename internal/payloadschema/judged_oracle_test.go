package payloadschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOracleResolutionProofUnion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		valid  bool
	}{
		{"native", map[string]any{"evidence_refs": []string{"run:one"}}, true},
		{"judged", map[string]any{"judged_case_ids": []string{"case:one"}}, true},
		{"mixed", map[string]any{"evidence_refs": []string{"run:one"}, "judged_case_ids": []string{"case:one"}}, true},
		{"neither", nil, false},
		{"empty judged", map[string]any{"judged_case_ids": []string{}}, false},
		{"empty native with judged", map[string]any{"judged_case_ids": []string{"case:one"}, "evidence_refs": []string{}}, false},
		{"duplicate judged", map[string]any{"judged_case_ids": []string{"case:one", "case:one"}}, false},
		{"duplicate native", map[string]any{"evidence_refs": []string{"run:one", "run:one"}}, false},
		{"invalid judged id", map[string]any{"judged_case_ids": []string{"not-a-case"}}, false},
		{"long judged id", map[string]any{"judged_case_ids": []string{"case:" + strings.Repeat("a", 124)}}, false},
		{"many judged", map[string]any{"judged_case_ids": []string{"case:1", "case:2", "case:3", "case:4", "case:5", "case:6", "case:7", "case:8", "case:9"}}, false},
		{"long native", map[string]any{"evidence_refs": []string{strings.Repeat("a", 257)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := map[string]any{"finding_id": "finding:19:0"}
			for key, field := range tc.fields {
				value[key] = field
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate("lane_report_resolved_finding", raw); (err == nil) != tc.valid {
				t.Fatalf("valid=%v want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
