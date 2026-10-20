package payloadschema

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// freshPayloadDocument parses the generated schema document the way Validate
// does, so guidance tests mutate their own copy and never the shared
// singleton another test reads.
func freshPayloadDocument(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(GeneratedPayloadSchemaDocument))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("generated payload schema document is not JSON: %v", err)
	}
	var strip func(any)
	strip = func(node any) {
		switch node := node.(type) {
		case map[string]any:
			if description, ok := node["description"].(string); ok {
				if at := strings.Index(description, guidanceMarker); at >= 0 {
					prefix := strings.TrimSpace(description[:at])
					if prefix == "" {
						delete(node, "description")
					} else {
						node["description"] = prefix
					}
					delete(node, "examples")
				}
			}
			for key, child := range node {
				if key != "examples" {
					strip(child)
				}
			}
		case []any:
			for _, child := range node {
				strip(child)
			}
		}
	}
	strip(value)
	return value
}

const guidanceNames = "work_transition_action_variant_approve_contract"
const guidanceNamesBoth = guidanceNames + ",work_transition_action_variant_supersede_contract"

func annotateGuidanceNames(t *testing.T, names string) map[string]any {
	t.Helper()
	root := freshPayloadDocument(t)
	split := strings.Split(names, ",")
	if err := AnnotateGuidance(root, split); err != nil {
		t.Fatalf("AnnotateGuidance refused the published schema names: %v", err)
	}
	return root
}

func guidanceNodeAtPath(t *testing.T, root map[string]any, path ...any) map[string]any {
	t.Helper()
	var current any = root
	for _, step := range path {
		switch typed := step.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("guidance path walks through a non-object at %v", step)
			}
			current = object[typed]
		case int:
			list, ok := current.([]any)
			if !ok {
				t.Fatalf("guidance path walks through a non-array at %v", step)
			}
			current = list[typed]
		default:
			t.Fatalf("unsupported path step %v", step)
		}
	}
	node, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("guidance path did not reach a schema object")
	}
	return node
}

var approveVariantPath = []any{"$defs", "work_transition_action_variant_approve_contract", "properties", "fields", "oneOf", 1, "properties", "decision_domains"}

// AnnotateGuidance carries one schema-derived example and an appended bounded
// description on the reachable property and ref schemas of the named
// definitions, including shared definitions reached through references.
func TestAnnotateGuidanceAnnotatesReachableSchemas(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNamesBoth)

	decisionDomains := guidanceNodeAtPath(t, root, approveVariantPath...)
	examples, ok := decisionDomains["examples"].([]any)
	if !ok || len(examples) != 1 {
		t.Fatalf("decision_domains carries %v, want one schema-derived example", decisionDomains["examples"])
	}
	// minItems is 0, so the minimal example is the empty array.
	if encoded, err := json.Marshal(examples[0]); err != nil || string(encoded) != `[]` {
		t.Fatalf("decision_domains example = %s (%v), want the minimal empty array", encoded, err)
	}
	if description, _ := decisionDomains["description"].(string); !strings.HasPrefix(description, guidanceMarker) {
		t.Fatalf("decision_domains description = %q, want the appended shape segment", description)
	}

	// A shared definition reached through a ref carries guidance once, so a
	// leaf failure can teach the subject pattern wherever subjects appear.
	subjects, ok := root["$defs"].(map[string]any)["workflow_outcome_subject"].(map[string]any)
	if !ok {
		t.Fatal("workflow_outcome_subject definition is missing")
	}
	if examples, ok := subjects["examples"].([]any); !ok || len(examples) != 1 {
		t.Fatalf("workflow_outcome_subject carries %v, want one example", subjects["examples"])
	}
	if description, _ := subjects["description"].(string); !strings.Contains(description, "matching ^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{1,127}$") {
		t.Fatalf("workflow_outcome_subject description does not teach its pattern: %q", description)
	}

	if _, exists := subjects["required"]; exists {
		// Guard against an annotation write leaking a validation keyword.
		t.Fatal("annotation wrote a validation keyword onto a shared definition")
	}
}

