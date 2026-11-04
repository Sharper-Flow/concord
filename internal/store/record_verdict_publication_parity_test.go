package store

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// recordVerdictParityResolve resolves one #/$defs reference, or returns the
// node itself when it is not a reference.
func recordVerdictParityResolve(defs map[string]any, node map[string]any) map[string]any {
	if node == nil {
		return nil
	}
	ref, ok := node["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/$defs/") {
		return node
	}
	if target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any); ok {
		return target
	}
	return node
}

// TestMultibyteStringBoundsKeepCoreAndPublicationAligned pins the repaired
// representation contract between core string bounds and the published
// schema. Core validation counts UTF-8 BYTES (validateWorkflowPayloadValue's
// len(text)); the published standard minLength/maxLength count UNICODE CODE
// POINTS, so they are DERIVED from the enforcing byte bounds — maxLength
// equals the byte maximum and minLength is ceil(byte minimum / 4) — and can
// never refuse a core-admitted string, while x-maxBytes/x-minBytes carry the
// enforcing byte bounds the byte-aware validators apply. Both divergence
// directions are asserted on real published fields with boundary fixtures:
// a two-byte one-code-point premise the core admits, a one-byte string under
// the byte floor the core refuses, a max-byte ASCII string at both limits,
// and a max-code-point all-two-byte string whose bytes double the maximum
// (CON-412).
func TestMultibyteStringBoundsKeepCoreAndPublicationAligned(t *testing.T) {
	twoByteOneRune := "é"
	if utf8.RuneCountInString(twoByteOneRune) != 1 || len(twoByteOneRune) != 2 {
		t.Fatalf("fixture %q is not one code point of two bytes", twoByteOneRune)
	}
	document := payloadschema.Document()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated payload schema document carries no definitions")
	}
	schemaNumber := func(node map[string]any, keyword string) string {
		if number, ok := node[keyword].(json.Number); ok {
			return number.String()
		}
		return ""
	}
	stringNode := func(variant string, field string) map[string]any {
		t.Helper()
		node := recordVerdictParityResolve(defs, map[string]any{"$ref": "#/$defs/" + variant})
		properties, ok := node["properties"].(map[string]any)
		if !ok {
			t.Fatalf("published document carries no closed %s variant; the older publisher never emitted one", variant)
		}
		fields := recordVerdictParityResolve(defs, properties["fields"].(map[string]any))
		if branches, ok := fields["oneOf"].([]any); ok {
			for _, raw := range branches {
				branch, _ := raw.(map[string]any)
				branchProperties, _ := branch["properties"].(map[string]any)
				if candidate, ok := branchProperties[field].(map[string]any); ok {
					return recordVerdictParityResolve(defs, candidate)
				}
			}
		}
		fieldProperties, _ := fields["properties"].(map[string]any)
		if fieldProperties[field] == nil {
			t.Fatalf("published %s fields declare no %s property in any branch", variant, field)
		}
		return recordVerdictParityResolve(defs, fieldProperties[field].(map[string]any))
	}
	// approve_contract.premise: the promoted floor is two bytes and the byte
	// maximum is 4096. The derived code-point bounds admit everything the
	// core admits, in both directions.
	premiseNode := stringNode("work_transition_action_variant_approve_contract", "premise")
	if minLength := schemaNumber(premiseNode, "minLength"); minLength != "1" {
		t.Fatalf("published premise minLength is %q, want the derived ceil(2/4)=1 code points", minLength)
	}
	if maxBytes := schemaNumber(premiseNode, "x-maxBytes"); maxBytes != "4096" {
		t.Fatalf("published premise x-maxBytes is %q, want the enforcing 4096 UTF-8 bytes", maxBytes)
	}
	if minBytes := schemaNumber(premiseNode, "x-minBytes"); minBytes != "2" {
		t.Fatalf("published premise x-minBytes is %q, want the enforcing 2 UTF-8 bytes", minBytes)
	}
	premiseField := WorkflowPayloadField{Name: "premise", ValueType: PayloadString, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)}
	if !validateWorkflowPayloadValue(premiseField, mustJSONRaw(t, twoByteOneRune)) {
		t.Fatal("core refuses the two-byte one-code-point string its own byte floor admits")
	}
	if err := payloadschema.ValidateValue(twoByteOneRune, premiseNode, document, "premise"); err != nil {
		t.Fatalf("published premise bounds refused a core-admitted string: %v", err)
	}
	if validateWorkflowPayloadValue(premiseField, mustJSONRaw(t, "v")) {
		t.Fatal("core admitted a one-byte string under its two-byte floor")
	}
	if err := payloadschema.ValidateValue("v", premiseNode, document, "premise"); err == nil {
		t.Fatal("published bounds admitted a one-byte string under the core byte floor")
	}
	maxByteASCII := strings.Repeat("v", 4096)
	if !validateWorkflowPayloadValue(premiseField, mustJSONRaw(t, maxByteASCII)) {
		t.Fatal("core refuses the 4096-byte string its own maximum admits")
	}
	if err := payloadschema.ValidateValue(maxByteASCII, premiseNode, document, "premise"); err != nil {
		t.Fatalf("published premise bounds refused a core-admitted maximum string: %v", err)
	}
	// The other direction at the max bound: approve_contract.premise is
	// bounded at 4096 bytes core-side. A 4096-code-point all-two-byte string
	// is 8192 bytes — core refuses it and the byte bound refuses it, where
	// the old code-point-equal maxLength admitted it.
	maxBoundMultibyte := strings.Repeat("é", 4096)
	if len(maxBoundMultibyte) != 8192 || utf8.RuneCountInString(maxBoundMultibyte) != 4096 {
		t.Fatal("fixture is not 4096 code points of 8192 bytes")
	}
	searchedNode := premiseNode
	if maxLength := schemaNumber(searchedNode, "maxLength"); maxLength != "4096" {
		t.Fatalf("published searched maxLength is %q, want 4096", maxLength)
	}
	if maxBytes := schemaNumber(searchedNode, "x-maxBytes"); maxBytes != "4096" {
		t.Fatalf("published searched x-maxBytes is %q, want the enforcing 4096 UTF-8 bytes", maxBytes)
	}
	searchedField := WorkflowPayloadField{Name: "searched", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)}
	if validateWorkflowPayloadValue(searchedField, mustJSONRaw(t, maxBoundMultibyte)) {
		t.Fatal("core admitted a string beyond its 4096-byte bound")
	}
	if err := payloadschema.ValidateValue(maxBoundMultibyte, searchedNode, document, "searched"); err == nil {
		t.Fatal("published bounds admitted an 8192-byte string over the core byte maximum")
	}
	// A multibyte string inside both bounds stays admitted everywhere: two
	// code points of four bytes sit at the floor and far under the maximum.
	insideBounds := strings.Repeat("é", 2)
	if !validateWorkflowPayloadValue(searchedField, mustJSONRaw(t, insideBounds)) {
		t.Fatal("core refuses a four-byte string inside its bounds")
	}
	if err := payloadschema.ValidateValue(insideBounds, searchedNode, document, "searched"); err != nil {
		t.Fatalf("published bounds refused a core-admitted multibyte string: %v", err)
	}
}

func mustJSONRaw(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
