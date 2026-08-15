package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var operationPayloadSchemas = map[string]string{
	"concord_product_view.resolve": "product_context", "concord_product_view.snapshot": "product_snapshot", "concord_product_view.portfolio": "product_row_page", "concord_product_view.blocked_sessions": "blocked_sessions_page",
	"concord_work_browse.list": "work_page", "concord_work_browse.ready": "work_page", "concord_work_browse.blocked": "blocked_work_page", "concord_work_browse.scope": "work_scope", "concord_work_browse.resource_claims": "resource_claims_page",
	"concord_work_trace.history": "work_event_page", "concord_work_trace.continuity": "continuity_snapshot", "concord_work_trace.relations": "work_relation_graph", "concord_work_trace.research": "research_pack", "concord_knowledge.search": "knowledge_page", "concord_knowledge.resolve_note": "canonical_note_result",
	"concord_work_define.capture": "mutation_result", "concord_work_define.revise_intent": "mutation_result", "concord_work_transition.lifecycle": "mutation_result", "concord_work_transition.workflow_action": "mutation_result", "concord_work_transition.worktree_claim": "mutation_result", "concord_work_transition.worktree_reclaim": "mutation_result",
	"concord_work_epic.create": "mutation_result", "concord_work_define.research_pack_create": "mutation_result", "concord_work_define.research_revision_append": "mutation_result", "concord_work_define.research_finding_record": "mutation_result", "concord_work_define.research_source_record": "mutation_result", "concord_work_define.research_freshness_set": "mutation_result", "concord_work_epic.add_entry": "mutation_result", "concord_work_epic.remove_entry": "mutation_result", "concord_work_epic.reorder_entry": "mutation_result", "concord_work_epic.change_requiredness": "mutation_result", "concord_work_epic.revise_narrative": "mutation_result", "concord_work_epic.entries": "epic_entries_result",
	"concord_work_relate.set_memberships": "mutation_result", "concord_work_relate.link": "mutation_result", "concord_work_relate.unlink": "mutation_result", "concord_work_relate.supersede": "mutation_result", "concord_work_relate.restore_superseded": "mutation_result", "concord_work_relate.resource_claim": "mutation_result", "concord_work_relate.resource_release": "mutation_result",
	"concord_work_compact.publish": "mutation_result", "concord_work_compact.lesson_publish": "mutation_result", "concord_work_compact.reconcile": "mutation_result",
}
var operationInputSchemas = map[string]string{
	"concord_product_view.resolve": "product_view_resolve_input", "concord_product_view.snapshot": "product_view_snapshot_input", "concord_product_view.portfolio": "product_row_portfolio_input", "concord_product_view.blocked_sessions": "product_view_blocked_sessions_input", "concord_work_browse.list": "work_browse_list_input", "concord_work_browse.ready": "work_browse_ready_input", "concord_work_browse.blocked": "work_browse_blocked_input", "concord_work_browse.scope": "work_browse_scope_input", "concord_work_browse.resource_claims": "work_browse_resource_claims_input", "concord_work_trace.history": "work_trace_history_input", "concord_work_trace.continuity": "work_trace_continuity_input", "concord_work_trace.relations": "work_trace_relations_input", "concord_work_trace.research": "work_trace_research_input", "concord_work_define.research_pack_create": "work_define_research_pack_create_input", "concord_work_define.research_revision_append": "work_define_research_revision_append_input", "concord_work_define.research_finding_record": "work_define_research_finding_record_input", "concord_work_define.research_source_record": "work_define_research_source_record_input", "concord_work_define.research_freshness_set": "work_define_research_freshness_set_input", "concord_knowledge.search": "knowledge_search_input", "concord_knowledge.resolve_note": "knowledge_resolve_input", "concord_work_define.capture": "work_define_capture_input", "concord_work_define.revise_intent": "work_define_revise_input", "concord_work_transition.lifecycle": "work_transition_lifecycle_input", "concord_work_transition.workflow_action": "work_transition_action_input", "concord_work_transition.worktree_claim": "work_transition_worktree_claim_input", "concord_work_transition.worktree_reclaim": "work_transition_worktree_reclaim_input", "concord_work_relate.set_memberships": "work_relate_memberships_input", "concord_work_relate.link": "work_relate_link_input", "concord_work_relate.unlink": "work_relate_unlink_input", "concord_work_relate.supersede": "work_relate_supersede_input", "concord_work_relate.restore_superseded": "work_relate_restore_input", "concord_work_relate.resource_claim": "work_relate_resource_claim_input", "concord_work_relate.resource_release": "work_relate_resource_release_input", "concord_work_compact.publish": "work_compact_publish_input", "concord_work_compact.lesson_publish": "work_compact_lesson_publish_input", "concord_work_compact.reconcile": "work_compact_reconcile_input",
	"concord_work_epic.create": "epic_create_input", "concord_work_epic.add_entry": "epic_add_entry_input", "concord_work_epic.remove_entry": "epic_remove_entry_input", "concord_work_epic.reorder_entry": "epic_reorder_entry_input", "concord_work_epic.change_requiredness": "epic_change_requiredness_input", "concord_work_epic.revise_narrative": "epic_revise_narrative_input", "concord_work_epic.entries": "epic_entries_input",
}

