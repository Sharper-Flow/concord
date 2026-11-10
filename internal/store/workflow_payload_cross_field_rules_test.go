package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The cross-field payload rules are typed declarations on the ENGINE action registry — the one
// owner core validation enforces and the published variants generate their
// closed branches from. These tests hold that ownership: every
// declaration is registry-valid, a declaration that drifts from its own
// fields fails validation, and every declared rule produces core and published
// verdicts that match in both directions.

// declaredActionPayloads collects current catalog payloads keyed by action ID.
func declaredActionPayloads(t *testing.T) map[string]WorkflowPayloadDefinition {
	t.Helper()
	payloads := map[string]WorkflowPayloadDefinition{}
	for id := range builtinWorkflowActionCatalog() {
		action, err := catalogAction(id)
		if err != nil {
			t.Fatal(err)
		}
		payloads[id] = action.Payload
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
		if len(cross.Combinations) != 0 || len(cross.KindMatches) != 0 {
			declared++
		}
		// The authoring payload answers to the canonical projection — the
		// same projection the generator publishes — so the declared fields
		// and the rules the publication names must stay registry-valid
		// together.
		projected, err := WorkflowActionCanonicalCrossFieldRules(actionID, declaredActionPayloads(t)[actionID].Fields)
		if err != nil {
			t.Fatalf("canonical cross-field projection for %s refused: %v", actionID, err)
		}
		if !validateCrossFieldDeclaration(declaredActionPayloads(t)[actionID].Fields, projected) {
			t.Fatalf("%s declares cross-field rules the registry refuses", actionID)
		}
	}
	if declared == 0 {
		fields, rules := syntheticCrossFieldFixture()
		if !validateCrossFieldDeclaration(fields, rules) {
			t.Fatal("registry refused a valid synthetic cross-field declaration")
		}
	}
}

func syntheticCrossFieldFixture() ([]WorkflowPayloadField, WorkflowActionCrossField) {
	return []WorkflowPayloadField{
			{Name: "choice", ValueType: PayloadString, Enum: []string{"with", "without"}},
			{Name: "detail", ValueType: PayloadString},
		}, WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{
			{Field: "choice", Value: "with", Requires: []string{"detail"}},
			{Field: "choice", Value: "without", Forbids: []string{"detail"}},
		}}
}

// declarationMutants are engine cross-field declarations the registry must
// refuse: each one is a way a cross-field declaration can drift from its
// own fields.
func TestRegistryRefusesDriftedCrossFieldDeclarations(t *testing.T) {
	base, valid := syntheticCrossFieldFixture()
	mutate := func(cross WorkflowActionCrossField) WorkflowActionCrossField { return cross }
	incomplete := WorkflowActionCrossField{Combinations: valid.Combinations[:1]}
	undeclaredDiscriminator := mutate(WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "unknown", Value: "xx", Requires: []string{"detail"}}}})
	undeclaredSide := mutate(WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "choice", Value: "with", Requires: []string{"no_such_field"}}}})
	nonEnumValue := mutate(WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "choice", Value: "other", Requires: []string{"detail"}}, {Field: "choice", Value: "without", Forbids: []string{"detail"}}}})
	twoDiscriminators := mutate(WorkflowActionCrossField{Combinations: append(append([]WorkflowPayloadCombination{}, valid.Combinations...), WorkflowPayloadCombination{Field: "unknown", Value: "xx"})})
	ruleWithoutConstraint := WorkflowActionCrossField{Combinations: []WorkflowPayloadCombination{{Field: "choice", Value: "with"}, {Field: "choice", Value: "without"}}}
	for name, cross := range map[string]WorkflowActionCrossField{
		"incomplete enum coverage":  incomplete,
		"undeclared discriminator":  undeclaredDiscriminator,
		"undeclared side field":     undeclaredSide,
		"value the enum refuses":    nonEnumValue,
		"two discriminators":        twoDiscriminators,
		"rule without a constraint": ruleWithoutConstraint,
	} {
		if validateCrossFieldDeclaration(base, cross) {
			t.Fatalf("registry accepted a drifted cross-field declaration: %s", name)
		}
	}
}

