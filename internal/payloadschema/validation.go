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

// loadDocumentOnce parses one generated schema document exactly once.
func loadDocumentOnce(once *sync.Once, raw string, target *map[string]any, what string) {
	once.Do(func() {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(target); err != nil {
			panic(fmt.Sprintf("generated %s document is not JSON: %v", what, err))
		}
	})
}

// Document returns the parsed generated schema document.
func Document() map[string]any {
	loadDocumentOnce(&documentOnce, GeneratedPayloadSchemaDocument, &documentValue, "payload schema")
	return documentValue
}

// HasIn reports whether the given document declares the named schema.
func HasIn(root map[string]any, name string) bool {
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = defs[name].(map[string]any)
	return ok
}

// DescribesArrayIn reports whether the named schema in the given document
// describes an array value rather than one element of one.
func DescribesArrayIn(root map[string]any, name string) bool {
	defs, ok := root["$defs"].(map[string]any)
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
	return ValidateInDocument(Document(), name, data)
}

// ValidateInDocument checks one JSON value against the named schema in the
// given document root. An unknown name is an error rather than a silent
// pass, so a declaration naming a schema the document never carried fails
// closed.
func ValidateInDocument(root map[string]any, name string, data []byte) error {
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
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		if err := validateCombinatorBranches(value, keyword, branches, root, path, evaluated); err != nil {
			return nil, err
		}
	}
	// Select the applicable composed explanation before the shared outer shape
	// can hide its field teaching. Both sets of constraints still govern admission.
	if err := validateTypeKeywords(value, schema, root, path, evaluated); err != nil {
		return nil, err
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		if err := validateConditionalBranch(value, schema, condition, root, path, evaluated); err != nil {
			return nil, err
		}
	}
	if branch, ok := schema["not"].(map[string]any); ok && validateSchemaValue(value, branch, root, path) == nil {
		return nil, withGuidance(fmt.Errorf("not mismatch at %s", path), schema)
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
	sort.Strings(remaining)
	if additionalPropertiesFalse, ok := unevaluated.(bool); ok && !additionalPropertiesFalse && len(remaining) > 0 {
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
// Admission reads only the match count; when the keyword is unsatisfied, the
// explanation is selected: a branch the value's own discriminator or closed
// fieldset rules out is local to that branch and does not contribute its
// refusal as a rule about the whole request. Alternatives that genuinely
// remain stay labeled together. Selection never compares message lengths.
func validateCombinatorBranches(value any, keyword string, branches []any, root map[string]any, path string, evaluated map[string]bool) error {
	matches := 0
	// branchFailures stays aligned with branches: a nil entry is a branch
	// that matched (or was not a schema object), so selection can name the
	// branch each failure belongs to.
	branchFailures := make([]error, len(branches))
	matchedEvaluated := map[string]bool{}
	for i, raw := range branches {
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
			branchFailures[i] = err
		}
	}
	if !combinatorSatisfied(keyword, matches, branches) {
		return combinatorMismatch(value, keyword, branches, branchFailures, matches, root, path)
	}
	for key := range matchedEvaluated {
		evaluated[key] = true
	}
	return nil
}

func combinatorMismatch(value any, keyword string, branches []any, branchFailures []error, matches int, root map[string]any, path string) error {
	// allOf requires every branch, so the first branch failure is the
	// whole reason. Returning it unwrapped keeps the offending field at
	// the front of the message, where a schema factored through $defs
	// would otherwise stack one identical frame per composition level.
	if keyword == "allOf" {
		if failure := firstBranchFailure(branchFailures); failure != nil {
			return failure
		}
		return fmt.Errorf("allOf mismatch at %s", path)
	}
	// Several branches matched a oneOf: no branch failure exists, the
	// alternatives genuinely remain, and the variant list labels them.
	if keyword == "oneOf" && matches > 1 {
		return fmt.Errorf("oneOf mismatch at %s: expected exactly one accepted variant {%s}", path, strings.Join(schemaVariantDescriptions(branches, root), "; "))
	}
	live, failures := applicableBranchFailures(value, branches, branchFailures, root)
	if len(live) > 0 && len(live) < failures {
		// The value's discriminator or fieldset ruled branches out, so
		// the surviving failures are the applicable ones. One survivor
		// is the whole reason and returns unwrapped; several survivors
		// are alternatives that genuinely remain and stay labeled.
		if len(live) == 1 {
			return branchFailures[live[0]]
		}
		messages := make([]error, 0, len(live))
		labeled := labelBranchFailures(branches, branchFailures, root)
		for _, index := range live {
			messages = append(messages, labeled[index])
		}
		return joinSchemaFailures(fmt.Sprintf("%s mismatch at %s: ", keyword, path), messages)
	}
	branchFailures = labelBranchFailures(branches, branchFailures, root)
	if keyword == "oneOf" {
		if matches == 0 && len(errorStrings(branchFailures)) > 0 {
			return joinSchemaFailures(fmt.Sprintf("oneOf mismatch at %s: ", path), branchFailures, fmt.Sprintf("; expected exactly one accepted variant {%s}", strings.Join(schemaVariantDescriptions(branches, root), "; ")))
		}
		return fmt.Errorf("oneOf mismatch at %s: expected exactly one accepted variant {%s}", path, strings.Join(schemaVariantDescriptions(branches, root), "; "))
	}
	if len(errorStrings(branchFailures)) > 0 {
		return joinSchemaFailures(fmt.Sprintf("%s mismatch at %s: ", keyword, path), branchFailures)
	}
	return fmt.Errorf("%s mismatch at %s", keyword, path)
}

// Keep attribution attached to each guided failure, rather than only in a
// trailing variant inventory that a bounded diagnostic may have to omit.
func labelBranchFailures(branches []any, failures []error, root map[string]any) []error {
	labeled := append([]error(nil), failures...)
	for index, failure := range failures {
		if !HasGuidance(failure) {
			continue
		}
		branch, _ := branches[index].(map[string]any)
		label := composedSchemaDiscriminator(composedSchemaFrames(resolveSchemaRef(branch, root), root, 0))
		if label == "" {
			label = "variant " + strconv.Itoa(index+1)
		}
		labeled[index] = fmt.Errorf("alternative %s: %w", label, failure)
	}
	return labeled
}

func firstBranchFailure(branchFailures []error) error {
	for _, failure := range branchFailures {
		if failure != nil {
			return failure
		}
	}
	return nil
}

// applicableBranchFailures returns the indexes of branchFailures whose branch
// the value does not rule out, with the number of branch failures there were.
// A branch is ruled out when a supplied discriminator property it pins with
// const or enum is accepted by another branch of the same keyword, or when its
// closed shape refused properties that another not-yet-ruled-out branch
// declares: the value's fieldset is the other branch's, so the
// unknown-property refusal is local to the forbidding branch. A value with
// nothing to select on keeps every branch failure.
func applicableBranchFailures(value any, branches []any, branchFailures []error, root map[string]any) ([]int, int) {
	failures := 0
	for _, failure := range branchFailures {
		if failure != nil {
			failures++
		}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, failures
	}
	resolved := make([]map[string]any, len(branches))
	for i, raw := range branches {
		branch, _ := raw.(map[string]any)
		resolved[i] = resolveSchemaRef(branch, root)
	}
	ruledOut := make([]bool, len(branches))
	ruleOutContradictedBranches(object, resolved, ruledOut)
	ruleOutForeignFieldsets(object, resolved, branchFailures, root, ruledOut)
	live := make([]int, 0, failures)
	for i, failure := range branchFailures {
		if failure != nil && !ruledOut[i] {
			live = append(live, i)
		}
	}
	return live, failures
}

func ruleOutContradictedBranches(object map[string]any, resolved []map[string]any, ruledOut []bool) {
	for _, name := range sortedKeys(object) {
		supplied := object[name]
		pinning := []int{}
		accepting := []int{}
		for i, schema := range resolved {
			property := schemaPropertyName(schema, name)
			if property == nil || !propertyPinsValue(property) {
				continue
			}
			pinning = append(pinning, i)
			if propertyAcceptsValue(property, supplied) {
				accepting = append(accepting, i)
			}
		}
		if len(pinning) == 0 || len(accepting) == 0 {
			continue
		}
		for _, i := range pinning {
			if !propertyAcceptsValue(schemaPropertyName(resolved[i], name), supplied) {
				ruledOut[i] = true
			}
		}
	}
}

func ruleOutForeignFieldsets(object map[string]any, resolved []map[string]any, branchFailures []error, root map[string]any, ruledOut []bool) {
	for i := range resolved {
		if ruledOut[i] || i >= len(branchFailures) || branchFailures[i] == nil {
			continue
		}
		unknown := closedBranchUnknownKeys(object, resolved[i], root, 0)
		if len(unknown) == 0 {
			continue
		}
		for j := range resolved {
			if j == i || ruledOut[j] || branchPinsContradictedValue(object, resolved[j]) {
				continue
			}
			if branchDeclaresAllKeys(resolved[j], unknown, root, 0) || unknownKeysStrictSubset(closedBranchUnknownKeys(object, resolved[j], root, 0), unknown) {
				ruledOut[i] = true
				break
			}
		}
	}
}

// A shared unknown member must not revive a branch that additionally rejects
// the caller's legitimate fieldset. Compare sets, not error counts or lengths.
func unknownKeysStrictSubset(candidate, present []string) bool {
	if len(candidate) >= len(present) {
		return false
	}
	keys := map[string]bool{}
	for _, key := range present {
		keys[key] = true
	}
	for _, key := range candidate {
		if !keys[key] {
			return false
		}
	}
	return true
}

// branchPinsContradictedValue reports whether a branch pins a supplied
// property with const or enum and refuses the supplied value. Such a branch
// rejects the value itself, not merely a foreign fieldset, so it cannot own
// the value's fieldset for selection purposes: when the discriminator the
// caller supplied matches no branch, the alternatives genuinely remain.
func branchPinsContradictedValue(object map[string]any, schema map[string]any) bool {
	for _, name := range sortedKeys(object) {
		property := schemaPropertyName(schema, name)
		if property == nil {
			continue
		}
		if propertyPinsValue(property) && !propertyAcceptsValue(property, object[name]) {
			return true
		}
	}
	return false
}

// schemaPropertyName returns the property schema a branch declares for one
// name, or nil when the branch does not declare it.
func schemaPropertyName(schema map[string]any, name string) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	property, _ := properties[name].(map[string]any)
	return property
}

