package store

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The knowledge manifest vocabulary is declared once, in
// contracts/concord-knowledge-index.v1.schema.json. This file binds the Go
// model to that declaration so a kind, a record field, or a top-level key
// cannot be added on one side alone. Divergence in either direction fails.

const knowledgeSchemaPath = "../../contracts/concord-knowledge-index.v1.schema.json"

type knowledgeSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Defs       map[string]json.RawMessage `json:"$defs"`
}

func loadKnowledgeSchema(t *testing.T) knowledgeSchema {
	t.Helper()
	data, err := os.ReadFile(knowledgeSchemaPath)
	if err != nil {
		t.Fatalf("read knowledge index schema: %v", err)
	}
	var schema knowledgeSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse knowledge index schema: %v", err)
	}
	if len(schema.Properties) == 0 || len(schema.Defs) == 0 {
		t.Fatal("knowledge index schema declares no properties or $defs")
	}
	return schema
}

// schemaEnum reads an enum from a subschema addressed by a chain of object
// keys, so a schema restructure fails loudly instead of silently matching
// nothing.
func schemaEnum(t *testing.T, raw json.RawMessage, keys ...string) []string {
	t.Helper()
	current := raw
	for _, key := range keys {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(current, &node); err != nil {
			t.Fatalf("schema path %v: %v", keys, err)
		}
		next, ok := node[key]
		if !ok {
			t.Fatalf("schema path %v: missing key %q", keys, key)
		}
		current = next
	}
	var holder struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(current, &holder); err != nil {
		t.Fatalf("schema path %v: %v", keys, err)
	}
	if len(holder.Enum) == 0 {
		t.Fatalf("schema path %v declares no enum", keys)
	}
	return holder.Enum
}

func schemaObjectKeys(t *testing.T, raw json.RawMessage, keys ...string) []string {
	t.Helper()
	current := raw
	for _, key := range keys {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(current, &node); err != nil {
			t.Fatalf("schema path %v: %v", keys, err)
		}
		next, ok := node[key]
		if !ok {
			t.Fatalf("schema path %v: missing key %q", keys, key)
		}
		current = next
	}
	var node map[string]json.RawMessage
	if err := json.Unmarshal(current, &node); err != nil {
		t.Fatalf("schema path %v: %v", keys, err)
	}
	if len(node) == 0 {
		t.Fatalf("schema path %v declares no members", keys)
	}
	return mapKeys(node)
}