func TestAnnotateGuidanceRefusesUnknownNames(t *testing.T) {
	root := freshPayloadDocument(t)
	if err := AnnotateGuidance(root, []string{"work_transition_action_variant_approve_contract", "not_a_schema"}); err == nil {
		t.Fatal("an unknown schema name was accepted silently")
	}
	empty := map[string]any{}
	if err := AnnotateGuidance(empty, []string{"work_transition_action_variant_approve_contract"}); err == nil {
		t.Fatal("a document without definitions was accepted silently")
	}
}

// Every attached example must pass the schema it annotates, references and
// compositions included: an example the schema would refuse teaches a lie.
func TestAnnotateGuidanceExamplesValidateAgainstTheirSchemas(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNamesBoth)
	checked := 0
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if examples, ok := node["examples"].([]any); ok {
			for _, example := range examples {
				checked++
				if err := ValidateValue(example, node, root, "$"); err != nil {
					t.Fatalf("attached example does not satisfy its own schema: %v", err)
				}
			}
		}
		if defs, ok := node["$defs"].(map[string]any); ok {
			for _, name := range sortedKeys(defs) {
				if child, ok := defs[name].(map[string]any); ok {
					walk(child)
				}
			}
		}
		for _, keyword := range []string{"properties", "patternProperties"} {
			if children, ok := node[keyword].(map[string]any); ok {
				for _, name := range sortedKeys(children) {
					if child, ok := children[name].(map[string]any); ok {
						walk(child)
					}
				}
			}
		}
		for _, keyword := range []string{"additionalProperties", "items", "if", "then", "else", "not"} {
			if child, ok := node[keyword].(map[string]any); ok {
				walk(child)
			}
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
			branches, _ := node[keyword].([]any)
			for _, raw := range branches {
				if branch, ok := raw.(map[string]any); ok {
					walk(branch)
				}
			}
		}
	}
	walk(root)
	if checked < 40 {
		t.Fatalf("guidance walk checked only %d examples, want the annotated tree", checked)
	}
}

// An annotation changes no validation keyword: the only added keys are
// examples and description, an existing description only gains the appended
// segment, and every other key keeps its exact value.
func TestAnnotateGuidanceChangesOnlyAnnotationKeywords(t *testing.T) {
	before := freshPayloadDocument(t)
	after := annotateGuidanceNames(t, guidanceNames)

	var compare func(want, got any, path string)
	compare = func(want, got any, path string) {
		wantObject, wantIsObject := want.(map[string]any)
		gotObject, gotIsObject := got.(map[string]any)
		if !wantIsObject || !gotIsObject {
			wantList, wantIsList := want.([]any)
			gotList, gotIsList := got.([]any)
			if wantIsList && gotIsList {
				if len(wantList) != len(gotList) {
					t.Fatalf("%s changed length under annotation: %d != %d", path, len(wantList), len(gotList))
				}
				for index := range wantList {
					compare(wantList[index], gotList[index], path+"[]")
				}
				return
			}
			if !guidanceLeafEqual(want, got) {
				t.Fatalf("%s changed under annotation: %v != %v", path, want, got)
			}
			return
		}
		for name := range wantObject {
			if _, exists := gotObject[name]; !exists {
				t.Fatalf("%s lost key %s under annotation", path, name)
			}
		}
		for name, gotValue := range gotObject {
			wantValue, exists := wantObject[name]
			switch name {
			case "examples":
				if exists {
					t.Fatalf("%s already carried examples before annotation", path)
				}
			case "description":
				gotDescription, _ := gotValue.(string)
				if exists {
					wantDescription, _ := wantValue.(string)
					if gotDescription == wantDescription {
						// An ungeneratable node keeps its description.
						break
					}
					suffix := strings.TrimPrefix(gotDescription, wantDescription+" ")
					if wantDescription == "" {
						suffix = gotDescription
					}
					if suffix == gotDescription || !strings.HasPrefix(suffix, guidanceMarker) {
						t.Fatalf("%s description was not a pure append: %q", path, gotDescription)
					}
				} else if gotDescription != "" && !strings.HasPrefix(gotDescription, guidanceMarker) {
					t.Fatalf("%s description is not guidance: %q", path, gotDescription)
				}
			default:
				if !exists {
					t.Fatalf("%s gained key %s under annotation", path, name)
				}
				compare(wantValue, gotValue, path+"."+name)
			}
		}
	}
	compare(before, after, "$")
}

