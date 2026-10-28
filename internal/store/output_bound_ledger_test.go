package store

// Structural output-bound ledger (test-only machinery, no per-family data).
//
// The output bound rule removes redundant output caps where no finite
// producer guard exists, keeps the 51200-byte result envelope ceiling with
// its typed whole-result refusal, and requires every remaining finite output
// maximum to admit the real input, store, and derivation bounds that feed it.
// This file owns the census, the source evaluation, and the closure machinery
// that holds the family files to that rule. It registers no rows itself: with an empty
// registration list the closure test fails RED listing every
// output-reachable bound that still lacks a source row.
//
// Machine-checked relationships:
//   - the census walks the published manifest's result roots (and aliases)
//     and enumerates every output-reachable maxItems/maxLength/x-maxBytes
//     keyword; the bound count is computed, never frozen;
//   - every registration pattern must match census bounds (wildcard stars
//     may cover repeated oneOf alternatives), must not be stale, and no
//     bound may be claimed twice;
//   - finite rows must evaluate a real source (input schema node, schema
//     node, Go guard call argument, Go constant, or final migrated SQLite
//     CHECK) and that source's capacity, converted across count units, must
//     admit the consumer bound;
//   - every parent node a wildcard pattern covers must retain the bound
//     keyword: a finite row fails when one arm's cap is removed even while
//     another arm stays capped, and an unbounded alternative still fails
//     while any arm keeps a cap;
//   - rows registered as "unbounded, byte-ceiling only" keep their node
//     reachable after the keyword is removed: they pass while the cap stays
//     removed and fail when a finite cap reappears; a finite row whose cap
//     was removed fails;
//   - Go string count units are inferred from the guard helper
//     implementation (len, len([]rune(...)), utf8.RuneCount, or
//     utf8.RuneCountInString), never from the declared unit alone, and a
//     guard mixing the measurements fails closed;
//   - maxBound composes ALTERNATIVE writers, whose per-unit capacities may
//     legitimately differ; combinePaired composes guards of the SAME value,
//     requires an inferred Go measurement, and takes their intersection;
//   - SQLite length() over TEXT measures only the NUL-terminated prefix, so
//     a character bound from it never stands as a full-string producer
//     proof: the source stays unproved unless a full-byte CHECK exists;
//   - refusal evidence is the canonical typed emitter only: a cited symbol
//     must directly carry the limit_exceeded kind, a *Refusal-suffixed call
//     or a forwarding helper proves nothing, and an unbounded row must cite
//     the exact canonical emitter of every manifest output class
//     (operations[].kind) that owns its bound — read bounds require
//     readSizeRefusal, mutation bounds require mutationResult, shared nodes
//     require both, and the harness never invents a citation;
//   - the census walks the supported schema-bearing keywords (properties,
//     patternProperties, dependentSchemas, items, additionalProperties,
//     unevaluatedProperties, unevaluatedItems, propertyNames, contains,
//     not, if, then, else, oneOf, anyOf, allOf, prefixItems) and fails
//     closed naming a known keyword it does not walk (contentSchema);
//   - the host ceiling is pinned to the manifest's envelope max_bytes
//     through the actual agent constant.
//
// Known, deliberate limits (recorded, not hidden): a schema source with both
// character and byte guards contributes their intersection; a bare Go
// constant carries no guard implementation to infer from, so
// goConstantBound trusts its declared unit and lets the producer/consumer
// comparison catch misuse, and combinePaired refuses it outright; the census
// reads the published contract documents from the working tree, whose sync
// with the generated Go schema is owned by the generator's own checks.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// ---------------------------------------------------------------------------
// Units and pinned documents
// ---------------------------------------------------------------------------

// boundUnit is the count unit a bound speaks in. Items count array elements;
// bytes count UTF-8 bytes; characters count Unicode code points.
type boundUnit string

const (
	boundItems      boundUnit = "items"
	boundBytes      boundUnit = "bytes"
	boundCharacters boundUnit = "characters"
)

// ledgerBoundKeywords are the schema keywords the census covers, each with
// the unit its numeric value speaks.
var ledgerBoundKeywords = []struct {
	keyword string
	unit    boundUnit
}{
	{"maxItems", boundItems},
	{"maxLength", boundCharacters},
	{"x-maxBytes", boundBytes},
}

const (
	// payloadSchemaFile is the published payload document the manifest's
	// result roots resolve into. Registration patterns are pointers into it.
	payloadSchemaFile = "contracts/agent-tool-surface-payloads.schema.json"
	// surfaceManifestFile is the tool-surface manifest: the census owner.
	surfaceManifestFile = "contracts/agent-tool-surface.v1.json"
	// agentEnvelopeFile holds the actual host ceiling constant.
	agentEnvelopeFile = "internal/agentcontract/envelope.go"
	// agentEnvelopeCeilingConst is the agent constant the ceiling reads from.
	agentEnvelopeCeilingConst = "MaxResultEnvelopeBytes"
	// ruledEnvelopeCeilingBytes pins the 51200-byte result ceiling. The ledger
	// checks the agent constant and the manifest against it.
	ruledEnvelopeCeilingBytes int64 = 51200
)

// Refusal classes come from the manifest's operations[].kind, and each class
// owns one canonical typed-refusal emitter: reads refuse through the shared
// read size refusal and mutations through the shared mutation envelope check
// (internal/agent/mutations.go mutationResult, whose body carries the
// limit_exceeded envelope refusal). An unbounded row must cite the exact
// canonical emitter of every class that owns its census bound — the harness
// never invents a second author-cited path, so a missing class fails the row
// until the row is updated.
const (
	ledgerReadRefusalClass     = "read"
	ledgerMutationRefusalClass = "mutation"
	ledgerReadRefusalPath      = "internal/agent/runtime.go:readSizeRefusal"
	ledgerMutationRefusalPath  = "internal/agent/mutations.go:mutationResult"
)

// ledgerClassRefusalPath names the canonical emitter one output class
// requires, or "" for a class the ledger has no emitter registered for.
func ledgerClassRefusalPath(class string) string {
	switch class {
	case ledgerReadRefusalClass:
		return ledgerReadRefusalPath
	case ledgerMutationRefusalClass:
		return ledgerMutationRefusalPath
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Output bound sources
// ---------------------------------------------------------------------------

// outputBoundSource is an opaque, evaluated-later description of where a
// bound comes from. Family files build sources only through the constructors
// below; every field is unexported on purpose.
type outputBoundSource struct {
	kind         string // input, schema, go_call, go_const, sqlite, max, sum, paired, unbounded, unproved
	file         string // schema document (repo-relative)
	pointer      string // JSON pointer to a bound keyword
	goFile       string
	goFunction   string
	goCallee     string
	goField      string
	goArgument   int
	symbol       string // Go constant name
	unit         boundUnit
	sqlTable     string // sqlite sources
	sqlColumn    string
	members      []outputBoundSource
	producer     string   // unbounded rows: why no finite producer guard exists; unproved rows: the saved gap
	refusalPaths []string // unbounded rows: the author-cited typed refusal paths, one per required class
	derive       func(*ledgerRuntime, *boundCensus) (evaluatedBound, error)
}

// inputBound cites a bound keyword inside the published payload document that
// is reachable from a manifest input root. The pointer is verified to be
// input-reachable on every run, and the value is read live: a copied number
// is not a source.
func inputBound(pointer string) outputBoundSource {
	return outputBoundSource{kind: "input", file: payloadSchemaFile, pointer: pointer}
}

// schemaBound cites a bound keyword in any published schema document
// (repo-relative file plus JSON pointer). The node is read live from the
// document. When the node carries both a character and a byte guard, both
// apply: their intersection is the source, not the character bound alone.
func schemaBound(file, pointer string) outputBoundSource {
	return outputBoundSource{kind: "schema", file: file, pointer: pointer}
}

// goCallBound cites a numeric guard argument passed to callee inside function
// in goFile. argument selects the call argument (zero-based) that must
// evaluate to an integer constant expression. When field is non-empty the
// call must carry a literal field-name string argument equal to it, which
// disambiguates sibling guard calls. The declared unit is checked against the
// guard implementation: the harness infers the actual count unit from how the
// guard measures its value (len for bytes/items; utf8.RuneCountInString,
// utf8.RuneCount, or len([]rune(...)) for characters; a mixed measurement
// fails closed) so a changed guard cannot hide behind a declared unit.
func goCallBound(file, function, callee, field string, argument int, unit boundUnit) outputBoundSource {
	return outputBoundSource{kind: "go_call", goFile: file, goFunction: function, goCallee: callee, goField: field, goArgument: argument, unit: unit}
}

// goConstantBound cites a Go constant declared in goFile. The value is read
// from the real declaration; a var or an unresolved name fails closed. A bare
// constant has no guard implementation to infer from, so the declared unit is
// trusted here and misuse is caught by the producer/consumer comparison.
func goConstantBound(file, symbol string, unit boundUnit) outputBoundSource {
	return outputBoundSource{kind: "go_const", goFile: file, symbol: symbol, unit: unit}
}

// sqliteBound cites the bound a final migrated CREATE TABLE CHECK declares
// for one column. The bound is parsed from sqlite_master of a migrated store
// at run time, never from an old migration copy. Units come from the SQL
// shape: length(CAST(col AS BLOB)) and octet_length(col) count bytes,
// length(col) follows the column's type affinity (TEXT counts characters),
// and json_array_length(col) counts items. A character bound from
// length(TEXT) measures only the NUL-terminated prefix, so it never stands
// as a full-string producer proof unless a full-byte CHECK bounds the same
// column.
func sqliteBound(table, column string) outputBoundSource {
	return outputBoundSource{kind: "sqlite", sqlTable: table, sqlColumn: column}
}

// maxBound composes ALTERNATIVE writers or derivations of one output: the
// output must admit the largest any member can emit. The writers may guard
// different units — a byte guard on one and a character guard on another are
// legitimate alternatives, not a mismatched pair — so no unit agreement is
// demanded between members. An unbounded member makes the composite
// unbounded. Mixing items with string units is refused; string members carry
// both their byte and character capacities so unit conversion stays sound.
// Guards of the SAME value whose unit agreement matters use combinePaired.
func maxBound(sources ...outputBoundSource) outputBoundSource {
	return outputBoundSource{kind: "max", members: sources}
}

// sumBound composes an output assembled from parts: the parts' capacities
// add. The same unit rules as maxBound apply.
func sumBound(sources ...outputBoundSource) outputBoundSource {
	return outputBoundSource{kind: "sum", members: sources}
}

// unboundedOutput registers a row with no finite producer guard. producer
// must explain what accumulates without a cardinality guard. Each cited
// refusal path must be a repo-relative file:Symbol that exists and directly
// emits the canonical typed refusal, and together the cited paths must cover
// every manifest output class that owns the bound: the shared read refusal
// (internal/agent/runtime.go:readSizeRefusal) for read-owned bounds and the
// shared mutation envelope check (internal/agent/mutations.go:mutationResult)
// for mutation-owned bounds. The harness never invents a citation: a shared
// read+mutation bound fails until the row cites both paths explicitly. The
// row passes only while its schema node carries no finite cap: a restored
// maximum fails.
func unboundedOutput(producer string, refusalPaths ...string) outputBoundSource {
	return outputBoundSource{kind: "unbounded", producer: producer, refusalPaths: refusalPaths}
}

// unprovedOutput registers a row whose producer is known to lack a finite
// proof. The row never closes the ledger: evaluation always fails, and the
// failure reports the saved gap text instead of an unknown-kind diagnostic,
// so the honest gap survives into the ledger output without being accepted
// as a source.
func unprovedOutput(gap string) outputBoundSource {
	return outputBoundSource{kind: "unproved", producer: gap}
}

// ---------------------------------------------------------------------------
// Registration registry
// ---------------------------------------------------------------------------

// censusRootRequest is one explicitly registered output node for results the
// manifest does not publish as a root (host or internal surfaces). file is a
// repo-relative schema document, pointer the node's JSON pointer inside it.
type censusRootRequest struct {
	file    string
	pointer string
}

type outputBoundRegistration struct {
	pattern string
	source  outputBoundSource
	invalid string
}

// ledgerRegistrations holds the family-file rows. Registration must be
// complete before the closure test runs: register from init() or a
// file-level var, never from another Test function body.
var ledgerRegistrations []outputBoundRegistration

// registerOutputBound registers one census row. pattern is a JSON pointer to
// a bound keyword (for example /$defs/workflow_read/properties/
// blocking_conditions/maxItems); a path.Match-style '*' may appear inside a
// segment to cover repeated oneOf alternatives ('*' crosses no '/'). Rows
// registered as unbounded keep their underlying node reachable after the
// keyword is removed: the row fails if a finite cap is restored, and a
// finite row fails once its cap is removed.
func registerOutputBound(pattern string, source outputBoundSource) {
	reg := outputBoundRegistration{pattern: pattern, source: source}
	if err := validateLedgerPattern(pattern); err != nil {
		reg.invalid = err.Error()
	}
	ledgerRegistrations = append(ledgerRegistrations, reg)
}

// retainedTwinPattern maps a pattern under a shared payload definition to the
// same pattern under its generator-owned retained output twin
// (RETAINED_OUTPUT_TWINS in scripts/generate-agent-contracts.py).
func retainedTwinPattern(pattern string) string {
	return "/$defs/retained_" + strings.TrimPrefix(pattern, "/$defs/")
}

// registerOutputBoundWithTwin registers a cap that a shared definition and its
// retained output twin both keep: one producer bounds both copies.
func registerOutputBoundWithTwin(pattern string, source outputBoundSource) {
	registerOutputBound(pattern, source)
	registerOutputBound(retainedTwinPattern(pattern), source)
}

// registerRetainedTwinBound registers a cap the retained output twin drops or
// raises (RETAINED_OUTPUT_TWINS and RETAINED_OUTPUT_RAISES). The twin replays
// stored values, so its row carries the retained producer source. The shared
// definition reaches output only inside a dispatched
// worker packet, and dispatch admission validates every packet against its
// declared schema (validateWorkflowPayloadSchema in workflow_preflight.go),
// so the shared row cites that admitted cap. pattern must be concrete: an
// input source names one node.
func registerRetainedTwinBound(pattern string, retained outputBoundSource) {
	registerOutputBound(pattern, inputBound(pattern))
	registerOutputBound(retainedTwinPattern(pattern), retained)
}

// splitLedgerPatternFile splits a registration pattern into its schema
// document and JSON pointer. A pattern inside the payload document is a bare
// pointer; a registered host/internal root is keyed "<file>#<pointer>" and
// its pattern carries the same "<file>#/" prefix. At most one "#" may
// appear: everything before it is the repo-relative document path.
func splitLedgerPatternFile(pattern string) (file, pointer string) {
	idx := strings.Index(pattern, "#")
	if idx < 0 {
		return "", pattern
	}
	return pattern[:idx], pattern[idx+1:]
}

func validateLedgerPattern(pattern string) error {
	if strings.Count(pattern, "#") > 1 {
		return fmt.Errorf("pattern %q carries more than one '#'", pattern)
	}
	file, pointer := splitLedgerPatternFile(pattern)
	if !strings.HasPrefix(pointer, "/") {
		if file == "" {
			return fmt.Errorf("pattern %q must be an absolute JSON pointer or a \"<file>#<pointer>\" registered root", pattern)
		}
		return fmt.Errorf("pattern %q must carry an absolute JSON pointer after '#'", pattern)
	}
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(segments) < 2 {
		return fmt.Errorf("pattern %q must point at a bound keyword", pattern)
	}
	last := segments[len(segments)-1]
	known := false
	for _, kw := range ledgerBoundKeywords {
		if last == kw.keyword {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("pattern %q must end in maxItems, maxLength, or x-maxBytes", pattern)
	}
	for i, segment := range segments {
		if segment == "" {
			return fmt.Errorf("pattern %q carries an empty segment", pattern)
		}
		if strings.ContainsAny(segment, "?[]\\") {
			return fmt.Errorf("pattern %q uses a match metacharacter the ledger does not support", pattern)
		}
		if strings.Contains(segment, "*") && i == len(segments)-1 {
			return fmt.Errorf("pattern %q wildcards the keyword itself", pattern)
		}
		for j := 0; j < len(segment); j++ {
			if segment[j] == '~' {
				if j+1 >= len(segment) || (segment[j+1] != '0' && segment[j+1] != '1') {
					return fmt.Errorf("pattern %q carries an unescaped '~'", pattern)
				}
				j++
			}
		}
	}
	if file != "" && (strings.Contains(file, "*") || filepath.Clean(file) != file || filepath.IsAbs(file)) {
		return fmt.Errorf("pattern %q carries an unsupported registered-root document %q", pattern, file)
	}
	return nil
}

// pointerMatches reports whether a census key matches a pattern. Segments are
// compared one by one; '*' inside a segment matches any run of characters
// within that segment and never crosses '/', which is the path.Match-style
// star the row contract allows for repeated oneOf alternatives.
func pointerMatches(pattern, key string) bool {
	patternSegments := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	keySegments := strings.Split(strings.TrimPrefix(key, "/"), "/")
	if len(patternSegments) != len(keySegments) {
		return false
	}
	for i := range patternSegments {
		if !ledgerMatchSegment(patternSegments[i], keySegments[i]) {
			return false
		}
	}
	return true
}

func ledgerMatchSegment(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	if pattern[0] == '*' {
		for i := 0; i <= len(name); i++ {
			if ledgerMatchSegment(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if name == "" || name[0] != pattern[0] {
		return false
	}
	return ledgerMatchSegment(pattern[1:], name[1:])
}

// ledgerParentPattern strips the trailing keyword segment from a pattern so
// the underlying schema node can be resolved even after the keyword itself
// was removed. A "<file>#<pointer>" registered-root pattern keeps its
// document prefix, matching the census node keys censusKey produces.
func ledgerParentPattern(pattern string) string {
	file, pointer := splitLedgerPatternFile(pattern)
	idx := strings.LastIndex(pointer, "/")
	if idx <= 0 {
		return pattern
	}
	parent := pointer[:idx]
	if file != "" {
		return file + "#" + parent
	}
	return parent
}

// ---------------------------------------------------------------------------
// Pointers and small JSON helpers
// ---------------------------------------------------------------------------

func escapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func unescapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}

func joinPointerToken(pointer, escapedToken string) string {
	if pointer == "" {
		return "/" + escapedToken
	}
	return pointer + "/" + escapedToken
}

func resolveSchemaPointer(root any, pointer string) (any, bool) {
	if pointer == "" {
		return root, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := root
	for _, raw := range strings.Split(pointer[1:], "/") {
		token := unescapePointerToken(raw)
		switch parent := current.(type) {
		case map[string]any:
			next, ok := parent[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(parent) {
				return nil, false
			}
			current = parent[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func numberValue(raw any) (int64, error) {
	switch value := raw.(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, fmt.Errorf("bound value %s is not an integer", value.String())
		}
		return parsed, nil
	case int64:
		return value, nil
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("bound value %v is not an integer", value)
		}
		return int64(value), nil
	case nil:
		return 0, fmt.Errorf("bound value is missing")
	default:
		return 0, fmt.Errorf("bound value %T is not a number", raw)
	}
}

func ledgerDeepCopyDoc(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("cannot marshal the census document for mutation: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var copied map[string]any
	if err := decoder.Decode(&copied); err != nil {
		t.Fatalf("cannot reparse the census document for mutation: %v", err)
	}
	return copied
}

func censusKey(file, pointer string) string {
	if file == payloadSchemaFile {
		return pointer
	}
	return file + "#" + pointer
}

// ---------------------------------------------------------------------------
// Ledger runtime: documents, Go sources, DDL
// ---------------------------------------------------------------------------

type goSource struct {
	file *ast.File
	path string // repository-relative; its directory is the package scope
}

// ledgerRuntime carries the live surfaces the ledger evaluates against: the
// published documents, parsed Go sources, and the final migrated DDL.
type ledgerRuntime struct {
	root    string
	mu      sync.Mutex
	docs    map[string]map[string]any
	goCache map[string]*goSource
	ddl     map[string]string
}

func newLedgerRuntime(root string) *ledgerRuntime {
	return &ledgerRuntime{root: root, docs: map[string]map[string]any{}, goCache: map[string]*goSource{}}
}

func (rt *ledgerRuntime) repoPath(file string) (string, error) {
	cleaned := filepath.Clean(file)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository", file)
	}
	return filepath.Join(rt.root, cleaned), nil
}

func (rt *ledgerRuntime) loadDoc(file string) (map[string]any, error) {
	rt.mu.Lock()
	cached, ok := rt.docs[file]
	rt.mu.Unlock()
	if ok {
		return cached, nil
	}
	path, err := rt.repoPath(file)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", file, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%s is not a JSON document: %w", file, err)
	}
	rt.mu.Lock()
	rt.docs[file] = document
	rt.mu.Unlock()
	return document, nil
}

func (rt *ledgerRuntime) loadGo(file string) (*goSource, error) {
	rt.mu.Lock()
	cached, ok := rt.goCache[file]
	rt.mu.Unlock()
	if ok {
		return cached, nil
	}
	path, err := rt.repoPath(file)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", file, err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, raw, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", file, err)
	}
	source := &goSource{file: parsed, path: file}
	rt.mu.Lock()
	rt.goCache[file] = source
	rt.mu.Unlock()
	return source, nil
}

// parseGoSource seeds a synthetic source under an arbitrary name, for
// table-driven extraction tests and mutation witnesses.
func (rt *ledgerRuntime) parseGoSource(name, src string) error {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("synthetic source %s does not parse: %w", name, err)
	}
	rt.mu.Lock()
	rt.goCache[name] = &goSource{file: parsed, path: name}
	rt.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// Go constant evaluation
// ---------------------------------------------------------------------------

type ledgerConstDef struct {
	expr ast.Expr
	iota int
}

func ledgerCollectConsts(decl *ast.GenDecl, into map[string]ledgerConstDef) {
	if decl.Tok != token.CONST {
		return
	}
	var previous []ast.Expr
	for index, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		values := valueSpec.Values
		if len(values) == 0 {
			values = previous
		} else {
			previous = values
		}
		for i, name := range valueSpec.Names {
			if i < len(values) {
				into[name.Name] = ledgerConstDef{expr: values[i], iota: index}
			}
		}
	}
}

// ledgerConstScope maps every const name visible to an expression: the
// package-level consts of every production file in the source's directory,
// the file's own consts, and the enclosing function's local consts when scope
// is set, each shadowing the last. Implicit const-block repetition (iota
// lists) re-evaluates the previous value list at the current spec index.
func (rt *ledgerRuntime) ledgerConstScope(goS *goSource, scope *ast.FuncDecl) (map[string]ledgerConstDef, error) {
	consts := map[string]ledgerConstDef{}
	if dir := filepath.Dir(goS.path); goS.path != "" && dir != "." {
		root, err := rt.repoPath(dir)
		if err != nil {
			return nil, err
		}
		// The package is every production file on disk in the directory
		// plus any source injected under it.
		names := map[string]bool{}
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("cannot read the package scope of %s: %w", goS.path, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				names[filepath.ToSlash(filepath.Join(dir, entry.Name()))] = true
			}
		}
		rt.mu.Lock()
		for name := range rt.goCache {
			if filepath.Dir(name) == dir {
				names[name] = true
			}
		}
		rt.mu.Unlock()
		ordered := make([]string, 0, len(names))
		for name := range names {
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && name != goS.path {
				ordered = append(ordered, name)
			}
		}
		sort.Strings(ordered)
		for _, name := range ordered {
			sibling, err := rt.loadGo(name)
			if err != nil {
				return nil, err
			}
			for _, decl := range sibling.file.Decls {
				if gen, ok := decl.(*ast.GenDecl); ok {
					ledgerCollectConsts(gen, consts)
				}
			}
		}
	}
	file := goS.file
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok {
			ledgerCollectConsts(gen, consts)
		}
	}
	if scope != nil && scope.Body != nil {
		ast.Inspect(scope.Body, func(n ast.Node) bool {
			if gen, ok := n.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				ledgerCollectConsts(gen, consts)
			}
			return true
		})
	}
	return consts, nil
}

// evalNumeric evaluates one integer constant expression against the package
// const scope (plus the enclosing function's local consts when scope is set).
// Anything it cannot resolve or reduce fails closed.
func (rt *ledgerRuntime) evalNumeric(goS *goSource, scope *ast.FuncDecl, expr ast.Expr) (int64, error) {
	consts, err := rt.ledgerConstScope(goS, scope)
	if err != nil {
		return 0, err
	}
	return ledgerEvalConst(consts, expr, 0, 0)
}

func ledgerEvalConst(consts map[string]ledgerConstDef, expr ast.Expr, iotaValue, depth int) (int64, error) {
	if depth > 32 {
		return 0, fmt.Errorf("constant expression nesting or cycle exceeds the ledger limit")
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT {
			return 0, fmt.Errorf("unsupported constant literal %s in a bound source", e.Value)
		}
		return ledgerConstInt(constant.MakeFromLiteral(e.Value, e.Kind, 0))
	case *ast.ParenExpr:
		return ledgerEvalConst(consts, e.X, iotaValue, depth+1)
	case *ast.Ident:
		if e.Name == "iota" {
			return int64(iotaValue), nil
		}
		def, ok := consts[e.Name]
		if !ok {
			return 0, fmt.Errorf("unresolved constant %s in a bound source", e.Name)
		}
		return ledgerEvalConst(consts, def.expr, def.iota, depth+1)
	case *ast.UnaryExpr:
		inner, err := ledgerEvalConst(consts, e.X, iotaValue, depth+1)
		if err != nil {
			return 0, err
		}
		switch e.Op {
		case token.SUB:
			return ledgerConstInt(constant.UnaryOp(token.SUB, constant.MakeInt64(inner), 0))
		case token.ADD:
			return inner, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator %s in a bound source", e.Op)
		}
	case *ast.BinaryExpr:
		left, err := ledgerEvalConst(consts, e.X, iotaValue, depth+1)
		if err != nil {
			return 0, err
		}
		right, err := ledgerEvalConst(consts, e.Y, iotaValue, depth+1)
		if err != nil {
			return 0, err
		}
		x := constant.MakeInt64(left)
		y := constant.MakeInt64(right)
		var value constant.Value
		switch e.Op {
		case token.QUO, token.REM:
			if right == 0 {
				return 0, fmt.Errorf("zero divisor in a bound source")
			}
			op := e.Op
			if op == token.QUO {
				op = token.QUO_ASSIGN
			}
			value = constant.BinaryOp(x, op, y)
		case token.ADD, token.SUB, token.MUL, token.AND, token.OR, token.XOR, token.AND_NOT:
			value = constant.BinaryOp(x, e.Op, y)
		case token.SHL, token.SHR:
			shift, exact := constant.Uint64Val(y)
			if !exact {
				return 0, fmt.Errorf("shift count in a bound source is not an exact integer")
			}
			value = constant.Shift(x, e.Op, uint(shift))
		default:
			return 0, fmt.Errorf("unsupported binary operator %s in a bound source", e.Op)
		}
		return ledgerConstInt(value)
	case *ast.CallExpr:
		// len of a string literal is a Go constant: its byte length.
		if ident, ok := e.Fun.(*ast.Ident); ok && ident.Name == "len" && len(e.Args) == 1 {
			if lit, ok := e.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					return 0, fmt.Errorf("unparsable string literal %s in a bound source: %w", lit.Value, err)
				}
				return int64(len(text)), nil
			}
		}
		return 0, fmt.Errorf("unsupported constant expression %T in a bound source", expr)
	default:
		return 0, fmt.Errorf("unsupported constant expression %T in a bound source", expr)
	}
}

