package payloadschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewArrayByteBoundCountsSerializedUTF8(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value []any
		limit string
		valid bool
	}{
		{"empty at bound", []any{}, "2", true},
		{"empty above bound", []any{}, "1", false},
		{"ascii at bound", []any{"a"}, "5", true},
		{"unicode at bound", []any{"é"}, "6", true},
		{"unicode exceeds bytes not characters", []any{"é"}, "5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := map[string]any{"type": "array", "x-maxArrayBytes": json.Number(tc.limit)}
			err := ValidateValue(tc.value, schema, map[string]any{}, "$.review.resolved_findings")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
			if err != nil && !strings.Contains(err.Error(), "$.review.resolved_findings") {
				t.Fatalf("failure lost the report field: %v", err)
			}
		})
	}
}