// guidanceLeafEqual compares non-object schema leaves by their canonical JSON,
// so slices and numbers compare by content rather than by identity.
func guidanceLeafEqual(want, got any) bool {
	wantEncoded, wantErr := json.Marshal(want)
	gotEncoded, gotErr := json.Marshal(got)
	if wantErr != nil || gotErr != nil {
		return wantErr == nil && gotErr == nil
	}
	return string(wantEncoded) == string(gotEncoded)
}

// Annotation is idempotent: a second call leaves one teaching segment.
func TestAnnotateGuidanceIsIdempotent(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNames)
	if err := AnnotateGuidance(root, []string{guidanceNames}); err != nil {
		t.Fatalf("second AnnotateGuidance refused: %v", err)
	}
	subjects := root["$defs"].(map[string]any)["workflow_outcome_subject"].(map[string]any)
	description, _ := subjects["description"].(string)
	if strings.Count(description, guidanceMarker) != 1 {
		t.Fatalf("repeated annotation stacked segments: %q", description)
	}
}

// A refused contract request against the annotated document carries the
// field, the failed constraint, the accepted shape, and one minimal example
// inside the boundary's 1000-byte message budget, with no branch-local
// unknown-property claim about decision_domains.
func TestAnnotatedContractFailureCarriesShapeAndExample(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNamesBoth)
	fields := `{
		"premise": "The premise states the bounded change.",
		"decision_domains": [],
		"outcome_predicates": [
			{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "exists",
			 "outcome_payload": {"kind": "exists", "surface": "go_source", "subjects": ["The acceptance sentence has spaces"]}}
		]
	}`
	data := contractGuidanceInput(t, "approve_contract", "guidance-approve-1", fields)

	err := ValidateInDocument(root, "work_transition_action_input", data)
	if err == nil {
		t.Fatal("the recorded predicate subject was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "pattern at $.fields.outcome_predicates[0].outcome_payload.subjects[0]") {
		t.Fatalf("annotated refusal lost the leaf failure: %s", got)
	}
	if !strings.Contains(got, guidanceMarker) || !strings.Contains(got, "example:") {
		t.Fatalf("annotated refusal does not teach shape and example: %s", got)
	}
	if !strings.Contains(got, "matching ^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{1,127}$") {
		t.Fatalf("annotated refusal does not name the accepted shape: %s", got)
	}
	if strings.Contains(got, "unknown property $.fields.decision_domains") {
		t.Fatalf("annotated refusal still carries the branch-local unknown-property claim: %s", got)
	}
	if len(got) > 1000 {
		t.Fatalf("annotated refusal carries %d bytes against the 1000-byte message budget: %s", len(got), got)
	}

	// The second recorded near-miss: subjects corrected, prose surface.
	fields = `{
		"premise": "The premise states the bounded change.",
		"decision_domains": [],
		"outcome_predicates": [
			{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "exists",
			 "outcome_payload": {"kind": "exists", "surface": "plugin TTFT event handling", "subjects": ["internal-store-workpin"]}}
		]
	}`
	data = contractGuidanceInput(t, "approve_contract", "guidance-approve-2", fields)
	err = ValidateInDocument(root, "work_transition_action_input", data)
	if err == nil {
		t.Fatal("the recorded prose surface was accepted")
	}
	got = err.Error()
	if !strings.Contains(got, "pattern at $.fields.outcome_predicates[0].outcome_payload.surface") {
		t.Fatalf("annotated refusal lost the surface leaf failure: %s", got)
	}
	if !strings.Contains(got, guidanceMarker) || len(got) > 1000 {
		t.Fatalf("annotated surface refusal is unguided or over budget (%d bytes): %s", len(got), got)
	}
}

