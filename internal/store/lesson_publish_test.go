package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lessonRepoFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Lesson Test")
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "schema_version": "1.1",
  "supported_kinds": [
    "work_note",
    "decision",
    "spec",
    "lesson",
    "research"
  ],
  "indexed_kinds": [
    "work_note",
    "decision",
    "spec",
    "lesson"
  ],
  "records": [
    {
      "id": "seed-lesson",
      "kind": "lesson",
      "path": "docs/lessons/2026-08-01-seed.md",
      "status": "published",
      "date": "2026-08-01T00:00:00Z",
      "title": "Seed lesson",
      "summary": "Seed record proving the manifest format.",
      "tags": ["seed"],
      "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "component_ids": [], "tag_ids": []},
      "sha256": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  ]
}
`
	if err := os.WriteFile(filepath.Join(repo, lessonManifestPath), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "docs/lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs/lessons/2026-08-01-seed.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "seed manifest")
	return repo
}

func TestPublishLessonRecordCommitsManifestAndNoteIdempotently(t *testing.T) {
	repo := lessonRepoFixture(t)
	ctx := context.Background()
	home := KnowledgeHome{RepoPath: repo}
	req := LessonPublication{
		LessonID: "lesson-test-boundaries", Title: "Test boundaries hold", Summary: "Publishing a lesson appends its manifest record and commits both files.",
		Content: "# Test boundaries hold\n\nWrite the failing test first.\n", Tags: []string{"testing"},
		Scopes:   KnowledgeRecordScopes{Mode: "explicit", ProjectIDs: []string{"project-1"}},
		Evidence: []string{"internal/store/lesson_publish_test.go"},
		Now:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.CommitOID == "" || first.Record.Kind != "lesson" || first.Note.ID != req.LessonID {
		t.Fatalf("first=%+v", first)
	}
	if _, err := os.Stat(filepath.Join(repo, first.Record.Path)); err != nil {
		t.Fatalf("lesson note missing: %v", err)
	}
	shardPath := filepath.Join(repo, lessonRecordDir, req.LessonID+".json")
	shardBytes, err := os.ReadFile(shardPath)
	if err != nil {
		t.Fatalf("lesson record shard missing: %v", err)
	}
	var shard KnowledgeRecord
	if err := json.Unmarshal(shardBytes, &shard); err != nil || shard.ID != req.LessonID {
		t.Fatalf("invalid lesson record shard: %v", err)
	}
	manifestBytes, _ := os.ReadFile(filepath.Join(repo, lessonManifestPath))
	var manifest struct {
		Records []KnowledgeRecord `json:"records"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range manifest.Records {
		if record.ID == req.LessonID {
			found = true
			if record.Status != "published" || len(record.Evidence) != 1 || record.Scopes.Mode != "explicit" {
				t.Fatalf("record=%+v", record)
			}
		}
	}
	if !found {
		t.Fatal("manifest lacks the published record")
	}

	commitsBefore := commitCount(t, repo)
	replay, err := PublishLessonRecord(ctx, home, req)
	if err != nil {
		t.Fatal(err)
	}
	if replay.CommitOID != first.CommitOID || commitCount(t, repo) != commitsBefore {
		t.Fatal("idempotent replay must not create a new commit")
	}

	conflict := req
	conflict.Summary = "different content changes the hash path"
	conflict.Content = "# different\n"
	if _, err := PublishLessonRecord(ctx, home, conflict); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("expected id-conflict refusal, got %v", err)
	}
}

func commitCount(t *testing.T, repo string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return int(strings.TrimSpace(string(out))[0] - '0')
}

func TestPublishLessonRecordValidatesScopesAndBounds(t *testing.T) {
	repo := lessonRepoFixture(t)
	home := KnowledgeHome{RepoPath: repo}
	base := LessonPublication{LessonID: "lesson-bounds", Title: "Bounds", Summary: "Bounds.", Content: "body", Now: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)}
	if err := validateLessonPublication(base); err != nil {
		t.Fatal(err) // defaults to home scope
	}
	homeWithIDs := base
	homeWithIDs.Scopes = KnowledgeRecordScopes{Mode: "home", ProjectIDs: []string{"project-1"}}
	if _, err := PublishLessonRecord(context.Background(), home, homeWithIDs); err == nil || !strings.Contains(err.Error(), "home scope") {
		t.Fatalf("expected home-scope refusal, got %v", err)
	}
	explicitEmpty := base
	explicitEmpty.Scopes = KnowledgeRecordScopes{Mode: "explicit"}
	if _, err := PublishLessonRecord(context.Background(), home, explicitEmpty); err == nil || !strings.Contains(err.Error(), "at least one scope") {
		t.Fatalf("expected explicit-scope refusal, got %v", err)
	}
	badEvidence := base
	badEvidence.Evidence = []string{"../escape"}
	if _, err := PublishLessonRecord(context.Background(), home, badEvidence); err == nil || !strings.Contains(err.Error(), "repository-relative") {
		t.Fatalf("expected evidence refusal, got %v", err)
	}
}

