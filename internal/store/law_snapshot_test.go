package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lawSnapshotRegistryJSON = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Synthetic Product law organization.","status":"current","architecture_relations":[]},{"domain_id":"product:reviews","name":"Reviews","purpose":"Synthetic review Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}` + "\n"

const lawSnapshotRegistryRootOnlyJSON = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Synthetic Product law organization.","status":"current","architecture_relations":[]}]}` + "\n"

const lawSnapshotRegistryExtendedJSON = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Synthetic Product law organization.","status":"current","architecture_relations":[]},{"domain_id":"product:reviews","name":"Reviews","purpose":"Synthetic review Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"product:billing","name":"Billing","purpose":"Synthetic billing Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}` + "\n"

// lawSnapshotDecisionContent builds one synthetic decision law file whose
// frontmatter carries only the retained metadata fields.
func lawSnapshotDecisionContent(id, extraFields string) string {
	front := `{"id":"` + id + `","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product:reviews","applies_to_domain_ids":[]`
	if extraFields != "" {
		front += "," + extraFields
	}
	front += "}"
	return "---\n" + front + "\n---\n\nSynthetic decision body.\n"
}

// lawSnapshotTierLawContent builds one synthetic constitution or spec law file.
func lawSnapshotTierLawContent(id, title, extraFields string) string {
	front := `{"id":"` + id + `","title":"` + title + `","authority":{"tier":"derived"},"home_domain_id":"product:reviews","applies_to_domain_ids":[]`
	if extraFields != "" {
		front += "," + extraFields
	}
	front += "}"
	return "---\n" + front + "\n---\n\nSynthetic tier body.\n"
}

const lawSnapshotRootLawContent = "---\n" +
	`{"id":"CD-9140","title":"Snapshot product-wide law","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product-root:product","applies_to_domain_ids":[],"product_wide_rationale":"This synthetic law governs the whole Product."}` + "\n" +
	"---\n\nSynthetic product-wide body.\n"

func lawSnapshotFixture(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	for path, content := range files {
		writeKnowledgeFile(t, repo, path, content)
	}
	return repo, commitKnowledgeRepo(t, repo, "synthetic law snapshot fixture")
}

func lawSnapshotMustLoad(t *testing.T, repo, commit string) *lawFolderSnapshot {
	t.Helper()
	snapshot, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	if err != nil {
		t.Fatalf("loadLawFolderSnapshot refused a valid synthetic home: %v", err)
	}
	return snapshot
}