func ledgerConstInt(value constant.Value) (int64, error) {
	parsed, exact := constant.Int64Val(value)
	if !exact {
		return 0, fmt.Errorf("bound source constant %s is not an exact integer", value.String())
	}
	return parsed, nil
}

// ---------------------------------------------------------------------------
// Go call matching and guard unit inference
// ---------------------------------------------------------------------------

func ledgerCalledName(expr ast.Expr) string {
	switch fun := expr.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return ledgerCalledName(fun.X) + "." + fun.Sel.Name
	case *ast.IndexExpr:
		return ledgerCalledName(fun.X)
	case *ast.IndexListExpr:
		return ledgerCalledName(fun.X)
	case *ast.ParenExpr:
		return ledgerCalledName(fun.X)
	default:
		return ""
	}
}

// ledgerCalleeMatches selects syntactic call shapes, including a method's
// unqualified name. Guard declaration resolution must instead match the full
// callee: a matching method name does not establish its implementation.
func ledgerCalleeMatches(call *ast.CallExpr, callee string) bool {
	name := ledgerCalledName(call.Fun)
	return name == callee || (!strings.Contains(callee, ".") && strings.HasSuffix(name, "."+callee))
}

func ledgerCallHasFieldLiteral(call *ast.CallExpr, field string) bool {
	for _, arg := range call.Args {
		literal, ok := arg.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && value == field {
			return true
		}
	}
	return false
}

func ledgerFuncDeclsNamed(file *ast.File, name string) []*ast.FuncDecl {
	var found []*ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
			found = append(found, fn)
		}
	}
	return found
}

// ledgerScanMeasurement reports how an expression measures a value:
// "rune_count" when a code-point measurement appears (utf8.RuneCountInString,
// utf8.RuneCount, or len([]rune(...))), "len" when only a direct len does.
// An expression carrying both measurements is ambiguous and fails closed
// with "".
func ledgerScanMeasurement(node ast.Node) string {
	runeCount, plainLen := false, false
	ast.Inspect(node, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.SelectorExpr:
			if e.Sel != nil && (e.Sel.Name == "RuneCountInString" || e.Sel.Name == "RuneCount") {
				runeCount = true
			}
		case *ast.CallExpr:
			if ident, ok := e.Fun.(*ast.Ident); ok && ident.Name == "len" {
				if ledgerRuneConversionArg(e) {
					// len([]rune(x)) counts code points, not bytes.
					runeCount = true
				} else {
					plainLen = true
				}
			}
		}
		return true
	})
	switch {
	case runeCount && plainLen:
		return "" // mixed measurements cannot be resolved to one count unit
	case runeCount:
		return "rune_count"
	case plainLen:
		return "len"
	default:
		return ""
	}
}

// ledgerRuneConversionArg reports whether a len(...) call's single argument
// is a []rune conversion, whose length counts code points.
func ledgerRuneConversionArg(call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	conv, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	arrayType, ok := conv.Fun.(*ast.ArrayType)
	if !ok || arrayType.Len != nil {
		return false
	}
	elt, ok := arrayType.Elt.(*ast.Ident)
	return ok && elt.Name == "rune"
}

// inferFromCallee resolves the callee's declaration, in the named file first
// and then in the sibling files of the same directory, and reports how the
// guard helper implementation measures its value.
func (rt *ledgerRuntime) inferFromCallee(goFile, callee string) string {
	if strings.Contains(callee, ".") {
		return ""
	}
	goS, err := rt.loadGo(goFile)
	if err == nil {
		if fns := ledgerFuncDeclsNamed(goS.file, callee); len(fns) == 1 && fns[0].Recv == nil && fns[0].Body != nil {
			if inferred := ledgerScanMeasurement(fns[0].Body); inferred != "" {
				return inferred
			}
		}
	}
	directory := filepath.Dir(goFile)
	entries, err := os.ReadDir(filepath.Join(rt.root, filepath.Clean(directory)))
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		names = append(names, filepath.Join(directory, entry.Name()))
	}
	sort.Strings(names)
	for _, name := range names {
		sibling, err := rt.loadGo(filepath.ToSlash(name))
		if err != nil {
			continue
		}
		if fns := ledgerFuncDeclsNamed(sibling.file, callee); len(fns) == 1 && fns[0].Recv == nil && fns[0].Body != nil {
			if inferred := ledgerScanMeasurement(fns[0].Body); inferred != "" {
				return inferred
			}
		}
	}
	return ""
}

// reconcileGoUnit holds the declared unit against the guard implementation's
// inferred measurement. This is what makes a changed guard visible: swapping
// len for utf8.RuneCountInString (or the reverse) flips the inference and the
// registered unit stops agreeing.
func reconcileGoUnit(declared boundUnit, inferred string) error {
	switch inferred {
	case "rune_count":
		if declared != boundCharacters {
			return fmt.Errorf("guard implementation counts characters (utf8.RuneCountInString) but the row declares %s; the declared unit would not detect a changed guard", declared)
		}
		return nil
	case "len":
		if declared == boundCharacters {
			return fmt.Errorf("guard implementation counts with len() but the row declares %s; the declared unit would not detect a changed guard", declared)
		}
		return nil
	default:
		return fmt.Errorf("cannot infer the guard's count unit from its implementation; cite a guard whose measurement (len or utf8.RuneCountInString) is visible")
	}
}

// ---------------------------------------------------------------------------
// Typed refusal path verification
// ---------------------------------------------------------------------------

// checkLedgerRefusalPath verifies that a repo-relative file:Symbol exists
// and directly emits the canonical typed refusal: the declaration's own body
// must carry the "limit_exceeded" error kind literal. A call whose callee
// merely ends in "Refusal" is not evidence, and a helper that forwards to an
// emitter is not the emitter: cite the innermost symbol carrying the
// limit_exceeded kind (the shared readSizeRefusal or mutationResult).
func checkLedgerRefusalPath(rt *ledgerRuntime, refusalPath string) error {
	colon := strings.LastIndex(refusalPath, ":")
	if colon <= 0 || colon == len(refusalPath)-1 {
		return fmt.Errorf("refusal path %q must be repo-relative file:Symbol", refusalPath)
	}
	file, symbol := refusalPath[:colon], refusalPath[colon+1:]
	if !strings.HasSuffix(file, ".go") {
		return fmt.Errorf("refusal path %q must cite a Go file", refusalPath)
	}
	if _, err := rt.repoPath(file); err != nil {
		return fmt.Errorf("refusal path %q: %w", refusalPath, err)
	}
	goS, err := rt.loadGo(file)
	if err != nil {
		return err
	}
	var body ast.Node
	for _, decl := range goS.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == symbol {
			body = fn
			break
		}
	}
	if body == nil {
		for _, decl := range goS.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				switch named := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range named.Names {
						if name.Name == symbol {
							body = gen
						}
					}
				case *ast.TypeSpec:
					if named.Name.Name == symbol {
						body = gen
					}
				}
			}
		}
	}
	if body == nil {
		return fmt.Errorf("refusal path %q names no declared symbol", refusalPath)
	}
	emits := false
	ast.Inspect(body, func(n ast.Node) bool {
		if e, ok := n.(*ast.BasicLit); ok && e.Kind == token.STRING {
			if value, err := strconv.Unquote(e.Value); err == nil && value == "limit_exceeded" {
				emits = true
			}
		}
		return true
	})
	if !emits {
		return fmt.Errorf("refusal path %q does not directly emit the canonical limit_exceeded refusal; cite the innermost emitter symbol, not a helper or a *Refusal-suffixed call", refusalPath)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SQLite DDL: final migrated CREATE TABLE text
// ---------------------------------------------------------------------------

// sqliteMasterDDL reads the final migrated schema straight from a store's
// database handle. The caller runs outside any transaction: the store pools
// one connection and an s.db-backed call inside a transaction parks forever.
func sqliteMasterDDL(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("cannot read the final migrated DDL: %w", err)
	}
	defer rows.Close()
	tables := map[string]string{}
	for rows.Next() {
		var name, createSQL string
		if err := rows.Scan(&name, &createSQL); err != nil {
			return nil, fmt.Errorf("cannot scan the final migrated DDL: %w", err)
		}
		tables[name] = createSQL
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cannot read the final migrated DDL: %w", err)
	}
	return tables, nil
}

var (
	ledgerBetweenProtect = regexp.MustCompile(`between\s+([0-9]+)\s+and\s+([0-9]+)`)
	ledgerMeasurementRe  = regexp.MustCompile(`length\s*\(\s*cast\s*\(\s*([a-z0-9_]+)\s+as\s+blob\s*\)\s*\)|octet_length\s*\(\s*([a-z0-9_]+)\s*\)|json_array_length\s*\(\s*([a-z0-9_]+)\s*\)|length\s*\(\s*([a-z0-9_]+)\s*\)`)
	ledgerComparatorRe   = regexp.MustCompile(`^\s*(<=|>=|=|<|>)\s*([0-9]+)`)
	ledgerBetweenBoundRe = regexp.MustCompile(`^\s*between\s+([0-9]+)\.\.([0-9]+)`)
)

func ledgerIsWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func ledgerCollapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ledgerStripSQLComments removes SQL line and block comments from DDL text
// without altering quoted values or identifiers. sqlite_master retains the
// original CREATE TABLE text, comments included, and an apostrophe inside a
// comment (schema.go carries several) would otherwise be read as a string
// delimiter by every quote-tracking scan below. A block comment collapses to
// one space so its removal cannot fuse adjacent tokens. Unterminated quotes
// or block comments fail closed; a line comment may run to end of input.
func ledgerStripSQLComments(ddl string) (string, error) {
	var out strings.Builder
	out.Grow(len(ddl))
	i := 0
	var quote byte
	for i < len(ddl) {
		c := ddl[i]
		switch {
		case quote != 0:
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(ddl) && ddl[i+1] == quote {
					out.WriteByte(quote)
					i += 2
					continue
				}
				quote = 0
			}
			i++
		case c == '-' && i+1 < len(ddl) && ddl[i+1] == '-':
			for i < len(ddl) && ddl[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(ddl) && ddl[i+1] == '*':
			end := strings.Index(ddl[i+2:], "*/")
			if end < 0 {
				return "", fmt.Errorf("DDL carries an unterminated block comment")
			}
			i += end + 4
			out.WriteByte(' ')
		case c == '\'' || c == '"' || c == '`':
			quote = c
			out.WriteByte(c)
			i++
		default:
			out.WriteByte(c)
			i++
		}
	}
	if quote != 0 {
		return "", fmt.Errorf("DDL ends inside an unterminated quoted value or identifier")
	}
	return out.String(), nil
}

// ledgerParenSpan returns the inner text and closing index of the paren group
// opened at open.
func ledgerParenSpan(s string, open int) (string, int, bool) {
	depth, inQuote := 0, false
	for i := open; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inQuote = false
			}
			continue
		}
		switch c {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i, true
			}
		}
	}
	return "", 0, false
}

func ledgerSplitTopLevel(body string, sep byte) []string {
	var parts []string
	depth, inQuote, start := 0, false, 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inQuote {
			if c == '\'' {
				if i+1 < len(body) && body[i+1] == '\'' {
					i++
					continue
				}
				inQuote = false
			}
			continue
		}
		switch c {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, body[start:])
}

// ledgerSplitTopLevelKeyword splits on a depth-zero, quote-free AND or OR
// word with word boundaries on both sides. Split fragments keep their
// surrounding spaces; every consumer trims or tolerates them.
func ledgerSplitTopLevelKeyword(body, word string) []string {
	var parts []string
	depth, inQuote, start := 0, false, 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inQuote {
			if c == '\'' {
				if i+1 < len(body) && body[i+1] == '\'' {
					i++
					continue
				}
				inQuote = false
			}
			continue
		}
		switch c {
		case '\'':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth != 0 || c != word[0] || i+len(word) > len(body) || body[i:i+len(word)] != word {
				continue
			}
			before := i == 0 || !ledgerIsWordByte(body[i-1])
			after := i+len(word) >= len(body) || !ledgerIsWordByte(body[i+len(word)])
			if before && after {
				parts = append(parts, body[start:i])
				start = i + len(word)
				i += len(word) - 1
			}
		}
	}
	return append(parts, body[start:])
}

// ledgerCheckBodies extracts the inner text of every CHECK group in a column
// or table constraint definition.
func ledgerCheckBodies(def string) []string {
	var bodies []string
	lower := strings.ToLower(def)
	for i := 0; i < len(lower); {
		idx := strings.Index(lower[i:], "check")
		if idx < 0 {
			break
		}
		at := i + idx
		before := at == 0 || !ledgerIsWordByte(lower[at-1])
		after := at+5 < len(lower) && !ledgerIsWordByte(lower[at+5])
		j := at + 5
		for j < len(def) && (def[j] == ' ' || def[j] == '\t' || def[j] == '\n') {
			j++
		}
		if before && after && j < len(def) && def[j] == '(' {
			inner, end, ok := ledgerParenSpan(def, j)
			if !ok {
				break
			}
			bodies = append(bodies, inner)
			i = end + 1
			continue
		}
		i = at + 5
	}
	return bodies
}