// Without annotation the refusal stays exactly the schema's own teaching: no
// guidance segment appears, and generic bounds keep their exact messages.
func TestUnannotatedRefusalCarriesNoGuidance(t *testing.T) {
	fields := `{
		"premise": "The premise states the bounded change.",
		"decision_domains": [],
		"outcome_predicates": [
			{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "exists",
			 "outcome_payload": {"kind": "exists", "surface": "go_source", "subjects": ["has spaces"]}}
		]
	}`
	data := contractGuidanceInput(t, "approve_contract", "unguided-1", fields)
	err := ValidateInDocument(freshPayloadDocument(t), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("the invalid subject was accepted")
	}
	if got := err.Error(); strings.Contains(got, guidanceMarker) {
		t.Fatalf("an unannotated document produced guidance: %s", got)
	}

	value := "é🙂x"
	schema := map[string]any{"type": "string", "maxLength": json.Number("2")}
	want := "maxLength at $.task: carries 3 Unicode code points against a limit of 2"
	if got := ValidateValue(value, schema, nil, "$.task"); got == nil || got.Error() != want {
		t.Fatalf("generic bound refusal = %v, want exactly %q", got, want)
	}
}

// The strict digest pattern's 64-hex repeat expands in full: the annotation
// exists and its example satisfies the exact 71-character bound.
func TestAnnotateGuidanceExpandsFullDigestRepeat(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNames)
	hash := root["$defs"].(map[string]any)["workflow_law_text_pin"].(map[string]any)["properties"].(map[string]any)["content_hash"].(map[string]any)
	examples, ok := hash["examples"].([]any)
	if !ok || len(examples) != 1 {
		t.Fatalf("content_hash carries %v, want one example", hash["examples"])
	}
	encoded, err := json.Marshal(examples[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 73 || !strings.HasPrefix(string(encoded), `"sha256:`) {
		t.Fatalf("content_hash example = %s, want a 71-character sha256 digest", encoded)
	}
	if err := ValidateValue(examples[0], hash, root, "$"); err != nil {
		t.Fatalf("digest example does not satisfy its own schema: %v", err)
	}
	if description, _ := hash["description"].(string); !strings.Contains(description, "matching ^sha256:[0-9a-f]{64}$ (71..71 code points)") {
		t.Fatalf("content_hash description does not teach the full pattern: %q", description)
	}
}

// A reference chain with sibling bounds at every hop generates against the
// whole chain, annotates every hop, and writes only annotations.
func TestAnnotateGuidanceFollowsChainedRefsWithSiblingBounds(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"outer": {"$ref": "#/$defs/middle", "maxLength": 5},
			"middle": {"$ref": "#/$defs/inner", "minLength": 2},
			"inner": {"type": "string", "pattern": "^[a-z]+$"}
		},
		"properties": {"code": {"$ref": "#/$defs/outer"}}
	}`)
	if err := AnnotateGuidance(root, []string{"outer", "middle", "inner"}); err != nil {
		t.Fatalf("chained refs refused: %v", err)
	}
	defs := root["$defs"].(map[string]any)
	outer, ok := defs["outer"].(map[string]any)
	if !ok {
		t.Fatal("outer definition missing")
	}
	examples, ok := outer["examples"].([]any)
	if !ok || len(examples) != 1 {
		t.Fatalf("outer carries %v, want one example built from the whole chain", outer["examples"])
	}
	// The example must satisfy pattern, the chain's minLength 2 and
	// maxLength 5 at once: the minimal synthesis honors the chain's floor.
	encoded, _ := json.Marshal(examples[0])
	if string(encoded) != `"aa"` {
		t.Fatalf("chained example = %s, want the minimal string at the chain's minLength floor", encoded)
	}
	if err := ValidateValue(examples[0], outer, root, "$"); err != nil {
		t.Fatalf("chained example does not satisfy the ref chain: %v", err)
	}
}

func TestGuidanceIntersectsRepeatedReferenceBounds(t *testing.T) {
	root := decodeSchema(t, `{"$defs":{"inner":{"type":"string","minLength":2,"maxLength":3},"outer":{"$ref":"#/$defs/inner","minLength":1,"maxLength":5}}}`)
	if err := AnnotateGuidance(root, []string{"outer"}); err != nil {
		t.Fatal(err)
	}
	outer := guidanceNodeAtPath(t, root, "$defs", "outer")
	if description := outer["description"].(string); !strings.Contains(description, "2..3 code points") {
		t.Fatalf("reference sibling relaxed the effective bound: %s", description)
	}
}

// A minLength floor beside a pattern with optional and star constructs is met
// by expanding the pattern's own optional structure, not by dropping it.
func TestAnnotateGuidanceMeetsMinLengthThroughOptionalExpansion(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {"token": {"type": "string", "pattern": "^x?a+$", "minLength": 3}},
		"properties": {"token": {"$ref": "#/$defs/token"}}
	}`)
	if err := AnnotateGuidance(root, []string{"token"}); err != nil {
		t.Fatalf("optional-expansion example refused: %v", err)
	}
	token := root["$defs"].(map[string]any)["token"].(map[string]any)
	examples, ok := token["examples"].([]any)
	if !ok || len(examples) != 1 {
		t.Fatalf("token carries %v, want one example", token["examples"])
	}
	encoded, _ := json.Marshal(examples[0])
	if string(encoded) != `"xaa"` {
		t.Fatalf("token example = %s, want the expanded optional x and two a's", encoded)
	}
	if err := ValidateValue(examples[0], token, root, "$"); err != nil {
		t.Fatalf("expanded example does not satisfy its own schema: %v", err)
	}
}