// propertyPinsValue reports whether the property schema constrains the value
// with const or enum, so a mismatch discriminates between variants.
func propertyPinsValue(property map[string]any) bool {
	if _, ok := property["const"]; ok {
		return true
	}
	_, ok := property["enum"].([]any)
	return ok
}

// propertyAcceptsValue reports whether the property's const or enum admits
// the supplied value. Types and bounds do not discriminate: a type mismatch
// is an ordinary branch failure, not a rule about which variant the caller
// aimed at.
func propertyAcceptsValue(property map[string]any, supplied any) bool {
	if constant, ok := property["const"]; ok {
		return reflect.DeepEqual(supplied, constant)
	}
	values, ok := property["enum"].([]any)
	if !ok {
		return false
	}
	for _, candidate := range values {
		if reflect.DeepEqual(supplied, candidate) {
			return true
		}
	}
	return false
}

// closedBranchUnknownKeys returns the keys a closed schema would refuse as
// unknown at one object level, mirroring the structural scan the validator
// applies: nothing a property, a pattern property, or an allOf frame declares
// is unknown.
func closedBranchUnknownKeys(object map[string]any, schema map[string]any, root map[string]any, depth int) []string {
	if depth > 8 {
		return nil
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		return nil
	}
	unknown := []string{}
	for _, key := range sortedKeys(object) {
		if branchNodeDeclaresKey(schema, key, root, depth) {
			continue
		}
		unknown = append(unknown, key)
	}
	return unknown
}

