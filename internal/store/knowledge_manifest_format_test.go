package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The knowledge manifest is a reviewed artifact, so its formatting is part of
// its contract: a publication must add a record, not rewrite the file. These
// tests bind the Go emitter to the authored bytes of
// docs/concord-knowledge-index.v1.json, which scripts/generate-knowledge-index.py
// derives from the record shards, and to the root key order declared by
// contracts/concord-knowledge-index.v1.schema.json.

const liveKnowledgeManifestPath = "../../docs/concord-knowledge-index.v1.json"

// jsonObjectKeyOrder reports the key order of one JSON object, which
// encoding/json discards when it decodes into a map or a struct.
func jsonObjectKeyOrder(data []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("json: expected an object")
	}
	keys := []string{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("json: invalid object key")
		}
		keys = append(keys, key)
		if err := walkJSONValue(decoder); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// schemaOrderedKeys reads a subschema's key order, which the sorted
// schemaObjectKeys helper discards.
func schemaOrderedKeys(t *testing.T, raw json.RawMessage, keys ...string) []string {
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
	order, err := jsonObjectKeyOrder(current)
	if err != nil {
		t.Fatalf("schema path %v: %v", keys, err)
	}
	if len(order) == 0 {
		t.Fatalf("schema path %v declares no members", keys)
	}
	return order
}

// TestKnowledgeManifestRootKeyOrderMatchesSchema binds the emitter's root key
// order to the schema's property order. The schema is where the manifest's
// meaning-grouped root shape is declared; a root key added there must be placed
// in canonicalManifestRootOrder, not appended wherever the Go struct happens to
// grow. Record keys are deliberately absent from this binding: the generator
// emits them sorted, so Go emits them from a map and needs no list.
func TestKnowledgeManifestRootKeyOrderMatchesSchema(t *testing.T) {
	data, err := os.ReadFile(knowledgeSchemaPath)
	if err != nil {
		t.Fatalf("read knowledge index schema: %v", err)
	}
	rootOrder := schemaOrderedKeys(t, json.RawMessage(data), "properties")
	if !reflect.DeepEqual(rootOrder, canonicalManifestRootOrder) {
		t.Errorf("canonicalManifestRootOrder diverges from %s\n  schema: %v\n  Go:     %v", knowledgeSchemaPath, rootOrder, canonicalManifestRootOrder)
	}
}

// TestMarshalLiveKnowledgeManifestIsByteIdentical is the whole contract in one
// assertion: parsing and re-emitting the repository's own generated aggregate
// must return the file unchanged. The aggregate is produced by
// scripts/generate-knowledge-index.py, so this binds the Go writer to the
// generator's bytes. Semantic equality is not enough — a byte-different rewrite
// buries the record a publication adds under the reordering noise of every
// record it did not touch.
func TestMarshalLiveKnowledgeManifestIsByteIdentical(t *testing.T) {
	data, err := os.ReadFile(liveKnowledgeManifestPath)
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
	if bytes.Equal(data, encoded) {
		return
	}
	want, got := strings.Split(string(data), "\n"), strings.Split(string(encoded), "\n")
	for index := 0; index < len(want) && index < len(got); index++ {
		if want[index] != got[index] {
			t.Fatalf("%s line %d differs after a Go round trip\n  authored: %q\n  emitted:  %q", liveKnowledgeManifestPath, index+1, want[index], got[index])
		}
	}
	t.Fatalf("%s changed length after a Go round trip: authored %d lines, emitted %d lines", liveKnowledgeManifestPath, len(want), len(got))
}

