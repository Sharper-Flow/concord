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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
		referenced, err := validateSchemaRef(value, ref, root, path)
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
	if err := validateTypeKeywords(value, schema, root, path, evaluated); err != nil {
		return nil, err
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		if err := validateCombinatorBranches(value, keyword, branches, root, path, evaluated); err != nil {
			return nil, err
		}
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		if err := validateConditionalBranch(value, schema, condition, root, path, evaluated); err != nil {
			return nil, err
		}
	}
	if branch, ok := schema["not"].(map[string]any); ok && validateSchemaValue(value, branch, root, path) == nil {
		return nil, fmt.Errorf("not mismatch at %s", path)
	}
	if err := validateUnevaluatedProperties(value, schema, root, path, evaluated); err != nil {
		return nil, err
	}
	return evaluated, nil
}

// validateUnevaluatedProperties enforces the unevaluatedProperties keyword
// over the keys no property, pattern, combinator, or branch evaluated, and
// folds the keys it validates into the caller's set.
func validateUnevaluatedProperties(value any, schema map[string]any, root map[string]any, path string, evaluated map[string]bool) error {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	unevaluated, exists := schema["unevaluatedProperties"]
	if !exists {
		return nil
	}
	remaining := make([]string, 0)
	for key := range object {
		if !evaluated[key] {
			remaining = append(remaining, key)
		}
	}
	if additionalPropertiesFalse, ok := unevaluated.(bool); ok && !additionalPropertiesFalse && len(remaining) > 0 {
		sort.Strings(remaining)
		segments := make([]string, 0, len(remaining))
		for _, key := range remaining {
			segments = append(segments, fmt.Sprintf("unevaluated property %s.%s", path, key))
		}
		return fmt.Errorf("%s", strings.Join(segments, "; "))
	}
	if child, ok := unevaluated.(map[string]any); ok {
		for _, key := range remaining {
			if err := validateSchemaValue(object[key], child, root, path+"."+key); err != nil {
				return err
			}
			evaluated[key] = true
		}
	}
	return nil
}