func ValidateOperationPayload(tool, operation string, data []byte, result bool) error {
	if err := validateUniqueJSON(data); err != nil {
		return err
	}
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

// validateUniqueJSON rejects duplicate object members before any operation can
// reach grant or approval evaluation. encoding/json otherwise keeps only the
// last member, which would make the signed request ambiguous.
func validateUniqueJSON(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := consumeUniqueJSON(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func consumeUniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate JSON object field %q", key)
			}
			seen[key] = true
			if err := consumeUniqueJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case json.Delim('['):
		for decoder.More() {
			if err := consumeUniqueJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return nil
	}
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
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
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
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := schema[keyword].([]any); ok {
			matches := 0
			for _, raw := range branches {
				branch, _ := raw.(map[string]any)
				if validateSchemaValue(value, branch, root, path) == nil {
					matches++
				}
			}
			if keyword == "allOf" && matches != len(branches) || keyword == "anyOf" && matches < 1 || keyword == "oneOf" && matches != 1 {
				if keyword == "oneOf" {
					return fmt.Errorf("oneOf mismatch at %s: expected exactly one accepted variant {%s}", path, strings.Join(schemaVariantDescriptions(branches), "; "))
				}
				return fmt.Errorf("%s mismatch at %s", keyword, path)
			}
		}
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		branch, _ := schema["else"].(map[string]any)
		if validateSchemaValue(value, condition, root, path) == nil {
			branch, _ = schema["then"].(map[string]any)
		}
		if branch != nil {
			if err := validateSchemaValue(value, branch, root, path); err != nil {
				return err
			}
		}
	}
	if branch, ok := schema["not"].(map[string]any); ok && validateSchemaValue(value, branch, root, path) == nil {
		return fmt.Errorf("not mismatch at %s", path)
	}
	return nil
}

func schemaVariantDescriptions(branches []any) []string {
	descriptions := make([]string, 0, len(branches))
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			descriptions = append(descriptions, "schema variant")
			continue
		}
		parts := []string{}
		if fields := schemaFieldList(branch["required"]); len(fields) > 0 {
			parts = append(parts, "requires ["+strings.Join(fields, ", ")+"]")
		} else {
			parts = append(parts, "requires no fields")
		}
		if forbidden := schemaForbiddenFields(branch["not"]); forbidden != "" {
			parts = append(parts, forbidden)
		}
		descriptions = append(descriptions, strings.Join(parts, " "))
	}
	return descriptions
}

func schemaFieldList(raw any) []string {
	values, ok := raw.([]any)
	if !ok {
		return nil
	}
	fields := make([]string, 0, len(values))
	for _, value := range values {
		if field, ok := value.(string); ok {
			fields = append(fields, field)
		}
	}
	return fields
}

func schemaForbiddenFields(raw any) string {
	notSchema, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	if fields := schemaFieldList(notSchema["required"]); len(fields) > 0 {
		return "without [" + strings.Join(fields, ", ") + "]"
	}
	branches, ok := notSchema["anyOf"].([]any)
	if !ok {
		return ""
	}
	seen := map[string]bool{}
	fields := []string{}
	for _, branch := range branches {
		branchMap, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		for _, field := range schemaFieldList(branchMap["required"]) {
			if !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
	}
	if len(fields) == 0 {
		return ""
	}
	return "without any of [" + strings.Join(fields, ", ") + "]"
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