func branchNodeDeclaresKey(schema map[string]any, key string, root map[string]any, depth int) bool {
	if depth > 8 {
		return false
	}
	schema = resolveSchemaRef(schema, root)
	if properties, ok := schema["properties"].(map[string]any); ok {
		if _, declared := properties[key]; declared {
			return true
		}
	}
	if patterns, ok := schema["patternProperties"].(map[string]any); ok && matchesPattern(patterns, key) {
		return true
	}
	composed, ok := schema["allOf"].([]any)
	if !ok {
		return false
	}
	for _, raw := range composed {
		frame, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if branchNodeDeclaresKey(frame, key, root, depth+1) {
			return true
		}
	}
	return false
}

// branchDeclaresAllKeys reports whether a branch declares every key, so a
// closed sibling refusing those keys is contradicted by the value's fieldset.
func branchDeclaresAllKeys(schema map[string]any, keys []string, root map[string]any, depth int) bool {
	for _, key := range keys {
		if !branchNodeDeclaresKey(schema, key, root, depth) {
			return false
		}
	}
	return true
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
		return withGuidance(fmt.Errorf("const mismatch at %s", path), schema)
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
				return withGuidance(fmt.Errorf("enum mismatch at %s", path), schema)
			}
			return withGuidance(fmt.Errorf("enum mismatch at %s: accepted values are %s", path, accepted), schema)
		}
	}
	if types, ok := schema["type"]; ok && !matchesAnyType(value, types) {
		return withGuidance(fmt.Errorf("type mismatch at %s", path), schema)
	}
	return nil
}