// schemaKindsWithStatus reads the record kinds a conditional status clause
// restricts to the given status. The tier is declared in the schema as a
// kind-to-status implication, so that is where the Go tier must read it from.
func schemaKindsWithStatus(t *testing.T, record json.RawMessage, status string) []string {
	t.Helper()
	var holder struct {
		AllOf []struct {
			If struct {
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"if"`
			Then struct {
				Properties struct {
					Status struct {
						Enum []string `json:"enum"`
					} `json:"status"`
				} `json:"properties"`
			} `json:"then"`
		} `json:"allOf"`
	}
	if err := json.Unmarshal(record, &holder); err != nil {
		t.Fatalf("parse $defs.record.allOf: %v", err)
	}
	for _, clause := range holder.AllOf {
		kinds := clause.If.Properties.Kind.Enum
		statuses := clause.Then.Properties.Status.Enum
		if len(kinds) == 0 || len(statuses) == 0 {
			continue
		}
		for _, candidate := range statuses {
			if candidate == status {
				return kinds
			}
		}
	}
	t.Fatalf("%s declares no record kinds restricted to status %q", knowledgeSchemaPath, status)
	return nil
}

func mapKeys[V any](value map[string]V) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func boolSetKeys(value map[string]bool) []string {
	return mapKeys(value)
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// jsonFieldNames returns the wire names of every exported field of a struct.
// Unexported bookkeeping fields carry no wire name and are not vocabulary.
func jsonFieldNames(t *testing.T, sample any) []string {
	t.Helper()
	typ := reflect.TypeOf(sample)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("expected a struct, got %s", typ.Kind())
	}
	names := make([]string, 0, typ.NumField())
	for index := range typ.NumField() {
		field := typ.Field(index)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			t.Fatalf("field %s carries no json name", field.Name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func requireSameVocabulary(t *testing.T, subject string, schema, code []string) {
	t.Helper()
	if reflect.DeepEqual(sortedCopy(schema), sortedCopy(code)) {
		return
	}
	inSchema := difference(schema, code)
	inCode := difference(code, schema)
	t.Errorf("%s diverges from %s\n  declared in schema, absent from Go: %v\n  present in Go, absent from schema: %v",
		subject, knowledgeSchemaPath, inSchema, inCode)
}

func difference(left, right []string) []string {
	present := make(map[string]bool, len(right))
	for _, value := range right {
		present[value] = true
	}
	out := []string{}
	for _, value := range left {
		if !present[value] {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func TestKnowledgeManifestVocabularyMatchesSchema(t *testing.T) {
	schema := loadKnowledgeSchema(t)
	record, ok := schema.Defs["record"]
	if !ok {
		t.Fatalf("%s declares no $defs.record", knowledgeSchemaPath)
	}

	requireSameVocabulary(t, "manifest top-level keys (manifestRootKeys)",
		mapKeys(schema.Properties), boolSetKeys(manifestRootKeys))

	requireSameVocabulary(t, "record fields (KnowledgeRecord json tags)",
		schemaObjectKeys(t, record, "properties"), jsonFieldNames(t, KnowledgeRecord{}))

	requireSameVocabulary(t, "manifest record kinds (manifestRecordKinds)",
		schemaEnum(t, record, "properties", "kind"), boolSetKeys(manifestRecordKinds))

	requireSameVocabulary(t, "closed knowledge kinds (knowledgeKindsClosed)",
		schemaEnum(t, schema.Properties["supported_kinds"], "items"), boolSetKeys(knowledgeKindsClosed))

	// supported_kinds and indexed_kinds draw from one vocabulary. Binding both
	// to the same Go set is what stops a kind from becoming declarable but not
	// indexable, which no record could then carry.
	requireSameVocabulary(t, "indexable knowledge kinds (knowledgeKindsClosed)",
		schemaEnum(t, schema.Properties["indexed_kinds"], "items"), boolSetKeys(knowledgeKindsClosed))

	// The law-bearing tier is the set of kinds the schema restricts to accepted.
	// Reading it back out of the schema's status clauses keeps the Go tier from
	// being a second, quietly divergent declaration of which kinds carry law.
	requireSameVocabulary(t, "law-bearing record kinds (manifestLawBearingKinds)",
		schemaKindsWithStatus(t, record, "accepted"), boolSetKeys(manifestLawBearingKinds))

	// Every kind the law-relation graph accepts must be law-bearing. A subject
	// outside that tier could author a relation with no status rule behind it.
	for _, kind := range boolSetKeys(manifestLawRelationSubjects) {
		if !manifestLawBearingKinds[kind] {
			t.Errorf("law relation subject %q is not a law-bearing kind", kind)
		}
	}

	requireSameVocabulary(t, "law relation kinds (lawRelationKinds)",
		schemaEnum(t, schema.Defs["lawRelation"], "properties", "kind"), boolSetKeys(lawRelationKinds))
}

// schemaRecordPathShape matches the one form of $defs.record.path the Go
// decomposition can be read out of: a negative lookahead over `|`-separated
// ineligibility members, applied to everything below docs/.
var schemaRecordPathShape = regexp.MustCompile(`^\^docs/\(\?!(.+)\)\.\*\\\.md\$$`)

// schemaIneligibleRE converts the schema's negative lookahead into an RE2
// predicate over the path remainder below docs/. RE2 cannot express the
// lookahead itself, but the alternation inside it is ordinary regex, so
// "ineligible" is exactly "some member matches at the start of the remainder".
func schemaIneligibleRE(t *testing.T, record json.RawMessage) *regexp.Regexp {
	t.Helper()
	var holder struct {
		Properties struct {
			Path struct {
				Pattern string `json:"pattern"`
			} `json:"path"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(record, &holder); err != nil {
		t.Fatalf("parse $defs.record.properties.path: %v", err)
	}
	pattern := holder.Properties.Path.Pattern
	shape := schemaRecordPathShape.FindStringSubmatch(pattern)
	if shape == nil {
		t.Fatalf("%s: $defs.record.path pattern %q is no longer the shape this binding decomposes",
			knowledgeSchemaPath, pattern)
	}
	compiled, err := regexp.Compile("^(?:" + shape[1] + ")")
	if err != nil {
		t.Fatalf("compile ineligibility alternation from %q: %v", pattern, err)
	}
	return compiled
}

// TestKnowledgeManifestIneligiblePathsMatchSchema binds validateManifestPath's
// prefix and substring rules to the schema alternation they decompose. The
// binding is differential rather than textual because RE2 carries no lookahead:
// for every probe, the Go verdict and the schema verdict must agree. A file
// excluded on one side alone fails here, in the direction it diverged.
func TestKnowledgeManifestIneligiblePathsMatchSchema(t *testing.T) {
	schema := loadKnowledgeSchema(t)
	record, ok := schema.Defs["record"]
	if !ok {
		t.Fatalf("%s declares no $defs.record", knowledgeSchemaPath)
	}
	ineligible := schemaIneligibleRE(t, record)

	probes := []string{
		// The two files CD-0014 and commit ea68397 accepted as binding
		// contracts. Both are eligible; neither carries a record yet.
		"docs/product-coordination-view.md",
		"docs/terminal-launcher-contract.md",
		// Live class exclusions, which this repeal must leave intact.
		"docs/work/notes.md",
		"docs/research/R7-expedited-parallel-work.md",
		"docs/generated-contracts.md",
		"docs/api/generated.md",
		"docs/Generated-Contracts.md",
		"docs/nested/deeply/GENERATED.md",
		// Ordinary eligible authored knowledge.
		"docs/README.md",
		"docs/decisions/CD-0014-terminal-launcher.md",
		"docs/priorities.md",
		// Near misses that must not be swept up by a substring rule.
		"docs/generation-policy.md",
		"docs/workflows.md",
		"docs/researcher-guide.md",
	}
	// Every authored markdown blob in the repository is a probe too: a
	// synthetic corpus cannot notice a rule that only bites a real file.
	probes = append(probes, repositoryDocsPaths(t)...)

	seen := make(map[string]bool, len(probes))
	for _, probe := range probes {
		if seen[probe] {
			continue
		}
		seen[probe] = true
		schemaSaysIneligible := ineligible.MatchString(strings.TrimPrefix(probe, "docs/"))
		_, goSaysIneligible := manifestPathIneligible(probe)
		if schemaSaysIneligible != goSaysIneligible {
			t.Errorf("%s: schema ineligible=%v, Go ineligible=%v", probe, schemaSaysIneligible, goSaysIneligible)
		}
		// validateManifestPath is the caller the manifest actually runs, so
		// prove the decomposition reaches it rather than only agreeing in
		// isolation.
		if err := validateManifestPath(probe); (err != nil) != goSaysIneligible {
			t.Errorf("%s: validateManifestPath disagrees with manifestPathIneligible", probe)
		}
	}
}

// repositoryDocsPaths lists every markdown blob below docs/, so the binding is
// exercised against the real corpus and not only against chosen probes.
func repositoryDocsPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	root := "../../docs"
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			return nil
		}
		out = append(out, "docs/"+filepath.ToSlash(strings.TrimPrefix(name, root+string(filepath.Separator))))
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("docs/ yielded no markdown blobs")
	}
	return out
}

