package payloadschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode/utf8"
)

// guidanceMarker prefixes the appended teaching segment an annotated schema
// carries in its description.
const guidanceMarker = "Accepted shape:"

// Annotation bounds. The agent boundary relays a refusal inside a 1000-byte
// message, so the shape-and-example segment stays within a budget the leaf
// constraint prefix cannot overrun. The shape is rendered compactly from the
// schema's structural constraints and is never chopped; an example may be as
// large as the segment budget admits. Generation that cannot stay inside the
// budgets fails explicitly instead of publishing partial teaching.
const (
	guidanceExampleMaxBytes = 450
	guidanceSegmentMaxBytes = 700
	guidanceDepthMax        = 24
	guidanceShapeDepthMax   = 8
	// Repeat expansions in synthesized patterns: a 64-hex digest expands in
	// full, while a degenerate {100000} stays bounded.
	guidancePatternRepeatMax = 512
)

// pendingGuidance is one planned annotation. Planning produces every segment
// before any write, so a generation failure leaves the document untouched.
type pendingGuidance struct {
	node     map[string]any
	examples []any
	segment  string
}

// AnnotateGuidance mutates the named definitions in root, and every schema
// they reach through properties, items, composition, references, and
// conditional or forbidden branches, to carry one schema-derived example and
// an appended description of the accepted shape. The only writes are the
// schema-standard examples and description annotations. Every generated
// example is validated against its own effective schema before anything is
// attached, and planning completes before the first write, so a failure
// returns an error and mutates nothing.
//
// A constrained node that cannot yield a schema-valid, in-budget example is a
// hard error: guidance is never silently skipped. Two node classes are exempt
// because they carry no authored field to teach: pure combinator containers,
// and nodes that only name fields of an enclosing object — the shape of
// oneOf branches and of forbidden (not) and conditional (if/then/else) frames
// that declare no value constraints of their own.
//
// boundNames select additional inputs for bounds-only annotation through the
// same walker, without an example obligation. Both selections share one atomic
// plan and deduplicate overlapping nodes. Bounds traversal refuses excessive
// depth or unresolved references instead of silently dropping their teaching.
func AnnotateGuidance(root map[string]any, names []string, boundNames ...string) error {
	if _, ok := root["$defs"].(map[string]any); !ok {
		return fmt.Errorf("payload schema definitions missing")
	}
	defs, _ := root["$defs"].(map[string]any)
	for _, name := range append(append([]string{}, names...), boundNames...) {
		if _, ok := defs[name].(map[string]any); !ok {
			return fmt.Errorf("unknown payload schema %s", name)
		}
	}
	// Track planned annotations as well as applied ones for overlapping inputs.
	plan := []pendingGuidance{}
	plannedBounds := map[uintptr]bool{}
	visited, seen := map[string]bool{}, map[uintptr]bool{}
	for _, name := range names {
		if err := planDefinition(root, name, visited, seen, 0, "$defs."+name, false, plannedBounds, &plan); err != nil {
			return err
		}
	}
	boundVisited, boundSeen := map[string]bool{}, map[uintptr]bool{}
	for _, name := range boundNames {
		if err := planDefinition(root, name, boundVisited, boundSeen, 0, "$defs."+name, true, plannedBounds, &plan); err != nil {
			return err
		}
	}
	for _, pending := range plan {
		applyGuidance(pending)
	}
	return nil
}

func applyGuidance(pending pendingGuidance) {
	node := pending.node
	if existing, ok := node["description"].(string); ok && existing != "" {
		node["description"] = existing + " " + pending.segment
	} else {
		node["description"] = pending.segment
	}
	if _, ok := node["examples"]; !ok && pending.examples != nil {
		node["examples"] = pending.examples
	}
}

