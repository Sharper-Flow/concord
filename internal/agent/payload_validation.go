package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var operationPayloadSchemas = map[string]string{
	"concord_product_view.resolve": "product_context", "concord_product_view.snapshot": "product_snapshot",
	"concord_work_browse.list": "work_page", "concord_work_browse.ready": "work_page", "concord_work_browse.blocked": "blocked_work_page", "concord_work_browse.scope": "work_scope",
	"concord_work_trace.history": "work_event_page", "concord_work_trace.relations": "work_relation_graph", "concord_knowledge.search": "knowledge_page", "concord_knowledge.resolve_note": "canonical_note_result",
	"concord_work_define.capture": "mutation_result", "concord_work_define.revise_intent": "mutation_result", "concord_work_transition.lifecycle": "mutation_result", "concord_work_transition.workflow_action": "mutation_result",
	"concord_work_relate.set_memberships": "mutation_result", "concord_work_relate.link": "mutation_result", "concord_work_relate.unlink": "mutation_result", "concord_work_relate.supersede": "mutation_result", "concord_work_relate.restore_superseded": "mutation_result",
	"concord_work_compact.publish": "mutation_result", "concord_work_compact.reconcile": "mutation_result",
}
var operationInputSchemas = map[string]string{
	"concord_product_view.resolve": "product_view_resolve_input", "concord_product_view.snapshot": "product_view_snapshot_input", "concord_work_browse.list": "work_browse_list_input", "concord_work_browse.ready": "work_browse_ready_input", "concord_work_browse.blocked": "work_browse_blocked_input", "concord_work_browse.scope": "work_browse_scope_input", "concord_work_trace.history": "work_trace_history_input", "concord_work_trace.relations": "work_trace_relations_input", "concord_knowledge.search": "knowledge_search_input", "concord_knowledge.resolve_note": "knowledge_resolve_input", "concord_work_define.capture": "work_define_capture_input", "concord_work_define.revise_intent": "work_define_revise_input", "concord_work_transition.lifecycle": "work_transition_lifecycle_input", "concord_work_transition.workflow_action": "work_transition_action_input", "concord_work_relate.set_memberships": "work_relate_memberships_input", "concord_work_relate.link": "work_relate_link_input", "concord_work_relate.unlink": "work_relate_unlink_input", "concord_work_relate.supersede": "work_relate_supersede_input", "concord_work_relate.restore_superseded": "work_relate_restore_input", "concord_work_compact.publish": "work_compact_publish_input", "concord_work_compact.reconcile": "work_compact_reconcile_input",
}

func ValidateOperationPayload(tool, operation string, data []byte, result bool) error {
	key := tool + "." + operation
	name := operationPayloadSchemas[key]
	if !result {
		name = operationInputSchemas[key]
	}
	if name == "" {
		return fmt.Errorf("unknown operation payload %s", key)
	}
	if err := ValidateGeneratedPayload(name, data); err != nil {
		return err
	}
	if err := ValidatePayloadSchema(name, data); err != nil {
		return err
	}
	return nil
}