// ledgerColumnDefName splits one DDL definition into its first identifier and
// the remainder, reporting false for table-level constraint keywords.
func ledgerColumnDefName(def string) (name, rest string, isColumn bool) {
	switch {
	case strings.HasPrefix(def, "\""):
		end := strings.Index(def[1:], "\"")
		if end < 0 {
			return "", def, false
		}
		return def[1 : 1+end], def[2+end:], true
	case strings.HasPrefix(def, "`"):
		end := strings.Index(def[1:], "`")
		if end < 0 {
			return "", def, false
		}
		return def[1 : 1+end], def[2+end:], true
	default:
		i := 0
		for i < len(def) && ledgerIsWordByte(def[i]) {
			i++
		}
		name := def[:i]
		switch strings.ToLower(name) {
		case "check", "constraint", "unique", "primary", "foreign":
			return "", def, false
		}
		return name, def[i:], true
	}
}

// ledgerAffinity classifies a column's declared type into the SQLite length
// semantics that apply to it: length() over TEXT counts characters and over
// BLOB counts bytes; numeric affinities have no string-bound meaning.
func ledgerAffinity(declaredType string) (boundUnit, error) {
	fields := strings.Fields(declaredType)
	declared := ""
	if len(fields) > 0 {
		declared = strings.ToUpper(fields[0])
	}
	switch {
	case declared == "":
		return boundBytes, nil // SQLite BLOB affinity
	case strings.Contains(declared, "INT"):
		return "", fmt.Errorf("length() over an %s column has no string-bound meaning", declared)
	case strings.Contains(declared, "CHAR"), strings.Contains(declared, "CLOB"), strings.Contains(declared, "TEXT"):
		return boundCharacters, nil
	case strings.Contains(declared, "BLOB"):
		return boundBytes, nil
	case strings.Contains(declared, "REAL"), strings.Contains(declared, "FLOA"), strings.Contains(declared, "DOUB"):
		return "", fmt.Errorf("length() over a %s column has no string-bound meaning", declared)
	default:
		return "", fmt.Errorf("unsupported column affinity %s for a length bound", declared)
	}
}

type ledgerMeasurement struct {
	unit boundUnit
	end  int
}

// ledgerQuotedSpans returns the index spans of quoted values and quoted
// identifiers in s. Text inside them is data: a literal such as
// 'length(x) <= 999999' is never a real measurement.
func ledgerQuotedSpans(s string) [][2]int {
	var spans [][2]int
	start, quote := -1, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote == 0 {
			if c == '\'' || c == '"' || c == '`' {
				quote, start = c, i
			}
			continue
		}
		if c != quote {
			continue
		}
		if i+1 < len(s) && s[i+1] == quote {
			i++
			continue
		}
		spans = append(spans, [2]int{start, i + 1})
		quote = 0
	}
	if quote != 0 {
		spans = append(spans, [2]int{start, len(s)})
	}
	return spans
}

// ledgerFindMeasurements locates every length/cast/octet/json_array
// measurement of exactly the named column in one normalized atom, ignoring
// matches inside quoted values and identifiers.
func ledgerFindMeasurements(atom, column string) []ledgerMeasurement {
	spans := ledgerQuotedSpans(atom)
	inQuote := func(at int) bool {
		for _, span := range spans {
			if at >= span[0] && at < span[1] {
				return true
			}
		}
		return false
	}
	var found []ledgerMeasurement
	for _, loc := range ledgerMeasurementRe.FindAllStringSubmatchIndex(atom, -1) {
		if inQuote(loc[0]) {
			continue
		}
		groups := [4]string{}
		for g := 0; g < 4; g++ {
			pair := loc[2*g+2 : 2*g+4]
			if pair[0] >= 0 {
				groups[g] = atom[pair[0]:pair[1]]
			}
		}
		measured := ""
		alt := -1
		for g, value := range groups {
			if value != "" {
				measured, alt = value, g
				break
			}
		}
		if measured != column {
			continue
		}
		switch alt {
		case 0, 1:
			found = append(found, ledgerMeasurement{unit: boundBytes, end: loc[1]})
		case 2:
			found = append(found, ledgerMeasurement{unit: boundItems, end: loc[1]})
		case 3:
			found = append(found, ledgerMeasurement{unit: boundCharacters, end: loc[1]})
		}
	}
	return found
}

// ledgerBoundTailJunk refuses anything but whitespace and closing
// parentheses after a parsed bound: trailing arithmetic (<= 63 + 1) or a
// conditional wrapper (CASE WHEN length(c) <= 64 THEN 1 ... / IIF(...)) is
// not the plain comparator it mimics, and silently taking its first integer
// would understate the real capacity.
func ledgerBoundTailJunk(atom, rest string) error {
	if trimmed := strings.Trim(rest, " \t\r\n)"); trimmed != "" {
		return fmt.Errorf("CHECK atom %q carries unsupported trailing structure %q after the bound; only a bare comparator or between bound is analyzed", ledgerCollapseSpace(atom), ledgerCollapseSpace(trimmed))
	}
	return nil
}

// ledgerComparatorUpper reads the numeric upper bound that follows one
// measurement, if the atom carries one. '<' is strict: its bound is n-1.
// An atom with trailing arithmetic or conditional structure after the bound
// fails closed instead of taking the first integer.
func ledgerComparatorUpper(atom string, from int) (int64, bool, error) {
	tail := atom[from:]
	if m := ledgerComparatorRe.FindStringSubmatch(tail); m != nil {
		if err := ledgerBoundTailJunk(atom, tail[len(m[0]):]); err != nil {
			return 0, false, err
		}
		value, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("unparsable bound number %q", m[2])
		}
		switch m[1] {
		case "<=":
			return value, true, nil
		case "<":
			return value - 1, true, nil
		case "=":
			return value, true, nil
		default: // >= or >: lower bound only
			return 0, false, nil
		}
	}
	if m := ledgerBetweenBoundRe.FindStringSubmatch(tail); m != nil {
		if err := ledgerBoundTailJunk(atom, tail[len(m[0]):]); err != nil {
			return 0, false, err
		}
		high, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("unparsable between bound %q", m[2])
		}
		return high, true, nil
	}
	return 0, false, nil
}

// ledgerExprBounds is what one reduced boolean expression admits about the
// target column's length. The three facts are orthogonal: constrainsTarget
// reports whether any target measurement participates at all, nullOnly
// reports that the expression admits no non-null target value, and
// unbounded reports that it can admit a non-null target value of
// unbounded length. When neither nullOnly nor unbounded holds, bounds
// carries the expression's upper bound per unit.
type ledgerExprBounds struct {
	constrainsTarget bool
	nullOnly         bool
	unbounded        bool
	bounds           map[boundUnit]int64
}

// ledgerUnwrapParens removes balanced outer parentheses that enclose the
// whole expression, repeatedly. Inner groups are never touched: when the
// paren group closes before the end of the expression, the expression is
// returned unchanged.
func ledgerUnwrapParens(expr string) string {
	s := strings.TrimSpace(expr)
	for strings.HasPrefix(s, "(") {
		inner, end, ok := ledgerParenSpan(s, 0)
		if !ok || strings.TrimSpace(s[end+1:]) != "" {
			return s
		}
		s = strings.TrimSpace(inner)
	}
	return s
}

// ledgerReduceExpr reduces a CHECK boolean expression recursively: balanced
// outer parentheses unwrap, AND intersects upper bounds, and OR takes their
// union maximum. An OR alternative is accepted only when it bounds the
// column or admits no non-null value (`column IS NULL`); an alternative
// that can admit unbounded non-null target values fails closed. An
// expression about other columns only (no target measurement) is
// unconstrained in target length: ignorable beside a bounding conjunct,
// rejected where it would stand as a target-length alternative. A
// lower-only predicate never invents an upper bound. A negated predicate is
// not analyzed and fails closed.
func ledgerReduceExpr(expr, column string, affinity boundUnit) (ledgerExprBounds, error) {
	expr = ledgerUnwrapParens(expr)
	if branches := ledgerSplitTopLevelKeyword(expr, "or"); len(branches) > 1 {
		reduced := make([]ledgerExprBounds, 0, len(branches))
		constrains, nullOnlyAll := false, true
		for _, branch := range branches {
			one, err := ledgerReduceExpr(branch, column, affinity)
			if err != nil {
				return ledgerExprBounds{}, err
			}
			if !one.nullOnly {
				nullOnlyAll = false
				constrains = constrains || one.constrainsTarget
			}
			reduced = append(reduced, one)
		}
		if nullOnlyAll {
			return ledgerExprBounds{nullOnly: true}, nil
		}
		if !constrains {
			// No alternative constrains the target column's length, so
			// the OR itself says nothing about it.
			return ledgerExprBounds{unbounded: true}, nil
		}
		union := map[boundUnit]int64{}
		for i, one := range reduced {
			if one.nullOnly {
				continue // admits no non-null target value
			}
			if one.unbounded || len(one.bounds) == 0 {
				return ledgerExprBounds{}, fmt.Errorf("OR branch %q may admit unbounded non-null values of %s", ledgerCollapseSpace(branches[i]), column)
			}
			for unit, value := range one.bounds {
				if current, ok := union[unit]; !ok || value > current {
					union[unit] = value
				}
			}
		}
		allUnits := map[boundUnit]bool{}
		for unit := range union {
			allUnits[unit] = true
		}
		for _, one := range reduced {
			if one.nullOnly {
				continue
			}
			for unit := range allUnits {
				if _, ok := one.bounds[unit]; !ok {
					return ledgerExprBounds{}, fmt.Errorf("OR branches bound %s in different units, so the check admits unbounded values", unit)
				}
			}
		}
		return ledgerExprBounds{constrainsTarget: true, bounds: union}, nil
	}
	atoms := ledgerSplitTopLevelKeyword(expr, "and")
	if len(atoms) == 1 {
		return ledgerReduceAtom(expr, column, affinity)
	}
	total := map[boundUnit]int64{}
	constrains, boundedAny := false, false
	for _, atom := range atoms {
		one, err := ledgerReduceExpr(atom, column, affinity)
		if err != nil {
			return ledgerExprBounds{}, err
		}
		if one.nullOnly {
			// One null-only conjunct admits no non-null target value, so
			// the whole conjunction does.
			return ledgerExprBounds{nullOnly: true}, nil
		}
		constrains = constrains || one.constrainsTarget
		if len(one.bounds) == 0 {
			// A conjunct that bounds nothing (about other columns, or a
			// lower-only predicate) cannot narrow the intersection;
			// ignoring it cannot overstate an upper bound.
			continue
		}
		boundedAny = true
		for unit, value := range one.bounds {
			if current, ok := total[unit]; !ok || value < current {
				total[unit] = value
			}
		}
	}
	if !boundedAny {
		return ledgerExprBounds{constrainsTarget: constrains, unbounded: true}, nil
	}
	return ledgerExprBounds{constrainsTarget: constrains, bounds: total}, nil
}

// ledgerReduceAtom reduces one leaf predicate that carries no top-level OR
// or AND. It reports the target measurement, null-only admission, or an
// unconstrained (unbounded, no bounds) result.
func ledgerReduceAtom(expr, column string, affinity boundUnit) (ledgerExprBounds, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return ledgerExprBounds{}, fmt.Errorf("empty CHECK predicate")
	}
	if strings.HasPrefix(trimmed, "not ") || strings.HasPrefix(trimmed, "not(") {
		return ledgerExprBounds{}, fmt.Errorf("negated predicate %q is not analyzed", ledgerCollapseSpace(trimmed))
	}
	if trimmed == column+" is null" {
		return ledgerExprBounds{nullOnly: true}, nil
	}
	bounds := map[boundUnit]int64{}
	measured := false
	for _, occurrence := range ledgerFindMeasurements(trimmed, column) {
		measured = true
		unit := occurrence.unit
		if unit == boundCharacters {
			// length() follows the column's declared affinity.
			if affinity != boundCharacters && affinity != boundBytes {
				return ledgerExprBounds{}, fmt.Errorf("CHECK measures %s over an unsupported column affinity", column)
			}
			unit = affinity
		}
		upper, hasBound, err := ledgerComparatorUpper(trimmed, occurrence.end)
		if err != nil {
			return ledgerExprBounds{}, err
		}
		if !hasBound {
			continue // a lower-only predicate bounds nothing
		}
		if current, ok := bounds[unit]; !ok || upper < current {
			bounds[unit] = upper
		}
	}
	if !measured {
		// The predicate says nothing about the target column's length.
		return ledgerExprBounds{unbounded: true}, nil
	}
	return ledgerExprBounds{constrainsTarget: true, bounds: bounds}, nil
}

// ledgerParseCheckBody reduces one CHECK body into the per-unit upper
// bounds it enforces on non-null values of the column. A CHECK that never
// measures the column is ignored entirely. The body reduces recursively
// under ledgerReduceExpr; measured expressions without any upper bound fail
// closed, and a null-only result bounds nothing.
func ledgerParseCheckBody(body, column string, affinity boundUnit) ([]boundConstraint, error) {
	normalized := ledgerCollapseSpace(strings.ToLower(body))
	if len(ledgerFindMeasurements(normalized, column)) == 0 {
		return nil, nil
	}
	normalized = ledgerBetweenProtect.ReplaceAllString(normalized, "between $1..$2")
	reduced, err := ledgerReduceExpr(normalized, column, affinity)
	if err != nil {
		return nil, fmt.Errorf("CHECK %q: %w", normalized, err)
	}
	if reduced.nullOnly {
		return nil, nil
	}
	if len(reduced.bounds) == 0 {
		return nil, fmt.Errorf("CHECK %q measures %s without any upper bound", normalized, column)
	}
	constraints := make([]boundConstraint, 0, len(reduced.bounds))
	for unit, value := range reduced.bounds {
		// A character bound in SQL comes from length() over a TEXT
		// affinity: it measures the NUL-terminated prefix, never the
		// complete stored value.
		constraints = append(constraints, boundConstraint{unit: unit, value: value, prefix: unit == boundCharacters, from: "sqlite CHECK " + normalized})
	}
	sort.Slice(constraints, func(i, j int) bool { return constraints[i].unit < constraints[j].unit })
	return constraints, nil
}

// parseLedgerColumnBounds reads the final DDL of one table and returns every
// CHECK bound that constrains the column. SQL comments are stripped first:
// sqlite_master retains them verbatim, and their apostrophes would corrupt
// every quote-tracking scan below. All returned constraints apply
// simultaneously: their intersection is the column's bound.
func parseLedgerColumnBounds(ddl, table, column string) ([]boundConstraint, error) {
	stripped, err := ledgerStripSQLComments(ddl)
	if err != nil {
		return nil, fmt.Errorf("table %s: %w", table, err)
	}
	open := strings.Index(stripped, "(")
	if open < 0 {
		return nil, fmt.Errorf("table %s has no parsable CREATE TABLE body", table)
	}
	inner, _, ok := ledgerParenSpan(stripped, open)
	if !ok {
		return nil, fmt.Errorf("table %s has an unbalanced CREATE TABLE body", table)
	}
	defs := ledgerSplitTopLevel(inner, ',')
	colLower := strings.ToLower(column)
	affinities := map[string]boundUnit{}
	foundColumn := false
	for _, def := range defs {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}
		if name, rest, isColumn := ledgerColumnDefName(def); isColumn {
			if strings.EqualFold(name, column) {
				foundColumn = true
			}
			affinity, err := ledgerAffinity(rest)
			if err != nil {
				affinity = ""
			}
			affinities[strings.ToLower(name)] = affinity
		}
	}
	var constraints []boundConstraint
	for _, def := range defs {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}
		for _, check := range ledgerCheckBodies(def) {
			parsed, err := ledgerParseCheckBody(check, colLower, affinities[colLower])
			if err != nil {
				return nil, fmt.Errorf("table %s: %w", table, err)
			}
			constraints = append(constraints, parsed...)
		}
	}
	if !foundColumn {
		return nil, fmt.Errorf("column %s is not declared in the final DDL of table %s", column, table)
	}
	if len(constraints) == 0 {
		return nil, fmt.Errorf("no finite length or array bound is declared for %s.%s in the final DDL", table, column)
	}
	return constraints, nil
}

// ---------------------------------------------------------------------------
// Evaluated bounds, unit conversion, composites
// ---------------------------------------------------------------------------

// boundConstraint is one numeric limit in one unit that applies to a value.
// prefix marks a SQLite length() over TEXT: it measures only the
// NUL-terminated prefix, so it is the CHECK's text but never a full-string
// producer proof on its own.
type boundConstraint struct {
	unit   boundUnit
	value  int64
	from   string
	prefix bool
}

// evaluatedBound is the evaluated form of an outputBoundSource: either
// unbounded, or a set of simultaneous constraints whose intersection is the
// real capacity. nulPrefix marks a source whose only string limits are TEXT
// prefix bounds: the stored value's full length stays unproved, so the
// source offers no finite capacity in any unit.
type evaluatedBound struct {
	unbounded    bool
	nulPrefix    bool
	constraints  []boundConstraint
	kind         string
	describe     string
	producer     string
	refusalPath  string
	refusalPaths []string
	inferred     string
}

// capacity reports the largest value the bound admits, expressed in the
// requested unit. Sound conversion: bytes N imply at most N code points, and
// characters N imply at most utf8.UTFMax*N bytes. Items never convert into
// or out of string units. A NUL-prefix source carries no finite capacity:
// SQLite length(TEXT) stops at the first NUL, so the CHECK admits values of
// unbounded full length, and a prefix constraint is skipped even beside a
// full-byte constraint because it never caps the complete value.
func (e evaluatedBound) capacity(unit boundUnit) (int64, bool) {
	if e.unbounded || e.nulPrefix || len(e.constraints) == 0 {
		return 0, false
	}
	best, first := int64(0), true
	for _, constraint := range e.constraints {
		if constraint.prefix {
			continue
		}
		var converted int64
		switch {
		case constraint.unit == unit:
			converted = constraint.value
		case constraint.unit == boundCharacters && unit == boundBytes:
			converted = constraint.value * int64(utf8.UTFMax)
		case constraint.unit == boundBytes && unit == boundCharacters:
			converted = constraint.value
		default:
			return 0, false
		}
		if first || converted < best {
			best, first = converted, false
		}
	}
	if first {
		return 0, false
	}
	return best, true
}

// combineComposite reduces max/sum members into one bound, enforcing the
// composite rules: no items/string mixing, sound conversion, and unbounded
// propagation. Members are ALTERNATIVE writers: no unit agreement is demanded
// between them, because a byte guard on one writer and a character guard on
// another legitimately contribute different per-unit capacities. Guards of
// the same value that must agree use combinePaired instead.
func combineComposite(kind string, members []evaluatedBound) (evaluatedBound, error) {
	if len(members) == 0 {
		return evaluatedBound{}, fmt.Errorf("%s composition carries no member sources", kind)
	}
	describe := make([]string, 0, len(members))
	anyItems, anyString, unbounded := false, false, false
	var itemCaps, byteCaps, charCaps []int64
	var refusalPaths []string
	for _, member := range members {
		describe = append(describe, member.describe)
		hasItems, hasString := false, false
		for _, constraint := range member.constraints {
			if constraint.unit == boundItems {
				hasItems = true
			} else {
				hasString = true
			}
		}
		if hasItems && hasString {
			return evaluatedBound{}, fmt.Errorf("%s member %q mixes items with string units", kind, member.describe)
		}
		switch {
		case member.unbounded:
			unbounded = true
			refusalPaths = append(refusalPaths, member.refusalPaths...)
		case hasItems:
			anyItems = true
			capacity, ok := member.capacity(boundItems)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("%s member %q has no item capacity", kind, member.describe)
			}
			itemCaps = append(itemCaps, capacity)
		default:
			anyString = true
			bytes, ok := member.capacity(boundBytes)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("%s member %q has no byte capacity", kind, member.describe)
			}
			characters, ok := member.capacity(boundCharacters)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("%s member %q has no character capacity", kind, member.describe)
			}
			byteCaps = append(byteCaps, bytes)
			charCaps = append(charCaps, characters)
		}
	}
	if anyItems && anyString {
		return evaluatedBound{}, fmt.Errorf("%s composition mixes items with string units; items are not a string unit", kind)
	}
	combine := func(values []int64) int64 {
		total := values[0]
		for _, value := range values[1:] {
			if kind == "sum" {
				total += value
			} else if value > total {
				total = value
			}
		}
		return total
	}
	label := kind + " over [" + strings.Join(describe, "; ") + "]"
	if unbounded {
		return evaluatedBound{unbounded: true, kind: kind, describe: label, refusalPaths: refusalPaths}, nil
	}
	if anyItems {
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundItems, value: combine(itemCaps), from: label}},
			kind:        kind,
			describe:    label,
		}, nil
	}
	return evaluatedBound{
		constraints: []boundConstraint{
			{unit: boundBytes, value: combine(byteCaps), from: label},
			{unit: boundCharacters, value: combine(charCaps), from: label},
		},
		kind:     kind,
		describe: label,
	}, nil
}

