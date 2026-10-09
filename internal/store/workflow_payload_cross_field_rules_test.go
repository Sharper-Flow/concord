package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The cross-field payload rules (legal input combinations, alternative field
// groups) are typed declarations on the ENGINE action registry — the one
// owner core validation enforces and the published variants generate their
// closed branches from (CON-412). These tests hold that ownership: every
// declaration is registry-valid, a declaration that drifts from its own
// fields fails validation, definition digests stay byte-identical to the
// released ones, and every declared rule produces core and published
// verdicts that match in both directions — including every partial
// alternative-group mutant.

// declaredActionPayloads collects every action payload declaration the
// registry and the recovery definitions own, keyed by action ID. It reads
// the same source the contract projection reads — the current builtin
// definitions (so record_verdict carries its restated batched payload) plus
// the recovery registry — so the differential judges the shape publication
// actually advertises.
func declaredActionPayloads(t *testing.T) map[string]WorkflowPayloadDefinition {
	t.Helper()
	payloads := map[string]WorkflowPayloadDefinition{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
			payloads[action.ID] = action.Payload
		}
	}
	for _, action := range BuiltinWorkflowRecoveryActionDefinitions() {
		payloads[action.ID] = action.Payload
	}
	return payloads
}

func TestDeclaredCrossFieldRulesAreRegistryValid(t *testing.T) {
	if err := validateEngineCrossFieldDeclarations(); err != nil {
		t.Fatal(err)
	}
	declared := 0
	for actionID := range declaredActionPayloads(t) {
		cross := workflowActionCrossField(actionID)
		if !validateCrossFieldDeclaration(declaredActionPayloads(t)[actionID].Fields, cross) {
			t.Fatalf("%s declares cross-field rules the registry refuses", actionID)
		}
		if len(cross.Combinations) != 0 || len(cross.Alternatives) != 0 {
			declared++
		}
	}
	if declared == 0 {
		t.Fatal("no action declares cross-field rules; the declaration owner and its differential would prove nothing")
	}
}

// declarationMutants are engine cross-field declarations the registry must
// refuse: each one is a way a cross-field declaration can drift from its
// own fields.
func TestRegistryRefusesDriftedCrossFieldDeclarations(t *testing.T) {
	base := builtinActionPolicies["record_alignment"].Payload
	mutate := func(cross WorkflowActionCrossField) WorkflowActionCrossField { return cross }
	incomplete := WorkflowActionCrossField{Combinations: builtinActionPolicies["record_alignment"].CrossField.Combinations[:1]}
	undeclaredDiscriminator := mutate(WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "searched", Value: "xx", Requires: []string{"related_ids"}}}})
	undeclaredSide := mutate(WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "outcome", Value: "related_found", Requires: []string{"no_such_field"}}}})
	nonEnumValue := mutate(WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "outcome", Value: "partially_found", Requires: []string{"related_ids"}}, {Field: "outcome", Value: "none_found", Forbids: []string{"related_ids"}}}})
	twoDiscriminators := mutate(WorkflowActionCrossField{Combinations: append(append([]WorkflowPayloadCombination{}, builtinActionPolicies["record_alignment"].CrossField.Combinations...), WorkflowPayloadCombination{Field: "searched", Value: "xx"})})
	bothFamilies := mutate(WorkflowActionCrossField{Combinations: builtinActionPolicies["record_alignment"].CrossField.Combinations, Alternatives: []WorkflowPayloadAlternative{{Fields: []string{"searched"}}}})
	emptyAlternative := WorkflowActionCrossField{Alternatives: []WorkflowPayloadAlternative{{Fields: []string{}}}}
	overlappingAlternatives := WorkflowActionCrossField{Alternatives: []WorkflowPayloadAlternative{{Fields: []string{"searched", "outcome"}}, {Fields: []string{"outcome", "related_ids"}}}}
	undeclaredForbid := WorkflowActionCrossField{Alternatives: []WorkflowPayloadAlternative{{Fields: []string{"searched"}}, {Fields: []string{"outcome"}, Forbids: []string{"no_such_field"}}}}
	groupedForbid := WorkflowActionCrossField{Alternatives: []WorkflowPayloadAlternative{{Fields: []string{"searched"}}, {Fields: []string{"outcome"}, Forbids: []string{"searched"}}}}
	duplicateForbid := WorkflowActionCrossField{Alternatives: []WorkflowPayloadAlternative{{Fields: []string{"searched"}}, {Fields: []string{"outcome"}, Forbids: []string{"related_ids", "related_ids"}}}}
	ruleWithoutConstraint := WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "outcome", Value: "related_found"}, {Field: "outcome", Value: "none_found"}}}
	for name, cross := range map[string]WorkflowActionCrossField{
		"incomplete enum coverage":     incomplete,
		"undeclared discriminator":     undeclaredDiscriminator,
		"undeclared side field":        undeclaredSide,
		"value the enum refuses":       nonEnumValue,
		"two discriminators":           twoDiscriminators,
		"both rule families":           bothFamilies,
		"empty alternative group":      emptyAlternative,
		"overlapping alternative":      overlappingAlternatives,
		"undeclared forbid":            undeclaredForbid,
		"forbid naming a group member": groupedForbid,
		"duplicate forbid":             duplicateForbid,
		"rule without a constraint":    ruleWithoutConstraint,
	} {
		if validateCrossFieldDeclaration(base.Fields, cross) {
			t.Fatalf("registry accepted a drifted cross-field declaration: %s", name)
		}
	}
}