// Generation fails explicitly, and mutates nothing, when a constrained field
// cannot yield a valid example.
func TestAnnotateGuidanceFailsExplicitlyAndLeavesTheDocumentUntouched(t *testing.T) {
	before := decodeSchema(t, `{
		"$defs": {
			"unsatisfiable": {"type": "string", "pattern": "^z[0-9]+$", "minLength": 80, "maxLength": 90}
		},
		"properties": {"field": {"$ref": "#/$defs/unsatisfiable"}}
	}`)
	root := decodeSchema(t, `{
		"$defs": {
			"unsatisfiable": {"type": "string", "pattern": "^z[0-9]+$", "minLength": 80, "maxLength": 90}
		},
		"properties": {"field": {"$ref": "#/$defs/unsatisfiable"}}
	}`)
	err := AnnotateGuidance(root, []string{"unsatisfiable"})
	if err == nil {
		t.Fatal("an unsatisfiable constrained field was silently skipped")
	}
	if !strings.Contains(err.Error(), "unsatisfiable") || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("failure does not name the schema and its cause: %v", err)
	}
	compareGuidanceUnchanged(t, before, root)
}

// An oversized example fails explicitly rather than being published or cut.
func TestAnnotateGuidanceFailsOnOversizedExample(t *testing.T) {
	names := make([]string, 0, 60)
	properties := map[string]any{}
	required := make([]any, 0, 60)
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("field%02d", i)
		names = append(names, name)
		properties[name] = map[string]any{"type": "string"}
		required = append(required, name)
	}
	schema := map[string]any{"type": "object", "properties": properties, "required": required}
	root := map[string]any{"$defs": map[string]any{"wide": schema}}
	err := AnnotateGuidance(root, []string{"wide"})
	if err == nil {
		t.Fatal("an oversized example was silently skipped")
	}
	if !strings.Contains(err.Error(), "example budget") {
		t.Fatalf("failure does not name the budget: %v", err)
	}
	if _, annotated := schema["examples"]; annotated {
		t.Fatal("the oversized node was annotated despite the failure")
	}
}

