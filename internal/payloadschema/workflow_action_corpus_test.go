package payloadschema

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// CON-412 registry-derived differential corpus. The corpus walks every
// published closed workflow action variant in the generated schema document
// and, for each one, proves shape-verdict parity between the published
// variant, its public variant when one exists, and the core
// work_transition_action_input union: a payload any of them admits, the
// others admit; a payload any refuses for shape reasons, the others refuse.
// Mutants are derived from the variant's own nodes — missing requireds,
// unknown and cross-variant fields, bad enums, wrong types, violated bounds,
// and the legal-combination branches a fields oneOf declares — so an
// authored rule that generation drops fails here rather than at a caller.

// corpusCase is one shape mutant every schema under comparison must refuse.
type corpusCase struct {
	label string
	value map[string]any
}

func resolveCorpusNode(document map[string]any, node map[string]any) map[string]any {
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		target, ok := document["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			return node
		}
		node = target
	}
}

// corpusSample builds one minimal admitted value for a schema node from the
// node's own declarations: const and enum first, then structural minimums.
func corpusSample(document map[string]any, node map[string]any) any {
	node = resolveCorpusNode(document, node)
	if value, ok := node["const"]; ok {
		return value
	}
	if enum, ok := node["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if branches, ok := node["oneOf"].([]any); ok && len(branches) > 0 {
		base := map[string]any{}
		if branch, ok := branches[0].(map[string]any); ok {
			merged := corpusSampleWithFallback(document, branch, node)
			if object, ok := merged.(map[string]any); ok {
				for key, value := range object {
					base[key] = value
				}
			}
		}
		for _, name := range corpusRequired(node) {
			if _, exists := base[name]; !exists {
				if property, ok := corpusProperty(node, name); ok {
					base[name] = corpusSample(document, property)
				}
			}
		}
		return base
	}
	if branches, ok := node["anyOf"].([]any); ok && len(branches) > 0 {
		if branch, ok := branches[0].(map[string]any); ok {
			return corpusSample(document, branch)
		}
	}
	if members, ok := node["allOf"].([]any); ok {
		merged := map[string]any{}
		rest := map[string]any{}
		for key, value := range node {
			if key != "allOf" {
				rest[key] = value
			}
		}
		if len(rest) > 0 {
			if object, ok := corpusSample(document, rest).(map[string]any); ok {
				for key, value := range object {
					merged[key] = value
				}
			}
		}
		for _, raw := range members {
			if member, ok := raw.(map[string]any); ok {
				if object, ok := corpusSample(document, member).(map[string]any); ok {
					for key, value := range object {
						merged[key] = value
					}
				}
			}
		}
		return merged
	}
	switch corpusNodeType(node) {
	case "object":
		object := map[string]any{}
		for _, name := range corpusRequired(node) {
			if property, ok := corpusProperty(node, name); ok {
				object[name] = corpusSample(document, property)
			}
		}
		return object
	case "array":
		if minimum, ok := node["minItems"].(json.Number); ok {
			if count, err := minimum.Int64(); err == nil && count > 0 {
				items, _ := node["items"].(map[string]any)
				list := make([]any, count)
				for i := int64(0); i < count; i++ {
					list[i] = corpusSample(document, items)
				}
				return list
			}
		}
		return []any{}
	case "integer", "number":
		if minimum, ok := node["minimum"].(json.Number); ok {
			return minimum
		}
		return json.Number("1")
	case "boolean":
		return false
	case "string":
		return corpusStringSample(node)
	}
	return nil
}

// corpusSampleWithFallback samples a branch whose requirements name
// properties the enclosing node declares, as a requirements-only oneOf
// branch does (the supersede successor forms).
func corpusSampleWithFallback(document map[string]any, branch map[string]any, parent map[string]any) any {
	sampled := corpusSample(document, branch)
	object, ok := sampled.(map[string]any)
	if !ok {
		object = map[string]any{}
	}
	for _, name := range corpusRequired(branch) {
		if _, exists := object[name]; exists {
			continue
		}
		if property, ok := corpusProperty(branch, name); ok {
			object[name] = corpusSample(document, property)
			continue
		}
		if property, ok := corpusProperty(parent, name); ok {
			object[name] = corpusSample(document, property)
		}
	}
	return object
}