func lawSnapshotMustRegistry(t *testing.T, snapshot *lawFolderSnapshot) KnowledgeDomainRegistry {
	t.Helper()
	registry, err := snapshot.Registry()
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestLawFolderSnapshotIncludesFolderLawWithoutManifest(t *testing.T) {
	content := lawSnapshotDecisionContent("CD-9101", "")
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9101-snapshot-decision.md": content,
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	if snapshot.CommitOID() != commit {
		t.Fatalf("snapshot is not bound to the explicit commit: got %s, want %s", snapshot.CommitOID(), commit)
	}
	record, ok := snapshot.Law("CD-9101")
	if !ok {
		t.Fatalf("folder law missing from snapshot: %v", snapshot.LawIDs())
	}
	sum := sha256.Sum256([]byte(content))
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	if record.SHA256 != wantHash {
		t.Fatalf("folder law digest is not the whole-file sha256: got %s, want %s", record.SHA256, wantHash)
	}
	if record.Kind != "decision" || record.Status != "accepted" {
		t.Fatalf("folder classification changed: kind=%s status=%s", record.Kind, record.Status)
	}
	if record.Path != ".concord/law/decisions/CD-9101-snapshot-decision.md" {
		t.Fatalf("folder path not carried: %s", record.Path)
	}
	if record.Authority.Tier != "legislated" || record.Authority.LegislatedBy != "work-0f1e2d3c4b5a6978" || record.Authority.ContractVersion != 1 {
		t.Fatalf("authored authority not carried: %+v", record.Authority)
	}
	if record.HomeDomainID != "product:reviews" {
		t.Fatalf("law home not carried: %s", record.HomeDomainID)
	}
	if record.Summary != "" || record.Date != "" || record.Tags != nil || record.Scopes.Mode != "" || record.Evidence != nil {
		t.Fatalf("snapshot carries retired metadata: %+v", record)
	}
}

func TestLawFolderSnapshotExcludesDocTreeLookalikes(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9101-snapshot-decision.md": lawSnapshotDecisionContent("CD-9101", ""),
		".concord/docs/decisions/CD-9102-lookalike.md":        lawSnapshotDecisionContent("CD-9102", ""),
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	if _, ok := snapshot.Law("CD-9102"); ok {
		t.Fatalf("doc-tree lookalike resolved as folder law")
	}
	ids := snapshot.LawIDs()
	if len(ids) != 1 || ids[0] != "CD-9101" {
		t.Fatalf("snapshot inventory is not law-folder only: %v", ids)
	}
}

func TestLawFolderSnapshotAcceptsNestedArbitraryFiles(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/team-a/2026/review-policy.md": lawSnapshotDecisionContent("CD-9101", ""),
		".concord/law/specs/guides/ingestion":                 lawSnapshotTierLawContent("SPEC-9104", "Synthetic spec", ""),
		".concord/law/constitution/founding.law":              lawSnapshotTierLawContent("PRODUCT-FOUNDING", "Synthetic founding constitution", ""),
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	if ids := snapshot.LawIDs(); len(ids) != 3 {
		t.Fatalf("nested law files not inventoried: %v", ids)
	}
	for id, wantPath := range map[string]string{
		"CD-9101":          ".concord/law/decisions/team-a/2026/review-policy.md",
		"SPEC-9104":        ".concord/law/specs/guides/ingestion",
		"PRODUCT-FOUNDING": ".concord/law/constitution/founding.law",
	} {
		record, ok := snapshot.Law(id)
		if !ok {
			t.Fatalf("%s missing from snapshot", id)
		}
		if record.Path != wantPath {
			t.Fatalf("%s carried path %s, want %s", id, record.Path, wantPath)
		}
		if record.Status != "accepted" {
			t.Fatalf("%s is not folder-accepted: %s", id, record.Status)
		}
	}
	decision, _ := snapshot.Law("CD-9101")
	if decision.Kind != "decision" {
		t.Fatalf("nested decision classified %s", decision.Kind)
	}
	spec, _ := snapshot.Law("SPEC-9104")
	if spec.Kind != "spec" {
		t.Fatalf("extensionless spec classified %s", spec.Kind)
	}
	constitution, _ := snapshot.Law("PRODUCT-FOUNDING")
	if constitution.Kind != "constitution" {
		t.Fatalf("non-markdown constitution classified %s", constitution.Kind)
	}
}

func TestLawFolderSnapshotSuppliesKindAndAcceptancePerTier(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/constitution/product-founding.md":       lawSnapshotTierLawContent("PRODUCT-FOUNDING", "Synthetic founding constitution", ""),
		".concord/law/specs/spec-9103.md":                     lawSnapshotTierLawContent("SPEC-9103", "Synthetic spec", `"criterion_bindings":[{"criterion":1,"scenario":"synthetic-scenario"}]`),
		".concord/law/decisions/CD-9101-snapshot-decision.md": lawSnapshotDecisionContent("CD-9101", ""),
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	for id, wantKind := range map[string]string{
		"PRODUCT-FOUNDING": "constitution",
		"SPEC-9103":        "spec",
		"CD-9101":          "decision",
	} {
		record, ok := snapshot.Law(id)
		if !ok {
			t.Fatalf("%s missing from snapshot", id)
		}
		if record.Kind != wantKind || record.Status != "accepted" {
			t.Fatalf("%s classified %s/%s, want %s/accepted", id, record.Kind, record.Status, wantKind)
		}
	}
	spec, _ := snapshot.Law("SPEC-9103")
	if len(spec.CriterionBindings) != 1 || spec.CriterionBindings[0].Scenario != "synthetic-scenario" {
		t.Fatalf("spec criterion binding not carried: %+v", spec.CriterionBindings)
	}
}

func TestLawFolderSnapshotHashesLawLargerThanRecordShardBound(t *testing.T) {
	content := lawSnapshotDecisionContent("CD-9101", "") + strings.Repeat("Synthetic long body line.\n", 1600)
	if len(content) <= maxKnowledgeRecord {
		t.Fatalf("fixture did not exceed the record shard bound: %d", len(content))
	}
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9101-long-body.md":  content,
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	record, ok := snapshot.Law("CD-9101")
	if !ok {
		t.Fatalf("law beyond the record shard bound refused: %v", snapshot.LawIDs())
	}
	sum := sha256.Sum256([]byte(content))
	if want := "sha256:" + hex.EncodeToString(sum[:]); record.SHA256 != want {
		t.Fatalf("long-body digest is not the whole-file sha256: got %s, want %s", record.SHA256, want)
	}
}

func TestLawFolderSnapshotHashesWholeFileIncludingMetadata(t *testing.T) {
	first := lawSnapshotDecisionContent("CD-9101", "")
	retitle := strings.Replace(first, `"id":"CD-9101","title":"Snapshot synthetic decision"`, `"id":"CD-9102","title":"Retitled synthetic decision"`, 1)
	if retitle == first {
		t.Fatalf("metadata mutation did not change the fixture")
	}
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9101-snapshot-decision.md": first,
		".concord/law/decisions/CD-9102-retitle.md":           retitle,
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	for id, raw := range map[string]string{"CD-9101": first, "CD-9102": retitle} {
		record, ok := snapshot.Law(id)
		if !ok {
			t.Fatalf("%s missing from snapshot", id)
		}
		sum := sha256.Sum256([]byte(raw))
		if want := "sha256:" + hex.EncodeToString(sum[:]); record.SHA256 != want {
			t.Fatalf("%s digest is not the whole-file sha256: got %s, want %s", id, record.SHA256, want)
		}
	}
	firstRecord, _ := snapshot.Law("CD-9101")
	secondRecord, _ := snapshot.Law("CD-9102")
	if firstRecord.SHA256 == secondRecord.SHA256 {
		t.Fatalf("frontmatter-only change did not move the digest")
	}
}

func TestLawFolderSnapshotParsesCRLFFrontMatterAndHashesRawBytes(t *testing.T) {
	content := strings.ReplaceAll(lawSnapshotDecisionContent("CD-9150", ""), "\n", "\r\n")
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9150-crlf.md":       content,
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	record, ok := snapshot.Law("CD-9150")
	if !ok {
		t.Fatalf("CRLF law file refused")
	}
	sum := sha256.Sum256([]byte(content))
	if want := "sha256:" + hex.EncodeToString(sum[:]); record.SHA256 != want {
		t.Fatalf("CRLF digest was normalized before hashing: got %s, want %s", record.SHA256, want)
	}
}

func TestLawFolderSnapshotParsesClosingDelimiterAtEOF(t *testing.T) {
	content := strings.SplitN(lawSnapshotDecisionContent("CD-9151", ""), "\n---\n\n", 2)[0] + "\n---"
	if !strings.HasSuffix(content, "---") || strings.HasSuffix(content, "---\n") {
		t.Fatalf("fixture does not end on the closing delimiter: %q", content[len(content)-8:])
	}
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9151-eof.md":        content,
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	record, ok := snapshot.Law("CD-9151")
	if !ok {
		t.Fatalf("EOF-closed law file refused")
	}
	sum := sha256.Sum256([]byte(content))
	if want := "sha256:" + hex.EncodeToString(sum[:]); record.SHA256 != want {
		t.Fatalf("EOF-closed digest was normalized before hashing: got %s, want %s", record.SHA256, want)
	}
}

func TestLawFolderSnapshotFrozenAtExplicitCommit(t *testing.T) {
	firstContent := lawSnapshotDecisionContent("CD-9101", "")
	laterContent := strings.Replace(firstContent, "Synthetic decision body.", "Second revision body.", 1)
	repo, first := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9101-snapshot-decision.md": firstContent,
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	frozen := lawSnapshotMustLoad(t, repo, first)
	frozenRecord, _ := frozen.Law("CD-9101")

	// An uncommitted working-tree change must never reach a committed read.
	writeKnowledgeFile(t, repo, ".concord/law/decisions/CD-9101-snapshot-decision.md", laterContent)
	dirty := lawSnapshotMustLoad(t, repo, first)
	dirtyRecord, _ := dirty.Law("CD-9101")
	if dirtyRecord.SHA256 != frozenRecord.SHA256 {
		t.Fatalf("uncommitted working-tree change leaked into the frozen snapshot")
	}

	second := commitKnowledgeRepo(t, repo, "second revision")
	again := lawSnapshotMustLoad(t, repo, first)
	againRecord, _ := again.Law("CD-9101")
	if againRecord.SHA256 != frozenRecord.SHA256 {
		t.Fatalf("later commit leaked into the frozen snapshot")
	}
	secondSnapshot := lawSnapshotMustLoad(t, repo, second)
	secondRecord, _ := secondSnapshot.Law("CD-9101")
	if secondRecord.SHA256 == frozenRecord.SHA256 {
		t.Fatalf("second revision did not move the digest at its own commit")
	}
}

func TestLawFolderSnapshotRegistryReadsOneConsistentCommit(t *testing.T) {
	repo, first := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9140-product-wide.md": lawSnapshotRootLawContent,
		".concord/docs/knowledge/domain-registry.json":   lawSnapshotRegistryRootOnlyJSON,
	})
	frozen := lawSnapshotMustLoad(t, repo, first)
	if got := len(lawSnapshotMustRegistry(t, frozen).Domains); got != 1 {
		t.Fatalf("frozen registry domain count changed: %d", got)
	}

	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", lawSnapshotRegistryExtendedJSON)
	second := commitKnowledgeRepo(t, repo, "registry revision")
	again := lawSnapshotMustLoad(t, repo, first)
	if got := len(lawSnapshotMustRegistry(t, again).Domains); got != 1 {
		t.Fatalf("later registry revision leaked into the frozen snapshot: %d domains", got)
	}
	secondSnapshot := lawSnapshotMustLoad(t, repo, second)
	if got := len(lawSnapshotMustRegistry(t, secondSnapshot).Domains); got != 3 {
		t.Fatalf("second revision registry not read at its own commit: %d domains", got)
	}
	if _, ok := secondSnapshot.Law("CD-9140"); !ok {
		t.Fatalf("law inventory changed without a law change")
	}
}