// A pure combinator container and a name-only branch carry no authored field,
// so they skip without error and stay unannotated.
func TestAnnotateGuidanceSkipsCombinatorAndNameOnlyNodes(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"choice": {"oneOf": [{"type": "string"}, {"type": "integer"}]},
			"variant": {"oneOf": [{"required": ["left"]}, {"required": ["right"]}]}
		},
		"properties": {"value": {"$ref": "#/$defs/choice"}}
	}`)
	if err := AnnotateGuidance(root, []string{"choice", "variant"}); err != nil {
		t.Fatalf("combinator containers refused: %v", err)
	}
	for _, name := range []string{"choice", "variant"} {
		node := root["$defs"].(map[string]any)[name].(map[string]any)
		if _, annotated := node["examples"]; annotated {
			t.Fatalf("%s is a pure combinator container but was annotated", name)
		}
		for _, raw := range node["oneOf"].([]any) {
			branch := raw.(map[string]any)
			_, annotated := branch["examples"]
			if _, typed := branch["type"]; typed {
				// A typed branch is an authored field and carries guidance.
				if !annotated {
					t.Fatalf("a typed branch of %s carries no guidance", name)
				}
				continue
			}
			if annotated {
				t.Fatalf("a name-only branch of %s was annotated", name)
			}
		}
	}
}

// Minimal shapes: an object without required fields samples nothing optional,
// an array carries exactly minItems, and a closed object's example carries
// exactly its required keys.
func TestAnnotateGuidanceBuildsTrulyMinimalShapes(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNamesBoth)
	defs := root["$defs"].(map[string]any)

	// decision_domains: minItems 0 → empty array.
	decisionDomains := guidanceNodeAtPath(t, root, approveVariantPath...)
	if encoded, _ := json.Marshal(decisionDomains["examples"].([]any)[0]); string(encoded) != `[]` {
		t.Fatalf("decision_domains example = %s, want []", encoded)
	}

	// architecture_binding: exactly its required keys, nothing optional.
	binding := defs["architecture_binding"].(map[string]any)
	example := binding["examples"].([]any)[0].(map[string]any)
	required := map[string]bool{}
	for _, name := range schemaFieldList(binding["required"]) {
		required[name] = true
	}
	if len(example) != len(required) {
		t.Fatalf("architecture_binding example carries %d keys against %d required", len(example), len(required))
	}
	for name := range example {
		if !required[name] {
			t.Fatalf("architecture_binding example carries optional key %q", name)
		}
	}

	// subjects: minItems 1 → exactly one entry.
	subjects := defs["workflow_outcome_subject"].(map[string]any)
	if len(subjects["examples"].([]any)) != 1 {
		t.Fatalf("subject example is not a single value")
	}

	// evidence: no minItems → empty array, no sampled entry.
	var evidenceNode map[string]any
	supersede := defs["work_transition_action_variant_supersede_contract"].(map[string]any)
	fieldsBranches := supersede["properties"].(map[string]any)["fields"].(map[string]any)["oneOf"].([]any)
	for _, raw := range fieldsBranches {
		branch := raw.(map[string]any)
		props := branch["properties"].(map[string]any)
		if node, ok := props["audit_evidence"].(map[string]any); ok {
			evidenceNode = node
		}
	}
	if evidenceNode == nil {
		t.Fatal("audit_evidence not found on the supersede fields branch")
	}
	if encoded, _ := json.Marshal(evidenceNode); !strings.Contains(string(encoded), `"minItems":1`) {
		t.Fatalf("audit_evidence lost its minimum: %s", encoded)
	}
	entries, ok := evidenceNode["examples"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("audit_evidence carries %v, want one one-entry example", evidenceNode["examples"])
	}
	if list, ok := entries[0].([]any); !ok || len(list) != 1 {
		t.Fatalf("audit_evidence example = %v, want exactly one entry for minItems 1", entries[0])
	}
}

// Every constrained node the two public definitions reach carries guidance;
// unconstrained nodes stay unannotated. A silent gap anywhere in the real
// published tree fails here, not at a caller.
func TestAnnotateGuidanceCoversEveryConstrainedReachableNode(t *testing.T) {
	root := annotateGuidanceNames(t, guidanceNamesBoth)
	covered, skipped := 0, 0
	var walk func(node map[string]any, path string, field bool)
	walk = func(node map[string]any, path string, field bool) {
		source := guidanceSource(node, root)
		if guidanceTeaches(source) || (field && guidanceCompositionTeaches(source)) {
			examples, ok := node["examples"].([]any)
			if !ok || len(examples) != 1 {
				t.Fatalf("%s declares constraints but carries no guidance", path)
			}
			if description, _ := node["description"].(string); !strings.Contains(description, guidanceMarker) {
				t.Fatalf("%s carries an example but no accepted shape: %q", path, description)
			}
			covered++
		} else {
			if _, annotated := node["examples"]; annotated {
				t.Fatalf("%s carries no authored field but was annotated", path)
			}
			skipped++
		}
		if ref, ok := node["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
			if target, ok := root["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any); ok {
				walk(target, path+"->"+ref, false)
			}
			return
		}
		if properties, ok := node["properties"].(map[string]any); ok {
			for _, name := range sortedKeys(properties) {
				if child, ok := properties[name].(map[string]any); ok {
					walk(child, path+"."+name, true)
				}
			}
		}
		if patterns, ok := node["patternProperties"].(map[string]any); ok {
			for _, pattern := range sortedKeys(patterns) {
				if child, ok := patterns[pattern].(map[string]any); ok {
					walk(child, path+"."+pattern, true)
				}
			}
		}
		for _, keyword := range []string{"additionalProperties", "items", "if", "then", "else", "not"} {
			if child, ok := node[keyword].(map[string]any); ok {
				walk(child, path+"."+keyword, keyword == "items" || keyword == "additionalProperties")
			}
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
			branches, _ := node[keyword].([]any)
			for index, raw := range branches {
				if branch, ok := raw.(map[string]any); ok {
					walk(branch, path+"."+keyword+"["+strconv.Itoa(index)+"]", false)
				}
			}
		}
	}
	defs := root["$defs"].(map[string]any)
	for _, name := range []string{"work_transition_action_variant_approve_contract", "work_transition_action_variant_supersede_contract"} {
		walk(defs[name].(map[string]any), name, false)
	}
	t.Logf("covered %d constrained nodes with %d allowed skips", covered, skipped)
	if covered < 100 {
		t.Fatalf("only %d constrained nodes covered; the walk is not reaching the published definitions", covered)
	}
}

// compareGuidanceUnchanged asserts two parsed documents differ by no keyword:
// every validation key keeps its exact value and only annotation keys differ.
func compareGuidanceUnchanged(t *testing.T, want, got any) {
	t.Helper()
	wantObject, wantIsObject := want.(map[string]any)
	gotObject, gotIsObject := got.(map[string]any)
	if !wantIsObject || !gotIsObject {
		wantEncoded, wantErr := json.Marshal(want)
		gotEncoded, gotErr := json.Marshal(got)
		if wantErr != nil || gotErr != nil || string(wantEncoded) != string(gotEncoded) {
			t.Fatalf("validation content changed: %v != %v", want, got)
		}
		return
	}
	for name := range wantObject {
		if _, exists := gotObject[name]; !exists {
			t.Fatalf("key %s was removed", name)
		}
	}
	for name, gotValue := range gotObject {
		wantValue, exists := wantObject[name]
		switch name {
		case "examples", "description":
			continue
		default:
			if !exists {
				t.Fatalf("key %s was added", name)
			}
			compareGuidanceUnchanged(t, wantValue, gotValue)
		}
	}
}