func ValidatePayloadSchema(name string, data []byte) error {
	var root map[string]any
	schemaDecoder := json.NewDecoder(strings.NewReader(GeneratedPayloadSchemaDocument))
	schemaDecoder.UseNumber()
	if err := schemaDecoder.Decode(&root); err != nil {
		return err
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return fmt.Errorf("payload schema definitions missing")
	}
	schema, ok := defs[name].(map[string]any)
	if !ok {
		return fmt.Errorf("unknown payload schema %s", name)
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	return validateSchemaValue(value, schema, root, "$")
}

func validateSchemaValue(value any, schema map[string]any, root map[string]any, path string) error {
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		defs := root["$defs"].(map[string]any)
		target, ok := defs[name].(map[string]any)
		if !ok {
			return fmt.Errorf("missing schema ref %s", ref)
		}
		return validateSchemaValue(value, target, root, path)
	}
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(value, constant) {
		return fmt.Errorf("const mismatch at %s", path)
	}
	if values, ok := schema["enum"].([]any); ok {
		found := false
		for _, candidate := range values {
			if reflect.DeepEqual(value, candidate) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("enum mismatch at %s", path)
		}
	}
	if types, ok := schema["type"]; ok && !matchesAnyType(value, types) {
		return fmt.Errorf("type mismatch at %s", path)
	}
	if object, ok := value.(map[string]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, raw := range required {
				name, _ := raw.(string)
				if _, exists := object[name]; !exists {
					return fmt.Errorf("missing required %s.%s", path, name)
				}
			}
		}
		if additional, exists := schema["additionalProperties"]; exists {
			if additionalPropertiesFalse, ok := additional.(bool); ok && additionalPropertiesFalse == false {
				for key := range object {
					if _, exists := properties[key]; !exists {
						return fmt.Errorf("unknown property %s.%s", path, key)
					}
				}
			} else if child, ok := additional.(map[string]any); ok {
				for key, entry := range object {
					if _, exists := properties[key]; !exists {
						if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
							return err
						}
					}
				}
			}
		}
		for key, raw := range properties {
			if entry, exists := object[key]; exists {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid schema property %s", key)
				}
				if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
					return err
				}
			}
		}
		if n, ok := schema["minProperties"].(json.Number); ok && len(object) < numberInt(n) {
			return fmt.Errorf("minProperties at %s", path)
		}
		if n, ok := schema["maxProperties"].(json.Number); ok && len(object) > numberInt(n) {
			return fmt.Errorf("maxProperties at %s", path)
		}
	}
	if array, ok := value.([]any); ok {
		if n, ok := schema["minItems"].(json.Number); ok && len(array) < numberInt(n) {
			return fmt.Errorf("minItems at %s", path)
		}
		if n, ok := schema["maxItems"].(json.Number); ok && len(array) > numberInt(n) {
			return fmt.Errorf("maxItems at %s", path)
		}
		if unique, ok := schema["uniqueItems"].(bool); ok && unique {
			seen := map[string]bool{}
			for _, entry := range array {
				raw, _ := json.Marshal(entry)
				if seen[string(raw)] {
					return fmt.Errorf("uniqueItems at %s", path)
				}
				seen[string(raw)] = true
			}
		}
		if child, ok := schema["items"].(map[string]any); ok {
			for i, entry := range array {
				if err := validateSchemaValue(entry, child, root, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	if text, ok := value.(string); ok {
		if n, ok := schema["minLength"].(json.Number); ok && len([]rune(text)) < numberInt(n) {
			return fmt.Errorf("minLength at %s", path)
		}
		if n, ok := schema["maxLength"].(json.Number); ok && len([]rune(text)) > numberInt(n) {
			return fmt.Errorf("maxLength at %s", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, _ := regexp.MatchString(pattern, text)
			if !matched {
				return fmt.Errorf("pattern at %s", path)
			}
		}
	}
	if number, ok := value.(json.Number); ok {
		n, _ := strconv.ParseFloat(string(number), 64)
		if min, ok := schema["minimum"].(json.Number); ok {
			m, _ := strconv.ParseFloat(string(min), 64)
			if n < m {
				return fmt.Errorf("minimum at %s", path)
			}
		}
		if max, ok := schema["maximum"].(json.Number); ok {
			m, _ := strconv.ParseFloat(string(max), 64)
			if n > m {
				return fmt.Errorf("maximum at %s", path)
			}
		}
	}
	for keyword := range map[string]bool{"allOf": true, "anyOf": true, "oneOf": true} {
		if branches, ok := schema[keyword].([]any); ok {
			matches := 0
			for _, raw := range branches {
				branch, _ := raw.(map[string]any)
				if validateSchemaValue(value, branch, root, path) == nil {
					matches++
				}
			}
			if keyword == "allOf" && matches != len(branches) || keyword == "anyOf" && matches < 1 || keyword == "oneOf" && matches != 1 {
				return fmt.Errorf("%s mismatch at %s", keyword, path)
			}
		}
	}
	if branch, ok := schema["not"].(map[string]any); ok && validateSchemaValue(value, branch, root, path) == nil {
		return fmt.Errorf("not mismatch at %s", path)
	}
	return nil
}
func matchesAnyType(value any, raw any) bool {
	types := []string{}
	switch typed := raw.(type) {
	case string:
		types = []string{typed}
	case []any:
		for _, entry := range typed {
			if kind, ok := entry.(string); ok {
				types = append(types, kind)
			}
		}
	}
	for _, kind := range types {
		switch kind {
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		case "number":
			if _, ok := value.(json.Number); ok {
				return true
			}
		case "integer":
			if number, ok := value.(json.Number); ok && !strings.ContainsAny(string(number), ".eE") {
				return true
			}
		}
	}
	return false
}
func numberInt(value json.Number) int { n, _ := strconv.Atoi(string(value)); return n }

func inputPayloadSchema(tool, operation string) string {
	return operationInputSchemas[tool+"."+operation]
}