// canonicalOrderManifest builds a manifest entirely in Go, with policy keys in
// a Go map whose iteration order the runtime randomizes.
func canonicalOrderManifest(t *testing.T) KnowledgeManifest {
	t.Helper()
	return KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
		uninterpreted: map[string]json.RawMessage{
			"knowledge_roots": json.RawMessage(`["docs/"]`),
			"exclusions":      json.RawMessage(`["docs/research/"]`),
			"doc_contract":    json.RawMessage(`{"enforced":false}`),
		},
		Dispositions: []KnowledgeDisposition{{
			Path: "docs/scratch.md", Disposition: "archived", Reason: "Superseded working note kept for provenance only.",
		}},
		Records: []KnowledgeRecord{{
			ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001-law.md", Status: "superseded",
			Date: "2026-08-18T00:00:00Z", Title: "Law", Summary: "A record carrying every optional key.",
			Tags: []string{}, Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}, domainIDsPresent: true},
			Successor: "CD-0002", SHA256: "sha256:" + strings.Repeat("a", 64),
			LawRelations: []KnowledgeRelation{{Kind: "supersedes", TargetID: "CD-0000"}},
			Evidence:     []string{"internal/store/knowledge_manifest.go"},
			HomeDomainID: "product-root:concord", AppliesToDomainIDs: []string{"product-root:concord"},
			ProductWideRationale: "Fixture law binds every child Domain.",
		}},
	}
}

// TestMarshalKnowledgeManifestEmitsCanonicalOrder proves the ordering is not
// vacuous. Go randomizes map iteration, so a manifest built in memory would
// emit a different root key order on every run if the emitter did not impose
// one. The record carries every optional key, so each one's position is
// asserted, not merely its presence.
func TestMarshalKnowledgeManifestEmitsCanonicalOrder(t *testing.T) {
	wantRoot := canonicalManifestRootOrder
	wantRecord := sortedRecordKeys
	for attempt := range 32 {
		encoded, err := marshalKnowledgeManifest(canonicalOrderManifest(t))
		if err != nil {
			t.Fatalf("attempt %d: marshal: %v", attempt, err)
		}
		rootOrder, err := jsonObjectKeyOrder(encoded)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if !reflect.DeepEqual(rootOrder, wantRoot) {
			t.Fatalf("attempt %d: top-level keys emitted out of canonical order\n  want: %v\n  got:  %v", attempt, wantRoot, rootOrder)
		}
		if got := emittedRecordKeyOrder(t, encoded, 0); !reflect.DeepEqual(got, wantRecord) {
			t.Fatalf("attempt %d: record keys emitted out of canonical order\n  want: %v\n  got:  %v", attempt, wantRecord, got)
		}
	}
}

// sortedRecordKeys is the record key order scripts/generate-knowledge-index.py
// emits — plain lexical order over every key the record carries — for a record
// carrying every optional key.
var sortedRecordKeys = []string{
	"applies_to_domain_ids", "date", "evidence", "home_domain_id", "id", "kind",
	"law_relations", "path", "product_wide_rationale", "scopes", "sha256",
	"status", "successor", "summary", "tags", "title",
}

// TestMarshalKnowledgeManifestNormalizesAuthoredRecordOrder states the rule the
// byte-identity test depends on. The aggregate is generated, so every record in
// it is already in sorted key order; a record parsed from any other order must
// still emit sorted, because the Go writer and the generator have to agree on
// one order rather than on whichever order a file happened to carry.
func TestMarshalKnowledgeManifestNormalizesAuthoredRecordOrder(t *testing.T) {
	authored := []byte(`{
  "schema_version": "1.2",
  "supported_kinds": ["decision"],
  "indexed_kinds": ["decision"],
  "domain_registry": {
    "schema_version": "1.0",
    "product_key": "concord",
    "root_domain_id": "product-root:concord",
    "domains": [
      {"domain_id": "product-root:concord", "name": "Concord", "purpose": "Product-wide law", "status": "current", "architecture_relations": []}
    ]
  },
  "records": [
    {
      "kind": "decision",
      "id": "CD-0001",
      "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
      "path": "docs/decisions/CD-0001-law.md",
      "status": "accepted",
      "date": "2026-08-18T00:00:00Z",
      "title": "Law",
      "summary": "A record authored in an order the generator does not use.",
      "tags": [],
      "home_domain_id": "product-root:concord",
      "product_wide_rationale": "Fixture law binds every child Domain.",
      "sha256": "sha256:` + strings.Repeat("a", 64) + `"
    }
  ]
}
`)
	want := []string{"date", "home_domain_id", "id", "kind", "path", "product_wide_rationale", "scopes", "sha256", "status", "summary", "tags", "title"}
	manifest, err := parseKnowledgeManifest(authored)
	if err != nil {
		t.Fatalf("parse authored manifest: %v", err)
	}
	encoded, err := marshalKnowledgeManifest(manifest)
	if err != nil {
		t.Fatalf("marshal authored manifest: %v", err)
	}
	if got := emittedRecordKeyOrder(t, encoded, 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("a shuffled record did not take the generator's key order\n  want: %v\n  got:  %v", want, got)
	}
}