// TestDeclaredCrossFieldRulesMatchCoreAndPublishedVerdicts is the per-rule
// differential the declarations own: for every declared legal input
// combination and alternative group, a payload the declaration admits is
// admitted by real core validation and by the published closed variant, and
// each rule's own invalid mutant — a required field dropped under its
// discriminator value, a forbidden field carried under it, an alternative
// group withheld, or two groups supplied — is refused by both, in both
// directions against the same declaration (CON-412).
func TestDeclaredCrossFieldRulesMatchCoreAndPublishedVerdicts(t *testing.T) {
	defs := firstCallPublishedDefs(t)
	declaredAny := false
	for actionID, payload := range declaredActionPayloads(t) {
		cross := workflowActionCrossField(actionID)
		if len(cross.Combinations) == 0 && len(cross.Alternatives) == 0 {
			continue
		}
		variantName := "work_transition_action_variant_" + actionID
		variant := firstCallResolve(defs, map[string]any{"$ref": "#/$defs/" + variantName})
		if _, hasVariant := variant["properties"]; !hasVariant {
			t.Fatalf("published schema carries no closed variant for %s", actionID)
		}
		// A valid sample assembled from the published variant: the same
		// first-call assembly a model would submit from the advertisement.
		// A fields object the declaration splits into branches samples its
		// first branch, whose own requireds and consts state the branch.
		sample := firstCallObject(defs, map[string]any{"$ref": "#/$defs/" + variantName})
		fieldsNode := mergeRefSiblings(defs, firstCallResolve(defs, variant["properties"].(map[string]any)["fields"].(map[string]any)))
		baseFields := crossFieldSampleObject(defs, fieldsNode)
		core := func(fields map[string]any) error {
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if actionID == "supersede_contract" {
				return validateWorkflowContractRecoveryPayload(encoded)
			}
			return validateWorkflowActionPayload(WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{ID: actionID, Payload: payload}}}, actionID, encoded)
		}
		published := func(fields map[string]any) error {
			encoded, err := json.Marshal(map[string]any{
				"work_id": sample["work_id"], "expected_version": sample["expected_version"],
				"action_id": actionID, "idempotency_key": sample["idempotency_key"], "fields": fields,
			})
			if err != nil {
				t.Fatal(err)
			}
			return payloadschema.Validate(variantName, encoded)
		}
		assertBoth := func(label string, fields map[string]any, admit bool) {
			t.Helper()
			encoded, err := json.Marshal(map[string]any{
				"work_id": sample["work_id"], "expected_version": sample["expected_version"],
				"action_id": actionID, "idempotency_key": sample["idempotency_key"], "fields": fields,
			})
			if err != nil {
				t.Fatal(err)
			}
			coreErr := core(fields)
			publishedErr := published(fields)
			if admit && (coreErr != nil || publishedErr != nil) {
				t.Fatalf("%s: declaration-admitted %s refused by core (%v) or published (%v): %s", actionID, label, coreErr, publishedErr, encoded)
			}
			if !admit && (coreErr == nil || publishedErr == nil) {
				t.Fatalf("%s: declaration-refused %s admitted by core (%v) or published (%v): %s", actionID, label, coreErr, publishedErr, encoded)
			}
		}
		for _, combination := range cross.Combinations {
			declaredAny = true
			// The neutral base carries only fields no sibling branch
			// reserves, so each rule's legal case starts from a payload the
			// whole declaration admits before this rule's own constraints.
			neutral := map[string]any{}
			for name, value := range baseFields {
				neutral[name] = value
			}
			for _, sibling := range cross.Combinations {
				if sibling.Value == combination.Value && sibling.Field == combination.Field {
					continue
				}
				for _, name := range sibling.Requires {
					delete(neutral, name)
				}
			}
			under := func() map[string]any {
				fields := map[string]any{}
				for name, value := range neutral {
					fields[name] = value
				}
				fields[combination.Field] = combination.Value
				for _, name := range combination.Requires {
					if _, present := fields[name]; !present {
						fields[name] = sampleFieldValue(t, defs, variantName, name)
					}
				}
				return fields
			}
			assertBoth("legal combination "+combination.Field+"="+combination.Value, under(), true)
			for _, name := range combination.Requires {
				dropped := under()
				delete(dropped, name)
				assertBoth("combination "+combination.Field+"="+combination.Value+" dropping "+name, dropped, false)
			}
			for _, name := range combination.Forbids {
				carried := under()
				carried[name] = sampleFieldValue(t, defs, variantName, name)
				assertBoth("combination "+combination.Field+"="+combination.Value+" carrying "+name, carried, false)
				delete(carried, name)
				assertBoth("combination "+combination.Field+"="+combination.Value+" without "+name, carried, true)
			}
		}
		if len(cross.Alternatives) != 0 {
			declaredAny = true
			groupFields := func(group WorkflowPayloadAlternative) []string { return group.Fields }
			for _, group := range cross.Alternatives {
				fields := map[string]any{}
				for name, value := range baseFields {
					fields[name] = value
				}
				for _, other := range cross.Alternatives {
					if strings.Join(groupFields(other), "\x00") == strings.Join(groupFields(group), "\x00") {
						continue
					}
					for _, name := range groupFields(other) {
						delete(fields, name)
					}
				}
				for _, name := range groupFields(group) {
					if _, present := fields[name]; !present {
						fields[name] = sampleFieldValue(t, defs, variantName, name)
					}
				}
				assertBoth("alternative group "+strings.Join(groupFields(group), "+"), fields, true)
				withheld := map[string]any{}
				for name, value := range fields {
					withheld[name] = value
				}
				for _, name := range groupFields(group) {
					delete(withheld, name)
				}
				assertBoth("alternative group "+strings.Join(groupFields(group), "+")+" withheld", withheld, false)
				// A field the group forbids cannot ride beside the
				// complete group even though no group claims it — one
				// negative mutant for each newly owned cross-field rule
				// (record_verdict's entry-level fields beside the batch).
				for _, name := range group.Forbids {
					carried := map[string]any{}
					for key, value := range fields {
						carried[key] = value
					}
					carried[name] = sampleFieldValue(t, defs, variantName, name)
					assertBoth("group "+strings.Join(groupFields(group), "+")+" carrying forbidden "+name, carried, false)
				}
			}
			// Two groups supplied together violate the exactly-one rule.
			if len(cross.Alternatives) > 1 {
				both := map[string]any{}
				for name, value := range baseFields {
					both[name] = value
				}
				for _, group := range cross.Alternatives {
					for _, name := range groupFields(group) {
						if _, present := both[name]; !present {
							both[name] = sampleFieldValue(t, defs, variantName, name)
						}
					}
				}
				assertBoth("two alternative groups supplied", both, false)
			}
			// One complete group plus each proper subset of every other
			// group: a partially supplied second group is refused by the
			// published closed branches, so the engine must refuse it too —
			// counting only complete groups admitted exactly these payloads
			// while publication refused them (CON-412 correction).
			for _, complete := range cross.Alternatives {
				for _, other := range cross.Alternatives {
					if strings.Join(groupFields(other), "\x00") == strings.Join(groupFields(complete), "\x00") {
						continue
					}
					for subset := 1; subset < len(groupFields(other)); subset++ {
						for _, drop := range subsetsOf(groupFields(other), subset) {
							fields := map[string]any{}
							for name, value := range baseFields {
								fields[name] = value
							}
							for _, group := range cross.Alternatives {
								if strings.Join(groupFields(group), "\x00") == strings.Join(groupFields(complete), "\x00") {
									continue
								}
								for _, name := range groupFields(group) {
									delete(fields, name)
								}
							}
							for _, name := range groupFields(complete) {
								if _, present := fields[name]; !present {
									fields[name] = sampleFieldValue(t, defs, variantName, name)
								}
							}
							for _, name := range drop {
								fields[name] = sampleFieldValue(t, defs, variantName, name)
							}
							assertBoth("complete group "+strings.Join(groupFields(complete), "+")+" plus partial group {"+strings.Join(drop, "+")+"}", fields, false)
						}
					}
				}
			}
			// A single incomplete group with nothing else supplied is
			// refused: no complete group exists.
			for _, group := range cross.Alternatives {
				if len(groupFields(group)) < 2 {
					continue
				}
				fields := map[string]any{}
				for name, value := range baseFields {
					fields[name] = value
				}
				for _, other := range cross.Alternatives {
					for _, name := range groupFields(other) {
						delete(fields, name)
					}
				}
				fields[groupFields(group)[0]] = sampleFieldValue(t, defs, variantName, groupFields(group)[0])
				assertBoth("incomplete group {"+groupFields(group)[0]+"} alone", fields, false)
			}
		}
	}
	if !declaredAny {
		t.Fatal("the differential exercised no declared cross-field rule")
	}
}

