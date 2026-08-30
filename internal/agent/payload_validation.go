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

func ValidateOperationPayload(tool, operation string, data []byte, result bool) error {
	if err := validateUniqueJSON(data); err != nil {
		return err
	}
	contract, ok := ValidateContractOperation(tool, operation)
	if !ok {
		return fmt.Errorf("unknown operation payload %s.%s", tool, operation)
	}
	name := contract.ResultSchema
	if !result {
		name = contract.InputSchema
	}
	if name == "" {
		return fmt.Errorf("operation payload schema is not generated for %s", contract.ID)
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
	if err := validateValueKeywords(value, schema, path); err != nil {
		return err
	}
	if object, ok := value.(map[string]any); ok {
		if err := validateObjectKeywords(object, schema, root, path); err != nil {
			return err
		}
	}
	if array, ok := value.([]any); ok {
		if err := validateArrayKeywords(array, schema, root, path); err != nil {
			return err
		}
	}
	if text, ok := value.(string); ok {
		if err := validateStringKeywords(text, schema, path); err != nil {
			return err
		}
	}
	if number, ok := value.(json.Number); ok {
		if err := validateNumberKeywords(number, schema, path); err != nil {
			return err
		}
	}
	if err := validateCompositionKeywords(value, schema, root, path); err != nil {
		return err
	}
	if err := validateConditionalKeyword(value, schema, root, path); err != nil {
		return err
	}
	if err := validateNegationKeyword(value, schema, root, path); err != nil {
		return err
	}
	return nil
}

func validateValueKeywords(value any, schema map[string]any, path string) error {
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
			accepted, err := json.Marshal(values)
			if err != nil {
				return fmt.Errorf("enum mismatch at %s", path)
			}
			return fmt.Errorf("enum mismatch at %s: accepted values are %s", path, accepted)
		}
	}
	if types, ok := schema["type"]; ok && !matchesAnyType(value, types) {
		return fmt.Errorf("type mismatch at %s", path)
	}
	return nil
}

func validateObjectKeywords(object map[string]any, schema map[string]any, root map[string]any, path string) error {
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
		if additionalPropertiesFalse, ok := additional.(bool); ok && !additionalPropertiesFalse {
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
	return nil
}

func validateArrayKeywords(array []any, schema map[string]any, root map[string]any, path string) error {
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
	return nil
}

func validateStringKeywords(text string, schema map[string]any, path string) error {
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
	return nil
}

func validateNumberKeywords(number json.Number, schema map[string]any, path string) error {
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
	return nil
}

func validateCompositionKeywords(value any, schema map[string]any, root map[string]any, path string) error {
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
	return nil
}

func validateConditionalKeyword(value any, schema map[string]any, root map[string]any, path string) error {
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
	return nil
}

func validateNegationKeyword(value any, schema map[string]any, root map[string]any, path string) error {
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