func TestPublishLessonRecordPreservesV12DomainManifest(t *testing.T) {
	repo := lessonRepoFixture(t)
	manifestPath := filepath.Join(repo, lessonManifestPath)
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(manifest), `"schema_version": "1.1"`, `"schema_version": "1.2"`, 1)
	updated = strings.Replace(updated, `  ],
  "records": [`, `  ],
  "domain_registry": {
    "schema_version": "1.0",
    "product_key": "concord",
    "root_domain_id": "product-root:concord",
    "domains": [{
      "domain_id": "product-root:concord",
      "name": "Concord",
      "purpose": "Product-wide Concord law and architecture",
      "status": "current",
      "architecture_relations": []
    }]
  },
  "records": [`, 1)
	updated = strings.ReplaceAll(updated, `"component_ids"`, `"domain_ids"`)
	if err := os.WriteFile(manifestPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	req := LessonPublication{
		LessonID: "lesson-v12-domain", Title: "Domain lessons", Summary: "A version 1.2 manifest keeps its domain registry and domain-only scopes.",
		Content: "# Domain lessons\n", Tags: []string{"domains"},
		Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"product-root:concord"}},
		Now:    time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
	}
	if _, err := PublishLessonRecord(context.Background(), KnowledgeHome{RepoPath: repo}, req); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseKnowledgeManifest(written); err != nil {
		t.Fatalf("published v1.2 manifest is invalid: %v", err)
	}
	if strings.Contains(string(written), `"component_ids"`) || !strings.Contains(string(written), `"domain_registry"`) {
		t.Fatalf("published v1.2 manifest lost Domain-only shape:\n%s", written)
	}

	legacyScope := req
	legacyScope.LessonID = "lesson-v12-component"
	legacyScope.Scopes = KnowledgeRecordScopes{Mode: "explicit", ComponentIDs: []string{"legacy-component"}}
	if _, err := PublishLessonRecord(context.Background(), KnowledgeHome{RepoPath: repo}, legacyScope); err == nil || !strings.Contains(err.Error(), "cannot use component") {
		t.Fatalf("expected v1.2 component-scope refusal, got %v", err)
	}
}

func TestMarshalKnowledgeManifestPreservesV12LawHomes(t *testing.T) {
	manifest := KnowledgeManifest{
		SchemaVersion: "1.2", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "store", Name: "Store", Purpose: "Durable Product authority", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{{
			ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001-law.md", Status: "accepted", Date: "2026-08-18T00:00:00Z",
			Title: "Law", Summary: "A current law retains its Domain ownership after lesson publication.", Tags: []string{},
			Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}, domainIDsPresent: true},
			HomeDomainID: "product-root:concord", AppliesToDomainIDs: []string{"store"}, SHA256: "sha256:" + strings.Repeat("a", 64),
		}},
	}
	written, err := marshalKnowledgeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseKnowledgeManifest(written)
	if err != nil {
		t.Fatalf("serialized v1.2 law manifest is invalid: %v", err)
	}
	if got := parsed.Records[0]; got.HomeDomainID != "product-root:concord" || len(got.AppliesToDomainIDs) != 1 || got.AppliesToDomainIDs[0] != "store" {
		t.Fatalf("law homes lost during serialization: %+v", got)
	}
}

func TestMarshalKnowledgeManifestMatchesKnowledgeIndexGenerator(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs/knowledge/records"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := KnowledgeManifest{
		SchemaVersion: "1.2", SupportedKinds: []string{"lesson", "research"}, IndexedKinds: []string{"lesson"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
		Records: []KnowledgeRecord{{
			ID: "lesson-round-trip", Kind: "lesson", Path: "docs/lesson-round-trip.md", Status: "published", Date: "2026-08-20T00:00:00Z",
			Title: "Round trip", Summary: "Python and Go use one aggregate byte format.", Tags: []string{"proof"},
			Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}, domainIDsPresent: true},
			SHA256: "sha256:" + strings.Repeat("a", 64),
		}},
	}
	aggregate, err := marshalKnowledgeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, lessonManifestPath), aggregate, 0o644); err != nil {
		t.Fatal(err)
	}
	shard, err := marshalKnowledgeRecord(manifest.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, lessonRecordDir, "lesson-round-trip.json"), shard, 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := json.MarshalIndent(manifest.DomainRegistry, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, lessonRecordDir, "../domain-registry.json"), append(registry, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	repoRoot := "."
	if _, err := os.Stat(filepath.Join(repoRoot, "scripts/generate-knowledge-index.py")); err != nil {
		repoRoot = filepath.Join("..", "..")
	}
	currentBytes, err := os.ReadFile(filepath.Join(repoRoot, lessonManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	currentManifest, err := parseKnowledgeManifest(currentBytes)
	if err != nil {
		t.Fatalf("current generated aggregate is not runtime-readable: %v", err)
	}
	currentRendered, err := marshalKnowledgeManifest(currentManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentBytes, currentRendered) {
		t.Fatal("Go manifest serialization diverges from the generated aggregate")
	}
	cmd := exec.Command("python3", filepath.Join(repoRoot, "scripts/generate-knowledge-index.py"), "--check", "--root", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator rejected Go serialization: %v\n%s", err, output)
	}
}
