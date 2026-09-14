// Package payloadschema owns validation of a JSON value against the generated
// payload schema document.
//
// The declaration of an action payload names a schema; the agent boundary and
// the workflow engine both answer to that name. Before this package existed the
// boundary resolved the schema and the engine reimplemented its rules in Go, so
// one declaration had two enforcement authorities and every divergence between
// them reached an agent as a refusal no published contract stated.
package payloadschema

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	documentOnce    sync.Once
	documentValue   map[string]any
	dateTimePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]([.][0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
)

// Document returns the parsed generated schema document.
func Document() map[string]any {
	documentOnce.Do(func() {
		decoder := json.NewDecoder(strings.NewReader(GeneratedPayloadSchemaDocument))
		decoder.UseNumber()
		if err := decoder.Decode(&documentValue); err != nil {
			panic(fmt.Sprintf("generated payload schema document is not JSON: %v", err))
		}
	})
	return documentValue
}

// Has reports whether the generated document declares the named schema.
func Has(name string) bool {
	defs, ok := Document()["$defs"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = defs[name].(map[string]any)
	return ok
}

// DescribesArray reports whether the named schema describes an array value
// rather than one element of one.
func DescribesArray(name string) bool {
	defs, ok := Document()["$defs"].(map[string]any)
	if !ok {
		return false
	}
	schema, ok := defs[name].(map[string]any)
	if !ok {
		return false
	}
	return schema["type"] == "array"
}

// Validate checks one JSON value against the named schema in the generated
// document. An unknown name is an error rather than a silent pass, so a
// declaration naming a schema that was never generated fails closed.
func Validate(name string, data []byte) error {
	root := Document()
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return fmt.Errorf("payload schema definitions missing")
	}
	schema, ok := defs[name].(map[string]any)
	if !ok {
		return fmt.Errorf("unknown payload schema %s", name)
	}
	value, err := decodeOne(data)
	if err != nil {
		return err
	}
	return ValidateValue(value, schema, root, "$")
}

// ValidateValue checks an already decoded value against an explicit schema and
// root, for a caller that owns its own document.
func ValidateValue(value any, schema, root map[string]any, path string) error {
	return validateSchemaValue(value, schema, root, path)
}

func decodeOne(data []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return value, nil
}

func validateSchemaValue(value any, schema map[string]any, root map[string]any, path string) error {
	_, err := validateSchemaValueWithEvaluated(value, schema, root, path)
	return err
}

func validateSchemaValueWithEvaluated(value any, schema map[string]any, root map[string]any, path string) (map[string]bool, error) {
	evaluated := map[string]bool{}
	if ref, ok := schema["$ref"].(string); ok {
		if !strings.HasPrefix(ref, "#/$defs/") {
			return nil, fmt.Errorf("unsupported schema ref %s", ref)
		}
		name := strings.TrimPrefix(ref, "#/$defs/")
		defs, _ := root["$defs"].(map[string]any)
		target, ok := defs[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing schema ref %s", ref)
		}
		referenced, err := validateSchemaValueWithEvaluated(value, target, root, path)
		if err != nil {
			return nil, err
		}
		for key := range referenced {
			evaluated[key] = true
		}
	}
	if err := validateValueKeywords(value, schema, path); err != nil {
		return nil, err
	}
	if object, ok := value.(map[string]any); ok {
		properties, err := validateObjectKeywords(object, schema, root, path)
		if err != nil {
			return nil, err
		}
		for key := range properties {
			evaluated[key] = true
		}
	}
	if array, ok := value.([]any); ok {
		if err := validateArrayKeywords(array, schema, root, path); err != nil {
			return nil, err
		}
	}
	if text, ok := value.(string); ok {
		if err := validateStringKeywords(text, schema, path); err != nil {
			return nil, err
		}
	}
	if number, ok := value.(json.Number); ok {
		if err := validateNumberKeywords(number, schema, path); err != nil {
			return nil, err
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		matches := 0
		matchedEvaluated := map[string]bool{}
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			branchEvaluated, err := validateSchemaValueWithEvaluated(value, branch, root, path)
			if err == nil {
				matches++
				for key := range branchEvaluated {
					matchedEvaluated[key] = true
				}
			}
		}
		if keyword == "allOf" && matches != len(branches) || keyword == "anyOf" && matches < 1 || keyword == "oneOf" && matches != 1 {
			if keyword == "oneOf" {
				return nil, fmt.Errorf("oneOf mismatch at %s: expected exactly one accepted variant {%s}", path, strings.Join(schemaVariantDescriptions(branches), "; "))
			}
			return nil, fmt.Errorf("%s mismatch at %s", keyword, path)
		}
		for key := range matchedEvaluated {
			evaluated[key] = true
		}
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		branch, _ := schema["else"].(map[string]any)
		if validateSchemaValue(value, condition, root, path) == nil {
			branch, _ = schema["then"].(map[string]any)
		}
		if branch != nil {
			branchEvaluated, err := validateSchemaValueWithEvaluated(value, branch, root, path)
			if err != nil {
				return nil, err
			}
			for key := range branchEvaluated {
				evaluated[key] = true
			}
		}
	}
	if branch, ok := schema["not"].(map[string]any); ok && validateSchemaValue(value, branch, root, path) == nil {
		return nil, fmt.Errorf("not mismatch at %s", path)
	}
	if object, ok := value.(map[string]any); ok {
		if unevaluated, exists := schema["unevaluatedProperties"]; exists {
			remaining := make([]string, 0)
			for key := range object {
				if !evaluated[key] {
					remaining = append(remaining, key)
				}
			}
			if additionalPropertiesFalse, ok := unevaluated.(bool); ok && !additionalPropertiesFalse && len(remaining) > 0 {
				return nil, fmt.Errorf("unevaluated property %s.%s", path, remaining[0])
			}
			if child, ok := unevaluated.(map[string]any); ok {
				for _, key := range remaining {
					if err := validateSchemaValue(object[key], child, root, path+"."+key); err != nil {
						return nil, err
					}
					evaluated[key] = true
				}
			}
		}
	}
	return evaluated, nil
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

func validateObjectKeywords(object map[string]any, schema map[string]any, root map[string]any, path string) (map[string]bool, error) {
	evaluated := map[string]bool{}
	properties, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			name, _ := raw.(string)
			if _, exists := object[name]; !exists {
				return nil, fmt.Errorf("missing required %s.%s", path, name)
			}
		}
	}
	patterns, _ := schema["patternProperties"].(map[string]any)
	if additional, exists := schema["additionalProperties"]; exists {
		if additionalPropertiesFalse, ok := additional.(bool); ok && !additionalPropertiesFalse {
			for key := range object {
				if _, exists := properties[key]; exists {
					continue
				}
				matched := false
				for pattern := range patterns {
					if regexp.MustCompile(pattern).MatchString(key) {
						matched = true
						break
					}
				}
				if !matched {
					return nil, fmt.Errorf("unknown property %s.%s", path, key)
				}
			}
		} else if child, ok := additional.(map[string]any); ok {
			for key, entry := range object {
				if _, exists := properties[key]; !exists && !matchesPattern(patterns, key) {
					if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
						return nil, err
					}
					evaluated[key] = true
				}
			}
		}
	}
	for key, raw := range properties {
		if entry, exists := object[key]; exists {
			child, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid schema property %s", key)
			}
			if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
				return nil, err
			}
			evaluated[key] = true
		}
	}
	for pattern, raw := range patterns {
		child, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid schema pattern property %s", pattern)
		}
		for key, entry := range object {
			if regexp.MustCompile(pattern).MatchString(key) {
				if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
					return nil, err
				}
				evaluated[key] = true
			}
		}
	}
	if n, ok := schema["minProperties"].(json.Number); ok && len(object) < numberInt(n) {
		return nil, fmt.Errorf("minProperties at %s", path)
	}
	if n, ok := schema["maxProperties"].(json.Number); ok && len(object) > numberInt(n) {
		return nil, fmt.Errorf("maxProperties at %s", path)
	}
	return evaluated, nil
}

func matchesPattern(patterns map[string]any, key string) bool {
	for pattern := range patterns {
		if regexp.MustCompile(pattern).MatchString(key) {
			return true
		}
	}
	return false
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
	if schema["format"] == "date-time" {
		if !dateTimePattern.MatchString(text) {
			return fmt.Errorf("date-time at %s: expected RFC3339 timestamp", path)
		}
		if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
			return fmt.Errorf("date-time at %s: %w", path, err)
		}
	}
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