// TestDeclaredCrossFieldRulesMatchCoreAndPublishedVerdicts is the per-rule
// differential the declarations own: for every declared legal input
// combination, a payload the declaration admits is
// admitted by real core validation and by the published closed variant, and
// each rule's own invalid mutant — a required field dropped under its
// discriminator value or a forbidden field carried under it — is refused by both, in both
// directions against the same declaration.
func TestDeclaredCrossFieldRulesMatchCoreAndPublishedVerdicts(t *testing.T) {
	defs := firstCallPublishedDefs(t)
	declaredAny := false
	for actionID, payload := range declaredActionPayloads(t) {
		cross := workflowActionCrossField(actionID)
		if len(cross.Combinations) == 0 {
			continue
		}
		// The published variants generate from the canonical projection of
		// the engine declaration onto the declared fields, so the
		// differential enumerates the same rules the publication names.
		projected, err := WorkflowActionCanonicalCrossFieldRules(actionID, payload.Fields)
		if err != nil {
			t.Fatalf("canonical cross-field projection for %s refused: %v", actionID, err)
		}
		cross = projected
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
		for _, field := range payload.Fields {
			if field.Required {
				if _, present := baseFields[field.Name]; !present {
					baseFields[field.Name] = sampleFieldValue(t, defs, variantName, field.Name)
				}
			}
		}
		core := func(fields map[string]any) error {
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validateWorkflowPlanActionPayload(actionID, encoded)
			return err
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
	}
	if !declaredAny {
		valid := json.RawMessage(`{"reproduction":"reproduces","root_cause":"cause"}`)
		old := json.RawMessage(`{"reproduction":"reproduces","root_cause":"cause","searched":"search","outcome":"none_found"}`)
		if _, err := validateWorkflowPlanActionPayload("record_diagnosis", valid); err != nil {
			t.Fatalf("current diagnosis refused by core: %v", err)
		}
		if _, err := validateWorkflowPlanActionPayload("record_diagnosis", old); err == nil {
			t.Fatal("retired diagnosis input admitted by core")
		}
		for _, tc := range []struct {
			fields json.RawMessage
			admit  bool
		}{{valid, true}, {old, false}} {
			payload := json.RawMessage(`{"work_id":"work","expected_version":1,"action_id":"record_diagnosis","idempotency_key":"key","fields":` + string(tc.fields) + `}`)
			if err := payloadschema.Validate("work_transition_action_variant_record_diagnosis", payload); (err == nil) != tc.admit {
				t.Fatalf("published diagnosis admission = %v, want %v: %v", err == nil, tc.admit, err)
			}
		}
	}
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

func TestCrossFieldSampleObjectUnion(t *testing.T) {
	defs := firstCallPublishedDefs(t)
	id := map[string]any{"$ref": "#/$defs/id"}
	// An enclosing requirement plus exclusive branch requirements: a sample
	// must satisfy both, and dropping either required field must refuse.
	union := func(keyword string) map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required":   []any{"product_id"},
			"properties": map[string]any{"product_id": id, "project_id": id, "work_id": id},
			keyword: []any{
				map[string]any{"required": []any{"work_id"}, "not": map[string]any{"required": []any{"project_id"}}},
				map[string]any{"required": []any{"project_id"}, "not": map[string]any{"required": []any{"work_id"}}},
			},
		}
	}
	root := map[string]any{"$defs": defs}
	validate := func(schema map[string]any, data []byte) error {
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		return payloadschema.ValidateValue(value, schema, root, "$")
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		for _, entry := range []string{"object", "value"} {
			t.Run(keyword+"/"+entry, func(t *testing.T) {
				schema := union(keyword)
				node := mergeRefSiblings(defs, union(keyword))
				var sample any
				if entry == "object" {
					sample = crossFieldSampleObject(defs, node)
				} else {
					sample = crossFieldSampleValue(defs, node)
				}
				encoded, err := json.Marshal(sample)
				if err != nil {
					t.Fatal(err)
				}
				if err := validate(schema, encoded); err != nil {
					t.Fatalf("sample lost enclosing or branch requirements: %v: %s", err, encoded)
				}
				for _, field := range []string{"product_id", "work_id"} {
					var mutant map[string]any
					if err := json.Unmarshal(encoded, &mutant); err != nil {
						t.Fatal(err)
					}
					delete(mutant, field)
					data, err := json.Marshal(mutant)
					if err != nil {
						t.Fatal(err)
					}
					if err := validate(schema, data); err == nil {
						t.Fatalf("sample without required %s was accepted: %s", field, data)
					}
				}
			})
		}
	}
}

// crossFieldSampleObject samples one admitted object for a schema node whose
// branches the cross-field declarations split, following oneOf/anyOf into
// their first branch and filling both required sets from their property declarations.
func crossFieldSampleObject(defs map[string]any, node map[string]any) map[string]any {
	node = mergeRefSiblings(defs, node)
	object := map[string]any{}
	required := requiredNames(node)
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if branches, ok := node[keyword].([]any); ok && len(branches) > 0 {
			if branch, ok := branches[0].(map[string]any); ok {
				for name, value := range crossFieldSampleObject(defs, branch) {
					object[name] = value
				}
				required = append(required, requiredNames(branch)...)
			}
		}
	}
	properties, _ := node["properties"].(map[string]any)
	for _, name := range required {
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

// crossFieldLiteralPrefix matches an identifier pattern that opens with a
// literal lowercase prefix and colon.
var crossFieldLiteralPrefix = regexp.MustCompile(`^\^([a-z_]+:)`)

func crossFieldSampleValue(defs map[string]any, node map[string]any) any {
	node = mergeRefSiblings(defs, node)
	if value, ok := node["const"]; ok {
		return value
	}
	if enum, ok := node["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if _, object := node["properties"]; object || node["type"] == "object" {
		return crossFieldSampleObject(defs, node)
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
				if unique, _ := node["uniqueItems"].(bool); unique {
					if binding, ok := list[i].(map[string]any); ok {
						if _, hasJob := binding["job_id"]; hasJob {
							binding["job_id"] = fmt.Sprintf("job:cross-field-%d", i)
						}
					}
				}
			}
			return list
		}
		return []any{}
	case "string":
		pattern, _ := node["pattern"].(string)
		switch {
		case strings.Contains(pattern, "sha256:"):
			return "sha256:" + strings.Repeat("0", 64)
		case strings.HasPrefix(pattern, "^msg:"):
			return "msg:" + strings.Repeat("0", 32)
		case strings.HasPrefix(pattern, "^finding:"):
			return "finding:0:0"
		case strings.HasPrefix(pattern, "^https://"):
			return "https://example.test/pull/1"
		case strings.Contains(pattern, "[0-9a-f]{40"):
			return strings.Repeat("0", 40)
		case crossFieldLiteralPrefix.MatchString(pattern):
			// An identifier pattern with a literal prefix such as
			// predicate:, case:, owner:, or control: takes that prefix.
			return crossFieldLiteralPrefix.FindStringSubmatch(pattern)[1] + "cross-field-sample"
		}
		// The default sample clears the two-byte floor every fold holds
		// workflow strings to, which minLength alone does not encode.
		return "cross-field-sample"
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
