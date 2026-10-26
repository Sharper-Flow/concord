// Package schemawire is test support for input-budget derivations: it
// computes the maximum serialized UTF-8 JSON size of one generated-schema
// node so a capacity test can prove a limit against declared maxima. It has
// no production caller and validates nothing.
//
// Wire rules. maxLength bounds code points, not serialized bytes: every
// permitted code point takes six encoded bytes — the encoder's own worst
// cost per scalar (control escapes, Go HTML escapes of < > &, and raw
// astral UTF-8 all stay at or under six) — and the two quotes are counted.
// No pattern analysis cheapens that figure: an unanchored pattern admits
// scalars it never names, an ASCII alphabet can still escape, and a class
// escape can cost more than one byte. Integers take the widest of their
// declared minimum and maximum, or the signed int64 worst case when the
// schema declares no bound. A format=date-time member needs a caller-pinned
// writer-derived bound; an unmapped one is a source finding, never an
// invented default. A pattern-only finding reference derives from its owner
// condition: the event sequence is a non-negative int64 (nineteen digits)
// and at most sixteen declared findings per anchor keep the ordinal at two
// digits; the quotes are counted. Excluded properties drop from the walk
// entirely — name, colon, and delimiters — so a caller can inject one
// precomputed term (the context aggregate minus its own two braces, plus
// the one comma the dropped members surrender) without double-counting
// names.
package schemawire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Options carries the derivation inputs one capacity test owns.
type Options struct {
	// Defs is the generated contract's $defs map.
	Defs map[string]any
	// RefBounds pins a $defs name to a caller-derived maximum, for defs the
	// caller composes once (the worker packet: its declared parts plus the
	// context aggregate).
	RefBounds map[string]int
	// PathBounds pins writer-owned bounds by schema path (the serialized
	// acceptance oracle at its writer's 32 KiB; writer-stamped date-times at
	// their writer's RFC3339Nano UTC serialization, quotes included).
	PathBounds map[string]int
	// Excludes drops named schema paths from the walk entirely.
	Excludes map[string]bool
	// FindingRefMax is the serialized wire maximum of one pattern-only
	// "finding:<seq>:<ordinal>" reference, quotes included, derived by the
	// caller from the event-sequence and per-anchor ordinal owner
	// conditions.
	FindingRefMax int
}

// Max returns the maximum serialized UTF-8 size of the schema node at path.
// An error names the exact path of any member the derivation cannot bound;
// the caller must inventory that member's owner before the budget derives.
func Max(node any, path string, opt Options) (int, error) {
	return walk(node, path, opt, map[string]bool{}, 0)
}

func walk(node any, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	if depth > 40 {
		return 0, fmt.Errorf("schema walk exceeded the depth bound at %s", path)
	}
	if bound, ok := opt.PathBounds[path]; ok {
		return bound, nil
	}
	if opt.Excludes[path] {
		return 0, nil
	}
	object, ok := node.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("schema node at %s is not an object", path)
	}
	if ref, ok := object["$ref"].(string); ok {
		return walkReference(ref, path, opt, stack, depth)
	}
	if constant, ok := object["const"]; ok {
		return len(JSONString(constant)), nil
	}
	if enum, ok := object["enum"].([]any); ok {
		widest := 0
		for _, entry := range enum {
			if size := len(JSONString(entry)); size > widest {
				widest = size
			}
		}
		return widest, nil
	}
	if bound, ok, err := numberMember(object["maxLength"], path, "maxLength"); err != nil {
		return 0, err
	} else if ok {
		return JSONStringWireBytes(bound), nil
	}
	return walkKind(object, path, opt, stack, depth)
}

func walkReference(ref, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	name := ref[strings.LastIndex(ref, "/")+1:]
	if bound, ok := opt.RefBounds[name]; ok {
		return bound, nil
	}
	if stack[name] {
		return 0, fmt.Errorf("recursive schema reference %s at %s", name, path)
	}
	resolved, ok := opt.Defs[name]
	if !ok {
		return 0, fmt.Errorf("schema reference %s at %s resolves no definition", name, path)
	}
	next := make(map[string]bool, len(stack)+1)
	for seen := range stack {
		next[seen] = true
	}
	next[name] = true
	return walk(resolved, path+"/"+name, opt, next, depth+1)
}

