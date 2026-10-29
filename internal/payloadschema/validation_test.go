package payloadschema

import (
	"encoding/json"
	"testing"
)

func TestReferenceKeepsSiblingConstraints(t *testing.T) {
	root := map[string]any{"$defs": map[string]any{"text": map[string]any{"type": "string"}}}
	schema := map[string]any{"$ref": "#/$defs/text", "maxLength": json.Number("2")}
	if err := ValidateValue("abc", schema, root, "$"); err == nil {
		t.Fatal("reference discarded its sibling bound")
	}
	if err := ValidateValue("ab", schema, root, "$"); err != nil {
		t.Fatal(err)
	}
}

func TestDateTimeFormatIsEnforced(t *testing.T) {
	schema := map[string]any{"type": "string", "format": "date-time"}
	for _, text := range []string{"2026-09-14T12:00:00Z", "2026-09-14T12:00:00.123456789+02:30"} {
		if err := ValidateValue(text, schema, nil, "$"); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	for _, text := range []string{"not-a-time", "2026-02-30T00:00:00Z", "2026-09-14", ""} {
		if err := ValidateValue(text, schema, nil, "$"); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

func TestBoundRefusalsNameUnitActualAndLimit(t *testing.T) {
	cases := []struct {
		value  any
		schema map[string]any
		want   string
	}{
		{"é🙂x", map[string]any{"type": "string", "maxLength": json.Number("2")}, "maxLength at $.task: carries 3 Unicode code points against a limit of 2"},
		{"", map[string]any{"type": "string", "minLength": json.Number("1")}, "minLength at $.task: carries 0 Unicode code points against a minimum of 1"},
		{[]any{"a", "b", "c"}, map[string]any{"type": "array", "maxItems": json.Number("2")}, "maxItems at $.task: carries 3 item(s) against a limit of 2"},
		{[]any{}, map[string]any{"type": "array", "minItems": json.Number("1")}, "minItems at $.task: carries 0 item(s) against a minimum of 1"},
	}
	for _, tc := range cases {
		err := ValidateValue(tc.value, tc.schema, nil, "$.task")
		if err == nil || err.Error() != tc.want {
			t.Errorf("refusal = %v, want %q", err, tc.want)
		}
	}
}
