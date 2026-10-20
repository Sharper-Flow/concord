package payloadschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// encodeDocument renders one parsed document canonically, so an atomicity or
// idempotence claim compares content rather than map identity.
func encodeDocument(t *testing.T, root map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("document is not JSON: %v", err)
	}
	return string(encoded)
}

// requireNumberLiteral guards the premise every bounds test rests on: the
// decoded schema carries literal json.Number values, never float64, so the
// rendered clause is exact for any magnitude the decoder read.
func requireNumberLiteral(t *testing.T, node map[string]any, key string) {
	t.Helper()
	if _, ok := node[key].(json.Number); !ok {
		t.Fatalf("%s carries %T, want a literal json.Number from the decoder", key, node[key])
	}
}

// Bounds-only planning states each node's declared clauses exactly, with no
// example and no example obligation.
func TestBoundsOnlyStatesClausesWithoutExamples(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {"thing": {"type": "string", "minLength": 2, "maxLength": 4}}
	}`)
	if err := AnnotateGuidance(root, nil, "thing"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	thing := guidanceNodeAtPath(t, root, "$defs", "thing")
	requireNumberLiteral(t, thing, "minLength")
	if description, _ := thing["description"].(string); description != "Bounds: 2..4 code points inclusive" {
		t.Fatalf("thing description = %q, want exactly its own clause", description)
	}
	if _, carries := thing["examples"]; carries {
		t.Fatal("a bounds-only annotation wrote an example")
	}
	if strings.Contains(thing["description"].(string), guidanceMarker) {
		t.Fatal("a bounds-only annotation claimed an accepted shape")
	}
}

func TestFullGuidanceRetainsNullExamples(t *testing.T) {
	root := decodeSchema(t, `{"$defs":{"typed":{"type":"null"},"constant":{"const":null},"enumerated":{"enum":[null]}}}`)
	names := []string{"typed", "constant", "enumerated"}
	if err := AnnotateGuidance(root, names); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		node := guidanceNodeAtPath(t, root, "$defs", name)
		examples, ok := node["examples"].([]any)
		if !ok || len(examples) != 1 || examples[0] != nil {
			t.Fatalf("%s examples = %#v, want one null example", name, node["examples"])
		}
	}
}

// Every range role renders with its unit and inclusive endpoints, a declared
// zero floor is stated, negative endpoints survive, and an exact large
// integer keeps every digit the decoder read.
func TestBoundsRangeGrammar(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"pair": {"type": "integer", "minimum": -100, "maximum": 100},
			"floorZero": {"type": "integer", "minimum": 0},
			"zeroFloor": {"type": "string", "minLength": 0, "maxLength": 64},
			"ceiling": {"type": "array", "maxItems": 8},
			"count": {"type": "object", "minProperties": 1, "maxProperties": 16},
			"big": {"type": "integer", "minimum": 123456789012345678}
		}
	}`)
	if err := AnnotateGuidance(root, nil, "pair", "floorZero", "zeroFloor", "ceiling", "count", "big"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	defs := root["$defs"].(map[string]any)
	want := map[string]string{
		"pair":      "Bounds: -100..100 value inclusive",
		"floorZero": "Bounds: >=0 value inclusive",
		"zeroFloor": "Bounds: 0..64 code points inclusive",
		"ceiling":   "Bounds: <=8 items inclusive",
		"count":     "Bounds: 1..16 properties inclusive",
		"big":       "Bounds: >=123456789012345678 value inclusive",
	}
	for name, clause := range want {
		node := defs[name].(map[string]any)
		description, _ := node["description"].(string)
		if description != clause {
			t.Fatalf("%s description = %q, want %q", name, description, clause)
		}
	}
	if _, carries := defs["big"].(map[string]any)["description"]; !carries {
		t.Fatal("the large-integer floor was not stated")
	}
}