func TestLawFolderSnapshotRefusesDuplicateLawIDs(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9110-first.md":      lawSnapshotDecisionContent("CD-9110", ""),
		".concord/law/decisions/CD-9110-second.md":     lawSnapshotDecisionContent("CD-9110", ""),
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	_, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindKnowledgeAmbiguous, "duplicate folder law ID")
	if !strings.Contains(err.Error(), "CD-9110-first.md") || !strings.Contains(err.Error(), "CD-9110-second.md") {
		t.Fatalf("duplicate refusal does not name both files: %v", err)
	}
}

func TestLawFolderSnapshotRefusesDuplicateJSONKeys(t *testing.T) {
	duplicated := "---\n" +
		`{"id":"CD-9111","id":"CD-9111","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product:reviews","applies_to_domain_ids":[]}` + "\n" +
		"---\n\nSynthetic decision body.\n"
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9111-duplicate.md":  duplicated,
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	_, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindInvalidNoteProof, "duplicate frontmatter JSON key")
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate-key refusal lost its cause: %v", err)
	}
}

func TestLawFolderSnapshotRefusesInvalidLawFiles(t *testing.T) {
	const lawPath = ".concord/law/decisions/CD-9120-synthetic.md"
	danglingHome := "---\n" +
		`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product:missing","applies_to_domain_ids":[]}` + "\n" +
		"---\n\nSynthetic body.\n"
	rootHomeNoRationale := "---\n" +
		`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product-root:product","applies_to_domain_ids":[]}` + "\n" +
		"---\n\nSynthetic body.\n"
	authorityUnknownField := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"contract_version":1}`, `"contract_version":1,"vehicle":"extra"}`, 1)
	relationsUnknownField := lawSnapshotDecisionContent("CD-9120", `"law_relations":[{"kind":"refines","target_id":"CD-9125","note":"extra"}]`)
	bindingsUnknownField := lawSnapshotTierLawContent("SPEC-9120", "Snapshot synthetic spec", `"criterion_bindings":[{"criterion":1,"scenario":"synthetic-scenario","weight":2}]`)
	authorityCaseVariant := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"tier":"legislated"`, `"tier":"derived","Tier":"legislated"`, 1)
	newlineID := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"id":"CD-9120"`, `"id":"CD-91\n20"`, 1)
	overboundVersion := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"contract_version":1`, `"contract_version":2147483648`, 1)
	nullHome := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"home_domain_id":"product:reviews"`, `"home_domain_id":null`, 1)
	missingHome := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"home_domain_id":"product:reviews",`, ``, 1)
	emptyHome := strings.Replace(lawSnapshotDecisionContent("CD-9120", ""), `"home_domain_id":"product:reviews"`, `"home_domain_id":""`, 1)
	appliesRepeatsHome := "---\n" +
		`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product:reviews","applies_to_domain_ids":["product:reviews"]}` + "\n" +
		"---\n\nSynthetic body.\n"
	nullRationale := "---\n" +
		`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product-root:product","applies_to_domain_ids":[],"product_wide_rationale":null}` + "\n" +
		"---\n\nSynthetic body.\n"
	cases := []struct {
		name   string
		files  map[string]string
		detail string
	}{
		{
			name:   "frontmatter is not JSON",
			files:  map[string]string{lawPath: "---\nnot json\n---\n\nSynthetic body.\n"},
			detail: "frontmatter",
		},
		{
			name: "frontmatter never closes",
			files: map[string]string{lawPath: "---\n" +
				`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product:reviews","applies_to_domain_ids":[]}` + "\n"},
			detail: "frontmatter",
		},
		{
			name: "missing id",
			files: map[string]string{lawPath: "---\n" +
				`{"title":"No identity","authority":{"tier":"derived"},"home_domain_id":"product:reviews","applies_to_domain_ids":[]}` + "\n---\n\nSynthetic body.\n"},
			detail: "law ID",
		},
		{
			name:   "reserved qualified id",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("project/CD-9120", "")},
			detail: "reserved qualified form",
		},
		{
			name:   "unmodeled retired field",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("CD-9120", `"summary":"retired summary field"`)},
			detail: "unmodeled field summary",
		},
		{
			name:   "shadow status field",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("CD-9120", `"status":"rejected"`)},
			detail: "folder-derived field status",
		},
		{
			name:   "shadow kind field",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("CD-9120", `"kind":"lesson"`)},
			detail: "folder-derived field kind",
		},
		{
			name:   "shadow sha256 field",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("CD-9120", `"sha256":"sha256:0000000000000000000000000000000000000000000000000000000000000000"`)},
			detail: "folder-derived field sha256",
		},
		{
			name: "unknown authority tier",
			files: map[string]string{lawPath: "---\n" +
				`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"divine"},"home_domain_id":"product:reviews","applies_to_domain_ids":[]}` + "\n---\n\nSynthetic body.\n"},
			detail: "authority tier is not closed",
		},
		{
			name: "derived tier carrying legislative fields",
			files: map[string]string{lawPath: "---\n" +
				`{"id":"CD-9120","title":"Snapshot synthetic decision","authority":{"tier":"derived","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product:reviews","applies_to_domain_ids":[]}` + "\n---\n\nSynthetic body.\n"},
			detail: "derived record authority carries legislative fields",
		},
		{
			name:   "criterion bindings outside spec tier",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("CD-9120", `"criterion_bindings":[{"criterion":1,"scenario":"synthetic-scenario"}]`)},
			detail: "criterion bindings are only valid on spec records",
		},
		{
			name: "duplicated criterion index on spec",
			files: map[string]string{
				".concord/law/specs/spec-9120.md": lawSnapshotTierLawContent("SPEC-9120", "Snapshot synthetic spec", `"criterion_bindings":[{"criterion":1,"scenario":"first-scenario"},{"criterion":1,"scenario":"second-scenario"}]`),
			},
			detail: "criterion binding index is invalid or duplicated",
		},
		{
			name:   "dangling home domain",
			files:  map[string]string{lawPath: danglingHome},
			detail: "dangling",
		},
		{
			name:   "root home without rationale",
			files:  map[string]string{lawPath: rootHomeNoRationale},
			detail: "root Domain",
		},
		{
			name:   "relation to unknown law",
			files:  map[string]string{lawPath: lawSnapshotDecisionContent("CD-9120", `"law_relations":[{"kind":"refines","target_id":"CD-9999"}]`)},
			detail: "law relation target",
		},
		{
			name: "relations outside decision and spec tiers",
			files: map[string]string{
				".concord/law/constitution/product-founding.md": lawSnapshotTierLawContent("PRODUCT-FOUNDING", "Synthetic founding constitution", `"law_relations":[{"kind":"refines","target_id":"CD-9120"}]`),
			},
			detail: "law_relations are only allowed on decision/spec records",
		},
		{
			name:   "unknown field inside authority",
			files:  map[string]string{lawPath: authorityUnknownField},
			detail: "field authority holds unknown",
		},
		{
			name:   "unknown field inside law_relations",
			files:  map[string]string{lawPath: relationsUnknownField},
			detail: "field law_relations holds unknown",
		},
		{
			name:   "unknown field inside criterion_bindings",
			files:  map[string]string{".concord/law/specs/spec-9120.md": bindingsUnknownField},
			detail: "field criterion_bindings holds unknown",
		},
		{
			name:   "case-variant member inside authority",
			files:  map[string]string{lawPath: authorityCaseVariant},
			detail: "field authority holds unknown",
		},
		{
			name:   "line break inside law ID",
			files:  map[string]string{lawPath: newlineID},
			detail: "law ID is empty, oversized, unclean",
		},
		{
			name:   "authority version beyond the schema bound",
			files:  map[string]string{lawPath: overboundVersion},
			detail: "contract_version exceeds",
		},
		{
			name:   "null home domain",
			files:  map[string]string{lawPath: nullHome},
			detail: "home_domain_id cannot be null",
		},
		{
			name:   "missing home domain",
			files:  map[string]string{lawPath: missingHome},
			detail: "requires exactly one home domain",
		},
		{
			name:   "empty home domain",
			files:  map[string]string{lawPath: emptyHome},
			detail: "law home domain is invalid",
		},
		{
			name:   "applies_to repeats the home domain",
			files:  map[string]string{lawPath: appliesRepeatsHome},
			detail: "repeat the home domain",
		},
		{
			name:   "null product-wide rationale",
			files:  map[string]string{lawPath: nullRationale},
			detail: "product_wide_rationale cannot be null",
		},
		{
			name:   "unknown tier directory",
			files:  map[string]string{".concord/law/wip/thing.md": lawSnapshotDecisionContent("CD-9120", "")},
			detail: "unknown tier directory",
		},
		{
			name:   "file at the law root",
			files:  map[string]string{".concord/law/loose.md": lawSnapshotDecisionContent("CD-9120", "")},
			detail: "not placed under a law tier directory",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			files := map[string]string{".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON}
			for path, content := range testCase.files {
				files[path] = content
			}
			repo, commit := lawSnapshotFixture(t, files)
			_, err := loadLawFolderSnapshot(t.Context(), repo, commit)
			requireFailureKind(t, err, KindInvalidNoteProof, testCase.name)
			if !strings.Contains(err.Error(), testCase.detail) {
				t.Fatalf("refusal lost its cause: %v", err)
			}
		})
	}
}