func walkKind(object map[string]any, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	kind, _ := object["type"].(string)
	if types, ok := object["type"].([]any); ok {
		for _, entry := range types {
			if name, ok := entry.(string); ok && len(name) > len(kind) {
				kind = name
			}
		}
	}
	switch kind {
	case "string":
		if strings.Contains(toString(object["pattern"]), "finding:") {
			return opt.FindingRefMax, nil
		}
		if object["format"] == "date-time" {
			return 0, fmt.Errorf("source finding: date-time member at %s has no writer-derived bound; inventory its writer before the budget derives", path)
		}
		return 0, fmt.Errorf("source finding: unbounded string member at %s; inventory its owner before the budget derives", path)
	case "array":
		return walkArray(object, path, opt, stack, depth)
	case "object":
		return walkProperties(object, path, opt, stack, depth)
	case "integer":
		return integerWidth(object["minimum"], object["maximum"]), nil
	case "number":
		return 324, nil // the widest JSON number a decoder must stand
	case "boolean":
		return 5, nil // "false"
	case "null":
		return 4, nil
	case "":
		return walkCombinator(object, path, opt, stack, depth)
	}
	return 0, fmt.Errorf("unrecognized schema node at %s", path)
}

func walkArray(object map[string]any, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	items, ok, err := numberMember(object["maxItems"], path, "maxItems")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("source finding: unbounded array member at %s; inventory its owner before the budget derives", path)
	}
	if items == 0 {
		return 2, nil
	}
	item, err := walk(object["items"], path+"/items", opt, stack, depth+1)
	if err != nil {
		return 0, err
	}
	return 2 + items*item + items - 1, nil
}

func walkProperties(object map[string]any, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	properties, _ := object["properties"].(map[string]any)
	total := 2
	first := true
	for name, member := range properties {
		size, err := walk(member, path+"/"+name, opt, stack, depth+1)
		if err != nil {
			return 0, err
		}
		if opt.Excludes[path+"/"+name] {
			continue
		}
		if !first {
			total++
		}
		first = false
		total += len(JSONString(name)) + 1 + size
	}
	return total, nil
}

func walkCombinator(object map[string]any, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	// if/then branches constrain the same properties rather than adding
	// content, so the widest allOf branch bounds the object too.
	for _, combinator := range []string{"oneOf", "anyOf", "allOf"} {
		if branches, ok := object[combinator].([]any); ok {
			return walkWidestBranch(branches, path+"/"+combinator, opt, stack, depth)
		}
	}
	return 0, fmt.Errorf("unrecognized schema node at %s", path)
}

func walkWidestBranch(branches []any, path string, opt Options, stack map[string]bool, depth int) (int, error) {
	widest := 0
	for _, branch := range branches {
		size, err := walk(branch, path, opt, stack, depth+1)
		if err != nil {
			return 0, err
		}
		if size > widest {
			widest = size
		}
	}
	return widest, nil
}

func numberMember(value any, path, name string) (int, bool, error) {
	switch number := value.(type) {
	case float64:
		return int(number), true, nil
	case int:
		return number, true, nil
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("non-integer %s at %s: %w", name, path, err)
		}
		return int(parsed), true, nil
	}
	return 0, false, nil
}

// integerWidth takes the widest declared bound; an integer the schema leaves
// unbounded takes the signed int64 worst case.
func integerWidth(minimum, maximum any) int {
	widest := len("-9223372036854775808")
	for _, bound := range []any{minimum, maximum} {
		switch value := bound.(type) {
		case float64:
			if digits := len(fmt.Sprintf("%d", int64(value))); digits > widest {
				widest = digits
			}
		case int:
			if digits := len(fmt.Sprintf("%d", value)); digits > widest {
				widest = digits
			}
		case json.Number:
			if digits := len(value.String()); digits > widest {
				widest = digits
			}
		}
	}
	return widest
}

func toString(value any) string {
	text, _ := value.(string)
	return text
}

// JSONString is the compact JSON serialization of one schema literal.
func JSONString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// JSONStringWireBytes is the serialized wire width of one string constrained
// to maxLength code points: the two quotes plus six encoded bytes per code
// point, the encoder's own worst cost per scalar. No pattern, class, or
// alphabet cheapens it.
func JSONStringWireBytes(maxLength int) int {
	return 2 + maxLength*6
}

// NextPowerOfTwo rounds one composed declared maximum up to the next power
// of two, the rounding every derived input budget takes.
func NextPowerOfTwo(value int) int {
	power := 1
	for power < value {
		power *= 2
	}
	return power
}
