package store

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// recordVerdictParityPublishedFields resolves the published record_verdict
// fields node from the current production action-discriminated variant.
func recordVerdictParityPublishedFields(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated payload schema document carries no definitions")
	}
	variant, ok := defs["work_transition_action_variant_record_verdict"].(map[string]any)
	if !ok {
		t.Fatal("published document carries no current record_verdict variant")
	}
	properties, _ := variant["properties"].(map[string]any)
	fields, ok := properties["fields"].(map[string]any)
	if !ok {
		t.Fatal("published record_verdict variant carries no fields schema")
	}
	return recordVerdictParityResolve(defs, fields)
}

func TestRecordVerdictReceiptPublicationMatchesCurrentAdmission(t *testing.T) {
	document := payloadschema.Document()
	publishedFields := recordVerdictParityPublishedFields(t, document)
	live, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	entry := func(refs []any) map[string]any {
		return map[string]any{"predicate_id": "predicate:primary", "receipt_refs": refs}
	}
	batch := func(value any) map[string]any { return map[string]any{"verdicts": value} }
	for _, tc := range []struct {
		name   string
		fields map[string]any
		want   bool
	}{
		{"one receipt", batch([]any{entry([]any{"receipt:captured"})}), true},
		{"multiple receipts", batch([]any{entry([]any{"receipt:first", "receipt:second"})}), true},
		{"missing batch", map[string]any{}, false},
		{"empty batch", batch([]any{}), false},
		{"null batch", batch(nil), false},
		{"missing receipt field", batch([]any{map[string]any{"predicate_id": "predicate:primary"}}), false},
		{"empty receipts", batch([]any{entry([]any{})}), false},
		{"null receipts", batch([]any{entry(nil)}), false},
		{"empty receipt reference", batch([]any{entry([]any{""})}), false},
		{"scalar form", map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []any{"receipt:captured"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := validateWorkflowActionPayload(live.Definition, "record_verdict", mustJSONRaw(t, tc.fields))
			published := payloadschema.ValidateValue(recordVerdictParityDecode(t, tc.fields), publishedFields, document, "fields")
			if (core == nil) != tc.want || (published == nil) != tc.want {
				t.Fatalf("core=%v publication=%v, want admission %v", core, published, tc.want)
			}
		})
	}
	for _, bounds := range []struct {
		criteria, receipts int
		want               bool
	}{{8, 1, true}, {9, 1, false}, {1, 32, true}, {1, 33, false}} {
		var entries []any
		for i := 0; i < bounds.criteria; i++ {
			var refs []any
			for j := 0; j < bounds.receipts; j++ {
				refs = append(refs, fmt.Sprintf("receipt:captured-%d", j))
			}
			value := entry(refs)
			value["predicate_id"] = fmt.Sprintf("predicate:criterion-%d", i)
			entries = append(entries, value)
		}
		fields := batch(entries)
		core := validateWorkflowActionPayload(live.Definition, "record_verdict", mustJSONRaw(t, fields))
		published := payloadschema.ValidateValue(recordVerdictParityDecode(t, fields), publishedFields, document, "fields")
		if (core == nil) != bounds.want || (published == nil) != bounds.want {
			t.Errorf("%d criteria with %d receipts: core=%v publication=%v, want admission %v", bounds.criteria, bounds.receipts, core, published, bounds.want)
		}
	}
	for _, name := range []string{"predicate_id", "verdict_kind", "evaluation_evidence", "incomparable_with_approved"} {
		for _, nested := range []bool{false, true} {
			value := entry([]any{"receipt:captured"})
			fields := batch([]any{value})
			if nested {
				if name == "predicate_id" {
					continue
				}
				value[name] = "forbidden"
			} else {
				fields[name] = "forbidden"
			}
			core := validateWorkflowActionPayload(live.Definition, "record_verdict", mustJSONRaw(t, fields))
			published := payloadschema.ValidateValue(recordVerdictParityDecode(t, fields), publishedFields, document, "fields")
			if core == nil || published == nil {
				t.Errorf("field %s (nested=%v) escaped the closed receipt schema: core=%v publication=%v", name, nested, core, published)
			}
		}
	}
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
	// The other direction at the max bound: record_alignment.searched is
	// bounded at 4096 bytes core-side. A 4096-code-point all-two-byte string
	// is 8192 bytes — core refuses it and the byte bound refuses it, where
	// the old code-point-equal maxLength admitted it.
	maxBoundMultibyte := strings.Repeat("é", 4096)
	if len(maxBoundMultibyte) != 8192 || utf8.RuneCountInString(maxBoundMultibyte) != 4096 {
		t.Fatal("fixture is not 4096 code points of 8192 bytes")
	}
	searchedNode := stringNode("work_transition_action_variant_record_alignment", "searched")
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

// recordVerdictParityDecode round-trips a value through UseNumber decoding,
// the numeric form the payload schema validator walks.
func recordVerdictParityDecode(t *testing.T, value any) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(mustJSONRaw(t, value)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustJSONRaw(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