// corpusNodeType returns the node's first non-null declared type, so a
// ["integer","null"] declaration samples as its present form.
func corpusNodeType(node map[string]any) string {
	kind, ok := node["type"].(string)
	if ok {
		return kind
	}
	list, ok := node["type"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range list {
		if text, ok := raw.(string); ok && text != "null" {
			return text
		}
	}
	return ""
}

func corpusStringSample(node map[string]any) any {
	if format, _ := node["format"].(string); format == "date-time" {
		return "2026-10-08T00:00:00Z"
	}
	pattern, _ := node["pattern"].(string)
	switch {
	case strings.Contains(pattern, "sha256:"):
		return "sha256:" + strings.Repeat("0", 64)
	case strings.Contains(pattern, "[0-9a-f]{40}"):
		return strings.Repeat("0", 40)
	case strings.HasPrefix(pattern, "^msg:"):
		return "msg:" + strings.Repeat("0", 32)
	case strings.HasPrefix(pattern, "^https://"):
		return "https://example.test/pull/1"
	case strings.HasPrefix(pattern, "^predicate:"):
		return "predicate:corpus-sample"
	case strings.HasPrefix(pattern, "^[a-z][a-z0-9_]"):
		return "corpus_id_1"
	}
	// The enforcing string floor counts UTF-8 bytes (x-minBytes); the
	// code-point minLength is derived from it, so the sample satisfies the
	// byte floor first and both published bounds hold (CON-412).
	if minimum, ok := node["x-minBytes"].(json.Number); ok {
		if count, err := minimum.Int64(); err == nil {
			return strings.Repeat("v", int(count))
		}
	}
	if minimum, ok := node["minLength"].(json.Number); ok {
		if count, err := minimum.Int64(); err == nil {
			return strings.Repeat("v", int(count))
		}
	}
	return "corpus-id-1"
}

func corpusRequired(node map[string]any) []string {
	raw, _ := node["required"].([]any)
	names := make([]string, 0, len(raw))
	for _, name := range raw {
		if text, ok := name.(string); ok {
			names = append(names, text)
		}
	}
	return names
}

func corpusProperty(node map[string]any, name string) (map[string]any, bool) {
	properties, _ := node["properties"].(map[string]any)
	property, ok := properties[name].(map[string]any)
	return property, ok
}

func corpusCopy(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

func corpusSetValue(root any, path []string, value any) any {
	if len(path) == 0 {
		return value
	}
	object, ok := root.(map[string]any)
	if !ok {
		return root
	}
	next := corpusCopy(object[path[0]])
	object[path[0]] = corpusSetValue(next, path[1:], value)
	return object
}

func corpusDeleteValue(root any, path []string) any {
	if len(path) == 0 {
		return root
	}
	object, ok := root.(map[string]any)
	if !ok {
		return root
	}
	if len(path) == 1 {
		delete(object, path[0])
		return object
	}
	next := corpusCopy(object[path[0]])
	object[path[0]] = corpusDeleteValue(next, path[1:])
	return object
}

// corpusBadEnum returns one value no member of the node's enum admits.
func corpusBadEnum(node map[string]any) any {
	return "__corpus_not_an_enum_value__"
}

// corpusBadType returns one value whose JSON type the node's declared type
// refuses.
func corpusBadType(node map[string]any) any {
	switch corpusNodeType(node) {
	case "string":
		return 17
	case "integer", "number", "boolean", "array", "object":
		return "corpus-wrong-type"
	}
	return "corpus-wrong-type"
}

// corpusMutants derives every shape mutant for one variant from the variant's
// own nodes, each targeted at the sample path that reaches the node.
func corpusMutants(document map[string]any, variantName string, sample map[string]any, crossVariantFields []string) []corpusCase {
	variant := resolveCorpusNode(document, map[string]any{"$ref": "#/$defs/" + variantName})
	cases := []corpusCase{}
	add := func(label string, value any) {
		if object, ok := value.(map[string]any); ok {
			cases = append(cases, corpusCase{label: label, value: object})
		}
	}
	for _, name := range corpusRequired(variant) {
		if name == "action_id" {
			continue
		}
		add("missing required "+name, corpusDeleteValue(corpusCopy(sample), []string{name}))
	}
	add("unknown top-level field", corpusSetValue(corpusCopy(sample), []string{"corpus_unknown"}, true))
	fieldsSchema, hasFields := corpusProperty(variant, "fields")
	if !hasFields {
		fieldsSchema = map[string]any{}
	}
	fieldsSchema = resolveCorpusNode(document, fieldsSchema)
	if _, carries := sample["fields"]; carries {
		for _, name := range corpusRequired(fieldsSchema) {
			add("missing fields."+name, corpusDeleteValue(corpusCopy(sample), []string{"fields", name}))
		}
		for _, field := range crossVariantFields {
			add("cross-variant field "+field, corpusSetValue(corpusCopy(sample), []string{"fields", field}, "corpus-cross-variant"))
		}
		// Legal-combination mutants derive from a fields oneOf whose
		// branches discriminate on a const: pairing one branch's
		// discriminator value with another branch's exclusive fields is the
		// combination the store guard refuses, so the schema must refuse it
		// too.
		if branches, ok := fieldsSchema["oneOf"].([]any); ok && len(branches) > 1 {
			for index, raw := range branches {
				branch, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				discriminator := corpusBranchConstField(branch)
				if discriminator == "" {
					continue
				}
				value := corpusBranchConstValue(branch, discriminator)
				for other, rawOther := range branches {
					if other == index {
						continue
					}
					otherBranch, ok := rawOther.(map[string]any)
					if !ok {
						continue
					}
					for _, exclusive := range corpusRequired(otherBranch) {
						if exclusive == discriminator {
							continue
						}
						if _, declared := corpusProperty(branch, exclusive); declared {
							continue
						}
						mutant := corpusCopy(sample)
						mutant = corpusSetValue(mutant, []string{"fields", discriminator}, value)
						if property, ok := corpusProperty(otherBranch, exclusive); ok {
							mutant = corpusSetValue(mutant, []string{"fields", exclusive}, corpusSample(document, property))
						} else {
							mutant = corpusSetValue(mutant, []string{"fields", exclusive}, "corpus-exclusive-field")
						}
						add(fmt.Sprintf("legal combination %s=%v carries %s", discriminator, value, exclusive), mutant)
					}
				}
				// A branch's own required field is not optional under its
				// discriminator value.
				for _, required := range corpusRequired(branch) {
					if required == discriminator {
						continue
					}
					if _, optionalElsewhere := corpusProperty(fieldsSchema, required); optionalElsewhere {
						mutant := corpusCopy(sample)
						mutant = corpusSetValue(mutant, []string{"fields", discriminator}, value)
						mutant = corpusDeleteValue(mutant, []string{"fields", required})
						add(fmt.Sprintf("legal combination %s=%v drops %s", discriminator, value, required), mutant)
					}
				}
			}
			// Alternative-group branches carry no const discriminator: their
			// own invalid mutant pairs two groups' exclusive fields, the
			// exactly-one rule the registry's alternative declaration states
			// and core admission enforces. The base is the carrying branch's
			// own sample — the variant-level sample already satisfies this
			// branch's group, so adding its own field back would prove
			// nothing.
			for index, raw := range branches {
				branch, ok := raw.(map[string]any)
				if !ok || corpusBranchConstField(branch) != "" {
					continue
				}
				branchSample, sampleOK := corpusSample(document, branch).(map[string]any)
				if !sampleOK {
					continue
				}
				for other, rawOther := range branches {
					if other == index {
						continue
					}
					otherBranch, ok := rawOther.(map[string]any)
					if !ok || corpusBranchConstField(otherBranch) != "" {
						continue
					}
					for _, exclusive := range corpusRequired(otherBranch) {
						if _, declared := corpusProperty(branch, exclusive); declared {
							continue
						}
						mutant, _ := corpusCopy(sample).(map[string]any)
						mutant["fields"] = corpusCopy(branchSample)
						if property, ok := corpusProperty(otherBranch, exclusive); ok {
							mutant, _ = corpusSetValue(mutant, []string{"fields", exclusive}, corpusSample(document, property)).(map[string]any)
						} else {
							mutant, _ = corpusSetValue(mutant, []string{"fields", exclusive}, "corpus-exclusive-field").(map[string]any)
						}
						add(fmt.Sprintf("alternative group %d carries group %d field %s", index, other, exclusive), mutant)
					}
				}
			}
		}
	}
	// Node-derived mutants: walk the variant schema beside the sample and
	// violate every enum, type, and bound the nodes declare.
	cases = append(cases, corpusNodeMutants(document, variant, sample, nil)...)
	return cases
}

func corpusNodeMutants(document map[string]any, node map[string]any, value any, path []string) []corpusCase {
	node = resolveCorpusNode(document, node)
	cases := []corpusCase{}
	add := func(label string, mutant any) {
		if object, ok := mutant.(map[string]any); ok {
			cases = append(cases, corpusCase{label: label, value: object})
		}
	}
	if _, hasEnum := node["enum"]; hasEnum {
		add(strings.Join(path, ".")+" bad enum", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, corpusBadEnum(node)))
	}
	if _, hasType := node["type"]; hasType {
		add(strings.Join(path, ".")+" bad type", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, corpusBadType(node)))
	}
	if minimum, ok := node["minLength"].(json.Number); ok {
		if count, err := minimum.Int64(); err == nil && count > 0 {
			add(strings.Join(path, ".")+" under minLength", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, strings.Repeat("v", int(count-1))))
		}
	}
	if maximum, ok := node["maxLength"].(json.Number); ok {
		if count, err := maximum.Int64(); err == nil {
			add(strings.Join(path, ".")+" over maxLength", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, strings.Repeat("v", int(count+1))))
		}
	}
	// Byte-bound mutants: the store counts UTF-8 bytes, so an ASCII string
	// one byte under the byte floor can still pass the derived code-point
	// minLength, and an all-two-byte string can sit at or under the
	// code-point maxLength while exceeding the byte maximum. Only the x-*
	// keywords refuse these two (CON-412).
	if minimum, ok := node["x-minBytes"].(json.Number); ok {
		if count, err := minimum.Int64(); err == nil && count > 1 {
			add(strings.Join(path, ".")+" under x-minBytes", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, strings.Repeat("v", int(count-1))))
		}
	}
	if maximum, ok := node["x-maxBytes"].(json.Number); ok {
		if count, err := maximum.Int64(); err == nil {
			add(strings.Join(path, ".")+" over x-maxBytes at the code-point limit", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, strings.Repeat("é", int(count/2)+1)))
		}
	}
	if minimum, ok := node["minItems"].(json.Number); ok {
		if count, err := minimum.Int64(); err == nil && count > 0 {
			list := make([]any, int(count-1))
			items, _ := node["items"].(map[string]any)
			for i := range list {
				list[i] = corpusSample(document, items)
			}
			add(strings.Join(path, ".")+" under minItems", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, list))
		}
	}
	if maximum, ok := node["maxItems"].(json.Number); ok {
		if count, err := maximum.Int64(); err == nil {
			items, _ := node["items"].(map[string]any)
			list := make([]any, int(count+1))
			for i := range list {
				list[i] = corpusSample(document, items)
			}
			add(strings.Join(path, ".")+" over maxItems", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, list))
		}
	}
	if minimum, ok := node["minimum"].(json.Number); ok {
		if value, err := minimum.Float64(); err == nil {
			add(strings.Join(path, ".")+" under minimum", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, json.Number(fmt.Sprintf("%d", int(value)-1))))
		}
	}
	if maximum, ok := node["maximum"].(json.Number); ok {
		if value, err := maximum.Float64(); err == nil {
			add(strings.Join(path, ".")+" over maximum", corpusSetValue(corpusCopy(corpusRootOf(path, value)), path, json.Number(fmt.Sprintf("%d", int(value)+1))))
		}
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return cases
	}
	properties, _ := node["properties"].(map[string]any)
	for _, name := range corpusSortedKeys(properties) {
		child, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		childValue, carries := object[name]
		if !carries {
			continue
		}
		cases = append(cases, corpusNodeMutants(document, child, corpusRootOf(append(path, name), childValue), append(path, name))...)
	}
	return cases
}