func TestLawFolderSnapshotRequiresFullCommitOID(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	_, err := loadLawFolderSnapshot(t.Context(), repo, commit[:8])
	requireFailureKind(t, err, KindInvalidNoteProof, "short commit OID")
}

func TestLawFolderSnapshotRequiresLowercaseCommitObject(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9101-snapshot-decision.md": lawSnapshotDecisionContent("CD-9101", ""),
		".concord/docs/knowledge/domain-registry.json":        lawSnapshotRegistryJSON,
	})
	tree := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", commit+"^{tree}"))
	runKnowledgeGit(t, repo, "tag", "-a", "synthetic-law-revision", "-m", "synthetic tagged revision", commit)
	tag := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "refs/tags/synthetic-law-revision"))
	for _, tc := range []struct{ name, oid, detail string }{
		{"tree", tree, "does not name a commit"},
		{"tag", tag, "does not name a commit"},
		{"uppercase", strings.ToUpper(commit), "lowercase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadLawFolderSnapshot(t.Context(), repo, tc.oid)
			requireFailureKind(t, err, KindInvalidNoteProof, tc.name+" as the snapshot commit")
			if !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("OID refusal lost its cause: %v", err)
			}
		})
	}
}

func TestLawFolderSnapshotBoundsRelationsWithValidTargets(t *testing.T) {
	for _, count := range []int{maxManifestRelations, maxManifestRelations + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			files := map[string]string{".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON}
			var relations []string
			for i := range count {
				id := fmt.Sprintf("CD-%04d", 9200+i)
				files[".concord/law/decisions/"+id+".md"] = lawSnapshotDecisionContent(id, "")
				relations = append(relations, `{"kind":"refines","target_id":"`+id+`"}`)
			}
			files[".concord/law/decisions/CD-9199.md"] = lawSnapshotDecisionContent("CD-9199", `"law_relations":[`+strings.Join(relations, ",")+`]`)
			repo, commit := lawSnapshotFixture(t, files)
			snapshot, err := loadLawFolderSnapshot(t.Context(), repo, commit)
			if count > maxManifestRelations {
				requireFailureKind(t, err, KindInvalidNoteProof, "overbound relations with valid targets")
				if !strings.Contains(err.Error(), "law_relations exceed the bounded relation count") {
					t.Fatalf("relation bound refusal lost its cause: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused exactly bounded relations: %v", err)
			}
			record, _ := snapshot.Law("CD-9199")
			if len(record.LawRelations) != count {
				t.Fatalf("bounded relations lost: got %d, want %d", len(record.LawRelations), count)
			}
		})
	}
}

