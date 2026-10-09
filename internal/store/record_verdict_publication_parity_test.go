package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// CON-412 behavioral red/green against the ORIGINAL production publisher and
// whole-store admission — no reconstruction of the old publication and no
// synthetic action identity. The same test source runs at the base commit
// (the pre-repair mechanism) and on the current tree:
//
//   - ENGINE: the base record_verdict payload validation carried no
//     cross-field declaration, so validateWorkflowActionPayload admitted a
//     both-forms, neither-form, or entry-field-beside-batch payload; the
//     whole-store path refused it only later, at the verdict shape guard,
//     after the consequential boundary work. The current tree refuses every
//     mutant at payload validation, before any guard work.
//   - PUBLICATION: the base published record_verdict fields object was the
//     lossy union merge of the wire shapes (flattenHostSchema semantics),
//     which kept the properties but dropped every group relationship, so
//     the published boundary validated all the mutant classes too. The
//     current closed variant refuses them.
//
// At the base commit this test fails behaviorally on both halves (each
// mutant is admitted by payload validation, admitted by the published
// fields, and refused only at the late guard stage). On the current tree
// every mutant is refused at payload validation, by the published variant,
// and by whole-store admission, and the two valid wire shapes pass all
// three. A compile failure is not red: the test uses only the base commit's
// own helpers (verdictBatchActionFields, seedWorkflowVerdictBatchFixture,
// runIssue933OperatorAction) and resolves whichever publication shape the
// production document carries.

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
// fields node from the production payload schema document: the closed
// action-discriminated variant when the surface publishes one, and otherwise
// the shared-input if/then condition the older production publisher emitted.
// Both shapes are read exactly as published — nothing is rebuilt here.
func recordVerdictParityPublishedFields(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated payload schema document carries no definitions")
	}
	if variant, ok := defs["work_transition_action_variant_record_verdict"].(map[string]any); ok {
		properties, _ := variant["properties"].(map[string]any)
		return recordVerdictParityResolve(defs, properties["fields"].(map[string]any))
	}
	shared, ok := defs["work_transition_action_shared_input"].(map[string]any)
	if !ok {
		t.Fatal("published document carries neither a record_verdict variant nor the shared action input")
	}
	conditions, _ := shared["allOf"].([]any)
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		trigger, _ := condition["if"].(map[string]any)
		triggerProperties, _ := trigger["properties"].(map[string]any)
		constNode, _ := triggerProperties["action_id"].(map[string]any)
		if constNode["const"] != "record_verdict" {
			continue
		}
		then, _ := condition["then"].(map[string]any)
		thenProperties, _ := then["properties"].(map[string]any)
		return recordVerdictParityResolve(defs, thenProperties["fields"].(map[string]any))
	}
	t.Fatal("published document names no record_verdict fields")
	return nil
}

func TestRecordVerdictCrossFieldOriginalMechanismRedGreen(t *testing.T) {
	document := payloadschema.Document()
	publishedFields := recordVerdictParityPublishedFields(t, document)
	// The batched payload restatement the pinned definitions carry: the same
	// field list both commits pin, so the comparison isolates the cross-field
	// declaration alone.
	definition := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{
		ID: "record_verdict", Payload: WorkflowPayloadDefinition{Closed: true, Fields: verdictBatchActionFields()},
	}}}
	preflightAdmits := func(fields map[string]any) bool {
		return validateWorkflowActionPayload(definition, "record_verdict", mustJSONRaw(t, fields)) == nil
	}
	publishedAdmits := func(fields map[string]any) bool {
		return payloadschema.ValidateValue(recordVerdictParityDecode(t, fields), publishedFields, document, "fields") == nil
	}
	// Whole-store admission through the real action execution path: an
	// approved three-predicate contract at the acceptance step, the same
	// fixture family the batch contract tests seed.
	const workID = "work-record-verdict-parity"
	fixture := seedWorkflowVerdictBatchFixture(t, workID)
	wholeStoreRefusal := func(t *testing.T, fields map[string]any) error {
		t.Helper()
		return runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", json.RawMessage(mustJSONRaw(t, fields)), fixture.owner, fixture.operator)
	}
	single := map[string]any{"contract_version": 1, "predicate_id": "predicate:batch-present", "verdict_kind": "ok", "evaluation_evidence": []any{"evidence:return-route-verification"}}
	batch := map[string]any{"contract_version": 1, "verdicts": []any{map[string]any{"predicate_id": "predicate:batch-absent", "verdict_kind": "ok", "evaluation_evidence": []any{"evidence:return-route-verification"}}}}
	// The alternative-group mutant space: both groups complete, no group at
	// all, and the entry-level members that cannot ride beside the complete
	// batch group (every forbidden member, one case each).
	mutants := map[string]map[string]any{
		"both forms":                                  {"contract_version": 1, "predicate_id": "predicate:batch-present", "verdicts": batch["verdicts"]},
		"neither form":                                {"contract_version": 1},
		"verdict_kind beside the batch":               {"contract_version": 1, "verdicts": batch["verdicts"], "verdict_kind": "ok"},
		"evaluation_evidence beside the batch":        {"contract_version": 1, "verdicts": batch["verdicts"], "evaluation_evidence": []any{"evidence:return-route-verification"}},
		"incomparable_with_approved beside the batch": {"contract_version": 1, "verdicts": batch["verdicts"], "incomparable_with_approved": true},
	}
	// Errorf, not Fatalf: every mutant reports every half, so the red run at
	// the base commit records the full behavioral divergence (engine,
	// publication, whole-store stage) in one pass instead of stopping at
	// the first.
	names := make([]string, 0, len(mutants))
	for name := range mutants {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fields := mutants[name]
		if preflightAdmits(fields) {
			t.Errorf("core payload validation admits the %s mutant", name)
		}
		if publishedAdmits(fields) {
			t.Errorf("published record_verdict fields admit the %s mutant", name)
		}
		err := wholeStoreRefusal(t, fields)
		if err == nil {
			t.Errorf("whole-store admission accepted the %s mutant", name)
		} else {
			var failure *Failure
			if errors.As(err, &failure) && failure.Op != "workflow_action_preflight" {
				t.Errorf("the %s mutant was refused at %q, after the consequential boundaries; payload validation owns the refusal", name, failure.Op)
			}
		}
	}
	for name, fields := range map[string]map[string]any{"single form": single, "batch form": batch} {
		if !preflightAdmits(fields) {
			t.Fatalf("core payload validation refuses the valid %s", name)
		}
		if err := payloadschema.ValidateValue(recordVerdictParityDecode(t, fields), publishedFields, document, "fields"); err != nil {
			t.Fatalf("published record_verdict fields refuse the valid %s: %v", name, err)
		}
		if err := wholeStoreRefusal(t, fields); err != nil {
			t.Fatalf("whole-store admission refuses the valid %s: %v", name, err)
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