// combinePaired reduces paired sources into one bound: guards of the
// SAME value whose constraints all apply simultaneously, so the composite is
// their intersection. A bare Go constant is refused as a member: its declared
// unit is not evidence of how the guard measures, so a guard whose
// measurement changed could never fail the pair. Each Go guard's unit must
// appear among every SQLite member's CHECK units; where a column carries
// both a byte and a character CHECK, both apply as intersection.
func combinePaired(members []evaluatedBound) (evaluatedBound, error) {
	if len(members) == 0 {
		return evaluatedBound{}, fmt.Errorf("paired composition carries no member sources")
	}
	describe := make([]string, 0, len(members))
	anyItems, anyString, unbounded := false, false, false
	var goUnits []boundUnit
	var sqliteConstraints [][]boundConstraint
	var constraints []boundConstraint
	for _, member := range members {
		describe = append(describe, member.describe)
		if member.unbounded {
			unbounded = true
		}
		if member.nulPrefix {
			return evaluatedBound{}, fmt.Errorf("paired member %q carries only a TEXT prefix bound, which proves no full-string capacity", member.describe)
		}
		if member.kind == "go_const" {
			return evaluatedBound{}, fmt.Errorf("paired member %q is a bare constant whose declared unit cannot detect a changed guard measurement; cite the guard implementation", member.describe)
		}
		hasItems, hasString := false, false
		for _, constraint := range member.constraints {
			if constraint.unit == boundItems {
				hasItems = true
			} else {
				hasString = true
			}
		}
		if hasItems && hasString {
			return evaluatedBound{}, fmt.Errorf("paired member %q mixes items with string units", member.describe)
		}
		switch {
		case hasItems:
			anyItems = true
		case hasString:
			anyString = true
		}
		switch member.kind {
		case "go_call":
			for _, constraint := range member.constraints {
				goUnits = append(goUnits, constraint.unit)
			}
		case "sqlite":
			sqliteConstraints = append(sqliteConstraints, member.constraints)
		}
		constraints = append(constraints, member.constraints...)
	}
	if anyItems && anyString {
		return evaluatedBound{}, fmt.Errorf("paired composition mixes items with string units; items are not a string unit")
	}
	for _, goUnit := range goUnits {
		for _, checkUnits := range sqliteConstraints {
			agree := false
			for _, constraint := range checkUnits {
				if constraint.unit == goUnit {
					agree = true
				}
			}
			if !agree {
				return evaluatedBound{}, fmt.Errorf("paired Go and SQLite guards disagree on count units: the Go guard counts %s but the SQLite column declares %v", goUnit, ledgerConstraintUnits(checkUnits))
			}
		}
	}
	label := "paired intersection over [" + strings.Join(describe, "; ") + "]"
	if unbounded {
		return evaluatedBound{unbounded: true, kind: "paired", describe: label}, nil
	}
	for i := range constraints {
		constraints[i].from = label
	}
	return evaluatedBound{constraints: constraints, kind: "paired", describe: label}, nil
}

func ledgerConstraintUnits(constraints []boundConstraint) []string {
	units := make([]string, 0, len(constraints))
	for _, constraint := range constraints {
		units = append(units, string(constraint.unit))
	}
	sort.Strings(units)
	return units
}

// ---------------------------------------------------------------------------
// Source evaluation
// ---------------------------------------------------------------------------

func keywordUnit(keyword string) (boundUnit, bool) {
	for _, kw := range ledgerBoundKeywords {
		if kw.keyword == keyword {
			return kw.unit, true
		}
	}
	return "", false
}

// nodeConstraints reads every bound keyword one schema node carries, with the
// cited keyword required to be present. Both guards of a node apply: their
// intersection is the source, never the character bound alone.
func nodeConstraints(node map[string]any, cited, from string) ([]boundConstraint, error) {
	if _, ok := node[cited]; !ok {
		return nil, fmt.Errorf("schema node %s carries no %s keyword", from, cited)
	}
	var constraints []boundConstraint
	for _, kw := range ledgerBoundKeywords {
		raw, ok := node[kw.keyword]
		if !ok {
			continue
		}
		value, err := numberValue(raw)
		if err != nil {
			return nil, fmt.Errorf("schema node %s: %w", from, err)
		}
		constraints = append(constraints, boundConstraint{unit: kw.unit, value: value, from: from})
	}
	return constraints, nil
}

func splitBoundPointer(pointer string) (parent string, keyword string, err error) {
	if !strings.HasPrefix(pointer, "/") {
		return "", "", fmt.Errorf("pointer %q is not an absolute JSON pointer", pointer)
	}
	idx := strings.LastIndex(pointer, "/")
	if idx <= 0 {
		return "", "", fmt.Errorf("pointer %q does not end in a bound keyword", pointer)
	}
	parent, keyword = pointer[:idx], pointer[idx+1:]
	if _, ok := keywordUnit(keyword); !ok {
		return "", "", fmt.Errorf("pointer %q does not end in a bound keyword", pointer)
	}
	return parent, keyword, nil
}

// evaluateSource evaluates one registration source against the live surfaces.
// Every unknown, ambiguous, or unsupported source fails closed.
func (rt *ledgerRuntime) evaluateSource(src outputBoundSource, census *boundCensus) (evaluatedBound, error) {
	switch src.kind {
	case "derived":
		if src.derive == nil {
			return evaluatedBound{}, fmt.Errorf("derived source has no evaluator")
		}
		return src.derive(rt, census)
	case "input", "schema":
		parent, keyword, err := splitBoundPointer(src.pointer)
		if err != nil {
			return evaluatedBound{}, err
		}
		key := censusKey(src.file, src.pointer)
		// A payload node is a producer source only where admission enforces
		// it; an output-only node is output validation, never a source.
		if src.kind == "input" || src.file == payloadSchemaFile {
			if census == nil {
				return evaluatedBound{}, fmt.Errorf("%s source %s cannot be evaluated without the census", src.kind, src.pointer)
			}
			if !census.inputs[key] {
				return evaluatedBound{}, fmt.Errorf("%s pointer %s is not input-reachable from the manifest input roots", src.kind, src.pointer)
			}
		}
		document, err := rt.loadDoc(src.file)
		if err != nil {
			return evaluatedBound{}, err
		}
		parentNode, ok := resolveSchemaPointer(document, parent)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("schema node %s does not resolve in %s", parent, src.file)
		}
		node, ok := parentNode.(map[string]any)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("schema node %s in %s is not an object", parent, src.file)
		}
		constraints, err := nodeConstraints(node, keyword, fmt.Sprintf("%s#%s", src.file, src.pointer))
		if err != nil {
			return evaluatedBound{}, err
		}
		describe := fmt.Sprintf("%s bound %s#%s", src.kind, src.file, src.pointer)
		return evaluatedBound{constraints: constraints, kind: src.kind, describe: describe}, nil
	case "go_call":
		if src.goFile == "" || src.goFunction == "" || src.goCallee == "" {
			return evaluatedBound{}, fmt.Errorf("go call source names no file, function, or callee")
		}
		if strings.Contains(src.goCallee, ".") {
			return evaluatedBound{}, fmt.Errorf("qualified guard %s has no proven package or receiver identity", src.goCallee)
		}
		if src.unit != boundItems && src.unit != boundBytes && src.unit != boundCharacters {
			return evaluatedBound{}, fmt.Errorf("go call source declares unit %q", src.unit)
		}
		goS, err := rt.loadGo(src.goFile)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, src.goFunction)
		if len(fns) == 0 {
			return evaluatedBound{}, fmt.Errorf("function %s is not declared in %s", src.goFunction, src.goFile)
		}
		if len(fns) > 1 {
			return evaluatedBound{}, fmt.Errorf("function %s is declared %d times in %s", src.goFunction, len(fns), src.goFile)
		}
		var matched []*ast.CallExpr
		if fns[0].Body != nil {
			ast.Inspect(fns[0].Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || ledgerCalledName(call.Fun) != src.goCallee {
					return true
				}
				if src.goField != "" && !ledgerCallHasFieldLiteral(call, src.goField) {
					return true
				}
				matched = append(matched, call)
				return true
			})
		}
		if len(matched) == 0 {
			return evaluatedBound{}, fmt.Errorf("no %s call%s matches in %s:%s", src.goCallee, ledgerFieldSuffix(src.goField), src.goFile, src.goFunction)
		}
		if len(matched) > 1 {
			return evaluatedBound{}, fmt.Errorf("%d %s calls match in %s:%s; the source is ambiguous", len(matched), src.goCallee, src.goFile, src.goFunction)
		}
		call := matched[0]
		if src.goArgument < 0 || src.goArgument >= len(call.Args) {
			return evaluatedBound{}, fmt.Errorf("argument %d does not exist on the matched %s call in %s:%s", src.goArgument, src.goCallee, src.goFile, src.goFunction)
		}
		value, err := rt.evalNumeric(goS, fns[0], call.Args[src.goArgument])
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("argument %d of %s in %s:%s: %w", src.goArgument, src.goCallee, src.goFile, src.goFunction, err)
		}
		inferred := ledgerScanMeasurement(call)
		if inferred == "" {
			inferred = rt.inferFromCallee(src.goFile, src.goCallee)
		}
		if err := reconcileGoUnit(src.unit, inferred); err != nil {
			return evaluatedBound{}, fmt.Errorf("%s:%s: %w", src.goFile, src.goFunction, err)
		}
		describe := fmt.Sprintf("go guard %s:%s %s(argument %d%s) = %d %s", src.goFile, src.goFunction, src.goCallee, src.goArgument, ledgerFieldSuffix(src.goField), value, src.unit)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: src.unit, value: value, from: describe}},
			kind:        "go_call",
			describe:    describe,
			inferred:    inferred,
		}, nil
	case "go_const":
		if src.goFile == "" || src.symbol == "" {
			return evaluatedBound{}, fmt.Errorf("go constant source names no file or symbol")
		}
		goS, err := rt.loadGo(src.goFile)
		if err != nil {
			return evaluatedBound{}, err
		}
		value, err := rt.evalNumeric(goS, nil, &ast.Ident{Name: src.symbol})
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("%s:%s: %w", src.goFile, src.symbol, err)
		}
		describe := fmt.Sprintf("go const %s:%s = %d %s", src.goFile, src.symbol, value, src.unit)
		return evaluatedBound{constraints: []boundConstraint{{unit: src.unit, value: value, from: describe}}, kind: "go_const", describe: describe}, nil
	case "sqlite":
		if src.sqlTable == "" || src.sqlColumn == "" {
			return evaluatedBound{}, fmt.Errorf("sqlite source names no table or column")
		}
		if rt.ddl == nil {
			return evaluatedBound{}, fmt.Errorf("the final migrated DDL is unavailable to the ledger runtime")
		}
		createSQL, ok := rt.ddl[src.sqlTable]
		if !ok {
			return evaluatedBound{}, fmt.Errorf("table %s is not in the final migrated schema", src.sqlTable)
		}
		constraints, err := parseLedgerColumnBounds(createSQL, src.sqlTable, src.sqlColumn)
		if err != nil {
			return evaluatedBound{}, err
		}
		describe := fmt.Sprintf("sqlite %s.%s bounded by [%s]", src.sqlTable, src.sqlColumn, ledgerConstraintFroms(constraints))
		hasFullBytes, prefixCharacters := false, false
		for _, constraint := range constraints {
			if constraint.unit == boundBytes {
				hasFullBytes = true
			}
			if constraint.prefix {
				prefixCharacters = true
			}
		}
		if prefixCharacters && !hasFullBytes {
			// SQLite length(TEXT) stops at the first NUL: the CHECK bounds
			// only the prefix, so the full-string length stays unproved. The
			// source honestly carries no finite capacity; a finite row needs
			// a full-byte CHECK or the Go admission instead.
			describe += " (TEXT length() stops at NUL: the CHECK bounds only the NUL-free prefix; cite a full-byte CHECK or the Go admission for the full string)"
			return evaluatedBound{nulPrefix: true, constraints: constraints, kind: "sqlite", describe: describe}, nil
		}
		return evaluatedBound{constraints: constraints, kind: "sqlite", describe: describe}, nil
	case "max", "sum":
		var members []evaluatedBound
		for i, member := range src.members {
			evaluated, err := rt.evaluateSource(member, census)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("%s member %d: %w", src.kind, i, err)
			}
			members = append(members, evaluated)
		}
		return combineComposite(src.kind, members)
	case "paired":
		members := make([]evaluatedBound, 0, len(src.members))
		for i, member := range src.members {
			evaluated, err := rt.evaluateSource(member, census)
			if err != nil {
				return evaluatedBound{}, fmt.Errorf("paired member %d: %w", i, err)
			}
			members = append(members, evaluated)
		}
		return combinePaired(members)
	case "unbounded":
		if strings.TrimSpace(src.producer) == "" {
			return evaluatedBound{}, fmt.Errorf("unbounded row carries no producer explanation")
		}
		paths := src.refusalPaths
		if len(paths) == 0 {
			return evaluatedBound{}, fmt.Errorf("unbounded row cites no typed refusal path")
		}
		for _, path := range paths {
			if err := checkLedgerRefusalPath(rt, path); err != nil {
				return evaluatedBound{}, err
			}
		}
		return evaluatedBound{
			unbounded:    true,
			kind:         "unbounded",
			describe:     fmt.Sprintf("unbounded, byte-ceiling only: %s (typed refusals: %s)", src.producer, strings.Join(paths, "; ")),
			producer:     src.producer,
			refusalPath:  strings.Join(paths, "; "),
			refusalPaths: paths,
		}, nil
	case "unproved":
		gap := strings.TrimSpace(src.producer)
		if gap == "" {
			return evaluatedBound{}, fmt.Errorf("unproved row carries no producer gap text")
		}
		return evaluatedBound{}, fmt.Errorf("unproved producer: %s", gap)
	default:
		detail := fmt.Sprintf("unknown output bound source kind %q", src.kind)
		if gap := strings.TrimSpace(src.producer); gap != "" {
			detail += "; the row's saved producer gap reads: " + gap
		}
		return evaluatedBound{}, fmt.Errorf("%s", detail)
	}
}

func ledgerFieldSuffix(field string) string {
	if field == "" {
		return ""
	}
	return fmt.Sprintf(", field %q", field)
}

func ledgerConstraintFroms(constraints []boundConstraint) string {
	froms := make([]string, 0, len(constraints))
	for _, constraint := range constraints {
		froms = append(froms, constraint.from)
	}
	return strings.Join(froms, "; ")
}

// ---------------------------------------------------------------------------
// Census
// ---------------------------------------------------------------------------

type censusBound struct {
	file    string
	pointer string
	keyword string
	unit    boundUnit
	value   int64
	owners  []string
	classes []string // manifest operations[].kind of every owning operation
}

type boundCensus struct {
	bounds map[string]*censusBound
	nodes  map[string][]string // output-reachable node key -> owning operation classes
	inputs map[string]bool
	// inputNodes are the schema nodes an input root reaches; an uncapped
	// node there widens admission, whatever an output row claims.
	inputNodes map[string]bool
	order      []string
}

type censusRootRef struct {
	direction string // input, output
	file      string
	pointer   string
	owner     string
	kind      string // manifest operations[].kind, empty for registered roots
}

type censusRootRefRaw struct {
	direction string
	ref       string
	owner     string
	kind      string
}

// resolveManifestRef follows the manifest's schema alias chain to a document
// and pointer, exactly as the inventory census does.
func resolveManifestRef(manifest map[string]any, ref string, depth int) (string, string, error) {
	if depth > 8 {
		return "", "", fmt.Errorf("manifest schema alias chain for %q is too deep", ref)
	}
	if strings.HasPrefix(ref, "#/schemas/") {
		name := strings.TrimPrefix(ref, "#/schemas/")
		schemas, _ := manifest["schemas"].(map[string]any)
		entry, ok := schemas[name].(map[string]any)
		if !ok {
			return "", "", fmt.Errorf("manifest carries no schemas/%s entry", name)
		}
		next, _ := entry["ref"].(string)
		if next == "" {
			return "", "", fmt.Errorf("manifest schemas/%s carries no ref", name)
		}
		return resolveManifestRef(manifest, next, depth+1)
	}
	idx := strings.Index(ref, "#")
	if idx < 0 {
		return "", "", fmt.Errorf("manifest schema ref %q names no document pointer", ref)
	}
	file := ref[:idx]
	if file == "" {
		file = payloadSchemaFile
	}
	pointer := ref[idx+1:]
	if !strings.HasPrefix(pointer, "/") {
		return "", "", fmt.Errorf("manifest schema ref %q carries no JSON pointer", ref)
	}
	return file, pointer, nil
}

func scanManifestRoots(value any, owner, kind string, refs *[]censusRootRefRaw) {
	switch node := value.(type) {
	case map[string]any:
		local, localKind := owner, kind
		if id, ok := node["id"].(string); ok && id != "" {
			local = id
		}
		if operationKind, ok := node["kind"].(string); ok && operationKind != "" {
			localKind = operationKind
		}
		for key, child := range node {
			switch key {
			case "input_schema", "result_schema", "output_schema":
				ref, ok := child.(string)
				if !ok {
					continue
				}
				direction := "output"
				if key == "input_schema" {
					direction = "input"
				}
				*refs = append(*refs, censusRootRefRaw{direction: direction, ref: ref, owner: local, kind: localKind})
			default:
				scanManifestRoots(child, local, localKind, refs)
			}
		}
	case []any:
		for _, child := range node {
			scanManifestRoots(child, owner, kind, refs)
		}
	}
}

// buildCensus walks every output root (manifest-owned plus explicitly
// registered host/internal nodes) and every input root, recording each output
// bound keyword pointer once with the union of its consuming operations, and
// each input-reachable bound pointer for inputBound verification. The bound
// count is computed, never frozen.
func buildCensus(rt *ledgerRuntime, extraRoots, inputRoots []censusRootRequest) (*boundCensus, []ledgerFailure) {
	var failures []ledgerFailure
	manifest, err := rt.loadDoc(surfaceManifestFile)
	if err != nil {
		return nil, []ledgerFailure{{Kind: "census", Subject: surfaceManifestFile, Detail: err.Error()}}
	}
	var raw []censusRootRefRaw
	scanManifestRoots(manifest, "manifest", "", &raw)
	var roots []censusRootRef
	for _, entry := range raw {
		file, pointer, err := resolveManifestRef(manifest, entry.ref, 0)
		if err != nil {
			failures = append(failures, ledgerFailure{Kind: "census", Subject: entry.ref, Detail: err.Error()})
			continue
		}
		roots = append(roots, censusRootRef{direction: entry.direction, file: file, pointer: pointer, owner: entry.owner, kind: entry.kind})
	}
	for _, extra := range extraRoots {
		roots = append(roots, censusRootRef{direction: "output", file: extra.file, pointer: extra.pointer, owner: "registered output root"})
	}
	for _, input := range inputRoots {
		roots = append(roots, censusRootRef{direction: "input", file: input.file, pointer: input.pointer, owner: "registered input root"})
	}
	census := &boundCensus{bounds: map[string]*censusBound{}, nodes: map[string][]string{}, inputs: map[string]bool{}, inputNodes: map[string]bool{}}
	for _, root := range roots {
		census.walkRoot(rt, root, &failures)
	}
	sort.Strings(census.order)
	for _, key := range census.order {
		sort.Strings(census.bounds[key].owners)
		sort.Strings(census.bounds[key].classes)
	}
	return census, failures
}