func TestLawFolderSnapshotRefusesSymlinkUnderLawRoot(t *testing.T) {
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/law/decisions/CD-9130-real.md", lawSnapshotDecisionContent("CD-9130", ""))
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", lawSnapshotRegistryJSON)
	if err := os.Symlink("CD-9130-real.md", filepath.Join(repo, ".concord/law/decisions/CD-9130-link.md")); err != nil {
		t.Fatal(err)
	}
	commit := commitKnowledgeRepo(t, repo, "symlink fixture")
	_, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindInvalidNoteProof, "law folder symlink")
	if !strings.Contains(err.Error(), "CD-9130-link.md") || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink refusal does not name the entry: %v", err)
	}
}

func TestLawFolderSnapshotRequiresIndependentRegistry(t *testing.T) {
	lawFiles := map[string]string{
		".concord/law/decisions/CD-9101-snapshot-decision.md": lawSnapshotDecisionContent("CD-9101", ""),
	}
	repo, commit := lawSnapshotFixture(t, lawFiles)
	_, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindDomainRegistryAbsent, "missing independent registry")

	malformed := map[string]string{
		".concord/docs/knowledge/domain-registry.json": "{not json\n",
	}
	for path, content := range lawFiles {
		malformed[path] = content
	}
	repo, commit = lawSnapshotFixture(t, malformed)
	_, err = loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindInvalidNoteProof, "malformed independent registry")

	duplicated := map[string]string{
		".concord/docs/knowledge/domain-registry.json": `{"schema_version":"1.0","schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Synthetic Product law organization.","status":"current","architecture_relations":[]}]}` + "\n",
	}
	for path, content := range lawFiles {
		duplicated[path] = content
	}
	repo, commit = lawSnapshotFixture(t, duplicated)
	_, err = loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindInvalidNoteProof, "duplicate registry JSON key")
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate registry key refusal lost its cause: %v", err)
	}
}

