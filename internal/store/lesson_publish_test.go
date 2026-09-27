package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const lessonFixtureBranch = "work/lesson-publish"

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
  "schema_version": "1.2",
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
      "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
      "sha256": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  ]
}
`
	writeAggregateAsShards(t, repo, []byte(manifest))
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

// lessonWorktreeFixture adds the claimed worktree a lesson publication writes
// through: a linked worktree on its own branch, the shape
// ResolveLessonPublicationHome returns. The canonical checkout stays on main
// and must never be written.
func lessonWorktreeFixture(t *testing.T) (canonical string, home KnowledgeHome) {
	t.Helper()
	canonical = lessonRepoFixture(t)
	worktree := addLessonWorktree(t, canonical, lessonFixtureBranch)
	return canonical, KnowledgeHome{RepoPath: worktree, HeadRef: lessonFixtureBranch}
}

// addLessonWorktree creates one linked worktree on its own branch from the
// canonical checkout's HEAD.
func addLessonWorktree(t *testing.T, canonical, branch string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "lesson-worktree-"+strings.ReplaceAll(branch, "/", "-"))
	if out, err := exec.Command("git", "-C", canonical, "worktree", "add", "--quiet", "-b", branch, worktree).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add %s: %v\n%s", branch, err, out)
	}
	return worktree
}

func lessonSatisfiedCoverage() *LessonCoverageDeclaration {
	return &LessonCoverageDeclaration{
		State:    "satisfied",
		Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "internal/store.TestPublishLessonRecordCommitsManifestAndNoteIdempotently"}},
	}
}

func gitInWorktree(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func committedPaths(t *testing.T, repo, commit string) []string {
	t.Helper()
	out := gitInWorktree(t, repo, "show", "--name-only", "--format=", commit)
	paths := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			paths = append(paths, strings.TrimSpace(line))
		}
	}
	sort.Strings(paths)
	return paths
}

func TestPublishLessonRecordCommitsManifestAndNoteIdempotently(t *testing.T) {
	t.Parallel()
	canonical, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	req := LessonPublication{
		LessonID: "lesson-test-boundaries", Title: "Test boundaries hold", Summary: "Publishing a lesson appends its manifest record and commits both files.",
		Content: "# Test boundaries hold\n\nWrite the failing test first.\n", Tags: []string{"testing"},
		Scopes:   KnowledgeRecordScopes{Mode: "explicit", ProjectIDs: []string{"project-1"}},
		Evidence: []string{"internal/store/lesson_publish_test.go"},
		Coverage: lessonSatisfiedCoverage(),
		Now:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.CommitOID == "" || first.Record.Kind != "lesson" || first.Note.ID != req.LessonID {
		t.Fatalf("first=%+v", first)
	}
	if first.Branch != lessonFixtureBranch {
		t.Fatalf("prepared delivery names branch %q, want %q", first.Branch, lessonFixtureBranch)
	}
	if _, err := os.Stat(filepath.Join(home.RepoPath, first.Record.Path)); err != nil {
		t.Fatalf("lesson note missing: %v", err)
	}
	shardPath := filepath.Join(home.RepoPath, lessonRecordDir, req.LessonID+".json")
	shardBytes, err := os.ReadFile(shardPath)
	if err != nil {
		t.Fatalf("lesson record shard missing: %v", err)
	}
	var shard KnowledgeRecord
	if err := json.Unmarshal(shardBytes, &shard); err != nil || shard.ID != req.LessonID {
		t.Fatalf("invalid lesson record shard: %v", err)
	}
	coveragePath := filepath.Join(home.RepoPath, lessonCoverageDir, req.LessonID+".json")
	coverageBytes, err := os.ReadFile(coveragePath)
	if err != nil {
		t.Fatalf("lesson coverage shard missing: %v", err)
	}
	var coverageShard struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Issue    string `json:"issue"`
		Evidence []struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(coverageBytes, &coverageShard); err != nil || coverageShard.ID != req.LessonID || coverageShard.State != "satisfied" || len(coverageShard.Evidence) != 1 || coverageShard.Issue != "" {
		t.Fatalf("invalid lesson coverage shard: %v %s", err, coverageBytes)
	}
	manifest := composeWorkingTreeManifest(t, home.RepoPath)
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

	commitsBefore := commitCount(t, home.RepoPath)
	replay, err := PublishLessonRecord(ctx, home, req)
	if err != nil {
		t.Fatal(err)
	}
	if replay.CommitOID != first.CommitOID || replay.Branch != first.Branch || commitCount(t, home.RepoPath) != commitsBefore {
		t.Fatal("idempotent replay must not create a new commit")
	}

	conflict := req
	conflict.Summary = "different content changes the hash path"
	conflict.Content = "# different\n"
	if _, err := PublishLessonRecord(ctx, home, conflict); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("expected id-conflict refusal, got %v", err)
	}
	if mainHead := strings.TrimSpace(gitInWorktree(t, canonical, "rev-parse", "HEAD")); commitCount(t, home.RepoPath) != 2 {
		t.Fatalf("unexpected worktree commit count %d", commitCount(t, home.RepoPath))
	} else if mainHead == first.CommitOID {
		t.Fatal("the canonical checkout carries the lesson commit")
	}
}

// TestPublishLessonRecordIsolatedThreeFileCommitOnTheClaimedBranch is the
// isolated-commit check: the prepared delivery touches exactly the note, the
// record shard, and the coverage shard, on the claimed branch, and the
// canonical default checkout stays untouched.
func TestPublishLessonRecordIsolatedThreeFileCommitOnTheClaimedBranch(t *testing.T) {
	t.Parallel()
	canonical, home := lessonWorktreeFixture(t)
	published, err := PublishLessonRecord(context.Background(), home, LessonPublication{
		LessonID: "lesson-isolated-commit", Title: "Isolated three-file commit", Summary: "One commit carries the note, the record shard, and the coverage shard alone.",
		Content:  "# Isolated three-file commit\n",
		Scopes:   KnowledgeRecordScopes{Mode: "home"},
		Coverage: lessonSatisfiedCoverage(),
		Now:      time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		published.Record.Path,
		lessonRecordDir + "/lesson-isolated-commit.json",
		lessonCoverageDir + "/lesson-isolated-commit.json",
	}
	sort.Strings(want)
	if got := committedPaths(t, home.RepoPath, published.CommitOID); !reflect.DeepEqual(got, want) {
		t.Fatalf("commit touched %v, want exactly %v", got, want)
	}
	branch := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != lessonFixtureBranch {
		t.Fatalf("the delivery landed on %q, want %q", branch, lessonFixtureBranch)
	}
	if mainHead := strings.TrimSpace(gitInWorktree(t, canonical, "rev-parse", "HEAD")); mainHead == published.CommitOID {
		t.Fatal("the canonical checkout carried the lesson commit")
	}
	if status := strings.TrimSpace(gitInWorktree(t, canonical, "status", "--porcelain")); status != "" {
		t.Fatalf("the canonical checkout is dirty: %q", status)
	}
}

// TestPublishLessonRecordRefusesTheCanonicalCheckout holds the failure the
// repair closes: a home pointing at the repository's default checkout is a
// typed refusal, never a write surface.
func TestPublishLessonRecordRefusesTheCanonicalCheckout(t *testing.T) {
	t.Parallel()
	canonical := lessonRepoFixture(t)
	_, err := PublishLessonRecord(context.Background(), KnowledgeHome{RepoPath: canonical, HeadRef: "main"}, LessonPublication{
		LessonID: "lesson-canonical-refused", Title: "Canonical refused", Summary: "The default checkout is never a lesson publication surface.",
		Content:  "# Canonical refused\n",
		Scopes:   KnowledgeRecordScopes{Mode: "home"},
		Coverage: lessonSatisfiedCoverage(),
		Now:      time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "default checkout") {
		t.Fatalf("expected canonical-checkout refusal, got %v", err)
	}
}

// TestPublishLessonRecordRequiresExplicitCoverageDisposition is the explicit
// coverage check: a publication without a caller-declared coverage state is
// refused, the state-conditional obligations hold, and no state is inferred.
func TestPublishLessonRecordRequiresExplicitCoverageDisposition(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "lesson-coverage-disposition", Title: "Explicit coverage", Summary: "The coverage declaration is explicit and state-conditional.",
		Content: "# Explicit coverage\n", Scopes: KnowledgeRecordScopes{Mode: "home"}, Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, err := PublishLessonRecord(ctx, home, base); err == nil || !strings.Contains(err.Error(), "explicit coverage declaration") {
		t.Fatalf("expected missing-coverage refusal, got %v", err)
	}
	satisfiedNoEvidence := base
	satisfiedNoEvidence.Coverage = &LessonCoverageDeclaration{State: "satisfied"}
	if _, err := PublishLessonRecord(ctx, home, satisfiedNoEvidence); err == nil || !strings.Contains(err.Error(), "requires evidence anchors") {
		t.Fatalf("expected satisfied-without-evidence refusal, got %v", err)
	}
	satisfiedWithIssue := base
	satisfiedWithIssue.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "internal/store.TestX"}}, Issue: "CON-508"}
	if _, err := PublishLessonRecord(ctx, home, satisfiedWithIssue); err == nil || !strings.Contains(err.Error(), "forbids issue") {
		t.Fatalf("expected satisfied-with-issue refusal, got %v", err)
	}
	outstandingNoIssue := base
	outstandingNoIssue.Coverage = &LessonCoverageDeclaration{State: "outstanding"}
	if _, err := PublishLessonRecord(ctx, home, outstandingNoIssue); err == nil || !strings.Contains(err.Error(), "issue identifier") {
		t.Fatalf("expected outstanding-without-issue refusal, got %v", err)
	}
	outOfScopeWithoutReason := base
	outOfScopeWithoutReason.Coverage = &LessonCoverageDeclaration{State: "out_of_scope"}
	if _, err := PublishLessonRecord(ctx, home, outOfScopeWithoutReason); err == nil || !strings.Contains(err.Error(), "coverage reason") {
		t.Fatalf("expected out_of_scope-without-reason refusal, got %v", err)
	}
	badAnchor := base
	badAnchor.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "../escape"}}}
	if _, err := PublishLessonRecord(ctx, home, badAnchor); err == nil || !strings.Contains(err.Error(), "anchor value") {
		t.Fatalf("expected malformed-anchor refusal, got %v", err)
	}
	outstanding := base
	outstanding.Coverage = &LessonCoverageDeclaration{State: "outstanding", Issue: "CON-508"}
	if _, err := PublishLessonRecord(ctx, home, outstanding); err != nil {
		t.Fatalf("an outstanding declaration with its issue publishes: %v", err)
	}
	if shard, err := os.ReadFile(filepath.Join(home.RepoPath, lessonCoverageDir, base.LessonID+".json")); err != nil || !strings.Contains(string(shard), `"issue": "CON-508"`) || strings.Contains(string(shard), "reason") {
		t.Fatalf("coverage shard=%s err=%v", shard, err)
	}
}

func TestPublishLessonRecordUsesOneInjectedPublicationDate(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	want := time.Date(2042, 12, 31, 23, 59, 59, 0, time.UTC)
	published, err := PublishLessonRecord(context.Background(), home, LessonPublication{
		LessonID: "lesson-injected-clock", Title: "Injected publication date", Summary: "The publication date comes from the injected clock.",
		Content: "# Injected publication date\n", Scopes: KnowledgeRecordScopes{Mode: "explicit", ProjectIDs: []string{"project-1"}}, Coverage: lessonSatisfiedCoverage(), Now: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Record.Date != "2042-12-31T00:00:00Z" || !strings.HasPrefix(published.Record.Path, "docs/lessons/2042-12-31-") {
		t.Fatalf("record=%+v", published.Record)
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
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	base := LessonPublication{LessonID: "lesson-bounds", Title: "Bounds", Summary: "Bounds.", Content: "body", Coverage: lessonSatisfiedCoverage(), Now: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)}
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
	t.Parallel()
	_, home := lessonWorktreeFixture(t)

	req := LessonPublication{
		LessonID: "lesson-v12-domain", Title: "Domain lessons", Summary: "A version 1.2 manifest keeps its domain registry and domain-only scopes.",
		Content: "# Domain lessons\n", Tags: []string{"domains"},
		Scopes:   KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"product-root:concord"}},
		Coverage: &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "internal/store.TestPublishLessonRecordPreservesV12DomainManifest"}}},
		Now:      time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
	}
	if _, err := PublishLessonRecord(context.Background(), home, req); err != nil {
		t.Fatal(err)
	}
	composed := composeWorkingTreeManifest(t, home.RepoPath)
	if knowledgeDomainRegistryZero(composed.DomainRegistry) {
		t.Fatal("published v1.2 manifest lost its domain registry")
	}
	shard, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash(knowledgeRecordTree), "lesson-v12-domain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(shard), `"component_ids"`) || !strings.Contains(string(shard), `"domain_ids"`) {
		t.Fatalf("published record shard lost Domain-only shape:\n%s", shard)
	}
}

// TestShardRoundTripPreservesV12LawHomes proves a law record written as a
// shard composes back with its Domain home intact.
func TestShardRoundTripPreservesV12LawHomes(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	manifest := KnowledgeManifest{
		SchemaVersion: "1.2", SupportedKinds: []string{"decision", "lesson"}, IndexedKinds: []string{"decision", "lesson"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "store", Name: "Store", Purpose: "Storage", ParentDomainID: "product-root:concord", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{{
			ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001-law.md", Status: "accepted", Date: "2026-08-18T00:00:00Z",
			Title: "Law", Summary: "A current law retains its Domain ownership after lesson publication.", Tags: []string{},
			Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			HomeDomainID: "product-root:concord", AppliesToDomainIDs: []string{"store"}, SHA256: "sha256:" + strings.Repeat("a", 64),
			ProductWideRationale: "Ownership survives lesson publication across every child Domain.",
		}},
	}
	writeManifestShards(t, repo, manifest)
	parsed := composeWorkingTreeManifest(t, repo)
	if got := parsed.Records[0]; got.HomeDomainID != "product-root:concord" || len(got.AppliesToDomainIDs) != 1 || got.AppliesToDomainIDs[0] != "store" {
		t.Fatalf("law homes lost during the shard round trip: %+v", got)
	}
}

// TestShardTreeComposesIdenticallyInGoAndPython proves the store and
// scripts/knowledge_index.py compose the same manifest from the same shards.
func TestShardTreeComposesIdenticallyInGoAndPython(t *testing.T) {
	t.Parallel()
	repoRoot := repositoryRootForTest(t)
	goManifest := composeWorkingTreeManifest(t, repoRoot)
	cmd := exec.Command("python3", "-c", "import sys, json; sys.path.insert(0, 'scripts'); import knowledge_index; sys.stdout.write(knowledge_index.compose_manifest_bytes().decode('utf-8'))")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("python composition failed: %v", err)
	}
	pyManifest, err := parseKnowledgeManifest(output)
	if err != nil {
		t.Fatalf("python composition is not runtime-readable: %v", err)
	}
	if len(pyManifest.Records) != len(goManifest.Records) {
		t.Fatalf("python composed %d records, Go composed %d", len(pyManifest.Records), len(goManifest.Records))
	}
	for i := range goManifest.Records {
		if goManifest.Records[i].ID != pyManifest.Records[i].ID || goManifest.Records[i].SHA256 != pyManifest.Records[i].SHA256 {
			t.Fatalf("record %d differs: go=%s/%s python=%s/%s", i, goManifest.Records[i].ID, goManifest.Records[i].SHA256, pyManifest.Records[i].ID, pyManifest.Records[i].SHA256)
		}
	}
	if !reflect.DeepEqual(goManifest.KnowledgeRoots, pyManifest.KnowledgeRoots) || !reflect.DeepEqual(goManifest.Exclusions, pyManifest.Exclusions) {
		t.Fatal("head fields differ between the Go and Python compositions")
	}
}

// eightKeyLessonRepoFixture seeds a git knowledge home whose manifest carries
// every top-level key the v1 contract declares, including policy fields that a
// publication must preserve: knowledge_roots, exclusions, and doc_contract.
// Those fields carry live policy, so a publication that drops them disables
// repository law.
func eightKeyLessonRepoFixture(t *testing.T) string {
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
	if err := os.MkdirAll(filepath.Join(repo, "docs/lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "schema_version": "1.2",
  "supported_kinds": ["work_note", "decision", "spec", "lesson", "research"],
  "indexed_kinds": ["work_note", "decision", "spec", "lesson"],
  "domain_registry": {
    "schema_version": "1.0",
    "product_key": "fixture-product",
    "root_domain_id": "product-root:fixture-product",
    "domains": [
      {"domain_id": "product-root:fixture-product", "name": "Fixture product", "purpose": "Fixture registry root.", "status": "current", "architecture_relations": []}
    ]
  },
  "knowledge_roots": ["docs/"],
  "exclusions": ["docs/research/"],
  "doc_contract": {
    "enforced": true,
    "spec": {"required_sections": ["Purpose"], "ac_required": true},
    "banned_phrases": ["utilize"]
  },
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
      "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
      "sha256": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  ]
}
`
	writeAggregateAsShards(t, repo, []byte(manifest))
	if err := os.WriteFile(filepath.Join(repo, "docs/lessons/2026-08-01-seed.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "seed manifest")
	return repo
}

func TestPublishLessonRecordPreservesEveryTopLevelManifestKey(t *testing.T) {
	t.Parallel()
	canonical := eightKeyLessonRepoFixture(t)
	home := KnowledgeHome{RepoPath: addLessonWorktree(t, canonical, lessonFixtureBranch), HeadRef: lessonFixtureBranch}
	headFile := filepath.Join(home.RepoPath, filepath.FromSlash(knowledgeHeadPath))
	before := map[string]json.RawMessage{}
	raw, err := os.ReadFile(headFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	// Six head keys: the aggregate's eight less domain_registry and records,
	// which live in their own shards.
	if len(before) != 6 {
		t.Fatalf("fixture head must carry six top-level keys, got %d: %v", len(before), sortedKeys(before))
	}

	published, err := PublishLessonRecord(context.Background(), home, LessonPublication{
		LessonID: "lesson-manifest-round-trip", Title: "Manifest round trip is lossless",
		Summary:  "Publishing a lesson preserves every top-level manifest key.",
		Content:  "# Manifest round trip is lossless\n\nPreserve the whole manifest.\n",
		Tags:     []string{"knowledge"},
		Scopes:   KnowledgeRecordScopes{Mode: "home"},
		Coverage: &LessonCoverageDeclaration{State: "out_of_scope", Reason: "A manifest-shape fixture lesson carries no accepted law to prove."},
		Now:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.CommitOID == "" {
		t.Fatal("publication produced no commit")
	}

	after := map[string]json.RawMessage{}
	raw, err = os.ReadFile(headFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range sortedKeys(before) {
		if _, ok := after[key]; !ok {
			t.Fatalf("publication dropped top-level manifest key %q; survivors: %v", key, sortedKeys(after))
		}
	}
	if len(after) != len(before) {
		t.Fatalf("top-level key count changed: before %v, after %v", sortedKeys(before), sortedKeys(after))
	}
	// Policy keys must survive semantically: publication owns records, not policy.
	for _, key := range []string{"knowledge_roots", "exclusions", "doc_contract"} {
		var want, got any
		if err := json.Unmarshal(before[key], &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after[key], &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("publication mutated %q: before %s, after %s", key, before[key], after[key])
		}
	}
}

func sortedKeys(value map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestPublishLessonRecordAssignsTheDerivedTier holds the one runtime writer of
// a knowledge record at the tier its standing earns. A lesson an agent writes
// while delivering a change is derived, so a later contract may revise it
// without an operator checkpoint. Only a legislated record keeps that gate.
func TestPublishLessonRecordAssignsTheDerivedTier(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	published, err := PublishLessonRecord(context.Background(), home, LessonPublication{
		LessonID: "lesson-authority-tier", Title: "A published lesson is derived",
		Summary:  "The runtime writer assigns the derived tier at write time.",
		Content:  "# A published lesson is derived\n\nStanding is fixed at write time.\n",
		Tags:     []string{"testing"},
		Scopes:   KnowledgeRecordScopes{Mode: "explicit", ProjectIDs: []string{"project-1"}},
		Coverage: &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "internal/store.TestPublishLessonRecordAssignsTheDerivedTier"}}},
		Now:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Record.Authority.Tier != "derived" {
		t.Fatalf("a published lesson carries tier %q", published.Record.Authority.Tier)
	}
	if published.Record.Authority.LegislatedBy != "" || published.Record.Authority.ContractVersion != 0 {
		t.Fatalf("a derived lesson carries legislation fields: %+v", published.Record.Authority)
	}

	shardBytes, err := os.ReadFile(filepath.Join(home.RepoPath, lessonRecordDir, "lesson-authority-tier.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shard struct {
		Authority struct {
			Tier         string `json:"tier"`
			LegislatedBy string `json:"legislated_by"`
		} `json:"authority"`
	}
	if err := json.Unmarshal(shardBytes, &shard); err != nil {
		t.Fatal(err)
	}
	if shard.Authority.Tier != "derived" {
		t.Fatalf("the committed shard carries tier %q", shard.Authority.Tier)
	}
	if shard.Authority.LegislatedBy != "" {
		t.Fatalf("the committed shard carries legislated_by %q", shard.Authority.LegislatedBy)
	}
}

// lessonHomeFixture seeds the store rows the home resolution reads: the
// Product knowledge home at project "proj" whose canonical locator points at
// the canonical checkout, plus a verified worktree claim.
func lessonHomeFixture(t *testing.T) (*Store, string, string, KnowledgeHome) {
	t.Helper()
	s := seedQueryFixture(t)
	canonical, home := lessonWorktreeFixture(t)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','proj','canonical_path',?,?,'now','now'); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod','proj','locator-1'); DELETE FROM fold_guard`, canonical, canonical); err != nil {
		t.Fatal(err)
	}
	return s, canonical, home.RepoPath, home
}

func insertWorktreeClaim(t *testing.T, s *Store, opID, workID, projectID, branch, path string) {
	t.Helper()
	base := strings.Repeat("a", 40)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'verified','operator','req','now','now')`, opID, workID, projectID, "set-"+workID, "repo-"+opID, branch, base, path); err != nil {
		t.Fatal(err)
	}
}

// TestResolveLessonPublicationHomeUsesTheClaimedWorktree proves publication
// resolves the claimed worktree of the knowledge-home Project: the owner's
// claim supplies the path and the branch, a distinct live publication work can
// own it while the lesson names the terminal source work, and every other
// shape refuses typed.
func TestResolveLessonPublicationHomeUsesTheClaimedWorktree(t *testing.T) {
	t.Parallel()
	s, canonical, worktreePath, _ := lessonHomeFixture(t)
	ctx := context.Background()

	// The source work holds the claim itself.
	insertWorktreeClaim(t, s, "wt-op-source", "blocked", "proj", lessonFixtureBranch, worktreePath)
	resolved, err := s.ResolveLessonPublicationHome(ctx, "blocked", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RepoPath != worktreePath || resolved.HeadRef != lessonFixtureBranch || resolved.HomeProjectID != "proj" || resolved.HomeLocatorID != "locator-1" {
		t.Fatalf("resolved=%+v, want the claimed worktree", resolved)
	}
	if resolved.RepoPath == canonical {
		t.Fatal("resolution returned the canonical checkout")
	}

	// A distinct live publication work owns its own claimed worktree of the
	// same knowledge-home Project; the lesson still names the terminal
	// source work.
	pubPath := addLessonWorktree(t, canonical, "work/lesson-delegated")
	insertWorktreeClaim(t, s, "wt-op-pub", "blocker", "proj", "work/lesson-delegated", pubPath)
	delegated, err := s.ResolveLessonPublicationHome(ctx, "blocked", "blocker")
	if err != nil {
		t.Fatal(err)
	}
	if delegated.RepoPath != pubPath || delegated.HeadRef != "work/lesson-delegated" || delegated.HomeProjectID != "proj" {
		t.Fatalf("delegated=%+v", delegated)
	}

	// No claim on the home Project refuses typed, with the publication-item
	// remedy.
	if _, err := s.ResolveLessonPublicationHome(ctx, "blocked", "missing-claim"); err == nil || !strings.Contains(err.Error(), "no claimed worktree") {
		t.Fatalf("expected no-claim refusal, got %v", err)
	}

	// A claim on a foreign Project refuses separately from no claim at all.
	insertWorktreeClaim(t, s, "wt-op-foreign", "missing-claim", "proj-foreign", "work/foreign", filepath.Join(t.TempDir(), "foreign-wt"))
	if _, err := s.ResolveLessonPublicationHome(ctx, "blocked", "missing-claim"); err == nil || !strings.Contains(err.Error(), "foreign Project") {
		t.Fatalf("expected foreign-Project refusal, got %v", err)
	}
}