func emittedRecordKeyOrder(t *testing.T, manifest []byte, index int) []string {
	t.Helper()
	var decoded struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatalf("decode emitted manifest: %v", err)
	}
	if index >= len(decoded.Records) {
		t.Fatalf("emitted manifest carries %d records, wanted record %d", len(decoded.Records), index)
	}
	order, err := jsonObjectKeyOrder(decoded.Records[index])
	if err != nil {
		t.Fatalf("emitted record %d: %v", index, err)
	}
	return order
}

// liveManifestRepoFixture seeds a git knowledge home from the repository's own
// manifest. A synthetic fixture cannot prove the diff claim: only the real file
// carries the whole generated record set a publication must leave alone.
func liveManifestRepoFixture(t *testing.T) (string, []byte) {
	t.Helper()
	authored, err := os.ReadFile(liveKnowledgeManifestPath)
	if err != nil {
		t.Fatalf("read live knowledge manifest: %v", err)
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Manifest Test")
	if err := os.MkdirAll(filepath.Join(repo, "docs/lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, lessonManifestPath), authored, 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "seed the live manifest")
	return repo, authored
}

// TestPublishLessonRecordDiffsOnlyTheAddedRecord states the outcome the
// formatting contract exists for. Deleting the published record's block from
// the rewritten manifest must restore the authored bytes exactly, so the whole
// diff is that block and the comma that attaches it.
func TestPublishLessonRecordDiffsOnlyTheAddedRecord(t *testing.T) {
	repo, authored := liveManifestRepoFixture(t)
	published, err := PublishLessonRecord(context.Background(), KnowledgeHome{RepoPath: repo}, LessonPublication{
		LessonID: "lesson-manifest-diff", Title: "A publication adds one record",
		Summary: "Publishing a lesson rewrites the manifest without reformatting it.",
		Content: "# A publication adds one record\n\nThe diff is the record.\n",
		Tags:    []string{"knowledge"},
		Scopes:  KnowledgeRecordScopes{Mode: "home"},
		Now:     time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rewritten, err := os.ReadFile(filepath.Join(repo, lessonManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	block := renderedRecordBlock(t, published.Record)
	if !strings.Contains(string(rewritten), block) {
		t.Fatalf("the rewritten manifest does not carry the published record as written:\n%s", block)
	}
	restored := strings.Replace(string(rewritten), ",\n"+block, "", 1)
	if restored == string(authored) {
		return
	}
	want, got := strings.Split(string(authored), "\n"), strings.Split(restored, "\n")
	for index := 0; index < len(want) && index < len(got); index++ {
		if want[index] != got[index] {
			t.Fatalf("publication changed manifest line %d outside the record it added\n  authored: %q\n  written:  %q", index+1, want[index], got[index])
		}
	}
	t.Fatalf("publication changed the manifest length outside the record it added: authored %d lines, written %d lines", len(want), len(got))
}

// renderedRecordBlock renders one record exactly as it appears inside the
// manifest's records array, at that array's indentation.
func renderedRecordBlock(t *testing.T, record KnowledgeRecord) string {
	t.Helper()
	compact, err := marshalManifestValue(manifestRecordEntry(record))
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	indented := bytes.Buffer{}
	if err := json.Indent(&indented, compact, "    ", "  "); err != nil {
		t.Fatalf("indent record: %v", err)
	}
	return "    " + indented.String()
}