// subsetsOf returns every subset of the given size of the names, in a
// deterministic order.
func subsetsOf(names []string, size int) [][]string {
	if size <= 0 || size > len(names) {
		return nil
	}
	var result [][]string
	var walk func(start int, chosen []string)
	walk = func(start int, chosen []string) {
		if len(chosen) == size {
			result = append(result, append([]string{}, chosen...))
			return
		}
		for i := start; i <= len(names)-(size-len(chosen)); i++ {
			walk(i+1, append(chosen, names[i]))
		}
	}
	walk(0, nil)
	return result
}

// mergeRefSiblings folds a node's own keywords over its $ref target —
// recursively through properties, items, and oneOf branches — so a sampler
// that resolves $ref keeps the bounds the node declares beside it
// ({"$ref": ..., "minItems": 1}), which the validators already honor. The
// validators apply the reference and the local keywords together, so numeric
// bounds intersect: the merge keeps the stricter of each pair.
func mergeRefSiblings(defs map[string]any, node map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range node {
		merged[key] = mergeRefSiblingsValue(defs, value)
	}
	if ref, ok := node["$ref"].(string); ok {
		if target, resolveable := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any); resolveable {
			resolved := mergeRefSiblings(defs, target)
			for key, value := range resolved {
				if key == "$ref" {
					continue
				}
				if own, present := merged[key]; present {
					if stricter, ok := stricterBound(key, own, value); ok {
						merged[key] = stricter
					}
					continue
				}
				merged[key] = value
			}
			delete(merged, "$ref")
		}
	}
	return merged
}