// validateObjectKeywords enforces the object keywords at one instance path.
// The structural scan collects every missing required field and, when
// additionalProperties is false, every undeclared key, and returns one
// refusal that names them all: missing fields in declared order, then
// undeclared keys sorted by name. Each segment keeps the per-field message
// callers match today. Value keywords keep stop-at-first behavior, visit map
// keys in lexical order, and run only after the structure is complete.
func validateObjectKeywords(object map[string]any, schema map[string]any, root map[string]any, path string) (map[string]bool, error) {
	evaluated := map[string]bool{}
	properties, _ := schema["properties"].(map[string]any)
	patterns, _ := schema["patternProperties"].(map[string]any)
	if err := validateObjectFieldset(object, schema, properties, patterns, path); err != nil {
		return nil, err
	}
	if err := validateAdditionalProperties(object, schema, properties, patterns, root, path, evaluated); err != nil {
		return nil, err
	}
	for _, key := range sortedKeys(properties) {
		if entry, exists := object[key]; exists {
			child, ok := properties[key].(map[string]any)
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

func validateObjectFieldset(object, schema, properties, patterns map[string]any, path string) error {
	segments := make([]error, 0)
	if required, ok := schema["required"].([]any); ok {
		for _, raw := range required {
			name, _ := raw.(string)
			if _, exists := object[name]; !exists {
				segments = append(segments, withGuidance(fmt.Errorf("missing required %s.%s", path, name), schemaPropertyName(schema, name)))
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
			segments = append(segments, unknownPropertyError(path, key, schema))
		}
	}
	if len(segments) > 0 {
		return joinSchemaFailures("", segments)
	}
	return nil
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
	for _, key := range sortedKeys(object) {
		if _, exists := properties[key]; !exists && !matchesPattern(patterns, key) {
			if err := validateSchemaValue(object[key], child, root, path+"."+key); err != nil {
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
	keys := sortedKeys(object)
	for _, pattern := range sortedKeys(patterns) {
		child, ok := patterns[pattern].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid schema pattern property %s", pattern)
		}
		for _, key := range keys {
			if regexp.MustCompile(pattern).MatchString(key) {
				if err := validateSchemaValue(object[key], child, root, path+"."+key); err != nil {
					return err
				}
				evaluated[key] = true
			}
		}
	}
	return nil
}

func matchesPattern(patterns map[string]any, key string) bool {
	for _, pattern := range sortedKeys(patterns) {
		if regexp.MustCompile(pattern).MatchString(key) {
			return true
		}
	}
	return false
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateArrayKeywords(array []any, schema map[string]any, root map[string]any, path string) error {
	if n, ok := schema["x-maxArrayBytes"].(json.Number); ok {
		raw, err := json.Marshal(array)
		if err != nil {
			return fmt.Errorf("x-maxArrayBytes at %s: %w", path, err)
		}
		if len(raw) > numberInt(n) {
			return withGuidance(fmt.Errorf("x-maxArrayBytes at %s: carries %d UTF-8 bytes against a limit of %d", path, len(raw), numberInt(n)), schema)
		}
	}
	if n, ok := schema["minItems"].(json.Number); ok && len(array) < numberInt(n) {
		return withGuidance(fmt.Errorf("minItems at %s: carries %d item(s) against a minimum of %d", path, len(array), numberInt(n)), schema)
	}
	if n, ok := schema["maxItems"].(json.Number); ok && len(array) > numberInt(n) {
		return withGuidance(fmt.Errorf("maxItems at %s: carries %d item(s) against a limit of %d", path, len(array), numberInt(n)), schema)
	}
	if unique, ok := schema["uniqueItems"].(bool); ok && unique {
		seen := map[string]bool{}
		for _, entry := range array {
			raw, _ := json.Marshal(entry)
			if seen[string(raw)] {
				return withGuidance(fmt.Errorf("uniqueItems at %s", path), schema)
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
			return withGuidance(fmt.Errorf("date-time at %s: expected RFC3339 timestamp", path), schema)
		}
		if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
			return withGuidance(fmt.Errorf("date-time at %s: %w", path, err), schema)
		}
	}
	// JSON Schema string lengths count Unicode code points.
	codePoints := utf8.RuneCountInString(text)
	if n, ok := schema["minLength"].(json.Number); ok && codePoints < numberInt(n) {
		return withGuidance(fmt.Errorf("minLength at %s: carries %d Unicode code points against a minimum of %d", path, codePoints, numberInt(n)), schema)
	}
	if n, ok := schema["maxLength"].(json.Number); ok && codePoints > numberInt(n) {
		return withGuidance(fmt.Errorf("maxLength at %s: carries %d Unicode code points against a limit of %d", path, codePoints, numberInt(n)), schema)
	}
	// The store's enforcing string bounds count UTF-8 bytes; the generated
	// x-maxBytes/x-minBytes keywords carry those byte bounds beside the
	// derived code-point bounds, so this validator refuses a byte-overrun
	// string the code-point maxLength alone would admit (CON-412).
	if n, ok := schema["x-maxBytes"].(json.Number); ok && len(text) > numberInt(n) {
		return withGuidance(fmt.Errorf("x-maxBytes at %s: carries %d UTF-8 bytes against a limit of %d", path, len(text), numberInt(n)), schema)
	}
	if n, ok := schema["x-minBytes"].(json.Number); ok && len(text) < numberInt(n) {
		return withGuidance(fmt.Errorf("x-minBytes at %s: carries %d UTF-8 bytes against a minimum of %d", path, len(text), numberInt(n)), schema)
	}
	if pattern, ok := schema["pattern"].(string); ok {
		matched, _ := regexp.MatchString(pattern, text)
		if !matched {
			return withGuidance(fmt.Errorf("pattern at %s", path), schema)
		}
	}
	return nil
}

func validateNumberKeywords(number json.Number, schema map[string]any, path string) error {
	n, _ := strconv.ParseFloat(string(number), 64)
	if lower, ok := schema["minimum"].(json.Number); ok {
		m, _ := strconv.ParseFloat(string(lower), 64)
		if n < m {
			return withGuidance(fmt.Errorf("minimum at %s", path), schema)
		}
	}
	if upper, ok := schema["maximum"].(json.Number); ok {
		m, _ := strconv.ParseFloat(string(upper), 64)
		if n > m {
			return withGuidance(fmt.Errorf("maximum at %s", path), schema)
		}
	}
	return nil
}

func errorStrings(branchFailures []error) []string {
	messages := make([]string, 0, len(branchFailures))
	for _, err := range branchFailures {
		if err != nil {
			messages = append(messages, err.Error())
		}
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
	for _, name := range sortedKeys(properties) {
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
		if matchesType(value, kind) {
			return true
		}
	}
	return false
}

func matchesType(value any, kind string) bool {
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
	return false
}
func numberInt(value json.Number) int { n, _ := strconv.Atoi(string(value)); return n }