// TestKnowledgeManifestIneligibleBindingDetectsDivergence proves the binding
// above is not vacuous: were the repealed exclusion still present in Go, the
// differential check would report it.
func TestKnowledgeManifestIneligibleBindingDetectsDivergence(t *testing.T) {
	schema := loadKnowledgeSchema(t)
	ineligible := schemaIneligibleRE(t, schema.Defs["record"])
	for _, repealed := range []string{"docs/product-coordination-view.md", "docs/terminal-launcher-contract.md"} {
		if ineligible.MatchString(strings.TrimPrefix(repealed, "docs/")) {
			t.Errorf("%s is still ineligible under the schema alternation", repealed)
		}
	}
	// A stale Go rule is what this binding exists to catch. Model one and
	// require the comparison to report it.
	stale := func(value string) bool {
		if _, yes := manifestPathIneligible(value); yes {
			return true
		}
		return value == "docs/terminal-launcher-contract.md"
	}
	probe := "docs/terminal-launcher-contract.md"
	if stale(probe) == ineligible.MatchString(strings.TrimPrefix(probe, "docs/")) {
		t.Fatal("a stale Go exclusion would not be reported by this binding")
	}
}

// TestKnowledgeManifestRepealedPathsValidate states the repeal directly: both
// accepted contracts may now carry a manifest record.
func TestKnowledgeManifestRepealedPathsValidate(t *testing.T) {
	for _, eligible := range []string{"docs/product-coordination-view.md", "docs/terminal-launcher-contract.md"} {
		if err := validateManifestPath(eligible); err != nil {
			t.Errorf("validateManifestPath(%q) = %v, want nil", eligible, err)
		}
	}
	for _, rejected := range []string{
		"docs/work/scratch.md",
		"docs/research/R1-probe.md",
		"docs/generated-agent-contracts.md",
		"docs/Generated.md",
	} {
		if err := validateManifestPath(rejected); err == nil {
			t.Errorf("validateManifestPath(%q) = nil, want an ineligibility failure", rejected)
		}
	}
}