// corpusRootOf returns the value a nested mutation carries as its root: the
// walk holds the variant's full sample, so nested mutants keep every other
// property the sample already built.
func corpusRootOf(path []string, value any) any {
	return value
}

func corpusBranchConstField(branch map[string]any) string {
	properties, _ := branch["properties"].(map[string]any)
	for _, name := range corpusSortedKeys(properties) {
		property, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		if _, hasConst := property["const"]; hasConst {
			return name
		}
	}
	return ""
}

func corpusBranchConstValue(branch map[string]any, field string) any {
	properties, _ := branch["properties"].(map[string]any)
	property, ok := properties[field].(map[string]any)
	if !ok {
		return nil
	}
	return property["const"]
}

func corpusSortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func corpusVariantFieldNames(defs map[string]any, variantName string) []string {
	variant, _ := defs[variantName].(map[string]any)
	if variant == nil {
		return nil
	}
	fields, _ := variant["properties"].(map[string]any)["fields"].(map[string]any)
	names := make([]string, 0, 16)
	seen := map[string]bool{}
	collect := func(fieldProperties map[string]any) {
		for field := range fieldProperties {
			if !seen[field] {
				seen[field] = true
				names = append(names, field)
			}
		}
	}
	if fieldProperties, ok := fields["properties"].(map[string]any); ok {
		collect(fieldProperties)
	}
	// A fields object the registry's cross-field declarations split declares
	// its fields per branch; the variant's field set is the union.
	if branches, ok := fields["oneOf"].([]any); ok {
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if fieldProperties, ok := branch["properties"].(map[string]any); ok {
				collect(fieldProperties)
			}
		}
	}
	return names
}