// The two selections walk independently and share their annotation plan.
func planDefinition(root map[string]any, name string, visited map[string]bool, seen map[uintptr]bool, depth int, path string, bounds bool, plannedBounds map[uintptr]bool, plan *[]pendingGuidance) error {
	if visited[name] {
		return nil
	}
	if depth > guidanceDepthMax {
		if bounds {
			return fmt.Errorf("%s: bound guidance exceeds schema depth", path)
		}
		return nil
	}
	visited[name] = true
	defs, _ := root["$defs"].(map[string]any)
	target, ok := defs[name].(map[string]any)
	if !ok {
		if bounds {
			return fmt.Errorf("%s: unknown bound reference #/$defs/%s", path, name)
		}
		return nil
	}
	return planSchema(target, root, visited, seen, depth+1, path, false, bounds, plannedBounds, plan)
}

// Visit schema-valued positions only, not objects inside annotations/examples.
func planSchema(schema map[string]any, root map[string]any, visited map[string]bool, seen map[uintptr]bool, depth int, path string, field bool, bounds bool, plannedBounds map[uintptr]bool, plan *[]pendingGuidance) error {
	if depth > guidanceDepthMax {
		if bounds {
			return fmt.Errorf("%s: bound guidance exceeds schema depth", path)
		}
		return nil
	}
	pointer := reflect.ValueOf(schema).Pointer()
	if seen[pointer] {
		return nil
	}
	seen[pointer] = true
	if bounds {
		planBoundsNode(schema, root, pointer, plannedBounds, plan)
	} else if err := planNode(schema, root, depth, path, field, pointer, plannedBounds, plan); err != nil {
		return err
	}
	if ref, ok := schema["$ref"].(string); ok {
		if !strings.HasPrefix(ref, "#/$defs/") {
			if bounds {
				return fmt.Errorf("%s: unsupported bound reference %s", path, ref)
			}
		} else if err := planDefinition(root, strings.TrimPrefix(ref, "#/$defs/"), visited, seen, depth, path+"->"+ref, bounds, plannedBounds, plan); err != nil {
			return err
		}
	}
	for _, key := range []string{"properties", "patternProperties", "$defs"} {
		children, _ := schema[key].(map[string]any)
		for _, name := range sortedKeys(children) {
			child, ok := children[name].(map[string]any)
			if !ok {
				continue
			}
			if err := planSchema(child, root, visited, seen, depth+1, path+"."+key+"."+name, key != "$defs", bounds, plannedBounds, plan); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "propertyNames", "contains", "if", "then", "else", "not", "allOf", "anyOf", "oneOf", "prefixItems"} {
		// items, additional properties, and positional entries are value
		// positions like named fields; the frame keywords are not.
		fieldChild := key == "items" || key == "additionalProperties" || key == "prefixItems"
		switch child := schema[key].(type) {
		case map[string]any:
			if err := planSchema(child, root, visited, seen, depth+1, path+"."+key, fieldChild, bounds, plannedBounds, plan); err != nil {
				return err
			}
		case []any:
			for index, raw := range child {
				if branch, ok := raw.(map[string]any); ok {
					if err := planSchema(branch, root, visited, seen, depth+1, fmt.Sprintf("%s.%s[%d]", path, key, index), fieldChild, bounds, plannedBounds, plan); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// A shared full/example plan may already carry these same bound clauses.
func planBoundsNode(schema map[string]any, root map[string]any, pointer uintptr, plannedBounds map[uintptr]bool, plan *[]pendingGuidance) {
	if plannedBounds[pointer] {
		return
	}
	if description, _ := schema["description"].(string); strings.Contains(description, "Bounds:") {
		plannedBounds[pointer] = true
		return
	}
	text := guidanceBoundsText(guidanceSource(schema, root))
	if text == "" {
		return
	}
	*plan = append(*plan, pendingGuidance{node: schema, segment: text})
	plannedBounds[pointer] = true
}

// planNode plans one node's teaching segment or refuses with the reason.
func planNode(schema map[string]any, root map[string]any, depth int, path string, field bool, pointer uintptr, plannedBounds map[uintptr]bool, plan *[]pendingGuidance) error {
	if description, _ := schema["description"].(string); strings.Contains(description, guidanceMarker) {
		return nil
	}
	source := guidanceSource(schema, root)
	if !guidanceTeaches(source) && !(field && guidanceCompositionTeaches(source)) {
		return nil
	}
	example, err := guidanceExample(source, root, depth)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// The example must satisfy the node's effective schema, references and
	// compositions included, before it can teach anything.
	if validateSchemaValue(example, schema, root, "$") != nil {
		return fmt.Errorf("%s: generated example does not satisfy its own schema", path)
	}
	encoded, err := json.Marshal(example)
	if err != nil {
		return fmt.Errorf("%s: generated example is not JSON: %w", path, err)
	}
	if len(encoded) > guidanceExampleMaxBytes {
		return fmt.Errorf("%s: generated example is %d bytes against the %d-byte example budget", path, len(encoded), guidanceExampleMaxBytes)
	}
	shape := guidanceShape(source, root, 0)
	if shape == "" {
		return fmt.Errorf("%s: schema constraints render no accepted shape", path)
	}
	// The rendered shape carries the node's own Bounds clauses whenever it
	// declares countable bounds, so a bounds pass over the same call must
	// not stack a second copy of them beside this segment.
	if strings.Contains(shape, "Bounds:") {
		plannedBounds[pointer] = true
	}
	segment := guidanceMarker + " " + shape + "; example: " + string(encoded)
	if len(segment) > guidanceSegmentMaxBytes {
		return fmt.Errorf("%s: guidance segment is %d bytes against the %d-byte budget", path, len(segment), guidanceSegmentMaxBytes)
	}
	*plan = append(*plan, pendingGuidance{node: schema, examples: []any{example}, segment: segment})
	return nil
}

// guidanceSource merges a reference chain with the node's own keywords, the
// nearer declaration winning, so a property declared through chained refs
// with sibling bounds generates against every hop.
func guidanceSource(schema map[string]any, root map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range schema {
		switch key {
		case "$ref", "description", "examples":
			continue
		}
		merged[key] = value
	}
	current := schema
	for depth := 0; depth < 8; depth++ {
		ref, ok := current["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/$defs/") {
			break
		}
		defs, _ := root["$defs"].(map[string]any)
		target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			break
		}
		for key, value := range target {
			switch key {
			case "$ref", "description", "examples":
				continue
			}
			if existing, exists := merged[key]; exists && guidanceStricterBound(key, value, existing) {
				merged[key] = value
			} else if !exists {
				merged[key] = value
			}
		}
		current = target
	}
	return merged
}

// Reference siblings intersect: a looser sibling never relaxes a referenced
// floor or ceiling. These are the numeric keywords this validator enforces.
func guidanceStricterBound(key string, candidate, present any) bool {
	left, leftOK := boundNumber(candidate)
	right, rightOK := boundNumber(present)
	if !leftOK || !rightOK {
		return false
	}
	switch key {
	case "minimum", "minLength", "x-minBytes", "minItems", "minProperties":
		return left > right
	case "maximum", "maxLength", "x-maxBytes", "maxItems", "maxProperties":
		return left < right
	}
	return false
}

// guidanceTeaches reports whether a node carries guidance a caller can act
// on: value constraints of its own, or an object shape whose required names
// it declares itself. A node that only names required fields of an enclosing
// object — the shape of oneOf branches and of forbidden and conditional
// frames — is not itself an authored field, so it may carry no guidance.
func guidanceTeaches(source map[string]any) bool {
	if guidanceHasValueConstraints(source) {
		return true
	}
	properties, _ := source["properties"].(map[string]any)
	if len(properties) > 0 {
		for _, name := range schemaFieldList(source["required"]) {
			if _, declared := properties[name].(map[string]any); !declared {
				return false
			}
		}
		return true
	}
	// Required-only and combinator-only containers name no authored field.
	return false
}

func guidanceCompositionTeaches(source map[string]any) bool {
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		branches, _ := source[keyword].([]any)
		for _, raw := range branches {
			if branch, ok := raw.(map[string]any); ok && (guidanceTeaches(branch) || guidanceCompositionTeaches(branch)) {
				return true
			}
		}
	}
	// Required-only and combinator-only nodes name fields of an enclosing
	// object or hold no authored field at all.
	return false
}

func guidanceHasValueConstraints(source map[string]any) bool {
	for _, keyword := range []string{
		"type", "const", "enum", "pattern",
		"minLength", "maxLength", "x-minBytes", "x-maxBytes",
		"minimum", "maximum", "minItems", "maxItems",
	} {
		if _, ok := source[keyword]; ok {
			return true
		}
	}
	return false
}

// guidanceExample builds one minimal example from the keywords the node
// declares: const and enum first, then one composition branch, then type and
// bounds. Nothing is looked up by field name. An ungeneratable constrained
// node is an error, never a silent gap.
func guidanceExample(schema map[string]any, root map[string]any, depth int) (any, error) {
	if depth > guidanceDepthMax {
		return nil, fmt.Errorf("schema nesting exceeds the %d-level guidance depth", guidanceDepthMax)
	}
	if constant, ok := schema["const"]; ok {
		return guidanceDeepCopy(constant)
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return guidanceDeepCopy(enum[0])
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		return guidanceCombinatorExample(schema, branches, root, depth)
	}
	if branches, ok := schema["anyOf"].([]any); ok {
		return guidanceCombinatorExample(schema, branches, root, depth)
	}
	if composed, ok := schema["allOf"].([]any); ok {
		return guidanceAllOfExample(schema, composed, root, depth)
	}
	switch kind := guidanceNodeType(schema); {
	case kind == "object" || (kind == "" && (schema["properties"] != nil || schema["required"] != nil)):
		return guidanceObjectExample(schema, root, depth)
	case kind == "array" || (kind == "" && (schema["items"] != nil || schema["minItems"] != nil)):
		return guidanceArrayExample(schema, root, depth)
	case kind == "string":
		return guidanceStringExample(schema)
	case kind == "integer" || kind == "number":
		return guidanceNumberExample(schema, kind)
	case kind == "boolean":
		return false, nil
	case kind == "null":
		return nil, nil
	}
	return nil, fmt.Errorf("schema declares no type or composition an example can be generated from")
}

func guidanceCombinatorExample(schema map[string]any, branches []any, root map[string]any, depth int) (any, error) {
	var firstErr error
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		example, err := guidanceExample(guidanceSource(branch, root), root, depth+1)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if validateSchemaValue(example, schema, root, "$") == nil {
			return example, nil
		}
	}
	if firstErr != nil {
		return nil, fmt.Errorf("no alternative yields an example: %w", firstErr)
	}
	return nil, fmt.Errorf("no alternative yields an example")
}

// guidanceAllOfExample merges the frames' declarations into one pseudo-schema
// so an example satisfies every frame at once, then proves it against the
// real composition. Frame property tables are copied, never aliased: the
// pseudo-schema is discarded and the real schema must stay untouched.
func guidanceAllOfExample(schema map[string]any, composed []any, root map[string]any, depth int) (any, error) {
	merged := map[string]any{}
	for key, value := range schema {
		if key == "allOf" {
			continue
		}
		merged[key] = value
	}
	for _, raw := range composed {
		frame, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		source := guidanceSource(frame, root)
		for key, value := range source {
			switch key {
			case "$ref", "description", "examples":
				continue
			case "required":
				merged[key] = guidanceUnionStrings(merged[key], value)
			case "properties", "patternProperties":
				combined := map[string]any{}
				if existing, ok := merged[key].(map[string]any); ok {
					for name, child := range existing {
						combined[name] = child
					}
				}
				additions, _ := value.(map[string]any)
				for name, child := range additions {
					combined[name] = child
				}
				merged[key] = combined
			default:
				if existing, exists := merged[key]; exists && guidanceStricterBound(key, value, existing) {
					merged[key] = value
				} else if !exists {
					merged[key] = value
				}
			}
		}
	}
	example, err := guidanceExample(merged, root, depth+1)
	if err != nil {
		return nil, err
	}
	if validateSchemaValue(example, schema, root, "$") != nil {
		return nil, fmt.Errorf("no example satisfies every composed frame")
	}
	return example, nil
}

func guidanceUnionStrings(present any, additions any) any {
	merged := schemaFieldList(present)
	seen := map[string]bool{}
	for _, field := range merged {
		seen[field] = true
	}
	for _, field := range schemaFieldList(additions) {
		if !seen[field] {
			seen[field] = true
			merged = append(merged, field)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	raw := make([]any, 0, len(merged))
	for _, field := range merged {
		raw = append(raw, field)
	}
	return raw
}

// guidanceObjectExample builds the minimal object: exactly the required
// fields, and nothing optional. A required name must resolve to a declared
// schema, the additionalProperties schema, or a pattern property; otherwise
// the shape is not teachable and generation fails explicitly.
func guidanceObjectExample(schema map[string]any, root map[string]any, depth int) (any, error) {
	object := map[string]any{}
	properties, _ := schema["properties"].(map[string]any)
	patterns, _ := schema["patternProperties"].(map[string]any)
	additional, hasAdditional := schema["additionalProperties"].(map[string]any)
	for _, name := range schemaFieldList(schema["required"]) {
		if _, exists := object[name]; exists {
			continue
		}
		child, ok := properties[name].(map[string]any)
		if !ok && hasAdditional {
			child, ok = additional, true
		}
		if !ok {
			for _, pattern := range sortedKeys(patterns) {
				if candidate, isSchema := patterns[pattern].(map[string]any); isSchema {
					child, ok = candidate, true
					break
				}
			}
		}
		if !ok {
			return nil, fmt.Errorf("required field %q has no declared schema to generate from", name)
		}
		value, err := guidanceExample(guidanceSource(child, root), root, depth+1)
		if err != nil {
			return nil, fmt.Errorf("required field %q: %w", name, err)
		}
		object[name] = value
	}
	return object, nil
}

// guidanceArrayExample builds the minimal array: exactly minItems entries,
// including an empty array when nothing is required.
func guidanceArrayExample(schema map[string]any, root map[string]any, depth int) (any, error) {
	count := 0
	if minimum, ok := boundNumber(schema["minItems"]); ok {
		count = int(minimum)
	}
	if maximum, ok := boundNumber(schema["maxItems"]); ok && count > int(maximum) {
		count = int(maximum)
	}
	items, hasItems := schema["items"].(map[string]any)
	list := make([]any, 0, count)
	for i := 0; i < count; i++ {
		if !hasItems {
			return nil, fmt.Errorf("array requires %d items but declares no item schema", count)
		}
		value, err := guidanceExample(guidanceSource(items, root), root, depth+1)
		if err != nil {
			return nil, err
		}
		list = append(list, value)
	}
	return list, nil
}

func guidanceStringExample(schema map[string]any) (any, error) {
	pattern, hasPattern := schema["pattern"].(string)
	candidates := []string{}
	if hasPattern {
		for _, synthesized := range guidancePatternCandidates(pattern) {
			// A synthesized literal grows only under an unanchored pattern.
			if fitted := guidanceFitBounds(synthesized, schema, !strings.HasPrefix(pattern, "^")); fitted != "" {
				candidates = append(candidates, fitted)
			}
		}
	} else if schema["format"] == "date-time" {
		candidates = append(candidates, "2026-01-01T00:00:00Z")
	}
	if fitted := guidanceFitBounds("a", schema, true); fitted != "" {
		candidates = append(candidates, fitted)
	}
	for _, candidate := range candidates {
		if validateStringKeywords(candidate, schema, "$") == nil {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("no string satisfies pattern %q together with its declared bounds", pattern)
}

// guidanceFitBounds grows a candidate with a plain filler until it meets the
// declared floors and truncates it at the declared ceilings. Code-point and
// UTF-8 byte bounds are both honored, mirroring the two units the validator
// enforces. Growth is refused where it would leave the pattern.
func guidanceFitBounds(candidate string, schema map[string]any, allowGrowth bool) string {
	floors := func(value string) bool {
		if minimum, ok := boundNumber(schema["minLength"]); ok && utf8.RuneCountInString(value) < int(minimum) {
			return false
		}
		if minimum, ok := boundNumber(schema["x-minBytes"]); ok && len(value) < int(minimum) {
			return false
		}
		return true
	}
	if allowGrowth {
		for !floors(candidate) && len(candidate) < 4096 {
			candidate += "v"
		}
	}
	if !floors(candidate) {
		return ""
	}
	if maximum, ok := boundNumber(schema["maxLength"]); ok && utf8.RuneCountInString(candidate) > int(maximum) {
		candidate = string([]rune(candidate)[:int(maximum)])
	}
	if maximum, ok := boundNumber(schema["x-maxBytes"]); ok && len(candidate) > int(maximum) {
		candidate = candidate[:int(maximum)]
		for len(candidate) > 0 && !utf8.ValidString(candidate) {
			candidate = candidate[:len(candidate)-1]
		}
	}
	if !floors(candidate) {
		return ""
	}
	return candidate
}

func guidanceNumberExample(schema map[string]any, kind string) (any, error) {
	value := 1.0
	if minimum, ok := boundNumber(schema["minimum"]); ok {
		value = minimum
	}
	if maximum, ok := boundNumber(schema["maximum"]); ok {
		if value > maximum {
			value = maximum
		}
		if minimum, ok := boundNumber(schema["minimum"]); ok && minimum > maximum {
			return nil, fmt.Errorf("declared minimum %v exceeds declared maximum %v", minimum, maximum)
		}
	}
	if kind == "integer" {
		value = float64(int64(value))
	}
	return json.Number(strconv.FormatFloat(value, 'f', -1, 64)), nil
}

func guidanceDeepCopy(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("declared value is not JSON: %w", err)
	}
	var copied any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&copied); err != nil {
		return nil, fmt.Errorf("declared value is not JSON: %w", err)
	}
	return copied, nil
}

func boundNumber(raw any) (float64, bool) {
	number, ok := raw.(json.Number)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// guidanceNodeType returns the node's first non-null declared type, so a
// ["integer","null"] declaration generates its present form.
func guidanceNodeType(schema map[string]any) string {
	kind, ok := schema["type"].(string)
	if ok {
		return kind
	}
	list, ok := schema["type"].([]any)
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

// guidancePatternCandidates synthesizes minimal literals from a regular
// expression's syntax tree: literals and classes become characters, minimum
// repetitions expand in full up to a bounded repeat, and optional constructs
// (quest, star, an expandable repeat) are emitted collapsed and then with one
// to three extra instances, so a minLength floor beside the pattern can be
// met by the pattern's own structure. Class characters prefer ordinary
// letters. The compiled pattern has the final word: only candidates that
// match it are returned.
func guidancePatternCandidates(pattern string) []string {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	candidates := []string{}
	for _, extra := range []int{0, 1, 2, 3} {
		var builder strings.Builder
		if !writePatternLiteral(parsed, &builder, 0, 'a', extra) {
			continue
		}
		candidate := builder.String()
		if expression.MatchString(candidate) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func writePatternLiteral(re *syntax.Regexp, builder *strings.Builder, depth int, preferred rune, extra int) bool {
	if depth > 32 || re == nil {
		return false
	}
	switch re.Op {
	case syntax.OpLiteral:
		builder.WriteString(string(re.Rune))
	case syntax.OpCharClass:
		if classAdmits(re.Rune, preferred) {
			builder.WriteRune(preferred)
		} else {
			builder.WriteRune(guidanceClassRune(re.Rune))
		}
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		builder.WriteRune(preferred)
	case syntax.OpCapture:
		if len(re.Sub) == 0 {
			return false
		}
		return writePatternLiteral(re.Sub[0], builder, depth+1, preferred, extra)
	case syntax.OpPlus:
		// Minimum count one, expandable to meet a declared floor.
		if len(re.Sub) == 0 {
			return false
		}
		for i := 0; i < 1+extra; i++ {
			if !writePatternLiteral(re.Sub[0], builder, depth+1, preferred, extra) {
				return false
			}
		}
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !writePatternLiteral(sub, builder, depth+1, preferred, extra) {
				return false
			}
		}
	case syntax.OpAlternate:
		if len(re.Sub) == 0 {
			return false
		}
		return writePatternLiteral(re.Sub[0], builder, depth+1, preferred, extra)
	case syntax.OpQuest:
		// Minimum count zero: emit nothing unless the caller asks for an
		// expanded variant, then exactly one instance.
		if extra > 0 && len(re.Sub) > 0 {
			return writePatternLiteral(re.Sub[0], builder, depth+1, preferred, extra-1)
		}
	case syntax.OpStar:
		for i := 0; i < extra; i++ {
			if len(re.Sub) == 0 || !writePatternLiteral(re.Sub[0], builder, depth+1, preferred, extra) {
				return false
			}
		}
	case syntax.OpRepeat:
		minimum := re.Min
		if minimum > guidancePatternRepeatMax {
			minimum = guidancePatternRepeatMax
		}
		if re.Max == -1 || re.Max > guidancePatternRepeatMax {
			// extra extends an unbounded repeat only; a bounded one keeps
			// its declared maximum.
			if re.Max != -1 {
				extra = 0
			}
		} else if minimum+extra > re.Max {
			extra = re.Max - minimum
			if extra < 0 {
				extra = 0
			}
		}
		for i := 0; i < minimum+extra; i++ {
			if len(re.Sub) == 0 || !writePatternLiteral(re.Sub[0], builder, depth+1, preferred, extra) {
				return false
			}
		}
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary, syntax.OpEmptyMatch:
		// Anchors and empty matches emit nothing.
	case syntax.OpNoMatch:
		return false
	default:
		return false
	}
	return true
}

func classAdmits(ranges []rune, candidate rune) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if ranges[i] <= candidate && candidate <= ranges[i+1] {
			return true
		}
	}
	return false
}

// guidanceClassRune picks one printable rune a character class admits,
// avoiding control characters, so the candidate reads as a value rather than
// as binary noise.
func guidanceClassRune(ranges []rune) rune {
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		candidate := lo
		if candidate < 0x21 {
			candidate = 0x21
		}
		if candidate > hi {
			if hi < 0x21 {
				continue
			}
			candidate = hi
		}
		if candidate > utf8.MaxRune {
			continue
		}
		return candidate
	}
	return 'a'
}

// guidanceShape renders the accepted shape compactly from the same keywords
// the validator enforces. Ranges are rendered as bounds, never as chopped
// text; nesting is bounded by depth with an explicit structural elision
// marker rather than a cut.
func guidanceShape(schema map[string]any, root map[string]any, depth int) string {
	if depth > guidanceShapeDepthMax {
		return "…"
	}
	source := guidanceSource(schema, root)
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if branches, ok := source[keyword].([]any); ok {
			shapes := []string{}
			for _, raw := range branches {
				if branch, ok := raw.(map[string]any); ok {
					shapes = append(shapes, guidanceShape(branch, root, depth+1))
				}
			}
			return keyword + " (" + strings.Join(shapes, "; ") + ")"
		}
	}
	if constant, ok := source["const"]; ok {
		if encoded, err := json.Marshal(constant); err == nil {
			return "exactly " + string(encoded)
		}
	}
	if enum, ok := source["enum"].([]any); ok && len(enum) > 0 {
		values := make([]string, 0, len(enum))
		for _, entry := range enum {
			if encoded, err := json.Marshal(entry); err == nil {
				values = append(values, string(encoded))
			}
		}
		return "one of " + strings.Join(values, ", ")
	}
	kind := guidanceNodeType(source)
	var head string
	switch kind {
	case "object":
		head = "object"
	case "array":
		head = "array"
		if items, ok := source["items"].(map[string]any); ok {
			if inner := guidanceShape(items, root, depth+1); inner != "" {
				head += " of " + inner
			}
		}
	case "string":
		if source["format"] == "date-time" {
			head = "RFC3339 timestamp"
		} else {
			head = "string"
		}
		if pattern, ok := source["pattern"].(string); ok {
			head += " matching " + pattern
		}
	case "integer", "number", "boolean", "null":
		head = kind
	default:
		head = "value"
	}
	if depth > 0 {
		return head
	}
	bounds := guidanceBoundsText(source)
	if bounds == "" {
		return head
	}
	return head + "; " + bounds
}

func guidanceBoundsText(source map[string]any) string {
	bounds := guidanceRangeText(source, "minLength", "maxLength", "code points")
	if text := guidanceRangeText(source, "x-minBytes", "x-maxBytes", "UTF-8 bytes"); text != "" {
		bounds = joinRange(bounds, text)
	}
	if text := guidanceRangeText(source, "minItems", "maxItems", "items"); text != "" {
		bounds = joinRange(bounds, text)
	}
	if text := guidanceRangeText(source, "minProperties", "maxProperties", "properties"); text != "" {
		bounds = joinRange(bounds, text)
	}
	if text := guidanceRangeText(source, "minimum", "maximum", "value"); text != "" {
		bounds = joinRange(bounds, text)
	}
	if bounds == "" {
		return ""
	}
	return "Bounds: " + bounds
}

func joinRange(present, addition string) string {
	if present == "" {
		return addition
	}
	return present + "; " + addition
}

// Every declared endpoint is inclusive, including a zero or negative floor.
func guidanceRangeText(source map[string]any, minimumKey string, maximumKey string, unit string) string {
	minimum, hasMinimum := source[minimumKey]
	maximum, hasMaximum := source[maximumKey]
	switch {
	case hasMinimum && hasMaximum:
		return fmt.Sprintf("%v..%v %s inclusive", minimum, maximum, unit)
	case hasMinimum:
		return fmt.Sprintf(">=%v %s inclusive", minimum, unit)
	case hasMaximum:
		return fmt.Sprintf("<=%v %s inclusive", maximum, unit)
	}
	return ""
}

// guidanceNote returns the teaching segment an annotated schema carries, so a
// refusal can state the accepted shape and one minimal example without any
// field-specific message table. An unannotated schema returns "".
func guidanceNote(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	description, _ := schema["description"].(string)
	index := strings.Index(description, guidanceMarker)
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(description[index:])
}

// withGuidance appends the schema's teaching segment to a leaf refusal.
// Unannotated schemas keep today's exact message.
func withGuidance(err error, schema map[string]any) error {
	if err == nil {
		return nil
	}
	if note := guidanceNote(schema); note != "" {
		return &guidedDiagnostic{message: err.Error() + "; " + note, selected: true}
	}
	return err
}

type guidedDiagnostic struct {
	message  string
	selected bool
}

func (d *guidedDiagnostic) Error() string { return d.message }

// HasGuidance reports whether the validator supplied canonical teaching, so a
// coarse outer-shape check can defer to it without changing admission.
func HasGuidance(err error) bool {
	var diagnostic *guidedDiagnostic
	return errors.As(err, &diagnostic)
}

// HasSelectedGuidance excludes genuinely unresolved alternatives. A coarse
// operation-level failure must not be replaced by an arbitrary variant's hint.
func HasSelectedGuidance(err error) bool {
	var diagnostic *guidedDiagnostic
	return errors.As(err, &diagnostic) && diagnostic.selected
}

func unknownPropertyError(path, key string, schema map[string]any) error {
	message := fmt.Sprintf("unknown property %s.%s", path, key)
	if guidanceNote(schema) != "" && len(key) > 128 {
		// An arbitrary member name can exceed the entire envelope. Identify it
		// exactly by digest and length instead of cutting its path or teaching.
		message = fmt.Sprintf("unknown property at %s: member name sha256:%x (%d UTF-8 bytes)", path, sha256.Sum256([]byte(key)), len(key))
	}
	return withGuidance(fmt.Errorf("%s", message), schema)
}

// joinSchemaFailures budgets whole errors, never substrings. A long guided
// refusal keeps its first complete field/constraint/shape/example and states
// how many other errors remain. Unguided structural messages stay unchanged.
func joinSchemaFailures(prefix string, failures []error, suffix ...string) error {
	parts := errorStrings(failures)
	message := prefix + strings.Join(parts, "; ") + strings.Join(suffix, "")
	guided := false
	for _, failure := range failures {
		guided = guided || HasGuidance(failure)
	}
	if !guided {
		return fmt.Errorf("%s", message)
	}
	if len(message) > 1000 {
		for _, failure := range failures {
			if HasGuidance(failure) {
				message = prefix + failure.Error() + fmt.Sprintf("; %d other field or alternative error(s)", len(parts)-1)
				break
			}
		}
	}
	return &guidedDiagnostic{message: message, selected: prefix == ""}
}
