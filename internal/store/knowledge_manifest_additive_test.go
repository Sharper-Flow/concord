package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// additiveManifestBytes encodes a valid schema-1.3 manifest whose records,
// domains, relations, dispositions, and nested objects exercise every decoder
// the manifest parse drives.
func additiveManifestBytes(t *testing.T) []byte {
	t.Helper()
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.3",
		SupportedKinds: []string{"decision", "spec"},
		IndexedKinds:   []string{"decision", "spec"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "memory", Name: "Memory", Purpose: "Registry law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: "product-root:concord", GoverningLawIDs: []string{"CD-0001"}}}},
			},
		},
		Records: []KnowledgeRecord{
			{
				ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001-fixture.md", Status: "accepted",
				Date: "2026-08-10T00:00:00Z", Title: "Fixture decision", Summary: "Decision summary", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
				HomeDomainID: "product-root:concord", ProductWideRationale: "Fixture law binds every child Domain.",
				LawRelations: []KnowledgeRelation{{Kind: "refines", TargetID: "spec-1"}},
				SHA256:       "sha256:" + strings.Repeat("a", 64),
			},
			{
				ID: "spec-1", Kind: "spec", Path: "docs/specs/fixture.md", Status: "accepted",
				Date: "2026-08-10T00:00:00Z", Title: "Fixture spec", Summary: "Spec summary", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "derived"},
				Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
				HomeDomainID: "memory", CriterionBindings: []KnowledgeCriterionBinding{{Criterion: 1, Scenario: "scenario-one"}},
				SHA256: "sha256:" + strings.Repeat("b", 64),
			},
		},
		Dispositions: []KnowledgeDisposition{{Path: "docs/scratch/retired.md", Disposition: "archived", Reason: "Superseded working note kept for provenance only."}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// additiveDocument decodes the fixture into a mutable document and injects one
// undeclared field at every level the manifest read path decodes: root, record,
// authority, scopes, law relation, criterion binding, domain, architecture
// relation, disposition, and the domain registry itself.
func additiveDocument(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(additiveManifestBytes(t), &document); err != nil {
		t.Fatal(err)
	}
	document["future_root"] = true
	registry := document["domain_registry"].(map[string]any)
	registry["future_registry"] = 1
	child := registry["domains"].([]any)[1].(map[string]any)
	child["future_domain"] = 1
	child["architecture_relations"].([]any)[0].(map[string]any)["future_relation"] = 1
	records := document["records"].([]any)
	decision := records[0].(map[string]any)
	decision["future_record"] = 1
	decision["authority"].(map[string]any)["future_tier"] = 1
	decision["scopes"].(map[string]any)["future_scope"] = 1
	decision["law_relations"].([]any)[0].(map[string]any)["future_law_relation"] = 1
	spec := records[1].(map[string]any)
	spec["future_record"] = 1
	spec["criterion_bindings"].([]any)[0].(map[string]any)["future_binding"] = 1
	// KnowledgeManifest.MarshalJSON emits the head fields only, so the
	// disposition enters the document here rather than by mutation.
	document["dispositions"] = []any{map[string]any{
		"path": "docs/scratch/retired.md", "disposition": "archived",
		"reason": "Superseded working note kept for provenance only.", "future_disposition": 1,
	}}
	return document
}

// TestParseAdmitsAdditiveManifestFieldsAtAKnownSchema holds the additive read
// rule: a release-pinned core parses a manifest authored after its release. At
// a known schema_version every decoder on the manifest path — root, record,
// authority, scopes, law relation, criterion binding, domain, architecture
// relation, and disposition — drops a field its model does not declare instead
// of refusing the document. The refusals that tolerance must not carry away
// stay asserted in the same test: an unknown schema_version, duplicate keys,
// and trailing values still refuse.
func TestParseAdmitsAdditiveManifestFieldsAtAKnownSchema(t *testing.T) {
	t.Parallel()
	additive, err := json.Marshal(additiveDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseKnowledgeManifest(additive)
	if err != nil {
		t.Fatalf("a manifest authored with additive fields was refused: %v", err)
	}
	if manifest.SchemaVersion != "1.3" || len(manifest.Records) != 2 || len(manifest.Dispositions) != 1 {
		t.Fatalf("additive read lost modeled content: %+v", manifest)
	}
	if got := manifest.Records[0].Summary; got != "Decision summary" {
		t.Fatalf("decision summary = %q", got)
	}
	if got := manifest.Records[1].CriterionBindings[0].Criterion; got != 1 {
		t.Fatalf("criterion binding = %d", got)
	}
	if got := manifest.DomainRegistry.Domains[1].ArchitectureRelations[0].Kind; got != "depends_on" {
		t.Fatalf("architecture relation kind = %q", got)
	}

	versionDrift := additiveDocument(t)
	versionDrift["schema_version"] = "1.4"
	drifted, err := json.Marshal(versionDrift)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseKnowledgeManifest(drifted)
	assertFailureKind(t, err, KindInvalidNoteProof)

	duplicated := append([]byte(`{"schema_version":"1.3",`), additive[1:]...)
	if err := rejectDuplicateJSONKeys(duplicated); err == nil {
		t.Fatal("the duplicate-key fixture carries no duplicate key")
	}
	_, err = parseKnowledgeManifest(duplicated)
	assertFailureKind(t, err, KindInvalidNoteProof)

	_, err = parseKnowledgeManifest(append(additive, []byte(" {}")...))
	assertFailureKind(t, err, KindInvalidNoteProof)
}

// modelJSONFields maps each JSON name a model struct declares to the Go type
// the decoder reads it into.
func modelJSONFields(model reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for index := 0; index < model.NumField(); index++ {
		field := model.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			fields[name] = field.Type
		}
	}
	return fields
}

// unmodeledShardKeys walks one decoded shard beside the Go type the reader
// decodes it into and returns every key path that type does not declare. The
// check is per object type, so a key declared on one type does not admit the
// same key on another. At the manifest root, a key manifestRootKeys declares
// but does not project (the doc_contract policy, CD-0114) is schema-owned and
// its subtree is not walked.
func unmodeledShardKeys(path string, value any, model reflect.Type) []string {
	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	var findings []string
	switch typed := value.(type) {
	case map[string]any:
		switch model.Kind() {
		case reflect.Struct:
			fields := modelJSONFields(model)
			for key, nested := range typed {
				child := path + "." + key
				if model == reflect.TypeFor[KnowledgeManifest]() {
					projected, declared := manifestRootKeys[key]
					if declared && !projected {
						continue
					}
				}
				fieldType, declared := fields[key]
				if !declared {
					findings = append(findings, child)
					continue
				}
				findings = append(findings, unmodeledShardKeys(child, nested, fieldType)...)
			}
		case reflect.Map:
			for key, nested := range typed {
				findings = append(findings, unmodeledShardKeys(path+"."+key, nested, model.Elem())...)
			}
		}
	case []any:
		if model.Kind() == reflect.Slice || model.Kind() == reflect.Array {
			for _, item := range typed {
				findings = append(findings, unmodeledShardKeys(path+"[]", item, model.Elem())...)
			}
		}
	}
	sort.Strings(findings)
	return findings
}

// TestUnmodeledShardKeysChecksEachObjectType holds the guard's precision: a
// record key that only another object type declares is found, a key that
// only shares a prefix with the schema-owned doc_contract subtree is found,
// and the doc_contract subtree itself is not walked.
func TestUnmodeledShardKeysChecksEachObjectType(t *testing.T) {
	t.Parallel()
	record := map[string]any{"id": "CD-0001", "reason": "declared only on dispositions", "scopes": map[string]any{"mode": "home", "state": "declared only on relations"}}
	if got, want := unmodeledShardKeys("$", record, reflect.TypeFor[KnowledgeRecord]()), []string{"$.reason", "$.scopes.state"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("record findings = %v, want %v", got, want)
	}
	head := map[string]any{"schema_version": "1.3", "doc_contract": map[string]any{"anything": true}, "doc_contract_extra": true}
	if got, want := unmodeledShardKeys("$", head, reflect.TypeFor[KnowledgeManifest]()), []string{"$.doc_contract_extra"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("head findings = %v, want %v", got, want)
	}
}

// TestCommittedManifestCarriesNoFieldTheModelDrops holds the guard that keeps
// the additive read honest: the reader silently drops what its model does not
// declare, so this repository's own shard tree may never carry such a field.
// Authoring that needs a new field adds it to the model in the same change; a
// field that must restrict older cores bumps schema_version instead.
func TestCommittedManifestCarriesNoFieldTheModelDrops(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	shards := map[string]reflect.Type{knowledgeHeadPath: reflect.TypeFor[KnowledgeManifest](), knowledgeRegistryPath: reflect.TypeFor[KnowledgeDomainRegistry]()}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(knowledgeRecordTree)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		shards[knowledgeRecordTree+"/"+entry.Name()] = reflect.TypeFor[KnowledgeRecord]()
	}
	for shard, model := range shards {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(shard)))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(content, &document); err != nil {
			t.Fatalf("%s is not valid JSON: %v", shard, err)
		}
		for _, key := range unmodeledShardKeys("$", document, model) {
			t.Errorf("%s carries %s, which the Go knowledge model drops", shard, key)
		}
	}
}