// Bound families on one node join under one Bounds: prefix, separated by
// "; ", each clause in its own unit.
func TestBoundsFamiliesJoinUnderOnePrefix(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {"name": {"type": "string", "minLength": 1, "maxLength": 128, "x-minBytes": 2, "x-maxBytes": 512}}
	}`)
	if err := AnnotateGuidance(root, nil, "name"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	name := guidanceNodeAtPath(t, root, "$defs", "name")
	description, _ := name["description"].(string)
	if want := "Bounds: 1..128 code points inclusive; 2..512 UTF-8 bytes inclusive"; description != want {
		t.Fatalf("name description = %q, want %q", description, want)
	}
	if strings.Count(description, "Bounds:") != 1 {
		t.Fatalf("families stacked separate Bounds prefixes: %q", description)
	}
}

// A node with no countable bounds is planned as nothing: no description
// appears and the call succeeds.
func TestBoundsWithoutCountableBoundsPlansNothing(t *testing.T) {
	root := decodeSchema(t, `{"$defs": {"plain": {"type": "string"}}}`)
	if err := AnnotateGuidance(root, nil, "plain"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	plain := guidanceNodeAtPath(t, root, "$defs", "plain")
	if _, carries := plain["description"]; carries {
		t.Fatalf("an unbounded node gained a description: %v", plain["description"])
	}
}

// The bounds walk follows #/$defs/ references: the target states its own
// clauses, and a sibling bound intersects the chain stricter-side first, so
// the looser sibling never relaxes the taught range.
func TestBoundsFollowsReferencesAndIntersectsSiblings(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"inner": {"type": "string", "minLength": 2, "maxLength": 3},
			"outer": {"$ref": "#/$defs/inner", "minLength": 1}
		}
	}`)
	if err := AnnotateGuidance(root, nil, "outer"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	for _, name := range []string{"inner", "outer"} {
		node := guidanceNodeAtPath(t, root, "$defs", name)
		description, _ := node["description"].(string)
		if description != "Bounds: 2..3 code points inclusive" {
			t.Fatalf("%s description = %q, want the intersected chain range", name, description)
		}
	}
}

// A reference cycle terminates and each node on the cycle states its own
// clauses exactly once.
func TestBoundsSurvivesReferenceCycles(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"a": {"type": "object", "maxProperties": 4, "properties": {"b": {"$ref": "#/$defs/b"}}},
			"b": {"type": "object", "maxProperties": 6, "properties": {"a": {"$ref": "#/$defs/a"}}}
		}
	}`)
	if err := AnnotateGuidance(root, nil, "a"); err != nil {
		t.Fatalf("cyclic bounds annotation refused: %v", err)
	}
	for name, clause := range map[string]string{"a": "Bounds: <=4 properties inclusive", "b": "Bounds: <=6 properties inclusive"} {
		node := guidanceNodeAtPath(t, root, "$defs", name)
		description, _ := node["description"].(string)
		if description != clause {
			t.Fatalf("%s description = %q, want %q", name, description, clause)
		}
	}
}

// Nested value positions state their own clauses, and no clause migrates
// between parent and child: an array's item floor is the item's note, the
// array's cardinality is the array's note, and branch labels stay
// independent of each other.
func TestBoundsStayOnTheirOwnNodes(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"list": {"type": "array", "minItems": 2, "items": {"type": "string", "minLength": 3}},
			"choice": {"oneOf": [
				{"type": "string", "minLength": 2, "maxLength": 4},
				{"type": "string", "minLength": 5, "maxLength": 8}
			]},
			"table": {"type": "object", "propertyNames": {"minLength": 2}, "maxProperties": 4},
			"bag": {"type": "array", "items": {"type": "string"}, "contains": {"minLength": 3}, "maxItems": 5},
			"pair": {"type": "array", "prefixItems": [{"type": "string", "maxLength": 2}, {"type": "integer"}], "minItems": 2, "maxItems": 2}
		}
	}`)
	if err := AnnotateGuidance(root, nil, "list", "choice", "table", "bag", "pair"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	list := guidanceNodeAtPath(t, root, "$defs", "list")
	if description, _ := list["description"].(string); description != "Bounds: >=2 items inclusive" {
		t.Fatalf("list description = %q, want only its own cardinality clause", description)
	}
	item := guidanceNodeAtPath(t, root, "$defs", "list", "items")
	if description, _ := item["description"].(string); description != "Bounds: >=3 code points inclusive" {
		t.Fatalf("items description = %q, want only its own length clause", description)
	}
	choice := guidanceNodeAtPath(t, root, "$defs", "choice")
	if _, carries := choice["description"]; carries {
		t.Fatalf("a pure combinator container with no own bounds gained a description: %v", choice["description"])
	}
	first := guidanceNodeAtPath(t, root, "$defs", "choice", "oneOf", 0)
	if description, _ := first["description"].(string); description != "Bounds: 2..4 code points inclusive" {
		t.Fatalf("branch 0 description = %q, want exactly its own clause", description)
	}
	second := guidanceNodeAtPath(t, root, "$defs", "choice", "oneOf", 1)
	if description, _ := second["description"].(string); description != "Bounds: 5..8 code points inclusive" {
		t.Fatalf("branch 1 description = %q, want exactly its own clause", description)
	}
	if description := first["description"].(string); strings.Contains(description, "5..8") {
		t.Fatalf("branch 0 carries branch 1's clause: %q", description)
	}
	names := guidanceNodeAtPath(t, root, "$defs", "table", "propertyNames")
	if description, _ := names["description"].(string); description != "Bounds: >=2 code points inclusive" {
		t.Fatalf("propertyNames description = %q, want its own member-name clause", description)
	}
	table := guidanceNodeAtPath(t, root, "$defs", "table")
	if description := table["description"].(string); strings.Contains(description, "code points") {
		t.Fatalf("the object carries its member-name floor: %q", description)
	} else if description != "Bounds: <=4 properties inclusive" {
		t.Fatalf("table description = %q, want only its own count clause", description)
	}
	has := guidanceNodeAtPath(t, root, "$defs", "bag", "contains")
	if description, _ := has["description"].(string); description != "Bounds: >=3 code points inclusive" {
		t.Fatalf("contains description = %q, want its own clause", description)
	}
	entry := guidanceNodeAtPath(t, root, "$defs", "pair", "prefixItems", 0)
	if description, _ := entry["description"].(string); description != "Bounds: <=2 code points inclusive" {
		t.Fatalf("prefixItems[0] description = %q, want its own clause", description)
	}
	latter := guidanceNodeAtPath(t, root, "$defs", "pair", "prefixItems", 1)
	if _, carries := latter["description"]; carries {
		t.Fatalf("an unbounded positional entry gained a description: %v", latter["description"])
	}
}