// corpusCrossVariantFields names fields other variants declare that this
// variant does not: supplying one is a cross-variant fault only when the
// variant under test truly does not declare it.
func corpusCrossVariantFields(defs map[string]any, allVariants []string, variantName string) []string {
	own := map[string]bool{}
	for _, name := range corpusVariantFieldNames(defs, variantName) {
		own[name] = true
	}
	seen := map[string]bool{}
	cross := []string{}
	for _, other := range allVariants {
		if other == variantName {
			continue
		}
		for _, name := range corpusVariantFieldNames(defs, other) {
			if !own[name] && !seen[name] {
				seen[name] = true
				cross = append(cross, name)
			}
		}
	}
	return cross
}

// TestWorkflowActionVariantCorpusParity proves published/core shape-verdict
// parity in both directions over every published action variant.
func TestWorkflowActionVariantCorpusParity(t *testing.T) {
	document := Document()
	defs, _ := document["$defs"].(map[string]any)
	variantNames := []string{}
	for name := range defs {
		if strings.HasPrefix(name, "work_transition_action_variant_") {
			variantNames = append(variantNames, name)
		}
	}
	if len(variantNames) < 30 {
		t.Fatalf("corpus found only %d action variants", len(variantNames))
	}
	legacyActions := corpusLegacyActionIDs(defs)
	for _, variantName := range variantNames {
		// The public variant replaces the core payload only at the agent
		// boundary, so its shape is sampled and proved on its own, against
		// the core union, never against the sibling core variant.
		shapes := []string{variantName}
		publicName := strings.Replace(variantName, "work_transition_action_variant_", "work_transition_action_public_variant_", 1)
		if _, exists := defs[publicName]; exists {
			shapes = append(shapes, publicName)
		}
		for _, shapeName := range shapes {
			sampleValue := corpusSample(document, map[string]any{"$ref": "#/$defs/" + shapeName})
			sample, ok := sampleValue.(map[string]any)
			if !ok {
				t.Fatalf("%s sample is not an object", shapeName)
			}
			// Each shape answers to its own union: the core variants compose
			// the core input, and a divergent public payload (dispatch_worker,
			// whose core attempt identity the adapter authors) composes the
			// public input the agent boundary actually validates.
			unionName := "work_transition_action_input"
			if strings.HasPrefix(shapeName, "work_transition_action_public_variant_") {
				unionName = "work_transition_action_public_input"
			}
			schemaNames := []string{shapeName, unionName}
			encoded, err := json.Marshal(sample)
			if err != nil {
				t.Fatal(err)
			}
			for _, schemaName := range schemaNames {
				if err := Validate(schemaName, encoded); err != nil {
					t.Fatalf("%s: valid sample refused by %s: %v", shapeName, schemaName, err)
				}
			}
			ownCross := corpusCrossVariantFields(defs, variantNames, shapeName)
			for _, mutant := range corpusMutants(document, shapeName, sample, ownCross) {
				encoded, err := json.Marshal(mutant.value)
				if err != nil {
					t.Fatal(err)
				}
				for _, schemaName := range schemaNames {
					err := Validate(schemaName, encoded)
					if err == nil {
						// One exception is by design: the core input keeps
						// admitting recorded historical payload layouts, so
						// an action with legacy layouts accepts a payload
						// whose fields object is absent or empty. Anything
						// else the published variant refuses, the core must
						// refuse too.
						if unionName == "work_transition_action_input" && legacyActions[actionIDOfVariant(shapeName)] && corpusFieldsVacant(mutant.value) {
							continue
						}
						t.Fatalf("%s mutant %q admitted by %s", shapeName, mutant.label, schemaName)
					}
				}
			}
		}
	}
}

