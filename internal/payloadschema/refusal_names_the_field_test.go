package payloadschema

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeSchema reads one schema document the way Validate does, so a test
// exercises the same json.Number-bearing shapes the production path sees.
func decodeSchema(t *testing.T, document string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("schema document is not JSON: %v", err)
	}
	return value
}

func decodeValue(t *testing.T, document string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("instance document is not JSON: %v", err)
	}
	return value
}

// A schema factored through $defs composes allOf at one instance path more
// than once. Each composition frame used to prepend "allOf mismatch at $: ",
// so the caller read the same structural keyword twice before the field that
// was actually wrong. For allOf every branch must match, so the branch failure
// is already the whole reason and no frame belongs in front of it.
func TestComposedRefusalNamesTheOffendingFieldOnce(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"inner": {
				"allOf": [
					{
						"type": "object",
						"properties": {"fields": {"type": "object", "additionalProperties": false, "properties": {"evidence_ref": {"type": "string"}}}}
					}
				]
			}
		},
		"type": "object",
		"allOf": [{"$ref": "#/$defs/inner"}]
	}`)
	value := decodeValue(t, `{"fields": {"evidence_commit": "abc"}}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("an unknown property under a composed schema was accepted")
	}
	if got := err.Error(); got != "unknown property $.fields.evidence_commit" {
		t.Errorf("refusal does not lead with the offending field: %s", got)
	}
}

// anyOf keeps a frame, because every branch failed and no single branch
// failure is the whole reason. The frame must still carry the causes.
func TestAnyOfRefusalKeepsItsCauses(t *testing.T) {
	root := decodeSchema(t, `{
		"anyOf": [
			{"type": "object", "required": ["left"]},
			{"type": "object", "required": ["right"]}
		]
	}`)
	value := decodeValue(t, `{"middle": 1}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("a value matching no anyOf branch was accepted")
	}
	got := err.Error()
	if !strings.HasPrefix(got, "anyOf mismatch at $: ") {
		t.Errorf("anyOf refusal lost its frame: %s", got)
	}
	for _, field := range []string{"left", "right"} {
		if !strings.Contains(got, field) {
			t.Errorf("anyOf refusal does not name %s: %s", field, got)
		}
	}
}

// Every oneOf variant in the agent payload contract is a bare $ref into
// $defs. The variant describer read "required" and "not" straight off the
// branch object, so each variant collapsed to "requires no fields" and the
// refusal named nothing the caller could act on. It must resolve the ref and
// report the const that discriminates the variants.
func TestOneOfVariantDescriptionResolvesItsRef(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"exists": {
				"type": "object",
				"additionalProperties": false,
				"required": ["kind", "surface"],
				"properties": {"kind": {"const": "exists"}, "surface": {"type": "string"}}
			},
			"check": {
				"type": "object",
				"additionalProperties": false,
				"required": ["kind", "check_ref"],
				"properties": {"kind": {"const": "check"}, "check_ref": {"type": "string"}}
			}
		},
		"oneOf": [{"$ref": "#/$defs/exists"}, {"$ref": "#/$defs/check"}]
	}`)
	value := decodeValue(t, `{"kind": "outcome", "surface": "s"}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("a value matching no oneOf variant was accepted")
	}
	got := err.Error()
	if strings.Contains(got, "requires no fields") {
		t.Errorf("variant description did not resolve its $ref: %s", got)
	}
	for _, want := range []string{`kind="exists"`, `kind="check"`, "surface", "check_ref"} {
		if !strings.Contains(got, want) {
			t.Errorf("variant description does not name %s: %s", want, got)
		}
	}
}
