package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestRecordDiagnosisCoreAndPublishedAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields json.RawMessage
		admit  bool
	}{
		{"current", json.RawMessage(`{"reproduction":"reproduces","root_cause":"cause"}`), true},
		{"retired search", json.RawMessage(`{"reproduction":"reproduces","root_cause":"cause","searched":"search","outcome":"none_found"}`), false},
		{"missing root cause", json.RawMessage(`{"reproduction":"reproduces"}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, coreErr := validateWorkflowPlanActionPayload("record_diagnosis", tc.fields)
			payload := json.RawMessage(`{"work_id":"work","expected_version":1,"action_id":"record_diagnosis","idempotency_key":"key","fields":` + string(tc.fields) + `}`)
			publishedErr := payloadschema.Validate("work_transition_action_variant_record_diagnosis", payload)
			if (coreErr == nil) != tc.admit || (publishedErr == nil) != tc.admit {
				t.Fatalf("admit = %v; core: %v; published: %v", tc.admit, coreErr, publishedErr)
			}
		})
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

func TestSchemaSampleObjectUnion(t *testing.T) {
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
					sample = schemaSampleObject(defs, node)
				} else {
					sample = schemaSampleValue(defs, node)
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

// schemaSampleObject samples one admitted object, following oneOf/anyOf
// into the first branch and filling required properties from both levels.
func schemaSampleObject(defs map[string]any, node map[string]any) map[string]any {
	node = mergeRefSiblings(defs, node)
	object := map[string]any{}
	required := requiredNames(node)
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if branches, ok := node[keyword].([]any); ok && len(branches) > 0 {
			if branch, ok := branches[0].(map[string]any); ok {
				for name, value := range schemaSampleObject(defs, branch) {
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
			object[name] = schemaSampleValue(defs, property)
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

// schemaSampleLiteralPrefix matches an identifier pattern that opens with a
// literal lowercase prefix and colon.
var schemaSampleLiteralPrefix = regexp.MustCompile(`^\^([a-z_]+:)`)

func schemaSampleValue(defs map[string]any, node map[string]any) any {
	node = mergeRefSiblings(defs, node)
	if value, ok := node["const"]; ok {
		return value
	}
	if enum, ok := node["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if _, object := node["properties"]; object || node["type"] == "object" {
		return schemaSampleObject(defs, node)
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if branches, ok := node[keyword].([]any); ok && len(branches) > 0 {
			if branch, ok := branches[0].(map[string]any); ok {
				return schemaSampleValue(defs, branch)
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
				list[i] = schemaSampleValue(defs, items)
				if unique, _ := node["uniqueItems"].(bool); unique {
					if binding, ok := list[i].(map[string]any); ok {
						if _, hasJob := binding["job_id"]; hasJob {
							binding["job_id"] = fmt.Sprintf("job:schema-sample-%d", i)
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
		case schemaSampleLiteralPrefix.MatchString(pattern):
			// An identifier pattern with a literal prefix such as
			// predicate:, case:, owner:, or control: takes that prefix.
			return schemaSampleLiteralPrefix.FindStringSubmatch(pattern)[1] + "schema-sample"
		}
		// The default sample clears the two-byte floor every fold holds
		// workflow strings to, which minLength alone does not encode.
		return "schema-sample"
	}
	return "schema-sample"
}
