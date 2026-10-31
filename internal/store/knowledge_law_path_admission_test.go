package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func lawTreeRegistry() KnowledgeDomainRegistry {
	return KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
		Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
	}
}

func lawTreeRecord(path, kind, status string) KnowledgeRecord {
	record := KnowledgeRecord{
		ID: "CD-0300", Kind: kind, Path: path, Status: status,
		Date: "2026-10-30T00:00:00Z", Title: "Law tree", Summary: "Carries law in the law tree",
		Tags:      []string{},
		Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1},
		Scopes:    KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
		SHA256:    "sha256:" + strings.Repeat("a", 64),
	}
	if status == "accepted" {
		record.HomeDomainID = "product-root:concord"
		record.ProductWideRationale = "The law tree is Product-wide placement law."
	}
	return record
}

func TestKnowledgeManifestAdmitsLawTreeRecordPaths(t *testing.T) {
	t.Parallel()
	for _, eligible := range []string{
		".concord/law/decisions/CD-0300-synthetic.md",
		".concord/law/specs/SPEC-0001-synthetic.md",
		".concord/law/constitution/founding.md",
		".concord/law/README.md",
		".concord/law/generation-policy.md",
	} {
		if err := validateManifestPath(eligible); err != nil {
			t.Errorf("validateManifestPath(%q) = %v, want nil", eligible, err)
		}
	}
	for _, rejected := range []string{
		".concord/law/work/scratch.md",
		".concord/law/research/R1-probe.md",
		".concord/law/generated-agent-contracts.md",
		".concord/law/Generated.md",
	} {
		if err := validateManifestPath(rejected); err == nil {
			t.Errorf("validateManifestPath(%q) = nil, want an ineligibility failure", rejected)
		}
	}
}

func TestLawTreeRecordParsesAndComposes(t *testing.T) {
	t.Parallel()
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.3",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: lawTreeRegistry(),
		Records:        []KnowledgeRecord{lawTreeRecord(".concord/law/decisions/CD-0300-synthetic.md", "decision", "accepted")},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseKnowledgeManifest(encoded)
	if err != nil {
		t.Fatalf("a law-tree decision refused to parse: %v", err)
	}
	if len(parsed.Records) != 1 || parsed.Records[0].Path != ".concord/law/decisions/CD-0300-synthetic.md" {
		t.Fatalf("parse lost the law-tree record: %+v", parsed.Records)
	}
	shards := knowledgeShards{
		head: mustJSONRaw(t, map[string]any{
			"schema_version":  "1.3",
			"supported_kinds": []string{"decision"},
			"indexed_kinds":   []string{"decision"},
		}),
		registry: mustJSONRaw(t, lawTreeRegistry()),
		records: map[string][]byte{
			"CD-0300.json": mustJSONRaw(t, lawTreeRecord(".concord/law/decisions/CD-0300-synthetic.md", "decision", "accepted")),
		},
		recordPathPrefix: manifestRecordPathPrefix,
	}
	composed, err := composeKnowledgeManifest(shards)
	if err != nil {
		t.Fatalf("a law-tree record refused to compose from the current shard layout: %v", err)
	}
	if len(composed.Records) != 1 || composed.Records[0].Path != ".concord/law/decisions/CD-0300-synthetic.md" {
		t.Fatalf("composition lost the law-tree record: %+v", composed.Records)
	}
}

func TestLawTreeAdmissionPreservesDocsTiersAndOverrides(t *testing.T) {
	t.Parallel()
	if err := validateManifestPath(".concord/docs/decisions/CD-0300-synthetic.md"); err != nil {
		t.Errorf("docs tree admission broke: %v", err)
	}
	if err := validateRecordPathForTier("docs/decisions/CD-0300-synthetic.md", preMigrationManifestRecordPathPrefix, nil); err != nil {
		t.Errorf("pre-migration docs tier admission broke: %v", err)
	}
	if err := validateRecordPathForTier(".concord/docs/decisions/CD-0300-synthetic.md", preMigrationManifestRecordPathPrefix, nil); err == nil {
		t.Error("a .concord/docs path validated under the pre-migration docs tier")
	}
	if err := validateRecordPathForTier(".concord/law/decisions/CD-0300-synthetic.md", preMigrationManifestRecordPathPrefix, nil); err == nil {
		t.Error("a law path validated under the pre-migration docs tier")
	}
	override := KnowledgeOperatorOverride{
		Path: ".concord/law/", ProductID: "concord", RecordedIn: "CD-0300",
		Reason: "the law tree is inside .concord and cannot be overridden",
	}
	if err := validateOperatorOverride(override, "concord"); err == nil {
		t.Error("an override naming .concord/law/ validated")
	}
	covered := func(string) bool { return true }
	if err := validateRecordPathForTier("external/knowledge/reference.md", manifestRecordPathPrefix, covered); err != nil {
		t.Errorf("the external override route broke: %v", err)
	}
	if err := validateRecordPathForTier("external/knowledge/reference.md", manifestRecordPathPrefix, nil); err == nil {
		t.Error("an uncovered external record path validated")
	}
}

func TestAggregateRecordPathPrefixRecognizesRelocatedLaw(t *testing.T) {
	t.Parallel()
	for _, paths := range [][]string{
		{".concord/law/decisions/CD-0300-synthetic.md"},
		{".concord/law/specs/synthetic.md", ".concord/docs/lessons/synthetic.md"},
	} {
		records := make([]map[string]string, len(paths))
		for i, path := range paths {
			records[i] = map[string]string{"path": path}
		}
		data, err := json.Marshal(map[string]any{"records": records})
		if err != nil {
			t.Fatal(err)
		}
		if got := aggregateRecordPathPrefix(data); got != manifestRecordPathPrefix {
			t.Errorf("aggregateRecordPathPrefix(%s) = %q", data, got)
		}
	}
}

func TestValidateNotePathAdmitsLawDecisionNotes(t *testing.T) {
	t.Parallel()
	for _, admitted := range []string{
		".concord/law/decisions/CD-0300-synthetic.md",
		".concord/docs/decisions/CD-0300-synthetic.md",
		"docs/decisions/CD-0300-synthetic.md",
	} {
		if err := validateNotePath(admitted); err != nil {
			t.Errorf("validateNotePath(%q) = %v, want nil", admitted, err)
		}
	}
	for _, refused := range []string{
		".concord/law/work/note.md",
		".concord/law/constitution/founding.md",
	} {
		if err := validateNotePath(refused); err == nil {
			t.Errorf("validateNotePath(%q) = nil, want a placement refusal", refused)
		}
	}
}