// validateSchemaRef resolves one #/$defs/ reference and validates the value
// against the referenced definition, returning its evaluated properties.
func validateSchemaRef(value any, ref string, root map[string]any, path string) (map[string]bool, error) {
	if !strings.HasPrefix(ref, "#/$defs/") {
		return nil, fmt.Errorf("unsupported schema ref %s", ref)
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	defs, _ := root["$defs"].(map[string]any)
	target, ok := defs[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing schema ref %s", ref)
	}
	return validateSchemaValueWithEvaluated(value, target, root, path)
}

// validateTypeKeywords dispatches the value's Go type to its keyword
// validator and folds the properties an object schema evaluated into the
// caller's set.
func validateTypeKeywords(value any, schema map[string]any, root map[string]any, path string, evaluated map[string]bool) error {
	if object, ok := value.(map[string]any); ok {
		properties, err := validateObjectKeywords(object, schema, root, path)
		if err != nil {
			return err
		}
		for key := range properties {
			evaluated[key] = true
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
	return nil
}

// validateConditionalBranch applies the if/then/else branch the value
// selects and folds its evaluated properties into the caller's set.
func validateConditionalBranch(value any, schema, condition map[string]any, root map[string]any, path string, evaluated map[string]bool) error {
	branch, _ := schema["else"].(map[string]any)
	if validateSchemaValue(value, condition, root, path) == nil {
		branch, _ = schema["then"].(map[string]any)
	}
	if branch == nil {
		return nil
	}
	branchEvaluated, err := validateSchemaValueWithEvaluated(value, branch, root, path)
	if err != nil {
		return err
	}
	for key := range branchEvaluated {
		evaluated[key] = true
	}
	return nil
}

// validateCombinatorBranches enforces one allOf, anyOf, or oneOf keyword and
// folds the properties its matching branches evaluated into the caller's set.
func validateCombinatorBranches(value any, keyword string, branches []any, root map[string]any, path string, evaluated map[string]bool) error {
	matches := 0
	branchFailures := make([]error, 0)
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
		} else {
			branchFailures = append(branchFailures, err)
		}
	}
	if !combinatorSatisfied(keyword, matches, branches) {
		if keyword == "oneOf" {
			return fmt.Errorf("oneOf mismatch at %s: expected exactly one accepted variant {%s}", path, strings.Join(schemaVariantDescriptions(branches, root), "; "))
		}
		// allOf requires every branch, so the first branch failure is the
		// whole reason. Returning it unwrapped keeps the offending field at
		// the front of the message, where a schema factored through $defs
		// would otherwise stack one identical frame per composition level.
		if keyword == "allOf" && len(branchFailures) > 0 {
			return branchFailures[0]
		}
		if len(branchFailures) > 0 {
			return fmt.Errorf("%s mismatch at %s: %s", keyword, path, strings.Join(errorStrings(branchFailures), "; "))
		}
		return fmt.Errorf("%s mismatch at %s", keyword, path)
	}
	for key := range matchedEvaluated {
		evaluated[key] = true
	}
	return nil
}

func combinatorSatisfied(keyword string, matches int, branches []any) bool {
	switch keyword {
	case "allOf":
		return matches == len(branches)
	case "anyOf":
		return matches >= 1
	case "oneOf":
		return matches == 1
	}
	return true
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

// validateObjectKeywords enforces the object keywords at one instance path.
// The structural scan collects every missing required field and, when
// additionalProperties is false, every undeclared key, and returns one
// refusal that names them all: missing fields in declared order, then
// undeclared keys sorted by name. Each segment keeps the per-field message
// callers match today. Value keywords keep stop-at-first behavior and run
// only after the structure is complete.
func validateObjectKeywords(object map[string]any, schema map[string]any, root map[string]any, path string) (map[string]bool, error) {
	evaluated := map[string]bool{}
	properties, _ := schema["properties"].(map[string]any)
	patterns, _ := schema["patternProperties"].(map[string]any)
	segments := make([]string, 0)
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			name, _ := raw.(string)
			if _, exists := object[name]; !exists {
				segments = append(segments, fmt.Sprintf("missing required %s.%s", path, name))
			}
		}
	}
	if additionalPropertiesFalse, ok := schema["additionalProperties"].(bool); ok && !additionalPropertiesFalse {
		unknown := make([]string, 0)
		for key := range object {
			if _, exists := properties[key]; exists {
				continue
			}
			if matchesPattern(patterns, key) {
				continue
			}
			unknown = append(unknown, key)
		}
		sort.Strings(unknown)
		for _, key := range unknown {
			segments = append(segments, fmt.Sprintf("unknown property %s.%s", path, key))
		}
	}
	if len(segments) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(segments, "; "))
	}
	if err := validateAdditionalProperties(object, schema, properties, patterns, root, path, evaluated); err != nil {
		return nil, err
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
	if err := validatePatternProperties(patterns, object, root, path, evaluated); err != nil {
		return nil, err
	}
	if n, ok := schema["minProperties"].(json.Number); ok && len(object) < numberInt(n) {
		return nil, fmt.Errorf("minProperties at %s", path)
	}
	if n, ok := schema["maxProperties"].(json.Number); ok && len(object) > numberInt(n) {
		return nil, fmt.Errorf("maxProperties at %s", path)
	}
	return evaluated, nil
}

// validateAdditionalProperties enforces the additionalProperties keyword in
// its schema form: it validates each key no property or pattern owns and
// folds it into the evaluated set. The false form is structural and
// validateObjectKeywords refuses it before this runs.
func validateAdditionalProperties(object, schema, properties, patterns map[string]any, root map[string]any, path string, evaluated map[string]bool) error {
	additional, exists := schema["additionalProperties"]
	if !exists {
		return nil
	}
	child, ok := additional.(map[string]any)
	if !ok {
		return nil
	}
	for key, entry := range object {
		if _, exists := properties[key]; !exists && !matchesPattern(patterns, key) {
			if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
				return err
			}
			evaluated[key] = true
		}
	}
	return nil
}