func TestLawFolderSnapshotEmptyWithoutLawFolder(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	if ids := snapshot.LawIDs(); len(ids) != 0 {
		t.Fatalf("missing law folder is not an empty inventory: %v", ids)
	}
	if _, ok := snapshot.Law("CD-9101"); ok {
		t.Fatalf("absent law resolved from an empty folder")
	}
	if len(lawSnapshotMustRegistry(t, snapshot).Domains) != 2 {
		t.Fatalf("registry not carried beside the empty inventory")
	}
}

// A home that commits neither law nor a Domain registry holds no law: the
// inventory is empty, and only a Domain read refuses, typed.
func TestLawFolderSnapshotEmptyWithoutLawOrRegistry(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{"README.md": "# not a law home\n"})
	snapshot := lawSnapshotMustLoad(t, repo, commit)
	if ids := snapshot.LawIDs(); len(ids) != 0 {
		t.Fatalf("a home with no law folder holds law: %v", ids)
	}
	_, err := snapshot.Registry()
	requireFailureKind(t, err, KindDomainRegistryAbsent, "registry read on a home that committed none")
}

func TestLawFolderSnapshotReturnsImmutableCopies(t *testing.T) {
	refines := "---\n" +
		`{"id":"CD-9126","title":"Snapshot refining decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product-root:product","applies_to_domain_ids":["product:reviews"],"product_wide_rationale":"This synthetic decision governs the whole Product.","law_relations":[{"kind":"refines","target_id":"CD-9125"}]}` + "\n" +
		"---\n\nSynthetic refining body.\n"
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/law/decisions/CD-9125-base.md":       lawSnapshotDecisionContent("CD-9125", ""),
		".concord/law/decisions/CD-9126-refines.md":    refines,
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
	})
	snapshot := lawSnapshotMustLoad(t, repo, commit)

	record, ok := snapshot.Law("CD-9126")
	if !ok {
		t.Fatalf("CD-9126 missing from snapshot")
	}
	record.AppliesToDomainIDs[0] = "product:mutated"
	record.LawRelations[0] = KnowledgeRelation{Kind: "conflicts_with", TargetID: "CD-9999"}
	record.CriterionBindings = append(record.CriterionBindings, KnowledgeCriterionBinding{Criterion: 9, Scenario: "mutated"})
	fresh, _ := snapshot.Law("CD-9126")
	if fresh.AppliesToDomainIDs[0] != "product:reviews" {
		t.Fatalf("snapshot mutable through returned applies_to slice: %+v", fresh.AppliesToDomainIDs)
	}
	if fresh.LawRelations[0].TargetID != "CD-9125" || fresh.LawRelations[0].Kind != "refines" {
		t.Fatalf("snapshot mutable through returned law_relations slice: %+v", fresh.LawRelations)
	}
	if len(fresh.CriterionBindings) != 0 {
		t.Fatalf("snapshot mutable through returned criterion_bindings slice: %+v", fresh.CriterionBindings)
	}

	ids := snapshot.LawIDs()
	ids[0] = "MUTATED"
	if freshIDs := snapshot.LawIDs(); len(freshIDs) != 2 || freshIDs[0] == "MUTATED" {
		t.Fatalf("snapshot mutable through returned ID slice: %v", freshIDs)
	}

	registry := lawSnapshotMustRegistry(t, snapshot)
	registry.Domains[1].Name = "mutated"
	registry.Domains[1].ArchitectureRelations = append(registry.Domains[1].ArchitectureRelations, KnowledgeArchitectureRelation{Kind: "depends_on", TargetDomainID: "product:reviews", GoverningLawIDs: []string{"CD-9125"}})
	freshRegistry := lawSnapshotMustRegistry(t, snapshot)
	if freshRegistry.Domains[1].Name != "Reviews" {
		t.Fatalf("snapshot mutable through returned registry domains: %s", freshRegistry.Domains[1].Name)
	}
	if len(freshRegistry.Domains[1].ArchitectureRelations) != 0 {
		t.Fatalf("snapshot mutable through returned registry relations: %+v", freshRegistry.Domains[1].ArchitectureRelations)
	}
}