// Overlapping full names and boundNames share one plan: the full teaching
// stands once, its clause set appears once, and no second segment stacks
// beside it — the parent keeps its cardinality clause and the child keeps
// its own, with exactly one example each.
func TestOverlappingFullAndBoundNamesPlanOnce(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {"list": {"type": "array", "minItems": 2, "items": {"type": "string", "minLength": 3}}}
	}`)
	if err := AnnotateGuidance(root, []string{"list"}, "list"); err != nil {
		t.Fatalf("overlapping annotation refused: %v", err)
	}
	list := guidanceNodeAtPath(t, root, "$defs", "list")
	description, _ := list["description"].(string)
	if strings.Count(description, "Bounds:") != 1 {
		t.Fatalf("overlapping inputs stacked bound clauses: %q", description)
	}
	if !strings.Contains(description, "Bounds: >=2 items inclusive") || !strings.Contains(description, guidanceMarker) {
		t.Fatalf("list description = %q, want the full teaching with its own clause", description)
	}
	if strings.Contains(description, "code points") {
		t.Fatalf("list carries its item's length clause: %q", description)
	}
	examples, ok := list["examples"].([]any)
	if !ok || len(examples) != 1 {
		t.Fatalf("list carries %v, want one example", list["examples"])
	}
	item := guidanceNodeAtPath(t, root, "$defs", "list", "items")
	itemDescription, _ := item["description"].(string)
	if strings.Count(itemDescription, "Bounds:") != 1 || !strings.Contains(itemDescription, "Bounds: >=3 code points inclusive") {
		t.Fatalf("items description = %q, want its own clause exactly once", itemDescription)
	}
	if strings.Contains(itemDescription, "items inclusive") {
		t.Fatalf("items carries the array's cardinality clause: %q", itemDescription)
	}
}

// Repeated bounds-only annotation of the same document adds nothing: the
// second call is byte-for-byte inert.
func TestBoundsAnnotationIsIdempotent(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"list": {"type": "array", "minItems": 2, "items": {"type": "string", "minLength": 3}},
			"choice": {"oneOf": [{"type": "string", "maxLength": 4}, {"type": "integer", "minimum": 0}]}
		}
	}`)
	if err := AnnotateGuidance(root, nil, "list", "choice"); err != nil {
		t.Fatalf("first bounds annotation refused: %v", err)
	}
	first := encodeDocument(t, root)
	if err := AnnotateGuidance(root, nil, "list", "choice"); err != nil {
		t.Fatalf("second bounds annotation refused: %v", err)
	}
	if again := encodeDocument(t, root); again != first {
		t.Fatalf("repeated bounds annotation changed the document:\nfirst  %s\nsecond %s", first, again)
	}
}

// A reference outside #/$defs fails the bounds pass closed, names the
// reference, and leaves the document untouched; the full pass stays lenient
// on the same shape.
func TestBoundsRefuseUnsupportedReferences(t *testing.T) {
	document := `{"$defs": {"bad": {"type": "object", "properties": {"x": {"$ref": "https://example.com/other.schema.json"}}}}}`
	root := decodeSchema(t, document)
	before := encodeDocument(t, root)
	err := AnnotateGuidance(root, nil, "bad")
	if err == nil {
		t.Fatal("an unsupported reference was followed silently")
	}
	if !strings.Contains(err.Error(), "unsupported bound reference") || !strings.Contains(err.Error(), "https://example.com/other.schema.json") {
		t.Fatalf("failure does not name the unsupported reference: %v", err)
	}
	if after := encodeDocument(t, root); after != before {
		t.Fatalf("a refused bounds pass mutated the document:\nbefore %s\nafter  %s", before, after)
	}
	lenient := decodeSchema(t, document)
	if err := AnnotateGuidance(lenient, []string{"bad"}); err != nil {
		t.Fatalf("the full pass must stay lenient on an unsupported reference: %v", err)
	}
}