// The operator hint must state the rules that produced the failure. A hint
// naming a rule the condition does not enforce is how the previous message
// went stale unnoticed for eleven days.
func TestKnowledgeManifestIneligibleHintNamesEnforcedRules(t *testing.T) {
	hint := manifestIneligibleHint()
	for _, prefix := range manifestIneligiblePrefixes {
		if !strings.Contains(hint, prefix) {
			t.Errorf("hint %q omits enforced prefix %q", hint, prefix)
		}
	}
	if !strings.Contains(hint, manifestIneligibleSubstring) {
		t.Errorf("hint %q omits enforced substring %q", hint, manifestIneligibleSubstring)
	}
	for _, repealed := range []string{"product-coordination-view", "terminal-launcher-contract"} {
		if strings.Contains(hint, repealed) {
			t.Errorf("hint %q still names the repealed exclusion %q", hint, repealed)
		}
	}
}

// TestKnowledgeManifestVocabularyBindingDetectsDivergence proves the binding
// above is not vacuous: a schema that gains or loses a member is reported in
// the direction it diverged.
func TestKnowledgeManifestVocabularyBindingDetectsDivergence(t *testing.T) {
	if got := difference([]string{"a", "b"}, []string{"b"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("schema-only divergence not reported: %v", got)
	}
	if got := difference([]string{"b"}, []string{"a", "b"}); len(got) != 0 {
		t.Fatalf("expected no divergence, got %v", got)
	}
	probe := &testing.T{}
	requireSameVocabulary(probe, "probe", []string{"a"}, []string{"b"})
	if !probe.Failed() {
		t.Fatal("requireSameVocabulary accepted divergent vocabularies")
	}
}

// The repository's own manifest is the artifact this package must be able to
// read. Fixtures model only the keys the package projects onto struct fields,
// so a top-level key added to the real file is invisible to them: knowledge
// closure policy landed in the manifest and lesson publication failed at parse
// with invalid_note_proof, undetected, because no test ever opened it.
func TestParseLiveKnowledgeManifest(t *testing.T) {
	const livePath = "../../docs/concord-knowledge-index.v1.json"
	data, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("read live knowledge manifest: %v", err)
	}
	manifest, err := parseKnowledgeManifest(data)
	if err != nil {
		t.Fatalf("parse live knowledge manifest %s: %v", livePath, err)
	}
	if len(manifest.Records) == 0 {
		t.Fatal("live knowledge manifest parsed with no records")
	}
}

// Parsing the live manifest is necessary but not sufficient: a key the package
// does not model must survive a publish round trip rather than be dropped.
func TestLiveKnowledgeManifestSurvivesRoundTrip(t *testing.T) {
	const livePath = "../../docs/concord-knowledge-index.v1.json"
	data, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf("read live knowledge manifest: %v", err)
	}
	manifest, err := parseKnowledgeManifest(data)
	if err != nil {
		t.Fatalf("parse live knowledge manifest: %v", err)
	}
	encoded, err := marshalKnowledgeManifest(manifest)
	if err != nil {
		t.Fatalf("marshal live knowledge manifest: %v", err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatalf("decode source manifest: %v", err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatalf("decode round-tripped manifest: %v", err)
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			t.Errorf("round trip dropped top-level key %q", key)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			t.Errorf("round trip invented top-level key %q", key)
		}
	}
}