// The pinned law read must neither substitute replacement objects nor fetch
// missing promisor objects implicitly. GIT_NO_REPLACE_OBJECTS is proved
// behaviorally against a real replacement ref; both variables are proved to
// reach the git process through the environment contract. The lazy-fetch
// refusal's own behavioral fixture would need a promisor (partial-clone)
// home; the environment contract here is its runner-level proof.
func TestLawFolderSnapshotIgnoresReplacementObjects(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", lawSnapshotRegistryJSON)
	original := lawSnapshotDecisionContent("CD-9301", "")
	writeKnowledgeFile(t, repo, ".concord/law/decisions/CD-9301-original.md", original)
	commit := commitKnowledgeRepo(t, repo, "original law")
	sum := sha256.Sum256([]byte(original))
	originalHash := "sha256:" + hex.EncodeToString(sum[:])

	replacement := strings.Replace(original, `"title":"Snapshot synthetic decision"`, `"title":"Replaced decision"`, 1)
	if replacement == original {
		t.Fatal("the replacement content did not differ")
	}
	writeKnowledgeFile(t, repo, ".concord/law/decisions/CD-9301-original.md", replacement)
	replacementCommit := commitKnowledgeRepo(t, repo, "replacement commit")
	runKnowledgeGit(t, repo, "replace", commit, replacementCommit)

	// Control: an unguarded git read at the same OID answers from the
	// replacement, so the pinned read's answer proves the environment held.
	replaced, err := runGit(t.Context(), repo, "cat-file", "blob", commit+":.concord/law/decisions/CD-9301-original.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != replacement {
		t.Fatal("the replacement ref did not take effect for the unguarded control read")
	}

	snapshot, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	if err != nil {
		t.Fatalf("the pinned law read refused a replaced-object home: %v", err)
	}
	record, ok := snapshot.Law("CD-9301")
	if !ok || record.SHA256 != originalHash {
		t.Fatalf("the pinned read substituted a replacement object: ok=%v hash=%s want %s", ok, record.SHA256, originalHash)
	}
	if record.Title != "Snapshot synthetic decision" {
		t.Fatalf("the pinned read answered from replaced bytes: %+v", record)
	}
}

func TestRunGitEnvReachesTheGitProcess(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=lawfolder.testkey",
		"GIT_CONFIG_VALUE_0=env-reached-git",
	}
	out, err := runGitEnv(t.Context(), repo, env, "config", "--get", "lawfolder.testkey")
	if err != nil || strings.TrimSpace(string(out)) != "env-reached-git" {
		t.Fatalf("runGitEnv did not pass its environment to git: out=%q err=%v", out, err)
	}
	if _, err := runGit(t.Context(), repo, "config", "--get", "lawfolder.testkey"); err == nil {
		t.Fatal("the unguarded runner saw the guarded runner's environment")
	}
}