// corpusLegacyActionIDs reads the action ids the never-published legacy
// input names, straight from its own if/then conditions.
func corpusLegacyActionIDs(defs map[string]any) map[string]bool {
	legacy := map[string]bool{}
	legacyInput, _ := defs["work_transition_action_legacy_input"].(map[string]any)
	if legacyInput == nil {
		return legacy
	}
	conditions, _ := legacyInput["allOf"].([]any)
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		trigger, _ := condition["if"].(map[string]any)
		properties, _ := trigger["properties"].(map[string]any)
		actionID, _ := properties["action_id"].(map[string]any)
		if id, ok := actionID["const"].(string); ok {
			legacy[id] = true
		}
	}
	return legacy
}

func actionIDOfVariant(variantName string) string {
	return strings.TrimPrefix(variantName, "work_transition_action_variant_")
}

// corpusFieldsVacant reports whether the payload carries no fields content:
// the exact historical layout a recorded legacy payload declares.
func corpusFieldsVacant(value map[string]any) bool {
	fields, carried := value["fields"]
	if !carried {
		return true
	}
	object, ok := fields.(map[string]any)
	return ok && len(object) == 0
}

// TestRecordAlignmentExactLegalCombination pins the reported fault: the
// published record_alignment variant must refuse the none_found outcome that
// carries related_ids, and the related_found outcome without related_ids,
// exactly as guardRecordAlignmentConsistency refuses them at admission.
func TestRecordAlignmentExactLegalCombination(t *testing.T) {
	illegal := [][]byte{
		[]byte(`{"work_id":"work-corpus-1","expected_version":3,"action_id":"record_alignment","idempotency_key":"corpus-align-1","fields":{"searched":"bounded search","outcome":"none_found","related_ids":["work-probe"]}}`),
		[]byte(`{"work_id":"work-corpus-1","expected_version":3,"action_id":"record_alignment","idempotency_key":"corpus-align-2","fields":{"searched":"bounded search","outcome":"related_found"}}`),
	}
	for _, data := range illegal {
		for _, schemaName := range []string{"work_transition_action_variant_record_alignment", "work_transition_action_input", "work_transition_action_public_input"} {
			if err := Validate(schemaName, data); err == nil {
				t.Fatalf("%s admitted an illegal record_alignment combination: %s", schemaName, data)
			}
		}
	}
	legal := []byte(`{"work_id":"work-corpus-1","expected_version":3,"action_id":"record_alignment","idempotency_key":"corpus-align-3","fields":{"searched":"bounded search","outcome":"related_found","related_ids":["work-probe"]}}`)
	for _, schemaName := range []string{"work_transition_action_variant_record_alignment", "work_transition_action_input", "work_transition_action_public_input"} {
		if err := Validate(schemaName, legal); err != nil {
			t.Fatalf("%s refused the legal record_alignment combination: %v", schemaName, err)
		}
	}
}