// ledgerPayloadInputRoots are the payload schema definitions every registered
// workflow definition version and the canonical recovery actions declare.
// Action payload admission validates every declared field against its schema
// in the published document (workflow_preflight.go), so each is an input root
// exactly like a manifest input, though the manifest does not list it. A live
// call validates against the published document whatever the pin's age
// (workflowLivePayloadSchemaDocument), so retained versions count too. A
// declared name the published document no longer carries refuses every live
// call (validateWorkflowPayloadSchema), so it admits nothing and roots nothing.
func ledgerPayloadInputRoots() []censusRootRequest {
	names := map[string]bool{}
	add := func(action WorkflowActionDefinition) {
		for _, field := range action.Payload.Fields {
			for _, ref := range []string{field.SchemaRef, field.ItemRef} {
				if ref != "" {
					names[ref] = true
				}
			}
		}
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
			add(action)
		}
	}
	for _, action := range BuiltinWorkflowCanonicalRecoveryActionDefinitions() {
		add(action)
	}
	sorted := make([]string, 0, len(names))
	document := workflowLivePayloadSchemaDocument()
	for name := range names {
		if payloadschema.HasIn(document, name) {
			sorted = append(sorted, name)
		}
	}
	sort.Strings(sorted)
	roots := make([]censusRootRequest, 0, len(sorted))
	for _, name := range sorted {
		roots = append(roots, censusRootRequest{file: payloadSchemaFile, pointer: "/$defs/" + name})
	}
	return roots
}

// ledgerAppendClass unions one operation class into a node's or bound's
// class set, skipping empty kinds (registered roots carry no class).
func ledgerAppendClass(classes []string, kind string) []string {
	if kind == "" || ledgerContainsString(classes, kind) {
		return classes
	}
	return append(classes, kind)
}

// ledgerUnionClasses merges two class sets.
func ledgerUnionClasses(a, b []string) []string {
	merged := a
	for _, kind := range b {
		merged = ledgerAppendClass(merged, kind)
	}
	return merged
}

func (c *boundCensus) walkRoot(rt *ledgerRuntime, root censusRootRef, failures *[]ledgerFailure) {
	document, err := rt.loadDoc(root.file)
	if err != nil {
		*failures = append(*failures, ledgerFailure{Kind: "census", Subject: root.file, Detail: err.Error()})
		return
	}
	node, ok := resolveSchemaPointer(document, root.pointer)
	if !ok {
		*failures = append(*failures, ledgerFailure{Kind: "census", Subject: root.file + "#" + root.pointer, Detail: "census root does not resolve"})
		return
	}
	c.walk(rt, root, root.file, node, root.pointer, map[string]bool{}, failures)
}

// ledgerUnsupportedSchemaKeywords are the schema-bearing keywords the census
// knows but does not walk: a node carrying one fails the census closed,
// because walking past it would silently miss the bounds under it. Anything
// outside both this list and the walked wrappers is unknown to the harness;
// JSON Schema allows unknown keywords, so coverage honesty rests on this
// list being extended when the schema vocabulary grows.
var ledgerUnsupportedSchemaKeywords = []string{"contentSchema"}

func (c *boundCensus) walk(rt *ledgerRuntime, root censusRootRef, file string, node any, pointer string, stack map[string]bool, failures *[]ledgerFailure) {
	key := censusKey(file, pointer)
	if stack[key] {
		return
	}
	object, ok := node.(map[string]any)
	if !ok {
		return
	}
	stack[key] = true
	defer delete(stack, key)
	if root.direction == "input" {
		c.inputNodes[key] = true
	}
	if root.direction == "output" {
		c.nodes[key] = ledgerAppendClass(c.nodes[key], root.kind)
		for _, keyword := range ledgerUnsupportedSchemaKeywords {
			if _, ok := object[keyword]; ok {
				*failures = append(*failures, ledgerFailure{
					Kind:    "census",
					Subject: key,
					Detail:  fmt.Sprintf("schema-bearing keyword %q is not walked; the census would silently miss bounds under it", keyword),
				})
			}
		}
	}
	for _, kw := range ledgerBoundKeywords {
		raw, ok := object[kw.keyword]
		if !ok {
			continue
		}
		value, err := numberValue(raw)
		if err != nil {
			*failures = append(*failures, ledgerFailure{Kind: "census", Subject: joinPointerToken(pointer, kw.keyword), Detail: err.Error()})
			continue
		}
		boundKey := censusKey(file, joinPointerToken(pointer, kw.keyword))
		if root.direction == "input" {
			c.inputs[boundKey] = true
			continue
		}
		bound, seen := c.bounds[boundKey]
		if !seen {
			bound = &censusBound{file: file, pointer: joinPointerToken(pointer, kw.keyword), keyword: kw.keyword, unit: kw.unit, value: value}
			c.bounds[boundKey] = bound
			c.order = append(c.order, boundKey)
		}
		if !ledgerContainsString(bound.owners, root.owner) {
			bound.owners = append(bound.owners, root.owner)
		}
		bound.classes = ledgerAppendClass(bound.classes, root.kind)
	}
	if ref, ok := object["$ref"].(string); ok {
		refFile, refPointer, err := rt.resolveSchemaRef(file, ref)
		if err != nil {
			*failures = append(*failures, ledgerFailure{Kind: "census", Subject: key, Detail: err.Error()})
		} else {
			target, targetErr := rt.loadDoc(refFile)
			if targetErr != nil {
				*failures = append(*failures, ledgerFailure{Kind: "census", Subject: refFile, Detail: targetErr.Error()})
			} else if resolved, ok := resolveSchemaPointer(target, refPointer); ok {
				c.walk(rt, root, refFile, resolved, refPointer, stack, failures)
			}
		}
	}
	for _, wrapper := range []string{"properties", "patternProperties", "dependentSchemas"} {
		if children, ok := object[wrapper].(map[string]any); ok {
			for name, child := range children {
				c.walk(rt, root, file, child, joinPointerToken(joinPointerToken(pointer, wrapper), escapePointerToken(name)), stack, failures)
			}
		}
	}
	for _, wrapper := range []string{"items", "additionalProperties", "contains", "not", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems", "propertyNames"} {
		if child, ok := object[wrapper].(map[string]any); ok {
			c.walk(rt, root, file, child, joinPointerToken(pointer, wrapper), stack, failures)
		}
	}
	for _, wrapper := range []string{"oneOf", "anyOf", "allOf", "prefixItems"} {
		children, ok := object[wrapper].([]any)
		if !ok {
			continue
		}
		for index, child := range children {
			c.walk(rt, root, file, child, joinPointerToken(joinPointerToken(pointer, wrapper), strconv.Itoa(index)), stack, failures)
		}
	}
}

func (rt *ledgerRuntime) resolveSchemaRef(fromFile, ref string) (string, string, error) {
	if strings.Contains(ref, "://") {
		return "", "", fmt.Errorf("external $ref %q leaves the census unclosed", ref)
	}
	if strings.HasPrefix(ref, "#") {
		return fromFile, strings.TrimPrefix(ref, "#"), nil
	}
	idx := strings.Index(ref, "#")
	if idx < 0 {
		return "", "", fmt.Errorf("external $ref %q leaves the census unclosed", ref)
	}
	file := ref[:idx]
	if file == "" {
		file = payloadSchemaFile
	}
	return file, ref[idx+1:], nil
}

func ledgerContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Ledger run
// ---------------------------------------------------------------------------

// ledgerFailure is one closed-coverage violation. Kind is one of: census,
// registration-invalid, duplicate-registration, stale-registration,
// removed-finite-bound, restored-cap, missing-registration,
// bound-not-admitted, unit-domain, refusal-class, source-evaluation, ceiling.
type ledgerFailure struct {
	Kind    string
	Subject string
	Detail  string
}

func (f ledgerFailure) String() string {
	return f.Kind + " " + f.Subject + ": " + f.Detail
}

// ledgerRefusalClassFailure checks one unbounded row against the output
// classes that own its bound or node: every owning class requires its exact
// canonical emitter among the author-cited refusal paths. The harness never
// invents a citation, so a missing class fails the row until the author adds
// the second path.
func ledgerRefusalClassFailure(pattern string, classes, paths []string) (ledgerFailure, bool) {
	var gaps []string
	for _, class := range classes {
		want := ledgerClassRefusalPath(class)
		if want == "" {
			gaps = append(gaps, fmt.Sprintf("class %q has no canonical emitter registered", class))
			continue
		}
		cited := false
		for _, path := range paths {
			if path == want {
				cited = true
				break
			}
		}
		if !cited {
			gaps = append(gaps, fmt.Sprintf("class %q requires the exact emitter %s", class, want))
		}
	}
	if len(gaps) == 0 {
		return ledgerFailure{}, false
	}
	return ledgerFailure{
		Kind:    "refusal-class",
		Subject: pattern,
		Detail:  fmt.Sprintf("the unbounded row's cited refusals do not cover every owning output class (%s); cite each required emitter explicitly — the harness does not invent author citations", strings.Join(gaps, "; ")),
	}, true
}

// runOutputBoundLedger matches registrations against the census, evaluates
// every source, and returns every closure violation. Missing rows, stale
// patterns, duplicate claims, removed finite maxima (including a wildcard
// arm that loses its cap while another arm stays capped), restored caps on
// unbounded rows, unbounded rows whose cited refusals do not cover every
// owning manifest output class, and producer bounds above their consumer all
// fail here.
func runOutputBoundLedger(rt *ledgerRuntime, census *boundCensus, registrations []outputBoundRegistration) []ledgerFailure {
	var failures []ledgerFailure
	patternCount := map[string]int{}
	claims := map[string]int{}
	for i := range registrations {
		reg := &registrations[i]
		if reg.invalid != "" {
			failures = append(failures, ledgerFailure{Kind: "registration-invalid", Subject: reg.pattern, Detail: reg.invalid})
			continue
		}
		patternCount[reg.pattern]++
		_, patternPointer := splitLedgerPatternFile(reg.pattern)
		keyword := patternPointer[strings.LastIndex(patternPointer, "/")+1:]
		parent := ledgerParentPattern(reg.pattern)
		wildcardParent := strings.Contains(parent, "*")
		var matched []string
		for _, key := range census.order {
			if pointerMatches(reg.pattern, key) {
				matched = append(matched, key)
				claims[key]++
			}
		}
		// A wildcard parent's coverage is every output-reachable node the
		// parent pattern matches, not only the arms that still carry the
		// keyword: partial removal must stay visible.
		var parentNodes []string
		if wildcardParent {
			for key := range census.nodes {
				if pointerMatches(parent, key) {
					parentNodes = append(parentNodes, key)
				}
			}
			sort.Strings(parentNodes)
		}
		present := len(matched) > 0 || len(parentNodes) > 0
		if !present && !wildcardParent {
			_, present = census.nodes[parent]
		}
		if len(matched) == 0 && !present {
			failures = append(failures, ledgerFailure{Kind: "stale-registration", Subject: reg.pattern, Detail: "no output-reachable schema node underlies this pattern; the registered bound no longer exists"})
			continue
		}
		evaluated, err := rt.evaluateSource(reg.source, census)
		if err != nil {
			failures = append(failures, ledgerFailure{Kind: "source-evaluation", Subject: reg.pattern, Detail: err.Error()})
			continue
		}
		for _, key := range matched {
			if failure, failed := ledgerCompareBound(evaluated, census.bounds[key], reg.pattern); failed {
				failures = append(failures, failure)
			}
		}
		if evaluated.unbounded && len(matched) > 0 {
			var classes []string
			for _, key := range matched {
				classes = ledgerUnionClasses(classes, census.bounds[key].classes)
			}
			if failure, failed := ledgerRefusalClassFailure(reg.pattern, classes, evaluated.refusalPaths); failed {
				failures = append(failures, failure)
			}
		}
		if len(matched) == 0 && !wildcardParent && !evaluated.unbounded {
			failures = append(failures, ledgerFailure{Kind: "removed-finite-bound", Subject: reg.pattern, Detail: "the finite consumer maximum this row proves was removed; re-register the row as unbounded, byte-ceiling only with a typed refusal path, or restore the cap"})
		}
		// Every parent node a wildcard pattern covers must retain a finite
		// keyword while a finite row stands: one arm losing its cap fails the
		// row even while another arm stays capped. An unbounded alternative
		// keeps passing while the caps stay removed; any arm that keeps a cap
		// already failed as restored-cap above.
		for _, nodeKey := range parentNodes {
			boundKey := nodeKey + "/" + keyword
			if _, ok := census.bounds[boundKey]; ok {
				continue
			}
			if !evaluated.unbounded {
				failures = append(failures, ledgerFailure{
					Kind:    "removed-finite-bound",
					Subject: boundKey,
					Detail:  fmt.Sprintf("the wildcard row %s covers this parent node, which no longer carries %s; every matched parent node must retain a finite keyword while the row proves a finite maximum", reg.pattern, keyword),
				})
			}
		}
		if evaluated.unbounded && len(matched) == 0 {
			// An uncapped node an input root also reaches widens admission:
			// the shared definition needs an output-only twin instead.
			inputParents := []string{parent}
			if wildcardParent {
				inputParents = parentNodes
			}
			for _, nodeKey := range inputParents {
				if census.inputNodes[nodeKey] {
					failures = append(failures, ledgerFailure{
						Kind:    "input-widened",
						Subject: nodeKey,
						Detail:  fmt.Sprintf("the row %s registers this node unbounded, but an input root reaches it, so the missing cap widens admission; keep the cap on the input definition and point the output at an output-only twin", reg.pattern),
					})
				}
			}
			// The unbounded row's caps stay removed; its class evidence is
			// checked against the underlying output node, which carries the
			// owning operations' classes even without the bound keyword.
			var classes []string
			if wildcardParent {
				for _, nodeKey := range parentNodes {
					classes = ledgerUnionClasses(classes, census.nodes[nodeKey])
				}
			} else {
				classes = census.nodes[parent]
			}
			if failure, failed := ledgerRefusalClassFailure(reg.pattern, classes, evaluated.refusalPaths); failed {
				failures = append(failures, failure)
			}
		}
	}
	for pattern, count := range patternCount {
		if count > 1 {
			failures = append(failures, ledgerFailure{Kind: "duplicate-registration", Subject: pattern, Detail: fmt.Sprintf("the same pattern is registered %d times", count)})
		}
	}
	for _, key := range census.order {
		bound := census.bounds[key]
		switch claims[key] {
		case 0:
			failures = append(failures, ledgerFailure{
				Kind:    "missing-registration",
				Subject: key,
				Detail:  fmt.Sprintf("%s=%d %s is output-reachable via %s but carries no registerOutputBound row", bound.keyword, bound.value, bound.unit, strings.Join(bound.owners, ", ")),
			})
		case 1:
		default:
			failures = append(failures, ledgerFailure{Kind: "duplicate-registration", Subject: key, Detail: fmt.Sprintf("claimed by %d registrations; exactly one row may cover a bound", claims[key])})
		}
	}
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].Kind != failures[j].Kind {
			return failures[i].Kind < failures[j].Kind
		}
		if failures[i].Subject != failures[j].Subject {
			return failures[i].Subject < failures[j].Subject
		}
		return failures[i].Detail < failures[j].Detail
	})
	return failures
}

// ledgerCompareBound checks one producer evaluation against one consumer
// bound. An unbounded producer fails any matched consumer bound (a restored
// cap) and passes when its bound stays removed: the envelope byte ceiling,
// not a field cap, is its bound.
func ledgerCompareBound(producer evaluatedBound, consumer *censusBound, pattern string) (ledgerFailure, bool) {
	if producer.unbounded {
		return ledgerFailure{Kind: "restored-cap", Subject: consumer.pointer, Detail: fmt.Sprintf("the row is registered as unbounded, byte-ceiling only (%s) but the schema carries %s=%d %s again; remove the cap or re-prove a finite producer", producer.describe, consumer.keyword, consumer.value, consumer.unit)}, true
	}
	capacity, ok := producer.capacity(consumer.unit)
	if !ok {
		return ledgerFailure{Kind: "unit-domain", Subject: consumer.pointer, Detail: fmt.Sprintf("producer %s cannot be compared in %s units against %s=%d (pattern %s)", producer.describe, consumer.unit, consumer.keyword, consumer.value, pattern)}, true
	}
	if capacity > consumer.value {
		return ledgerFailure{Kind: "bound-not-admitted", Subject: consumer.pointer, Detail: fmt.Sprintf("output %s=%d %s is below producer capacity %d %s from %s (pattern %s)", consumer.keyword, consumer.value, consumer.unit, capacity, consumer.unit, producer.describe, pattern)}, true
	}
	return ledgerFailure{}, false
}