// stricterBound intersects one keyword the node and its reference both
// declare, keeping the bound both validators enforce.
func stricterBound(key string, own, resolved any) (any, bool) {
	ownNumber, ownOK := own.(float64)
	resolvedNumber, resolvedOK := resolved.(float64)
	if !ownOK || !resolvedOK {
		return nil, false
	}
	switch key {
	case "minLength", "minItems", "minimum":
		return max(ownNumber, resolvedNumber), true
	case "maxLength", "maxItems", "maximum":
		return min(ownNumber, resolvedNumber), true
	}
	return nil, false
}

func mergeRefSiblingsValue(defs map[string]any, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return mergeRefSiblings(defs, typed)
	case []any:
		merged := make([]any, len(typed))
		for i, item := range typed {
			merged[i] = mergeRefSiblingsValue(defs, item)
		}
		return merged
	default:
		return value
	}
}

// crossFieldSampleObject samples one admitted object for a schema node whose
// branches the cross-field declarations split, following oneOf/anyOf into
// their first branch and filling the enclosing node's own requireds.
func crossFieldSampleObject(defs map[string]any, node map[string]any) map[string]any {
	node = mergeRefSiblings(defs, node)
	object := map[string]any{}
	if branches, ok := node["oneOf"].([]any); ok && len(branches) > 0 {
		if branch, ok := branches[0].(map[string]any); ok {
			for name, value := range crossFieldSampleObject(defs, branch) {
				object[name] = value
			}
		}
	}
	properties, _ := node["properties"].(map[string]any)
	for _, name := range requiredNames(node) {
		if _, present := object[name]; present {
			continue
		}
		if property, ok := properties[name].(map[string]any); ok {
			object[name] = crossFieldSampleValue(defs, property)
		}
	}
	return object
}