// A reference that resolves nowhere fails the bounds pass closed and mutates
// nothing.
func TestBoundsRefuseMissingReferences(t *testing.T) {
	root := decodeSchema(t, `{"$defs": {"bad": {"type": "object", "properties": {"x": {"$ref": "#/$defs/gone"}}}}}`)
	before := encodeDocument(t, root)
	err := AnnotateGuidance(root, nil, "bad")
	if err == nil {
		t.Fatal("a missing reference target was followed silently")
	}
	if !strings.Contains(err.Error(), "unknown bound reference") {
		t.Fatalf("failure does not name the missing reference: %v", err)
	}
	if after := encodeDocument(t, root); after != before {
		t.Fatalf("a refused bounds pass mutated the document:\nbefore %s\nafter  %s", before, after)
	}
}

// Nesting beyond the guidance depth fails the bounds pass closed and mutates
// nothing. The full pass over the same tree keeps its own preserved depth
// behavior: the walker stops silently where nothing generates, and a
// generatable chain fails in example generation exactly as before.
func TestBoundsRefuseDepthOverrun(t *testing.T) {
	schema := map[string]any{"type": "string", "maxLength": json.Number("8")}
	for i := 0; i < guidanceDepthMax+4; i++ {
		schema = map[string]any{
			"type":       "object",
			"properties": map[string]any{"child": schema},
			"required":   []any{"child"},
		}
	}
	document := map[string]any{"$defs": map[string]any{"deep": schema}}
	root := decodeSchema(t, encodeDocument(t, document))
	before := encodeDocument(t, root)
	err := AnnotateGuidance(root, nil, "deep")
	if err == nil {
		t.Fatal("a depth overrun was walked silently")
	}
	if !strings.Contains(err.Error(), "bound guidance exceeds schema depth") {
		t.Fatalf("failure does not name the depth overrun: %v", err)
	}
	if after := encodeDocument(t, root); after != before {
		t.Fatalf("a refused bounds pass mutated the document:\nbefore %s\nafter  %s", before, after)
	}

	// A generatable chain fails the full pass in generation, as it did
	// before the walker was shared.
	generative := decodeSchema(t, encodeDocument(t, document))
	err = AnnotateGuidance(generative, []string{"deep"})
	if err == nil || !strings.Contains(err.Error(), "schema nesting exceeds the 24-level guidance depth") {
		t.Fatalf("full-pass depth behavior changed: %v", err)
	}

	// Where nothing generates, the full walker ends the branch silently:
	// shallow levels annotate, the overrun stays unannotated, no error.
	shallow := map[string]any{"type": "string", "maxLength": json.Number("8")}
	for i := 0; i < guidanceDepthMax+4; i++ {
		shallow = map[string]any{"type": "object", "properties": map[string]any{"child": shallow}}
	}
	lenient := map[string]any{"$defs": map[string]any{"deep": shallow}}
	if err := AnnotateGuidance(lenient, []string{"deep"}); err != nil {
		t.Fatalf("the full pass must stay lenient where nothing generates: %v", err)
	}
	node := shallow
	for i := 0; i < guidanceDepthMax+2; i++ {
		node = node["properties"].(map[string]any)["child"].(map[string]any)
	}
	if _, carries := node["description"]; carries {
		t.Fatal("the full walk annotated past the depth overrun")
	}
}

// The bounds walk plans nothing for a full pass that already taught the
// tree's nodes without bounds-bearing shapes: clause teaching follows the
// plan, and the number literals stay json.Number end to end.
func TestBoundsNumberTypesStayLiteral(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {"count": {"type": "integer", "minimum": 3, "maximum": 9007199254740993}}
	}`)
	count := guidanceNodeAtPath(t, root, "$defs", "count")
	requireNumberLiteral(t, count, "maximum")
	if reflect.TypeOf(count["maximum"]).String() != "json.Number" {
		t.Fatalf("maximum carries %T, want json.Number", count["maximum"])
	}
	if err := AnnotateGuidance(root, nil, "count"); err != nil {
		t.Fatalf("bounds-only annotation refused: %v", err)
	}
	description, _ := count["description"].(string)
	if want := "Bounds: 3..9007199254740993 value inclusive"; description != want {
		t.Fatalf("count description = %q, want the exact decoded digits %q", description, want)
	}
}
