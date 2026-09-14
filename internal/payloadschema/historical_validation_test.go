package payloadschema

import (
	"encoding/json"
	"testing"
)

func TestHistoricalReproReferenceSibling(t *testing.T) {
	root := map[string]any{"$defs": map[string]any{"text": map[string]any{"type": "string"}}}
	schema := map[string]any{"$ref": "#/$defs/text", "maxLength": json.Number("2")}
	if err := validateSchemaValue("abc", schema, root, "$"); err == nil {
		t.Error("REPRO: reference discarded its sibling constraint")
	}
}

func TestHistoricalReproDateTimeValidation(t *testing.T) {
	schema := map[string]any{"type": "string", "format": "date-time"}
	if err := validateSchemaValue("2026-02-30T00:00:00Z", schema, nil, "$"); err == nil {
		t.Error("REPRO: date-time accepted an invalid calendar date")
	}
}
