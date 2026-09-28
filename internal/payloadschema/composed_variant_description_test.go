package payloadschema

import (
	"strings"
	"testing"
)

// The agent envelope schema factors every outcome variant through allOf: the
// discriminating const and the required fields sit inside composition frames,
// and the variant itself is a bare $ref. A describer that reads "required"
// and "properties" only off the resolved variant reports every branch as
// "requires no fields", so a root oneOf mismatch names nothing the caller
// can act on. It must compose the frames.
func TestComposedVariantDescriptionNamesItsRequirements(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"base": {
				"allOf": [
					{"$ref": "#/$defs/identity"},
					{"if": {"properties": {"authority": {"const": "degraded"}}, "required": ["authority"]},
					 "then": {"properties": {"omissions": {"minItems": 1}}}}
				]
			},
			"identity": {
				"type": "object",
				"required": ["tool", "operation"],
				"properties": {"tool": {"type": "string"}, "operation": {"type": "string"}}
			},
			"ok": {
				"allOf": [
					{"$ref": "#/$defs/base"},
					{"type": "object", "required": ["outcome"], "properties": {"outcome": {"const": "ok"}}}
				]
			},
			"errorOutcome": {
				"allOf": [
					{"$ref": "#/$defs/base"},
					{"type": "object", "required": ["outcome", "error"], "properties": {"outcome": {"const": "error"}, "error": {"type": "object"}}}
				]
			}
		},
		"oneOf": [{"$ref": "#/$defs/ok"}, {"$ref": "#/$defs/errorOutcome"}]
	}`)
	value := decodeValue(t, `{"tool":"t","operation":"op","outcome":"pending"}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("a value matching no composed variant was accepted")
	}
	got := err.Error()
	if strings.Contains(got, "requires no fields") {
		t.Errorf("a composed variant must not describe itself as requiring no fields: %s", got)
	}
	for _, want := range []string{`outcome="ok"`, `outcome="error"`, "error"} {
		if !strings.Contains(got, want) {
			t.Errorf("composed refusal does not name %s: %s", want, got)
		}
	}
}

// A conditional frame must not report its requirements as unconditional. The
// if branch requires "authority" only when the envelope is degraded, so the
// variant's unconditional requirement list stays the frames' own required
// lists.
func TestComposedVariantDescriptionKeepsConditionalFramesOut(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"variant": {
				"allOf": [
					{"type": "object", "required": ["outcome"], "properties": {"outcome": {"const": "ok"}}},
					{"if": {"required": ["authority"], "properties": {"authority": {"const": "degraded"}}},
					 "then": {"required": ["omissions"]}}
				]
			}
		},
		"oneOf": [{"$ref": "#/$defs/variant"}]
	}`)
	value := decodeValue(t, `{"outcome":"pending"}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("a value matching the variant was wrongly refused")
	}
	got := err.Error()
	if !strings.Contains(got, "requires [outcome]") || strings.Contains(got, "omissions") {
		t.Errorf("refusal must name the unconditional requirements alone: %s", got)
	}
}