// requiredNames reads a node's required list, tolerating its absence.
func requiredNames(node map[string]any) []string {
	raw, _ := node["required"].([]any)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		if name, ok := item.(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func crossFieldSampleValue(defs map[string]any, node map[string]any) any {
	node = mergeRefSiblings(defs, node)
	if value, ok := node["const"]; ok {
		return value
	}
	if enum, ok := node["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if branches, ok := node[keyword].([]any); ok && len(branches) > 0 {
			if branch, ok := branches[0].(map[string]any); ok {
				return crossFieldSampleValue(defs, branch)
			}
		}
	}
	switch node["type"] {
	case "integer", "number":
		if minimum, ok := node["minimum"].(float64); ok {
			return int64(minimum)
		}
		return 1
	case "boolean":
		return false
	case "array":
		items, _ := node["items"].(map[string]any)
		if minimum, ok := node["minItems"].(float64); ok && minimum > 0 {
			list := make([]any, int(minimum))
			for i := range list {
				list[i] = crossFieldSampleValue(defs, items)
			}
			return list
		}
		return []any{}
	case "object":
		return crossFieldSampleObject(defs, node)
	case "string":
		pattern, _ := node["pattern"].(string)
		switch {
		case strings.HasPrefix(pattern, "^predicate:"):
			return "predicate:cross-field-sample"
		case strings.Contains(pattern, "sha256:"):
			return "sha256:" + strings.Repeat("0", 64)
		case strings.HasPrefix(pattern, "^msg:"):
			return "msg:" + strings.Repeat("0", 32)
		case strings.HasPrefix(pattern, "^https://"):
			return "https://example.test/pull/1"
		case strings.Contains(pattern, "[0-9a-f]{40}"):
			return strings.Repeat("0", 40)
		}
		// The default sample clears the two-byte floor every fold holds
		// workflow strings to, which minLength alone does not encode.
		return "cross-field-sample"
	}
	if _, carries := node["properties"]; carries {
		return crossFieldSampleObject(defs, node)
	}
	return "cross-field-sample"
}

// sampleFieldValue builds one admitted value for a field of a published
// variant branch, so a mutant that carries a field the sample omitted still
// breaks the rule through a value the field's own schema admits.
func sampleFieldValue(t *testing.T, defs map[string]any, variantName string, field string) any {
	t.Helper()
	variant := firstCallResolve(defs, map[string]any{"$ref": "#/$defs/" + variantName})
	fields := mergeRefSiblings(defs, firstCallResolve(defs, variant["properties"].(map[string]any)["fields"].(map[string]any)))
	branches, _ := fields["oneOf"].([]any)
	candidates := []any{}
	if properties, ok := fields["properties"].(map[string]any); ok {
		if branch, ok := properties[field]; ok {
			candidates = append(candidates, branch)
		}
	}
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if property, ok := branch["properties"].(map[string]any)[field]; ok {
			candidates = append(candidates, property)
		}
	}
	for _, candidate := range candidates {
		if value := crossFieldSampleValue(defs, candidate.(map[string]any)); value != nil {
			return value
		}
	}
	return "corpus-field-value"
}