// validatePatternProperties validates each object key its declared pattern
// matches and folds it into the evaluated set.
func validatePatternProperties(patterns map[string]any, object map[string]any, root map[string]any, path string, evaluated map[string]bool) error {
	for pattern, raw := range patterns {
		child, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid schema pattern property %s", pattern)
		}
		for key, entry := range object {
			if regexp.MustCompile(pattern).MatchString(key) {
				if err := validateSchemaValue(entry, child, root, path+"."+key); err != nil {
					return err
				}
				evaluated[key] = true
			}
		}
	}
	return nil
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
		return fmt.Errorf("minItems at %s: carries %d item(s) against a minimum of %d", path, len(array), numberInt(n))
	}
	if n, ok := schema["maxItems"].(json.Number); ok && len(array) > numberInt(n) {
		return fmt.Errorf("maxItems at %s: carries %d item(s) against a limit of %d", path, len(array), numberInt(n))
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
	// JSON Schema string lengths count Unicode code points.
	codePoints := utf8.RuneCountInString(text)
	if n, ok := schema["minLength"].(json.Number); ok && codePoints < numberInt(n) {
		return fmt.Errorf("minLength at %s: carries %d Unicode code points against a minimum of %d", path, codePoints, numberInt(n))
	}
	if n, ok := schema["maxLength"].(json.Number); ok && codePoints > numberInt(n) {
		return fmt.Errorf("maxLength at %s: carries %d Unicode code points against a limit of %d", path, codePoints, numberInt(n))
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
	if lower, ok := schema["minimum"].(json.Number); ok {
		m, _ := strconv.ParseFloat(string(lower), 64)
		if n < m {
			return fmt.Errorf("minimum at %s", path)
		}
	}
	if upper, ok := schema["maximum"].(json.Number); ok {
		m, _ := strconv.ParseFloat(string(upper), 64)
		if n > m {
			return fmt.Errorf("maximum at %s", path)
		}
	}
	return nil
}

func errorStrings(errs []error) []string {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, err.Error())
	}
	return messages
}

func schemaVariantDescriptions(branches []any, root map[string]any) []string {
	descriptions := make([]string, 0, len(branches))
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			descriptions = append(descriptions, "schema variant")
			continue
		}
		frames := composedSchemaFrames(resolveSchemaRef(branch, root), root, 0)
		parts := []string{}
		if discriminator := composedSchemaDiscriminator(frames); discriminator != "" {
			parts = append(parts, discriminator)
		}
		if fields := composedSchemaRequiredFields(frames); len(fields) > 0 {
			parts = append(parts, "requires ["+strings.Join(fields, ", ")+"]")
		} else {
			parts = append(parts, "requires no fields")
		}
		if forbidden := composedSchemaForbiddenFields(frames); forbidden != "" {
			parts = append(parts, forbidden)
		}
		descriptions = append(descriptions, strings.Join(parts, " "))
	}
	return descriptions
}

// composedSchemaFrames returns the branch plus every schema its allOf keyword
// composes into it, resolving local refs, so a variant factored through allOf
// describes itself by what its frames require. A frame that carries only
// if/then contributes nothing: its requirements hold only when its condition
// does, so they are not unconditional requirements of the variant. The depth
// bound matches resolveSchemaRef, because describing a refusal must not
// itself refuse on a cyclic or self-referential schema.
func composedSchemaFrames(branch map[string]any, root map[string]any, depth int) []map[string]any {
	frames := []map[string]any{branch}
	if depth >= 8 {
		return frames
	}
	composed, ok := branch["allOf"].([]any)
	if !ok {
		return frames
	}
	for _, entry := range composed {
		frame, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		frames = append(frames, composedSchemaFrames(resolveSchemaRef(frame, root), root, depth+1)...)
	}
	return frames
}

func composedSchemaDiscriminator(frames []map[string]any) string {
	for _, frame := range frames {
		if discriminator := schemaDiscriminator(frame); discriminator != "" {
			return discriminator
		}
	}
	return ""
}

func composedSchemaRequiredFields(frames []map[string]any) []string {
	fields := []string{}
	seen := map[string]bool{}
	for _, frame := range frames {
		for _, field := range schemaFieldList(frame["required"]) {
			if !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
	}
	return fields
}

func composedSchemaForbiddenFields(frames []map[string]any) string {
	for _, frame := range frames {
		if forbidden := schemaForbiddenFields(frame["not"]); forbidden != "" {
			return forbidden
		}
	}
	return ""
}

// resolveSchemaRef follows a "$ref" so a variant declared as a bare reference
// into $defs describes itself by its own required fields rather than by the
// empty reference object that names it. A ref that does not resolve leaves the
// branch as it stands, because describing a refusal must not itself refuse.
func resolveSchemaRef(branch map[string]any, root map[string]any) map[string]any {
	for depth := 0; depth < 8; depth++ {
		ref, ok := branch["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/$defs/") {
			return branch
		}
		defs, _ := root["$defs"].(map[string]any)
		target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			return branch
		}
		branch = target
	}
	return branch
}

// schemaDiscriminator reports the constant property that tells one variant of
// a oneOf from its siblings, so a caller reads which variant it aimed at
// instead of counting required-field lists.
func schemaDiscriminator(branch map[string]any) string {
	properties, ok := branch["properties"].(map[string]any)
	if !ok {
		return ""
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		property, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		if constant, ok := property["const"].(string); ok {
			return fmt.Sprintf("%s=%q", name, constant)
		}
	}
	return ""
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
