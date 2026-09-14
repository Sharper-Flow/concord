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