// ledgerCeilingFailures pins the host ceiling: the actual agent constant must
// equal the manifest's published envelope max_bytes, and both must equal the
// ruled 51200 bytes.
func ledgerCeilingFailures(rt *ledgerRuntime) []ledgerFailure {
	var failures []ledgerFailure
	manifest, err := rt.loadDoc(surfaceManifestFile)
	if err != nil {
		return []ledgerFailure{{Kind: "ceiling", Subject: surfaceManifestFile, Detail: err.Error()}}
	}
	envelope, _ := manifest["envelope"].(map[string]any)
	published, err := numberValue(envelope["max_bytes"])
	if err != nil {
		return []ledgerFailure{{Kind: "ceiling", Subject: surfaceManifestFile, Detail: "manifest envelope.max_bytes: " + err.Error()}}
	}
	goS, err := rt.loadGo(agentEnvelopeFile)
	if err != nil {
		return []ledgerFailure{{Kind: "ceiling", Subject: agentEnvelopeFile, Detail: err.Error()}}
	}
	actual, err := rt.evalNumeric(goS, nil, &ast.Ident{Name: agentEnvelopeCeilingConst})
	if err != nil {
		return []ledgerFailure{{Kind: "ceiling", Subject: agentEnvelopeFile + ":" + agentEnvelopeCeilingConst, Detail: err.Error()}}
	}
	if actual != published {
		failures = append(failures, ledgerFailure{Kind: "ceiling", Subject: agentEnvelopeFile + ":" + agentEnvelopeCeilingConst, Detail: fmt.Sprintf("agent constant is %d but the manifest envelope.max_bytes is %d", actual, published)})
	}
	if published != ruledEnvelopeCeilingBytes {
		failures = append(failures, ledgerFailure{Kind: "ceiling", Subject: surfaceManifestFile, Detail: fmt.Sprintf("envelope.max_bytes moved to %d; the output bound rule pins the ceiling at %d bytes", published, ruledEnvelopeCeilingBytes)})
	}
	return failures
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func ledgerRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve the ledger test file path")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

func ledgerHasFailure(failures []ledgerFailure, kind, subject string) bool {
	for _, failure := range failures {
		if failure.Kind == kind && failure.Subject == subject {
			return true
		}
	}
	return false
}

// TestOutputBoundLedgerClosure is the ledger's main gate. With an empty
// registration list it fails RED: every output-reachable bound is a missing
// source row until the family files register one.
func TestOutputBoundLedgerClosure(t *testing.T) {
	t.Parallel()
	root := ledgerRepoRoot(t)
	rt := newLedgerRuntime(root)
	s := openTemp(t)
	ddl, err := sqliteMasterDDL(s.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	rt.ddl = ddl
	census, censusFailures := buildCensus(rt, nil, ledgerPayloadInputRoots())
	if len(censusFailures) > 0 {
		details := make([]string, 0, len(censusFailures))
		for _, failure := range censusFailures {
			details = append(details, failure.String())
		}
		t.Fatalf("the output bound census is broken:\n%s", strings.Join(details, "\n"))
	}
	failures := runOutputBoundLedger(rt, census, ledgerRegistrations)
	failures = append(failures, ledgerCeilingFailures(rt)...)
	if len(failures) == 0 {
		return
	}
	byKind := map[string]int{}
	for _, failure := range failures {
		byKind[failure.Kind]++
	}
	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	summary := make([]string, 0, len(kinds)+len(failures)+4)
	for _, kind := range kinds {
		summary = append(summary, fmt.Sprintf("%s: %d", kind, byKind[kind]))
	}
	if byKind["missing-registration"] > 0 {
		definitions := map[string]int{}
		for _, failure := range failures {
			if failure.Kind != "missing-registration" {
				continue
			}
			definition := failure.Subject
			if rest, ok := strings.CutPrefix(definition, "/$defs/"); ok {
				definition = strings.SplitN(rest, "/", 2)[0]
			}
			definitions[definition]++
		}
		names := make([]string, 0, len(definitions))
		for name := range definitions {
			names = append(names, name)
		}
		sort.Strings(names)
		grouped := make([]string, 0, len(names))
		for _, name := range names {
			grouped = append(grouped, name+"="+strconv.Itoa(definitions[name]))
		}
		summary = append(summary, "missing rows by definition: "+strings.Join(grouped, " "))
	}
	for _, failure := range failures {
		summary = append(summary, "  "+failure.String())
	}
	summary = append(summary, "fix: register every output-reachable bound in the output_bound_*_sources_test.go family files via registerOutputBound; cite real input, store, or derivation sources, or unboundedOutput with a typed refusal path")
	t.Fatalf("output bound ledger is not closed: %d failure(s)\n%s", len(failures), strings.Join(summary, "\n"))
}

// TestLedgerRemovedCapWitnesses pins the mutation semantics on the real
// published document: an admitted finite row passes, a removed finite maximum
// fails, a removed unbounded maximum passes, a restored cap on an unbounded
// row fails, a vanished node is stale, duplicate claims fail, and a producer
// above its consumer fails.
func TestLedgerRemovedCapWitnesses(t *testing.T) {
	t.Parallel()
	const pattern = "/$defs/workflow_read/properties/conditions/maxItems"
	root := ledgerRepoRoot(t)
	original, err := newLedgerRuntime(root).loadDoc(payloadSchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	finiteAdmitted := goConstantBound("witness.go", "witnessAdmitted", boundItems)
	finiteExceeds := goConstantBound("witness.go", "witnessExceeds", boundItems)
	unboundedRow := unboundedOutput("workflow conditions accumulate one row per stored external condition with no finite producer guard", "internal/agent/runtime.go:readSizeRefusal")
	removeKeyword := func(doc map[string]any) {
		node := doc["$defs"].(map[string]any)["workflow_read"].(map[string]any)["properties"].(map[string]any)["conditions"].(map[string]any)
		delete(node, "maxItems")
	}
	restoreKeyword := func(doc map[string]any) {
		node := doc["$defs"].(map[string]any)["workflow_read"].(map[string]any)["properties"].(map[string]any)["conditions"].(map[string]any)
		node["maxItems"] = json.Number("100")
	}
	removeNode := func(doc map[string]any) {
		properties := doc["$defs"].(map[string]any)["workflow_read"].(map[string]any)["properties"].(map[string]any)
		delete(properties, "conditions")
	}
	run := func(mutate func(map[string]any), sources ...outputBoundSource) []ledgerFailure {
		registrations := make([]outputBoundRegistration, len(sources))
		for i, source := range sources {
			registrations[i] = outputBoundRegistration{pattern: pattern, source: source}
		}
		rt := newLedgerRuntime(root)
		rt.docs[payloadSchemaFile] = ledgerDeepCopyDoc(t, original)
		if mutate != nil {
			mutate(rt.docs[payloadSchemaFile])
		}
		if err := rt.parseGoSource("witness.go", "package store\n\nconst witnessAdmitted = 64\n\nconst witnessExceeds = 4096\n"); err != nil {
			t.Fatal(err)
		}
		census, censusFailures := buildCensus(rt, nil, nil)
		if len(censusFailures) > 0 {
			t.Fatalf("witness census is broken: %v", censusFailures)
		}
		return runOutputBoundLedger(rt, census, registrations)
	}
	guards := []string{"input-widened", "removed-finite-bound", "restored-cap", "stale-registration", "bound-not-admitted", "unit-domain", "source-evaluation", "refusal-class"}
	countGuards := func(failures []ledgerFailure) int {
		total := 0
		for _, failure := range failures {
			for _, kind := range guards {
				if failure.Kind == kind {
					total++
				}
			}
		}
		return total
	}
	if failures := run(nil, finiteAdmitted); countGuards(failures) != 0 {
		t.Fatalf("an admitted finite row raised guard failures: %v", failures)
	}
	if failures := run(removeKeyword, finiteAdmitted); !ledgerHasFailure(failures, "removed-finite-bound", pattern) {
		t.Fatalf("removing a finite maximum must fail the row: %v", failures)
	}
	if failures := run(removeKeyword, unboundedRow); countGuards(failures) != 0 {
		t.Fatalf("removing an unbounded maximum must pass: %v", failures)
	}
	if failures := run(restoreKeyword, unboundedRow); !ledgerHasFailure(failures, "restored-cap", pattern) {
		t.Fatalf("a restored cap on an unbounded row must fail: %v", failures)
	}
	composite := maxBound(finiteAdmitted, unboundedRow)
	if failures := run(removeKeyword, composite); countGuards(failures) != 0 {
		t.Fatalf("an unbounded alternative writer must permit removal of the maximum: %v", failures)
	}
	if failures := run(restoreKeyword, composite); !ledgerHasFailure(failures, "restored-cap", pattern) {
		t.Fatalf("an unbounded alternative writer must reject a restored cap: %v", failures)
	}
	if failures := run(removeNode, unboundedRow); !ledgerHasFailure(failures, "stale-registration", pattern) {
		t.Fatalf("a vanished node must leave the row stale: %v", failures)
	}
	if failures := run(nil, finiteAdmitted, finiteAdmitted); !ledgerHasFailure(failures, "duplicate-registration", pattern) {
		t.Fatalf("duplicate claims of one bound must fail: %v", failures)
	}
	if failures := run(nil, finiteExceeds); !ledgerHasFailure(failures, "bound-not-admitted", pattern) {
		t.Fatalf("a producer above its consumer bound must fail: %v", failures)
	}
}

// TestLedgerPointerTraversal covers pointer escapes, resolution, and the
// segment-scoped star matcher.
func TestLedgerPointerTraversal(t *testing.T) {
	t.Parallel()
	document := map[string]any{
		"$defs": map[string]any{
			"a": map[string]any{
				"properties": map[string]any{
					"x/y": map[string]any{"maxItems": json.Number("3")},
					"m~n": map[string]any{"maxLength": json.Number("4")},
				},
			},
		},
	}
	cases := []struct {
		pointer string
		want    any
		found   bool
	}{
		{"", document, true},
		{"/$defs", document["$defs"], true},
		{"/$defs/a/properties/x~1y/maxItems", json.Number("3"), true},
		{"/$defs/a/properties/m~0n/maxLength", json.Number("4"), true},
		{"/$defs/a/properties/x~1y", document["$defs"].(map[string]any)["a"].(map[string]any)["properties"].(map[string]any)["x/y"], true},
		{"/$defs/a/properties/missing", nil, false},
		{"$defs/a", nil, false},
	}
	for _, tc := range cases {
		got, ok := resolveSchemaPointer(document, tc.pointer)
		if ok != tc.found || ok && !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("resolveSchemaPointer(%q) = (%v, %v), want (%v, %v)", tc.pointer, got, ok, tc.want, tc.found)
		}
	}
	matchCases := []struct {
		pattern, key string
		want         bool
	}{
		{"/$defs/work_transition_action_variant_*/properties/fields/properties/evidence_refs/maxItems", "/$defs/work_transition_action_variant_record_finding/properties/fields/properties/evidence_refs/maxItems", true},
		{"/$defs/work_transition_action_variant_*/properties/fields/properties/evidence_refs/maxItems", "/$defs/work_transition_action_variant_x/properties/other/maxItems", false},
		{"/$defs/*/maxItems", "/$defs/a/properties/b/maxItems", false},
		{"/$defs/a/maxItems", "/$defs/a/maxLength", false},
		{"internal/agentcontract/generated_contracts.go#/$defs/tool_result/maxItems", "internal/agentcontract/generated_contracts.go#/$defs/tool_result/maxItems", true},
		{"internal/agentcontract/generated_contracts.go#/$defs/tool_result/maxItems", "/$defs/tool_result/maxItems", false},
	}
	for _, tc := range matchCases {
		if got := pointerMatches(tc.pattern, tc.key); got != tc.want {
			t.Fatalf("pointerMatches(%q, %q) = %v, want %v", tc.pattern, tc.key, got, tc.want)
		}
	}
	if parent := ledgerParentPattern("internal/agentcontract/generated_contracts.go#/$defs/tool_result/maxItems"); parent != "internal/agentcontract/generated_contracts.go#/$defs/tool_result" {
		t.Fatalf("a registered-root parent pattern = %q, want the document-prefixed node", parent)
	}
	if err := validateLedgerPattern("/$defs/a/maxItems"); err != nil {
		t.Fatalf("valid pattern refused: %v", err)
	}
	if err := validateLedgerPattern("internal/agentcontract/generated_contracts.go#/$defs/tool_result/maxItems"); err != nil {
		t.Fatalf("a registered-root file#pointer pattern was refused: %v", err)
	}
	for _, bad := range []string{"$defs/a/maxItems", "/$defs/a/maxProperties", "/$defs/a/maxItems/x", "/$defs/a/?maxItems", "/$defs/a~b/maxItems", "generated.go#$defs/a/maxItems", "a#b#/$defs/a/maxItems", "/a#/$defs/a/maxItems", "a.go#/$defs/a/maxItems/x", "a.go#/$defs/a/max*"} {
		if err := validateLedgerPattern(bad); err == nil {
			t.Fatalf("invalid pattern %q was accepted", bad)
		}
	}
}

// TestLedgerRefTraversal covers manifest alias resolution, owner union,
// input/output direction split, and the unclosed-census refusal of external
// refs.
func TestLedgerRefTraversal(t *testing.T) {
	t.Parallel()
	manifest := map[string]any{
		"tools": []any{map[string]any{
			"id": "t",
			"operations": []any{
				map[string]any{"id": "t.a", "input_schema": "#/schemas/in", "result_schema": "#/schemas/out"},
				map[string]any{"id": "t.b", "result_schema": "#/schemas/outAlias"},
			},
		}},
		"schemas": map[string]any{
			"in":       map[string]any{"ref": payloadSchemaFile + "#/$defs/in"},
			"out":      map[string]any{"ref": payloadSchemaFile + "#/$defs/out"},
			"outAlias": map[string]any{"ref": payloadSchemaFile + "#/$defs/out"},
		},
	}
	payload := func() map[string]any {
		return map[string]any{"$defs": map[string]any{
			"in":  map[string]any{"properties": map[string]any{"note": map[string]any{"maxLength": json.Number("5")}}},
			"out": map[string]any{"properties": map[string]any{"items": map[string]any{"maxItems": json.Number("3")}}},
		}}
	}
	build := func(doc map[string]any) (*boundCensus, []ledgerFailure) {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		rt.docs[surfaceManifestFile] = manifest
		rt.docs[payloadSchemaFile] = doc
		return buildCensus(rt, nil, nil)
	}
	census, failures := build(payload())
	if len(failures) != 0 {
		t.Fatalf("alias census reported failures: %v", failures)
	}
	bound, ok := census.bounds["/$defs/out/properties/items/maxItems"]
	if !ok {
		t.Fatal("the aliased output bound is missing from the census")
	}
	if len(bound.owners) != 2 || !ledgerContainsString(bound.owners, "t.a") || !ledgerContainsString(bound.owners, "t.b") {
		t.Fatalf("alias owners = %v, want the union of both operations", bound.owners)
	}
	if !census.inputs["/$defs/in/properties/note/maxLength"] {
		t.Fatal("the input bound is not recorded as input-reachable")
	}
	if census.inputs["/$defs/out/properties/items/maxItems"] {
		t.Fatal("an output bound is recorded as input-reachable")
	}
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	rt.docs[surfaceManifestFile] = manifest
	rt.docs[payloadSchemaFile] = payload()
	census, _ = buildCensus(rt, nil, nil)
	if _, err := rt.evaluateSource(schemaBound(payloadSchemaFile, "/$defs/out/properties/items/maxItems"), census); err == nil {
		t.Fatal("an output-only payload node was accepted as a producer source")
	}
	if _, err := rt.evaluateSource(schemaBound(payloadSchemaFile, "/$defs/in/properties/note/maxLength"), census); err != nil {
		t.Fatalf("an input-reachable payload node was refused: %v", err)
	}
	external := payload()
	out := external["$defs"].(map[string]any)["out"].(map[string]any)
	out["$ref"] = "https://example.invalid/elsewhere"
	_, failures = build(external)
	if !ledgerHasFailure(failures, "census", "/$defs/out") {
		t.Fatalf("an external $ref must leave the census unclosed: %v", failures)
	}
}

// TestLedgerSourceExtraction covers constant evaluation, call-argument
// selection, ambiguity, and guard unit inference against synthetic sources.
func TestLedgerSourceExtraction(t *testing.T) {
	t.Parallel()
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	const source = `package store

import "unicode/utf8"

const witnessSingle = 7
const witnessDivision = 15 / 4
const witnessZeroDivisor = 15 / 0

const (
	witnessGrouped = 2 * witnessSingle
	witnessNext    = witnessGrouped + 1
)

const (
	witnessShift = 1 << iota
	witnessShift2
	witnessShift3
)

func guardLen(field string, value string, limit int) bool {
	return len(value) > limit
}

func guardRunes(field string, value string, limit int) bool {
	return utf8.RuneCountInString(value) > limit
}

func guardRunesConverted(field string, value string, limit int) bool {
	return len([]rune(value)) > limit
}

func guardRuneCount(field string, value string, limit int) bool {
	return utf8.RuneCount(value) > limit
}

func guardMixed(field string, value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	return utf8.RuneCount(value) <= 32
}

func guardOpaque(field string, value string, limit int) bool {
	return limit > 0
}

func guardSite(value string) {
	_ = guardLen("premise", value, witnessGrouped)
	_ = guardLen("other", value, 10)
	_ = guardRunes("premise", value, witnessNext)
	_ = guardRunesConverted("premise", value, witnessGrouped)
	_ = guardRuneCount("premise", value, witnessNext)
	_ = guardMixed("premise", value, 8)
}
`
	if err := rt.parseGoSource("witness.go", source); err != nil {
		t.Fatal(err)
	}
	if err := rt.parseGoSource("ambiguous.go", "package store\n\nfunc guardDup(field string, value string, limit int) bool {\n\treturn len(value) > limit\n}\n\nfunc guardDupSite(value string) {\n\t_ = guardDup(\"premise\", value, 8)\n\t_ = guardDup(\"premise\", value, 9)\n}\n"); err != nil {
		t.Fatal(err)
	}
	if err := rt.parseGoSource("varwitness.go", "package store\n\nvar witnessVar = 5\n"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		src     outputBoundSource
		want    int64
		wantErr bool
	}{
		{name: "single literal", src: goConstantBound("witness.go", "witnessSingle", boundItems), want: 7},
		{name: "integer division", src: goConstantBound("witness.go", "witnessDivision", boundItems), want: 3},
		{name: "zero divisor fails closed", src: goConstantBound("witness.go", "witnessZeroDivisor", boundItems), wantErr: true},
		{name: "grouped arithmetic", src: goConstantBound("witness.go", "witnessGrouped", boundItems), want: 14},
		{name: "grouped addition", src: goConstantBound("witness.go", "witnessNext", boundItems), want: 15},
		{name: "iota shift second", src: goConstantBound("witness.go", "witnessShift2", boundItems), want: 2},
		{name: "iota shift third", src: goConstantBound("witness.go", "witnessShift3", boundItems), want: 4},
		{name: "unresolved name fails closed", src: goConstantBound("witness.go", "witnessMissing", boundItems), wantErr: true},
		{name: "var is not a const", src: goConstantBound("varwitness.go", "witnessVar", boundItems), wantErr: true},
		{name: "len guard with field literal", src: goCallBound("witness.go", "guardSite", "guardLen", "premise", 2, boundBytes), want: 14},
		{name: "field literal disambiguates siblings", src: goCallBound("witness.go", "guardSite", "guardLen", "other", 2, boundBytes), want: 10},
		{name: "len guard rejects character unit", src: goCallBound("witness.go", "guardSite", "guardLen", "premise", 2, boundCharacters), wantErr: true},
		{name: "rune guard accepts character unit", src: goCallBound("witness.go", "guardSite", "guardRunes", "premise", 2, boundCharacters), want: 15},
		{name: "rune guard rejects byte unit", src: goCallBound("witness.go", "guardSite", "guardRunes", "premise", 2, boundBytes), wantErr: true},
		{name: "len of a rune conversion counts code points", src: goCallBound("witness.go", "guardSite", "guardRunesConverted", "premise", 2, boundCharacters), want: 14},
		{name: "len of a rune conversion rejects byte unit", src: goCallBound("witness.go", "guardSite", "guardRunesConverted", "premise", 2, boundBytes), wantErr: true},
		{name: "utf8.RuneCount accepts character unit", src: goCallBound("witness.go", "guardSite", "guardRuneCount", "premise", 2, boundCharacters), want: 15},
		{name: "mixed measurements fail closed", src: goCallBound("witness.go", "guardSite", "guardMixed", "premise", 2, boundCharacters), wantErr: true},
		{name: "opaque guard fails closed", src: goCallBound("witness.go", "guardSite", "guardOpaque", "premise", 2, boundItems), wantErr: true},
		{name: "missing field literal fails", src: goCallBound("witness.go", "guardSite", "guardLen", "absent", 2, boundBytes), wantErr: true},
		{name: "string argument is not numeric", src: goCallBound("witness.go", "guardSite", "guardLen", "premise", 0, boundBytes), wantErr: true},
		{name: "duplicate call sites are ambiguous", src: goCallBound("ambiguous.go", "guardDupSite", "guardDup", "premise", 2, boundBytes), wantErr: true},
	}
	for _, tc := range cases {
		evaluated, err := rt.evaluateSource(tc.src, nil)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected a closed failure, got %+v", tc.name, evaluated)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != tc.want {
			t.Fatalf("%s: evaluated %+v, want one constraint with value %d", tc.name, evaluated.constraints, tc.want)
		}
		if tc.src.kind == "go_call" && evaluated.inferred == "" {
			t.Fatalf("%s: no guard measurement was inferred", tc.name)
		}
		if tc.src.kind == "go_const" && evaluated.inferred != "" {
			t.Fatalf("%s: a bare constant must not claim an inferred guard unit", tc.name)
		}
	}
}

// TestLedgerUnitConversion covers capacity conversion, intersection, domain
// refusal, composite reduction, and the Go/SQLite pairing rule.
func TestLedgerUnitConversion(t *testing.T) {
	t.Parallel()
	limit := func(unit boundUnit, value int64) evaluatedBound {
		return evaluatedBound{kind: "schema", constraints: []boundConstraint{{unit: unit, value: value, from: "fixture"}}}
	}
	cases := []struct {
		name   string
		bound  evaluatedBound
		unit   boundUnit
		want   int64
		wantOK bool
	}{
		{"bytes stay bytes", limit(boundBytes, 100), boundBytes, 100, true},
		{"bytes cap code points", limit(boundBytes, 100), boundCharacters, 100, true},
		{"characters expand in bytes", limit(boundCharacters, 100), boundBytes, 4 * 100, true},
		{"character and byte guards intersect in bytes", evaluatedBound{kind: "schema", constraints: []boundConstraint{{unit: boundCharacters, value: 100}, {unit: boundBytes, value: 256}}}, boundBytes, 256, true},
		{"character and byte guards intersect in characters", evaluatedBound{kind: "schema", constraints: []boundConstraint{{unit: boundCharacters, value: 100}, {unit: boundBytes, value: 256}}}, boundCharacters, 100, true},
		{"tighter byte guard wins", evaluatedBound{kind: "schema", constraints: []boundConstraint{{unit: boundCharacters, value: 100}, {unit: boundBytes, value: 80}}}, boundBytes, 80, true},
		{"items stay items", limit(boundItems, 5), boundItems, 5, true},
		{"items never become bytes", limit(boundItems, 5), boundBytes, 0, false},
		{"bytes never become items", limit(boundBytes, 5), boundItems, 0, false},
	}
	for _, tc := range cases {
		got, ok := tc.bound.capacity(tc.unit)
		if ok != tc.wantOK || ok && got != tc.want {
			t.Fatalf("%s: capacity = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
	goBytes := evaluatedBound{kind: "go_const", describe: "go bytes 100", constraints: []boundConstraint{{unit: boundBytes, value: 100, from: "fixture"}}}
	goCharacters := evaluatedBound{kind: "go_call", describe: "go characters 30", constraints: []boundConstraint{{unit: boundCharacters, value: 30, from: "fixture"}}, inferred: "rune_count"}
	items := evaluatedBound{kind: "schema", describe: "items 5", constraints: []boundConstraint{{unit: boundItems, value: 5, from: "fixture"}}}
	sqliteBytes := evaluatedBound{kind: "sqlite", describe: "sqlite bytes 64", constraints: []boundConstraint{{unit: boundBytes, value: 64, from: "fixture"}}}
	sqliteCharacters := evaluatedBound{kind: "sqlite", describe: "sqlite characters 128", constraints: []boundConstraint{{unit: boundCharacters, value: 128, from: "fixture"}}}
	sqliteBoth := evaluatedBound{kind: "sqlite", describe: "sqlite both", constraints: []boundConstraint{{unit: boundBytes, value: 64, from: "fixture"}, {unit: boundCharacters, value: 128, from: "fixture"}}}
	unbounded := evaluatedBound{kind: "unbounded", unbounded: true, describe: "unbounded fixture"}

	combined, err := combineComposite("max", []evaluatedBound{goBytes, goCharacters})
	if err != nil {
		t.Fatalf("max composite refused: %v", err)
	}
	if bytes, ok := combined.capacity(boundBytes); !ok || bytes != 120 {
		t.Fatalf("max composite byte capacity = (%d, %v), want (120, true): a 30-character writer can emit 120 bytes", bytes, ok)
	}
	if characters, ok := combined.capacity(boundCharacters); !ok || characters != 100 {
		t.Fatalf("max composite character capacity = (%d, %v), want (100, true)", characters, ok)
	}
	combined, err = combineComposite("sum", []evaluatedBound{limit(boundBytes, 10), limit(boundBytes, 20)})
	if err != nil {
		t.Fatalf("sum composite refused: %v", err)
	}
	if bytes, _ := combined.capacity(boundBytes); bytes != 30 {
		t.Fatalf("sum composite byte capacity = %d, want 30", bytes)
	}
	if _, err := combineComposite("max", []evaluatedBound{items, goBytes}); err == nil {
		t.Fatal("mixing items with string units must be refused")
	}
	combined, err = combineComposite("max", []evaluatedBound{unbounded, goBytes})
	if err != nil || !combined.unbounded {
		t.Fatalf("an unbounded member must make the composite unbounded: (%+v, %v)", combined, err)
	}
	// maxBound composes ALTERNATIVE writers: a byte guard on one writer and
	// a character guard on another are legitimate alternatives, not a
	// mismatched pair, so max demands no unit agreement between members.
	combined, err = combineComposite("max", []evaluatedBound{goBytes, sqliteCharacters})
	if err != nil {
		t.Fatalf("alternative byte and character writers must compose: %v", err)
	}
	if bytes, ok := combined.capacity(boundBytes); !ok || bytes != 512 {
		t.Fatalf("max alternative byte capacity = (%d, %v), want (512, true): a 128-character writer can emit 512 bytes", bytes, ok)
	}
	if characters, ok := combined.capacity(boundCharacters); !ok || characters != 128 {
		t.Fatalf("max alternative character capacity = (%d, %v), want (128, true)", characters, ok)
	}
	if _, err := combineComposite("max", []evaluatedBound{goCharacters, sqliteBytes}); err != nil {
		t.Fatalf("alternative character and byte writers must compose: %v", err)
	}
	if _, err := combineComposite("max", []evaluatedBound{goBytes, sqliteBoth}); err != nil {
		t.Fatalf("a column carrying both CHECK units admits the alternative: %v", err)
	}
	if _, err := combineComposite("max", []evaluatedBound{goBytes, sqliteBytes}); err != nil {
		t.Fatalf("alternative agreeing guards must compose: %v", err)
	}

	// Paired sources compose guards of the SAME value: every member's
	// constraints apply simultaneously and the pair is their intersection,
	// never an alternative maximum. A bare constant is refused as a member,
	// and the Go guard's unit must sit beside every SQLite member's units.
	goByteGuard := evaluatedBound{kind: "go_call", describe: "go bytes 100", constraints: []boundConstraint{{unit: boundBytes, value: 100, from: "fixture"}}, inferred: "len"}
	paired, err := combinePaired([]evaluatedBound{goByteGuard, sqliteBytes})
	if err != nil {
		t.Fatalf("agreeing paired guards must compose: %v", err)
	}
	if bytes, ok := paired.capacity(boundBytes); !ok || bytes != 64 {
		t.Fatalf("paired byte capacity = (%d, %v), want (64, true): the simultaneous intersection, not the maximum", bytes, ok)
	}
	if _, err := combinePaired([]evaluatedBound{goByteGuard, sqliteCharacters}); err == nil {
		t.Fatal("paired Go bytes and SQLite characters guards must be refused")
	}
	if _, err := combinePaired([]evaluatedBound{goCharacters, sqliteBytes}); err == nil {
		t.Fatal("paired Go characters and SQLite bytes guards must be refused")
	}
	if _, err := combinePaired([]evaluatedBound{goBytes, sqliteBytes}); err == nil {
		t.Fatal("a bare constant carries no inferred measurement and must be refused as a paired member")
	}
	if _, err := combinePaired(nil); err == nil {
		t.Fatal("an empty pair must be refused")
	}
	paired, err = combinePaired([]evaluatedBound{unbounded, goByteGuard})
	if err != nil || !paired.unbounded {
		t.Fatalf("an unbounded member must make the pair unbounded: (%+v, %v)", paired, err)
	}
}

// TestLedgerSQLiteColumnBounds covers CHECK parsing on synthetic DDL and
// proves the pipeline against the real final migrated schema. It asserts
// only what each CHECK says: a parsed TEXT character bound is a NUL-prefix
// constraint (length(TEXT) stops at the first NUL), and whether it proves a
// full output capacity is decided at source evaluation, never here —
// TestLedgerSQLiteTextPrefixHonesty owns that separation.
func TestLedgerSQLiteColumnBounds(t *testing.T) {
	t.Parallel()
	ddl := `CREATE TABLE witness_table (
  id TEXT PRIMARY KEY,
  name TEXT CHECK(length(name) > 0 AND length(name) <= 128),
  blob_ref TEXT CHECK(length(CAST(blob_ref AS BLOB)) BETWEEN 2 AND 16384),
  tags TEXT NOT NULL CHECK(json_valid(tags) AND json_array_length(tags) <= 32),
  nonce TEXT CHECK(length(nonce) >= 16 AND length(nonce) <= 256),
  nullable TEXT CHECK(nullable IS NULL OR (json_valid(nullable) AND length(CAST(nullable AS BLOB))<=64)),
  strict TEXT CHECK(length(strict) < 11),
  enumish TEXT CHECK(enumish IN ('a and b','c')),
  unbounded TEXT CHECK(length(unbounded) > 0),
  nested TEXT CHECK(length(nested) <= 10 OR (length(nested) > 0 AND length(nested) <= 100)),
  mixed TEXT CHECK(mixed IS NULL OR length(mixed) <= 32),
  permit TEXT CHECK(length(permit) <= 64 OR random() > 0),
  lowly TEXT CHECK(length(lowly) >= 16 OR length(lowly) >= 32),
  arith TEXT CHECK(length(arith) <= 63 + 1),
  cased TEXT CHECK(CASE WHEN length(cased) <= 64 THEN 1 ELSE 0 END = 1),
  iifed TEXT CHECK(IIF(length(iifed) <= 64, 1, 0) = 1),
  CHECK (flag IS NULL OR flag IN ('a','b')),
  flagged TEXT CHECK(flagged IS NULL OR length(flagged) <= 48),
  CHECK (length(name) <= 200)
)`
	commentDDL := `CREATE TABLE comment_witness (
  -- the author's note: don't trust ( this comment's length(note) <= 999999
  note TEXT CHECK(/* it's a max ( */ length(note) <= 64), -- trailing ' quote ((
  quoted TEXT CHECK(quoted = 'isn''t length(quoted) <= 999999')
)`
	cases := []struct {
		table    string
		column   string
		want     map[boundUnit]int64
		wantErr  bool
		contains string
	}{
		{column: "name", want: map[boundUnit]int64{boundCharacters: 128}},
		{column: "blob_ref", want: map[boundUnit]int64{boundBytes: 16384}},
		{column: "tags", want: map[boundUnit]int64{boundItems: 32}},
		{column: "nonce", want: map[boundUnit]int64{boundCharacters: 256}},
		{column: "nullable", want: map[boundUnit]int64{boundBytes: 64}},
		{column: "strict", want: map[boundUnit]int64{boundCharacters: 10}},
		// OR takes the union maximum of its alternatives: 100, not 10.
		{column: "nested", want: map[boundUnit]int64{boundCharacters: 100}},
		// A `column IS NULL` alternative admits no non-null value; the
		// measured alternative still bounds the column.
		{column: "mixed", want: map[boundUnit]int64{boundCharacters: 32}},
		{column: "flagged", want: map[boundUnit]int64{boundCharacters: 48}},
		// Comments, their apostrophes, and their unbalanced parentheses
		// vanish before any quote-tracking scan runs.
		{table: "comment_witness", column: "note", want: map[boundUnit]int64{boundCharacters: 64}},
		// A quoted pseudo-measurement is data, never a bound.
		{table: "comment_witness", column: "quoted", wantErr: true, contains: "no finite"},
		{column: "enumish", wantErr: true, contains: "no finite"},
		{column: "unbounded", wantErr: true, contains: "without any upper bound"},
		// Lower-only alternatives bound nothing and are never invented.
		{column: "lowly", wantErr: true, contains: "unbounded"},
		// An unknown permissive alternative may admit unbounded values.
		{column: "permit", wantErr: true, contains: "unbounded non-null"},
		// Trailing arithmetic or a conditional wrapper around the bound
		// fails closed instead of silently taking the first integer.
		{column: "arith", wantErr: true, contains: "trailing"},
		{column: "cased", wantErr: true, contains: "trailing"},
		{column: "iifed", wantErr: true, contains: "trailing"},
		{column: "absent", wantErr: true, contains: "not declared"},
	}
	for _, tc := range cases {
		table := tc.table
		if table == "" {
			table = "witness_table"
		}
		selectedDDL := ddl
		if table == "comment_witness" {
			selectedDDL = commentDDL
		}
		constraints, err := parseLedgerColumnBounds(selectedDDL, table, tc.column)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected failure, got %v", tc.column, constraints)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("%s: error %q does not name %q", tc.column, err.Error(), tc.contains)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.column, err)
		}
		got := map[boundUnit]int64{}
		for _, constraint := range constraints {
			// All CHECK bounds on one column apply simultaneously: the
			// effective per-unit bound is their intersection.
			if existing, ok := got[constraint.unit]; !ok || constraint.value < existing {
				got[constraint.unit] = constraint.value
			}
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: constraints %v, want %v", tc.column, constraints, tc.want)
		}
		for unit, value := range tc.want {
			if got[unit] != value {
				t.Fatalf("%s: %s bound = %d, want %d", tc.column, unit, got[unit], value)
			}
		}
	}
	for _, check := range []string{
		"length(value) <= 64 OR other IS NULL",
		"value IS NOT NULL OR length(value) <= 64",
		"NOT (length(value) <= 64)",
		"not length(value) <= 64",
		"length(value) <= 63 + 1",
		"length(value) between 1 and 64 + 1",
	} {
		if _, err := ledgerParseCheckBody(check, "value", boundCharacters); err == nil {
			t.Fatalf("an unbounded alternate branch was accepted: %s", check)
		}
	}
	if constraints, err := ledgerParseCheckBody("length(value) <= 64 AND (other IS NULL OR other = 'x')", "value", boundCharacters); err != nil || len(constraints) != 1 || constraints[0].value != 64 {
		t.Fatalf("an unrelated nested OR conjunct must be ignored, not rejected: %v %v", constraints, err)
	}
	s := openTemp(t)
	ddlTables, err := sqliteMasterDDL(s.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	tables := make([]string, 0, len(ddlTables))
	for table := range ddlTables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	parsed := 0
	for _, table := range tables {
		defs := ledgerSplitTopLevel(mustTableBody(t, ddlTables[table]), ',')
		for _, def := range defs {
			name, _, isColumn := ledgerColumnDefName(strings.TrimSpace(def))
			if !isColumn || name == "" {
				continue
			}
			if constraints, err := parseLedgerColumnBounds(ddlTables[table], table, name); err == nil {
				parsed += len(constraints)
			}
		}
	}
	if parsed < 50 {
		t.Fatalf("the real final migrated DDL yielded only %d parsable CHECK bounds; the sqlite pipeline is broken", parsed)
	}
}

func mustTableBody(t *testing.T, ddl string) string {
	t.Helper()
	stripped, err := ledgerStripSQLComments(ddl)
	if err != nil {
		t.Fatalf("DDL comments cannot be stripped safely: %v", err)
	}
	open := strings.Index(stripped, "(")
	if open < 0 {
		t.Fatalf("DDL carries no body: %.80s", stripped)
	}
	inner, _, ok := ledgerParenSpan(stripped, open)
	if !ok {
		t.Fatalf("DDL body is unbalanced: %.80s", stripped)
	}
	return inner
}

// TestLedgerRefusalPathCheck covers the typed refusal path verification
// against synthetic sources and the shared real read refusal owner.
func TestLedgerRefusalPathCheck(t *testing.T) {
	t.Parallel()
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	const refusals = `package agent

func refusalLiteral(base string) string {
	return coreError(base, "limit_exceeded", "too large", "reduce_limit", false)
}

func refusalHelper(base string, size int) string {
	return readSizeRefusal(base, size)
}

func refusalMissing(base string) string {
	return base
}
`
	if err := rt.parseGoSource("refusalwitness.go", refusals); err != nil {
		t.Fatal(err)
	}
	if err := checkLedgerRefusalPath(rt, "refusalwitness.go:refusalLiteral"); err != nil {
		t.Fatalf("a literal limit_exceeded emitter was refused: %v", err)
	}
	if err := checkLedgerRefusalPath(rt, "refusalwitness.go:refusalHelper"); err == nil {
		t.Fatal("a helper that only forwards to an emitter was accepted; cite the innermost emitting symbol")
	}
	if err := checkLedgerRefusalPath(rt, "refusalwitness.go:refusalMissing"); err == nil {
		t.Fatal("a symbol with no typed refusal emission was accepted")
	}
	if err := checkLedgerRefusalPath(rt, "refusalwitness.go:noSuchSymbol"); err == nil {
		t.Fatal("a missing symbol was accepted")
	}
	if err := checkLedgerRefusalPath(rt, "refusalwitness.go"); err == nil {
		t.Fatal("a path without file:Symbol was accepted")
	}
	if err := checkLedgerRefusalPath(rt, "internal/agent/runtime.go:readSizeRefusal"); err != nil {
		t.Fatalf("the shared read typed refusal path was refused: %v", err)
	}
}

// TestLedgerWildcardArmWitnesses pins the wildcard coverage rule on a
// synthetic oneOf: every parent node a wildcard pattern covers must retain a
// finite keyword while a finite row stands, so removing one arm's cap fails
// even while the other arm stays capped. An unbounded alternative keeps
// passing only while every cap stays removed.
func TestLedgerWildcardArmWitnesses(t *testing.T) {
	t.Parallel()
	const pattern = "/$defs/out/oneOf/*/properties/v/maxLength"
	const arm0 = "/$defs/out/oneOf/0/properties/v/maxLength"
	const arm1 = "/$defs/out/oneOf/1/properties/v/maxLength"
	manifest := map[string]any{
		"tools": []any{map[string]any{
			"id":         "w",
			"operations": []any{map[string]any{"id": "w.a", "result_schema": "#/schemas/out"}},
		}},
		"schemas": map[string]any{"out": map[string]any{"ref": payloadSchemaFile + "#/$defs/out"}},
	}
	payload := func() map[string]any {
		arm := func() map[string]any {
			return map[string]any{"properties": map[string]any{"v": map[string]any{"maxLength": json.Number("64")}}}
		}
		return map[string]any{"$defs": map[string]any{"out": map[string]any{"oneOf": []any{arm(), arm()}}}}
	}
	removeArmCap := func(index int) func(map[string]any) {
		return func(doc map[string]any) {
			arms := doc["$defs"].(map[string]any)["out"].(map[string]any)["oneOf"].([]any)
			node := arms[index].(map[string]any)["properties"].(map[string]any)["v"].(map[string]any)
			delete(node, "maxLength")
		}
	}
	run := func(mutate func(map[string]any), source outputBoundSource) []ledgerFailure {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		rt.docs[surfaceManifestFile] = manifest
		rt.docs[payloadSchemaFile] = ledgerDeepCopyDoc(t, payload())
		if mutate != nil {
			mutate(rt.docs[payloadSchemaFile])
		}
		if err := rt.parseGoSource("witness.go", "package store\n\nconst witnessCap = 64\n"); err != nil {
			t.Fatal(err)
		}
		census, censusFailures := buildCensus(rt, nil, nil)
		if len(censusFailures) > 0 {
			t.Fatalf("wildcard witness census is broken: %v", censusFailures)
		}
		return runOutputBoundLedger(rt, census, []outputBoundRegistration{{pattern: pattern, source: source}})
	}
	guards := []string{"input-widened", "removed-finite-bound", "restored-cap", "stale-registration", "bound-not-admitted", "unit-domain", "source-evaluation", "refusal-class"}
	countGuards := func(failures []ledgerFailure) int {
		total := 0
		for _, failure := range failures {
			for _, kind := range guards {
				if failure.Kind == kind {
					total++
				}
			}
		}
		return total
	}
	finite := goConstantBound("witness.go", "witnessCap", boundCharacters)
	unboundedRow := unboundedOutput("witness values accumulate with no finite producer guard", "internal/agent/runtime.go:readSizeRefusal")
	if failures := run(nil, finite); countGuards(failures) != 0 {
		t.Fatalf("a wildcard row over fully capped arms raised guard failures: %v", failures)
	}
	if failures := run(removeArmCap(1), finite); !ledgerHasFailure(failures, "removed-finite-bound", arm1) {
		t.Fatalf("partial removal must fail even while another arm remains capped: %v", failures)
	}
	if failures := run(func(doc map[string]any) { removeArmCap(0)(doc); removeArmCap(1)(doc) }, finite); !ledgerHasFailure(failures, "removed-finite-bound", arm0) || !ledgerHasFailure(failures, "removed-finite-bound", arm1) {
		t.Fatalf("removing every wildcard arm must fail the finite row per arm: %v", failures)
	}
	if failures := run(func(doc map[string]any) { removeArmCap(0)(doc); removeArmCap(1)(doc) }, unboundedRow); countGuards(failures) != 0 {
		t.Fatalf("an unbounded alternative must pass while every cap stays removed: %v", failures)
	}
	if failures := run(removeArmCap(1), unboundedRow); !ledgerHasFailure(failures, "restored-cap", arm0) {
		t.Fatalf("an unbounded alternative must fail while any arm keeps its cap: %v", failures)
	}
}

// TestLedgerRegisteredRootWitness proves the file#pointer registered
// host/internal roots end to end: the census covers the registered node, a
// file#pointer pattern validates, matches, and claims its bound, a removed
// cap resolves through the document-prefixed parent lookup instead of going
// stale, and a vanished node stays stale. The census seeds no manifest
// roots, so the registration list is the census's whole world.
func TestLedgerRegisteredRootWitness(t *testing.T) {
	t.Parallel()
	const rootDoc = "witness_surface.schema.json"
	const pattern = rootDoc + "#/$defs/tool_result/items/maxItems"
	rootDocument := func() map[string]any {
		return map[string]any{"$defs": map[string]any{"tool_result": map[string]any{"items": map[string]any{"maxItems": json.Number("64")}}}}
	}
	run := func(mutate func(map[string]any), source outputBoundSource) []ledgerFailure {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		rt.docs[surfaceManifestFile] = map[string]any{}
		rt.docs[rootDoc] = ledgerDeepCopyDoc(t, rootDocument())
		if mutate != nil {
			mutate(rt.docs[rootDoc])
		}
		if err := rt.parseGoSource("witness.go", "package store\n\nconst witnessCap = 64\n"); err != nil {
			t.Fatal(err)
		}
		census, censusFailures := buildCensus(rt, []censusRootRequest{{file: rootDoc, pointer: "/$defs/tool_result"}}, nil)
		if len(censusFailures) > 0 {
			t.Fatalf("registered-root census is broken: %v", censusFailures)
		}
		return runOutputBoundLedger(rt, census, []outputBoundRegistration{{pattern: pattern, source: source}})
	}
	finite := goConstantBound("witness.go", "witnessCap", boundItems)
	if failures := run(nil, finite); len(failures) != 0 {
		t.Fatalf("a file#pointer registered root must census, match, and close: %v", failures)
	}
	if failures := run(func(doc map[string]any) {
		delete(doc["$defs"].(map[string]any)["tool_result"].(map[string]any)["items"].(map[string]any), "maxItems")
	}, finite); !ledgerHasFailure(failures, "removed-finite-bound", pattern) {
		t.Fatalf("a removed registered-root cap must resolve through the file#pointer parent lookup, not go stale: %v", failures)
	}
	if failures := run(func(doc map[string]any) {
		delete(doc["$defs"].(map[string]any)["tool_result"].(map[string]any), "items")
	}, finite); !ledgerHasFailure(failures, "stale-registration", pattern) {
		t.Fatalf("a vanished registered node must leave the row stale: %v", failures)
	}
}

// TestLedgerInputWidenedWitness proves a registered input root marks the
// nodes it reaches: an unbounded row on a node that an input root also
// reaches fails as a widened admission, while the same row passes when only
// an output root reaches the node.
func TestLedgerInputWidenedWitness(t *testing.T) {
	t.Parallel()
	const rootDoc = "witness_surface.schema.json"
	const pattern = rootDoc + "#/$defs/shared/maxLength"
	document := map[string]any{"$defs": map[string]any{
		"tool_result":  map[string]any{"$ref": "#/$defs/shared"},
		"action_field": map[string]any{"$ref": "#/$defs/shared"},
		"shared":       map[string]any{"type": "string"},
	}}
	run := func(inputRoots []censusRootRequest) []ledgerFailure {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		rt.docs[surfaceManifestFile] = map[string]any{}
		rt.docs[rootDoc] = ledgerDeepCopyDoc(t, document)
		census, censusFailures := buildCensus(rt, []censusRootRequest{{file: rootDoc, pointer: "/$defs/tool_result"}}, inputRoots)
		if len(censusFailures) > 0 {
			t.Fatalf("input-widened census is broken: %v", censusFailures)
		}
		return runOutputBoundLedger(rt, census, []outputBoundRegistration{{pattern: pattern, source: unboundedOutput("the shared witness accumulates with no finite producer guard", ledgerReadRefusalPath)}})
	}
	if failures := run(nil); ledgerHasFailure(failures, "input-widened", rootDoc+"#/$defs/shared") {
		t.Fatalf("an output-only node must not fail as widened admission: %v", failures)
	}
	if failures := run([]censusRootRequest{{file: rootDoc, pointer: "/$defs/action_field"}}); !ledgerHasFailure(failures, "input-widened", rootDoc+"#/$defs/shared") {
		t.Fatalf("an uncapped node an input root reaches must fail as widened admission: %v", failures)
	}
}

// TestLedgerSQLiteTextPrefixHonesty keeps the parser's TEXT prefix bound
// separate from any claim that it caps the full output: length(TEXT) stops
// at the first NUL, so a prefix-only source carries no finite capacity in
// any unit and says so, while a full-byte CHECK beside it proves the complete
// value. The live witness inserts a NUL-tailed value under a prefix CHECK
// and reads both lengths back from SQLite.
func TestLedgerSQLiteTextPrefixHonesty(t *testing.T) {
	t.Parallel()
	prefixDDL := `CREATE TABLE prefix_witness (note TEXT CHECK(length(note) <= 128))`
	byteDDL := `CREATE TABLE byte_witness (note TEXT CHECK(length(CAST(note AS BLOB)) <= 4096))`
	bothDDL := `CREATE TABLE both_witness (note TEXT CHECK(length(note) <= 128) CHECK(length(CAST(note AS BLOB)) <= 4096))`
	constraints, err := parseLedgerColumnBounds(prefixDDL, "prefix_witness", "note")
	if err != nil || len(constraints) != 1 || constraints[0].unit != boundCharacters || !constraints[0].prefix {
		t.Fatalf("a TEXT length bound must parse as a prefix constraint: %v %v", constraints, err)
	}
	constraints, err = parseLedgerColumnBounds(byteDDL, "byte_witness", "note")
	if err != nil || len(constraints) != 1 || constraints[0].unit != boundBytes || constraints[0].prefix {
		t.Fatalf("a full-byte CAST bound must parse without the prefix marker: %v %v", constraints, err)
	}
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	rt.ddl = map[string]string{"prefix_witness": prefixDDL, "byte_witness": byteDDL, "both_witness": bothDDL}
	prefixSource, err := rt.evaluateSource(sqliteBound("prefix_witness", "note"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !prefixSource.nulPrefix || !strings.Contains(prefixSource.describe, "NUL") {
		t.Fatalf("a prefix-only source must be marked NUL-unproved and say so: %+v", prefixSource)
	}
	for _, unit := range []boundUnit{boundBytes, boundCharacters} {
		if _, ok := prefixSource.capacity(unit); ok {
			t.Fatalf("a TEXT prefix source must carry no finite %s capacity", unit)
		}
	}
	byteSource, err := rt.evaluateSource(sqliteBound("byte_witness", "note"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if byteSource.nulPrefix {
		t.Fatal("a full-byte CHECK bounds the complete value and must not be marked prefix")
	}
	if bytes, ok := byteSource.capacity(boundBytes); !ok || bytes != 4096 {
		t.Fatalf("full-byte capacity = (%d, %v), want (4096, true)", bytes, ok)
	}
	bothSource, err := rt.evaluateSource(sqliteBound("both_witness", "note"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if bothSource.nulPrefix {
		t.Fatal("a full-byte CHECK beside the prefix bound proves the full string")
	}
	if bytes, ok := bothSource.capacity(boundBytes); !ok || bytes != 4096 {
		t.Fatalf("both-checks capacity = (%d, %v), want (4096, true)", bytes, ok)
	}
	s := openTemp(t)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`CREATE TABLE nul_witness (note TEXT CHECK(length(note) <= 4))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nul_witness (note) VALUES ('abc' || CAST(zeroblob(8) AS TEXT))`); err != nil {
		t.Fatalf("the prefix CHECK refused a NUL-tailed value; the witness premise does not hold: %v", err)
	}
	var prefixLen, fullLen int64
	if err := db.QueryRow(`SELECT length(note), length(CAST(note AS BLOB)) FROM nul_witness`).Scan(&prefixLen, &fullLen); err != nil {
		t.Fatal(err)
	}
	if prefixLen != 3 || fullLen <= 4 {
		t.Fatalf("NUL-tail witness: length(note)=%d but the stored value is %d bytes; the CHECK bounded only the NUL-free prefix", prefixLen, fullLen)
	}
}

// TestLedgerUnprovedReporting pins repair semantics for honest gaps: an
// unproved row never closes the ledger and its failure carries the saved gap
// text, and an unknown-kind failure carries the row's producer text instead
// of printing only the kind.
func TestLedgerUnprovedReporting(t *testing.T) {
	t.Parallel()
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	const researchGap = "research statement fields accumulate without any store CHECK or full-string Go admission"
	if _, err := rt.evaluateSource(unprovedOutput(researchGap), nil); err == nil {
		t.Fatal("an unproved row must never close the ledger")
	} else if !strings.Contains(err.Error(), researchGap) {
		t.Fatalf("the failure must report the saved gap text, got: %v", err)
	}
	if _, err := rt.evaluateSource(unprovedOutput("   "), nil); err == nil {
		t.Fatal("an unproved row without gap text must fail closed")
	}
	const warningGap = "workflow warnings accumulate one entry per stored condition"
	if _, err := rt.evaluateSource(outputBoundSource{kind: "alien", producer: warningGap}, nil); err == nil {
		t.Fatal("an unknown kind must fail closed")
	} else if !strings.Contains(err.Error(), warningGap) {
		t.Fatalf("an unknown-kind failure must carry the row's saved producer gap, got: %v", err)
	}
}

// TestLedgerRefusalClassBinding pins the typed-refusal evidence rule: a call
// whose callee merely ends in "Refusal" proves nothing. The cited symbol of
// an unbounded row must reach a real emitter — the shared read refusal
// (internal/agent/runtime.go readSizeRefusal) for read outputs and the shared
// mutation envelope check (internal/agent/mutations.go mutationResult, whose
// body carries the limit_exceeded envelope refusal) for mutation outputs.
// RED by design: the current check accepts any *Refusal-suffixed call as
// evidence, so both fake witnesses below are still accepted until the
// mechanism binds evidence to the real emitters.
func TestLedgerRefusalClassBinding(t *testing.T) {
	t.Parallel()
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	const refusals = `package agent

func madeUpCaller(base string) string {
	return madeUpRefusal(base, 1)
}

func fakeHelperCaller(base string) string {
	return fakeRefusal(base)
}

func fakeRefusal(base string) string {
	return base + ": refused"
}
`
	if err := rt.parseGoSource("refusalclass_witness.go", refusals); err != nil {
		t.Fatal(err)
	}
	for _, symbol := range []string{"madeUpCaller", "fakeHelperCaller"} {
		if err := checkLedgerRefusalPath(rt, "refusalclass_witness.go:"+symbol); err == nil {
			t.Fatalf("%s reaches no real emitter; a *Refusal-suffixed call alone must not prove the typed refusal path", symbol)
		}
	}
	if err := checkLedgerRefusalPath(rt, "internal/agent/runtime.go:readSizeRefusal"); err != nil {
		t.Fatalf("the real shared read emitter must verify: %v", err)
	}
	if err := checkLedgerRefusalPath(rt, "internal/agent/mutations.go:mutationResult"); err != nil {
		t.Fatalf("the real shared mutation envelope emitter must verify: %v", err)
	}
}

// TestLedgerQualifiedGuardResolution pins helper resolution: an imported- or
// receiver-qualified call site must not silently resolve to an unrelated
// same-bare-name helper in the cited file or any other owner. RED by design:
// ledgerCalleeMatches strips qualifiers and inferFromCallee resolves bare
// names, so each qualified witness below still derives its bound from the
// local len-based helper until the mechanism requires callee identity.
func TestLedgerQualifiedGuardResolution(t *testing.T) {
	t.Parallel()
	const helper = "func Guard(field string, value string, limit int) bool {\n\treturn len(value) > limit\n}\n\n"
	qualified := []struct {
		name string
		call string
	}{
		{name: "package-qualified call", call: `stringsutil.Guard("premise", value, 8)`},
		{name: "receiver-qualified call", call: `w.Guard("premise", value, 8)`},
	}
	for _, tc := range qualified {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		source := "package store\n\nimport \"example.com/stringsutil\"\n\n" + helper + "func site(value string) {\n\t_ = " + tc.call + "\n}\n"
		if err := rt.parseGoSource("qualified_witness/site.go", source); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		evaluated, err := rt.evaluateSource(goCallBound("qualified_witness/site.go", "site", "Guard", "premise", 2, boundBytes), nil)
		if err == nil {
			t.Fatalf("%s: derived %+v from the unrelated same-bare-name helper; qualified resolution must fail closed instead", tc.name, evaluated)
		}
	}
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	unqualified := "package store\n\n" + helper + "func site(value string) {\n\t_ = Guard(\"premise\", value, 8)\n}\n"
	if err := rt.parseGoSource("qualified_witness/plain.go", unqualified); err != nil {
		t.Fatalf("control source does not parse: %v", err)
	}
	if _, err := rt.evaluateSource(goCallBound("qualified_witness/plain.go", "site", "Guard", "premise", 2, boundBytes), nil); err != nil {
		t.Fatalf("an unqualified call to the same-file helper must keep resolving: %v", err)
	}
}

// TestLedgerUnknownSchemaKeywords pins census coverage honesty: a
// schema-bearing keyword the walker does not support must either be walked
// (its bounds join the census and demand rows) or fail the census closed
// naming the keyword. RED by design: the walker skips keywords outside its
// wrapper list, so each witness bound below is silently missing from
// coverage today.
func TestLedgerUnknownSchemaKeywords(t *testing.T) {
	t.Parallel()
	manifest := map[string]any{
		"tools": []any{map[string]any{
			"id":         "k",
			"operations": []any{map[string]any{"id": "k.a", "result_schema": "#/schemas/out"}},
		}},
		"schemas": map[string]any{"out": map[string]any{"ref": payloadSchemaFile + "#/$defs/out"}},
	}
	known := map[string]any{"maxLength": json.Number("9")}
	unsupported := []struct {
		keyword string
		node    map[string]any
		bound   string
	}{
		{keyword: "unevaluatedProperties", node: map[string]any{"maxLength": json.Number("4")}, bound: "/$defs/out/unevaluatedProperties/maxLength"},
		{keyword: "unevaluatedItems", node: map[string]any{"maxLength": json.Number("4")}, bound: "/$defs/out/unevaluatedItems/maxLength"},
		{keyword: "dependentSchemas", node: map[string]any{"extra": map[string]any{"properties": map[string]any{"v": map[string]any{"maxLength": json.Number("4")}}}}, bound: "/$defs/out/dependentSchemas/extra/properties/v/maxLength"},
	}
	for _, tc := range unsupported {
		payload := map[string]any{"$defs": map[string]any{"out": map[string]any{
			"properties": map[string]any{"known": known},
			tc.keyword:   tc.node,
		}}}
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		rt.docs[surfaceManifestFile] = manifest
		rt.docs[payloadSchemaFile] = payload
		census, censusFailures := buildCensus(rt, nil, nil)
		if len(censusFailures) > 0 {
			failedClosed := false
			for _, failure := range censusFailures {
				if strings.Contains(failure.Detail, tc.keyword) {
					failedClosed = true
				}
			}
			if !failedClosed {
				t.Fatalf("%s: the census failed for an unrelated reason: %v", tc.keyword, censusFailures)
			}
			continue
		}
		if census.bounds[tc.bound] == nil {
			t.Fatalf("%s: the census neither walked the %s bound at %s nor failed closed; the bound is silently missing from coverage", tc.keyword, tc.keyword, tc.bound)
		}
	}
}

// TestLedgerSharedNodeRefusalClasses is the real shared-node class witness:
// output classes derive from the manifest's operations[].kind, a node or
// bound consumed by both a read and a mutation operation is shared, and an
// unbounded row over it must cite both exact canonical emitters — the shared
// read refusal for the read class and the shared mutation envelope check for
// the mutation class. The harness never invents a second author-cited path:
// a row citing only one class's emitter fails until its author adds the
// other, and an unrelated call fails as a non-emitter.
func TestLedgerSharedNodeRefusalClasses(t *testing.T) {
	t.Parallel()
	const (
		sharedPattern    = "/$defs/shared/properties/v/maxLength"
		readOnlyPattern  = "/$defs/read_only/properties/v/maxLength"
		mutOnlyPattern   = "/$defs/mut_only/properties/v/maxLength"
		sharedNodeClass  = "/$defs/shared/properties/v"
		readOnlyNodeKey  = "/$defs/read_only/properties/v"
		mutOnlyNodeClass = "/$defs/mut_only/properties/v"
	)
	manifest := map[string]any{
		"tools": []any{map[string]any{
			"id": "s",
			"operations": []any{
				map[string]any{"id": "s.read", "kind": "read", "result_schema": "#/schemas/shared"},
				map[string]any{"id": "s.mutate", "kind": "mutation", "result_schema": "#/schemas/shared"},
				map[string]any{"id": "s.read_only", "kind": "read", "result_schema": "#/schemas/read_only"},
				map[string]any{"id": "s.mut_only", "kind": "mutation", "result_schema": "#/schemas/mut_only"},
			},
		}},
		"schemas": map[string]any{
			"shared":    map[string]any{"ref": payloadSchemaFile + "#/$defs/shared"},
			"read_only": map[string]any{"ref": payloadSchemaFile + "#/$defs/read_only"},
			"mut_only":  map[string]any{"ref": payloadSchemaFile + "#/$defs/mut_only"},
		},
	}
	payload := func() map[string]any {
		empty := func() map[string]any {
			return map[string]any{"properties": map[string]any{"v": map[string]any{}}}
		}
		return map[string]any{"$defs": map[string]any{
			"shared":    empty(),
			"read_only": empty(),
			"mut_only":  empty(),
		}}
	}
	build := func() (*ledgerRuntime, *boundCensus) {
		rt := newLedgerRuntime(ledgerRepoRoot(t))
		rt.docs[surfaceManifestFile] = manifest
		rt.docs[payloadSchemaFile] = ledgerDeepCopyDoc(t, payload())
		census, censusFailures := buildCensus(rt, nil, nil)
		if len(censusFailures) > 0 {
			t.Fatalf("shared-node witness census is broken: %v", censusFailures)
		}
		return rt, census
	}
	run := func(rt *ledgerRuntime, census *boundCensus, sources ...outputBoundSource) []ledgerFailure {
		registrations := make([]outputBoundRegistration, 0, len(sources))
		for _, source := range sources {
			registrations = append(registrations,
				outputBoundRegistration{pattern: sharedPattern, source: source},
				outputBoundRegistration{pattern: readOnlyPattern, source: source},
				outputBoundRegistration{pattern: mutOnlyPattern, source: source},
			)
		}
		return runOutputBoundLedger(rt, census, registrations)
	}
	rt, census := build()
	if classes := census.nodes[sharedNodeClass]; len(classes) != 2 || !ledgerContainsString(classes, ledgerReadRefusalClass) || !ledgerContainsString(classes, ledgerMutationRefusalClass) {
		t.Fatalf("the shared node's classes = %v, want read+mutation derived from operations[].kind", classes)
	}
	if classes := census.nodes[readOnlyNodeKey]; len(classes) != 1 || !ledgerContainsString(classes, ledgerReadRefusalClass) {
		t.Fatalf("the read-only node's classes = %v, want [read]", classes)
	}
	if classes := census.nodes[mutOnlyNodeClass]; len(classes) != 1 || !ledgerContainsString(classes, ledgerMutationRefusalClass) {
		t.Fatalf("the mutation-only node's classes = %v, want [mutation]", classes)
	}
	readOnlyRow := unboundedOutput("the read-only witness accumulates with no finite producer guard", ledgerReadRefusalPath)
	if failures := run(rt, census, readOnlyRow); ledgerHasFailure(failures, "refusal-class", readOnlyPattern) {
		t.Fatalf("a read-only node is satisfied by the shared read emitter: %v", failures)
	}
	rt3, census3 := build()
	mutOnlyRow := unboundedOutput("the mutation-only witness accumulates with no finite producer guard", ledgerReadRefusalPath)
	failures := run(rt3, census3, mutOnlyRow)
	if !ledgerHasFailure(failures, "refusal-class", mutOnlyPattern) {
		t.Fatalf("a mutation-owned node must reject a row citing only the read emitter: %v", failures)
	}
	rt4, census4 := build()
	sharedSingle := unboundedOutput("the shared witness accumulates with no finite producer guard", ledgerReadRefusalPath)
	failures = run(rt4, census4, sharedSingle)
	if !ledgerHasFailure(failures, "refusal-class", sharedPattern) {
		t.Fatalf("a shared read+mutation node must reject a row citing only the read emitter: %v", failures)
	}
	rt5, census5 := build()
	sharedBoth := unboundedOutput("the shared witness accumulates with no finite producer guard", ledgerReadRefusalPath, ledgerMutationRefusalPath)
	failures = run(rt5, census5, sharedBoth)
	if ledgerHasFailure(failures, "refusal-class", sharedPattern) {
		t.Fatalf("a shared node must accept a row citing both canonical emitters: %v", failures)
	}
	rt6, census6 := build()
	if err := rt6.parseGoSource("sharedclass_witness.go", "package agent\n\nfunc madeUpCaller(base string) string {\n\treturn madeUpRefusal(base)\n}\n"); err != nil {
		t.Fatal(err)
	}
	sharedFake := unboundedOutput("the shared witness accumulates with no finite producer guard", ledgerReadRefusalPath, "sharedclass_witness.go:madeUpCaller")
	failures = run(rt6, census6, sharedFake)
	if !ledgerHasFailure(failures, "source-evaluation", sharedPattern) {
		t.Fatalf("a cited non-emitter must fail the row's source evaluation: %v", failures)
	}
}
