package store

import (
	"context"
	"encoding/json"
	"fmt"
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
	if err := os.MkdirAll(filepath.Join(repo, ".concord", "docs"), 0o755); err != nil {
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
      "path": ".concord/docs/lessons/2026-08-01-seed.md",
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
	if err := os.MkdirAll(filepath.Join(repo, ".concord/docs/lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord/docs/lessons/2026-08-01-seed.md"), []byte("seed\n"), 0o644); err != nil {
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

// TestPublishLessonRecordRefusesABrokenOverrideAnchor proves the
// working-tree manifest read runs the same anchor gate a committed read runs
// (CD-0194 D2): publication builds on the composed manifest, so a head whose
// override anchor does not prove out refuses before anything is written.
func TestPublishLessonRecordRefusesABrokenOverrideAnchor(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	headPath := filepath.Join(home.RepoPath, filepath.FromSlash(knowledgeHeadPath))
	raw, err := os.ReadFile(headPath)
	if err != nil {
		t.Fatal(err)
	}
	var head map[string]any
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	head["operator_overrides"] = []map[string]any{{
		"path": "external/knowledge/", "product_id": "concord",
		"recorded_in": "seed-lesson", "reason": "the operator recorded this placement for the external tree",
	}}
	encoded, err := json.MarshalIndent(head, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	req := LessonPublication{
		LessonID: "lesson-gate-probe", Title: "Gate probe", Summary: "A publication that must refuse on the broken anchor.",
		Content: "# Gate probe\n\nBody.\n", Tags: []string{"testing"},
		Scopes:   KnowledgeRecordScopes{Mode: "explicit", ProjectIDs: []string{"project-1"}},
		Evidence: []string{"internal/store/lesson_publish_test.go"},
		Coverage: lessonSatisfiedCoverage(),
		Now:      time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC),
	}
	before := commitCount(t, home.RepoPath)
	if _, err := PublishLessonRecord(ctx, home, req); err == nil || !strings.Contains(err.Error(), "only a decision carries operator override authority") {
		t.Fatalf("expected a broken-anchor refusal, got %v", err)
	}
	if commitCount(t, home.RepoPath) != before {
		t.Fatal("a refused publication wrote a commit")
	}
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

	// Advance the claimed branch past the lesson commit: a replay must
	// return the lesson's own prepared commit, not the branch's current
	// HEAD.
	gitInWorktree(t, home.RepoPath, "commit", "--quiet", "--allow-empty", "-m", "unrelated advance")

	commitsBefore := commitCount(t, home.RepoPath)

	// A changed coverage declaration is drift, never a silent acceptance:
	// the replay must carry the lesson's original coverage exactly.
	changedCoverage := req
	changedCoverage.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "internal/store.TestPublishLessonRecordRefusesAnchorKindsOutsideTheCoverageSchema"}}}
	if _, err := PublishLessonRecord(ctx, home, changedCoverage); err == nil || !strings.Contains(err.Error(), "coverage declaration does not match") {
		t.Fatalf("expected changed-coverage replay refusal, got %v", err)
	}
	if commitCount(t, home.RepoPath) != commitsBefore {
		t.Fatal("a refused replay created a commit")
	}

	replay, err := PublishLessonRecord(ctx, home, req)
	if err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD")); head == first.CommitOID {
		t.Fatal("the unrelated advance failed to move the branch head")
	}
	if replay.CommitOID != first.CommitOID || replay.Branch != first.Branch || commitCount(t, home.RepoPath) != commitsBefore {
		t.Fatal("idempotent replay must return the original lesson commit without a new commit")
	}

	conflict := req
	conflict.Summary = "different content changes the hash path"
	conflict.Content = "# different\n"
	if _, err := PublishLessonRecord(ctx, home, conflict); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("expected id-conflict refusal, got %v", err)
	}
	if mainHead := strings.TrimSpace(gitInWorktree(t, canonical, "rev-parse", "HEAD")); commitCount(t, home.RepoPath) != 3 {
		t.Fatalf("unexpected worktree commit count %d", commitCount(t, home.RepoPath))
	} else if mainHead == first.CommitOID {
		t.Fatal("the canonical checkout carries the lesson commit")
	}
}

// TestPublishLessonRecordReplayComparesTheCompleteRecord holds the replay
// join: an identical replay matches on the committed record's own date and
// path, so a replay on a later day returns the lesson's original prepared
// commit, while a metadata-only change is a different record that refuses
// typed even when the body hash, the slug, and the path still collide.
func TestPublishLessonRecordReplayComparesTheCompleteRecord(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "lesson-replay-record", Title: "Replay compares the record", Summary: "A replay reproduces the committed record exactly.",
		Content: "# Replay compares the record\n", Tags: []string{"replay"},
		Scopes:   KnowledgeRecordScopes{Mode: "explicit", ProjectIDs: []string{"project-1"}},
		Evidence: []string{"internal/store/lesson_publish_test.go"},
		Coverage: lessonSatisfiedCoverage(),
		Now:      time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatal(err)
	}
	commits := commitCount(t, home.RepoPath)

	// An identical replay on a later day still matches: the date and the
	// path come from the committed record, not from the replay's clock.
	later := base
	later.Now = time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	replay, err := PublishLessonRecord(ctx, home, later)
	if err != nil {
		t.Fatalf("identical replay on a later day: %v", err)
	}
	if replay.CommitOID != first.CommitOID || replay.Record.Date != first.Record.Date || replay.Record.Path != first.Record.Path {
		t.Fatalf("replay record %+v lost the original delivery %+v", replay.Record, first.Record)
	}

	// A metadata-only change is a different record: each refuses even though
	// the body hash, the slugified path, and the coverage bytes still match.
	for name, change := range map[string]func(*LessonPublication){
		"same-slug title": func(r *LessonPublication) { r.Title = "replay compares THE record" },
		"summary":         func(r *LessonPublication) { r.Summary = "A changed summary is a different record." },
		"tags":            func(r *LessonPublication) { r.Tags = []string{"replay", "extra"} },
		"scopes":          func(r *LessonPublication) { r.Scopes = KnowledgeRecordScopes{Mode: "home"} },
		"evidence":        func(r *LessonPublication) { r.Evidence = []string{"internal/store/git_knowledge.go"} },
	} {
		mutated := later
		change(&mutated)
		if _, err := PublishLessonRecord(ctx, home, mutated); err == nil || !strings.Contains(err.Error(), "already claimed") {
			t.Fatalf("expected %s replay refusal, got %v", name, err)
		}
	}
	if after := commitCount(t, home.RepoPath); after != commits {
		t.Fatalf("replay verification created commits: before %d after %d", commits, after)
	}
}

// TestPublishLessonRecordReplayBindsToThePreparedCommitTree holds the
// prepared-delivery binding: the replay reads the note, the record shard, and
// the coverage shard from the lesson's own prepared commit tree, never from
// the working tree, and it returns the prepared delivery only while the
// claimed branch head still delivers those exact bytes. After a later commit
// changes the coverage shard, both declarations refuse typed: the new one is
// not in the prepared commit, and the original one no longer rides the branch
// a pull request would carry.
func TestPublishLessonRecordReplayBindsToThePreparedCommitTree(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "lesson-replay-binding", Title: "Replay binds to the prepared commit",
		Summary: "A replay returns the commit whose tree carries the accepted declaration.",
		Content: "# Replay binds to the prepared commit\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
		Coverage: &LessonCoverageDeclaration{State: "out_of_scope", Reason: "A replay-binding fixture lesson carries no accepted law to prove."},
		Now:      time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatal(err)
	}
	coverageShardRel := lessonCoverageDir + "/" + base.LessonID + ".json"

	// A later commit changes the branch's coverage declaration to a second
	// reason; the lesson's prepared commit still carries the first.
	second := *base.Coverage
	second.Reason = "A second reason the branch now carries after a later commit."
	changedBytes, err := marshalLessonCoverageShard(base.LessonID, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home.RepoPath, coverageShardRel), changedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	gitInWorktree(t, home.RepoPath, "add", "--", coverageShardRel)
	gitInWorktree(t, home.RepoPath, "commit", "--quiet", "-m", "coverage change")
	commits := commitCount(t, home.RepoPath)

	replaySecond := base
	replaySecond.Coverage = &second
	if _, err := PublishLessonRecord(ctx, home, replaySecond); err == nil || !strings.Contains(err.Error(), "coverage declaration does not match") {
		t.Fatalf("expected prepared-commit coverage refusal, got %v", err)
	}

	// The original declaration refuses too: the prepared commit still carries
	// it, but the branch head no longer does, so a normal pull request from
	// the branch would deliver the second declaration, not the returned
	// commit's bytes.
	if _, err := PublishLessonRecord(ctx, home, base); err == nil || !strings.Contains(err.Error(), "no longer delivers") {
		t.Fatalf("expected branch-head delivery refusal, got %v", err)
	}
	fromCommit := gitInWorktree(t, home.RepoPath, "cat-file", "blob", first.CommitOID+":"+coverageShardRel)
	wantBytes, err := marshalLessonCoverageShard(base.LessonID, *base.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if fromCommit != string(wantBytes) {
		t.Fatalf("prepared commit carries coverage %q, want %q", fromCommit, wantBytes)
	}
	if after := commitCount(t, home.RepoPath); after != commits {
		t.Fatalf("replay verification created commits: before %d after %d", commits, after)
	}
}

// TestPublishLessonRecordReplayRequiresTheBranchToDeliverThePreparedCommit
// holds the prepared-delivery join: a replay returns the prepared branch and
// commit only while the branch head and its worktree still deliver exactly
// the prepared commit's lesson bytes. A later head commit that leaves the
// lesson's three paths unchanged keeps the replay valid; an uncommitted note
// or coverage edit, staged or not, refuses until the committed bytes are
// restored on the branch, index, and worktree.
func TestPublishLessonRecordReplayRequiresTheBranchToDeliverThePreparedCommit(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "lesson-replay-branch-delivery", Title: "The branch delivers the prepared commit",
		Summary: "A replay names one pull-request-deliverable lesson or refuses.",
		Content: "# The branch delivers the prepared commit\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
		Coverage: &LessonCoverageDeclaration{State: "out_of_scope", Reason: "A branch-delivery fixture lesson carries no accepted law to prove."},
		Now:      time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatal(err)
	}
	noteRel := first.Record.Path
	coverageRel := lessonCoverageDir + "/" + base.LessonID + ".json"
	noteBytes, err := os.ReadFile(filepath.Join(home.RepoPath, noteRel))
	if err != nil {
		t.Fatal(err)
	}
	coverageBytes, err := os.ReadFile(filepath.Join(home.RepoPath, coverageRel))
	if err != nil {
		t.Fatal(err)
	}

	// A later head commit that leaves the lesson's three paths unchanged
	// keeps the replay valid, and the replay still names the prepared commit.
	if err := os.WriteFile(filepath.Join(home.RepoPath, "UNRELATED.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInWorktree(t, home.RepoPath, "add", "--", "UNRELATED.txt")
	gitInWorktree(t, home.RepoPath, "commit", "--quiet", "-m", "unrelated advance")
	commits := commitCount(t, home.RepoPath)
	replay, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatalf("replay after an unrelated head advance: %v", err)
	}
	if replay.CommitOID != first.CommitOID {
		t.Fatalf("replay returned %s, want the prepared commit %s", replay.CommitOID, first.CommitOID)
	}

	// An uncommitted note edit refuses: a pull request from the branch would
	// not carry the prepared commit's lesson bytes.
	if err := os.WriteFile(filepath.Join(home.RepoPath, noteRel), append(noteBytes, []byte("\ndrift\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishLessonRecord(ctx, home, base); err == nil || !strings.Contains(err.Error(), "uncommitted change to the lesson") {
		t.Fatalf("expected uncommitted-note replay refusal, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.RepoPath, noteRel), noteBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// A staged coverage edit refuses too: the index, not only the worktree,
	// must deliver the prepared commit's bytes.
	second := *base.Coverage
	second.Reason = "A staged coverage edit the replay must refuse before any return."
	changed, err := marshalLessonCoverageShard(base.LessonID, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home.RepoPath, coverageRel), changed, 0o644); err != nil {
		t.Fatal(err)
	}
	gitInWorktree(t, home.RepoPath, "add", "--", coverageRel)
	if _, err := PublishLessonRecord(ctx, home, base); err == nil || !strings.Contains(err.Error(), "uncommitted change to the lesson") {
		t.Fatalf("expected staged-coverage replay refusal, got %v", err)
	}

	// Restoring the committed bytes on the branch, index, and worktree lets
	// the replay succeed again without a new commit.
	if err := os.WriteFile(filepath.Join(home.RepoPath, coverageRel), coverageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	gitInWorktree(t, home.RepoPath, "add", "--", coverageRel)
	replay, err = PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatalf("replay after restoring the committed bytes: %v", err)
	}
	if replay.CommitOID != first.CommitOID || replay.Branch != lessonFixtureBranch {
		t.Fatalf("replay=%+v", replay)
	}
	if after := commitCount(t, home.RepoPath); after != commits {
		t.Fatalf("replay verification created commits: before %d after %d", commits, after)
	}
}

// TestPublishLessonRecordReplayRefusesAnUncommittedMatchingCoverageEdit holds
// the working-tree trap: a replay must not accept a coverage declaration that
// matches only an uncommitted working-tree edit of the coverage shard.
func TestPublishLessonRecordReplayRefusesAnUncommittedMatchingCoverageEdit(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "lesson-replay-uncommitted", Title: "Uncommitted edits never replay",
		Summary: "A replay accepts only the prepared commit's bytes, never working-tree edits.",
		Content: "# Uncommitted edits never replay\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
		Coverage: &LessonCoverageDeclaration{State: "outstanding", Issue: "CON-508"},
		Now:      time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatal(err)
	}
	coverageShardRel := lessonCoverageDir + "/" + base.LessonID + ".json"

	edited := LessonCoverageDeclaration{State: "outstanding", Issue: "CON-999"}
	editedBytes, err := marshalLessonCoverageShard(base.LessonID, edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home.RepoPath, coverageShardRel), editedBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	replayEdited := base
	replayEdited.Coverage = &edited
	commits := commitCount(t, home.RepoPath)
	if _, err := PublishLessonRecord(ctx, home, replayEdited); err == nil || !strings.Contains(err.Error(), "coverage declaration does not match") {
		t.Fatalf("expected uncommitted-edit replay refusal, got %v", err)
	}
	if after := commitCount(t, home.RepoPath); after != commits {
		t.Fatalf("refused replay created commits: before %d after %d", commits, after)
	}

	committedBytes, err := marshalLessonCoverageShard(base.LessonID, *base.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home.RepoPath, coverageShardRel), committedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	replay, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatalf("identical replay after restoring the committed bytes: %v", err)
	}
	if replay.CommitOID != first.CommitOID {
		t.Fatalf("replay returned %s, want %s", replay.CommitOID, first.CommitOID)
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

	// The outstanding pointer is the law-coverage union: a positive integer
	// issue number is as valid as a Linear issue identifier, and the shard
	// commits the number as a bare JSON integer.
	outstandingNumber := base
	outstandingNumber.LessonID = "lesson-coverage-issue-number"
	outstandingNumber.Title = "Explicit coverage number"
	outstandingNumber.Coverage = &LessonCoverageDeclaration{State: "outstanding", IssueNumber: 508}
	if _, err := PublishLessonRecord(ctx, home, outstandingNumber); err != nil {
		t.Fatalf("an outstanding declaration with its issue number publishes: %v", err)
	}
	if shard, err := os.ReadFile(filepath.Join(home.RepoPath, lessonCoverageDir, outstandingNumber.LessonID+".json")); err != nil || !strings.Contains(string(shard), `"issue": 508`) {
		t.Fatalf("coverage shard=%s err=%v", shard, err)
	}
	// Both pointers at once refuse: the union is exactly one pointer.
	bothPointers := base
	bothPointers.Coverage = &LessonCoverageDeclaration{State: "outstanding", Issue: "CON-508", IssueNumber: 508}
	if _, err := PublishLessonRecord(ctx, home, bothPointers); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected both-pointers refusal, got %v", err)
	}
	// A zero or negative issue number is no pointer at all.
	zeroNumber := base
	zeroNumber.Coverage = &LessonCoverageDeclaration{State: "outstanding", IssueNumber: 0}
	if _, err := PublishLessonRecord(ctx, home, zeroNumber); err == nil || !strings.Contains(err.Error(), "issue identifier") {
		t.Fatalf("expected zero-issue-number refusal, got %v", err)
	}
	satisfiedWithNumber := base
	satisfiedWithNumber.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "go_test", Value: "internal/store.TestX"}}, IssueNumber: 508}
	if _, err := PublishLessonRecord(ctx, home, satisfiedWithNumber); err == nil || !strings.Contains(err.Error(), "forbids issue") {
		t.Fatalf("expected satisfied-with-issue-number refusal, got %v", err)
	}
}

// TestPublishLessonRecordRefusesAnchorKindsOutsideTheCoverageSchema aligns
// the accepted anchor inputs with contracts/law-coverage.schema.json: the
// closed kind set is go_test, scenario, validator, generated, and the
// validator kind names a check script CI invokes — not a harness script and
// not an adapter test, which the schema refuses and check-json would fail.
func TestPublishLessonRecordRefusesAnchorKindsOutsideTheCoverageSchema(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	base := LessonPublication{
		LessonID: "lesson-anchor-schema", Title: "Anchor schema alignment", Summary: "Anchor kinds and validator values stay inside the coverage schema.",
		Content: "# Anchor schema alignment\n", Scopes: KnowledgeRecordScopes{Mode: "home"}, Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	adapterAnchor := base
	adapterAnchor.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "adapter_test", Value: "adapter/opencode/concord.test.ts#some registered test"}}}
	if _, err := PublishLessonRecord(context.Background(), home, adapterAnchor); err == nil || !strings.Contains(err.Error(), "go_test, scenario, validator, generated") {
		t.Fatalf("expected adapter_test kind refusal, got %v", err)
	}
	harnessValidator := base
	harnessValidator.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "validator", Value: "scripts/test-evidence-anchors.py"}}}
	if _, err := PublishLessonRecord(context.Background(), home, harnessValidator); err == nil || !strings.Contains(err.Error(), "anchor value") {
		t.Fatalf("expected harness validator refusal, got %v", err)
	}
	checkValidator := base
	checkValidator.Coverage = &LessonCoverageDeclaration{State: "satisfied", Evidence: []LessonCoverageAnchor{{Kind: "validator", Value: "scripts/check-json.py"}}}
	if _, err := PublishLessonRecord(context.Background(), home, checkValidator); err != nil {
		t.Fatalf("a check-script validator anchor publishes: %v", err)
	}
	if shard, err := os.ReadFile(filepath.Join(home.RepoPath, lessonCoverageDir, base.LessonID+".json")); err != nil || !strings.Contains(string(shard), `"kind": "validator"`) {
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
	if published.Record.Date != "2042-12-31T00:00:00Z" || !strings.HasPrefix(published.Record.Path, ".concord/docs/lessons/2042-12-31-") {
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
	oneCharacterID := base
	oneCharacterID.LessonID = "x"
	if _, err := PublishLessonRecord(context.Background(), home, oneCharacterID); err == nil || !strings.Contains(err.Error(), "identifier") {
		t.Fatalf("expected coverage-incompatible lesson ID refusal, got %v", err)
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
			ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-law.md", Status: "accepted", Date: "2026-08-18T00:00:00Z",
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
	if err := os.MkdirAll(filepath.Join(repo, ".concord/docs/lessons"), 0o755); err != nil {
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
  "knowledge_roots": [".concord/docs/"],
  "exclusions": [".concord/docs/research/"],
  "doc_contract": {
    "enforced": true,
    "spec": {"required_sections": ["Purpose"], "ac_required": true},
    "banned_phrases": ["utilize"]
  },
  "records": [
    {
      "id": "seed-lesson",
      "kind": "lesson",
      "path": ".concord/docs/lessons/2026-08-01-seed.md",
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
	if err := os.WriteFile(filepath.Join(repo, ".concord/docs/lessons/2026-08-01-seed.md"), []byte("seed\n"), 0o644); err != nil {
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

	// A publication owner that does not exist refuses before any claim read.
	if _, err := s.ResolveLessonPublicationHome(ctx, "blocked", "missing-claim"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected missing-owner refusal, got %v", err)
	}

	// A live publication work with no claim on the home Project refuses
	// typed, with the publication-item remedy.
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			workCreatedEvent("pub-claimless", "q-create-pub-claimless"),
			operationEvent("q-project-pub-claimless", "work_project.added", SubjectWorkItem, "pub-claimless", map[string]any{
				"work_id": "pub-claimless", "project_id": "proj", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "pub-claimless"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveLessonPublicationHome(ctx, "blocked", "pub-claimless"); err == nil || !strings.Contains(err.Error(), "no claimed worktree") {
		t.Fatalf("expected no-claim refusal, got %v", err)
	}

	// A claim on a foreign Project refuses separately from no claim at all.
	insertWorktreeClaim(t, s, "wt-op-foreign", "pub-claimless", "proj-foreign", "work/foreign", filepath.Join(t.TempDir(), "foreign-wt"))
	if _, err := s.ResolveLessonPublicationHome(ctx, "blocked", "pub-claimless"); err == nil || !strings.Contains(err.Error(), "foreign Project") {
		t.Fatalf("expected foreign-Project refusal, got %v", err)
	}

	// A terminal publication owner refuses: a terminal work holds no active
	// lane, so it cannot own the claimed worktree a publication writes
	// through. A live publication work remains the route for a terminal
	// source item.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workTransitionEvent("q-complete-blocker", "blocker", "needed", "completed", 3, 4)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveLessonPublicationHome(ctx, "blocked", "blocker"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("expected terminal-owner refusal, got %v", err)
	}
}

// requireLessonZeroEffects asserts a refusing publication left the claimed
// worktree byte-neutral: the same branch head and the same status output as
// before the refused call.
func requireLessonZeroEffects(t *testing.T, repo, headBefore, statusBefore string) {
	t.Helper()
	if head := strings.TrimSpace(gitInWorktree(t, repo, "rev-parse", "HEAD")); head != headBefore {
		t.Fatalf("the claimed branch head moved to %s, want %s", head, headBefore)
	}
	if status := gitInWorktree(t, repo, "status", "--porcelain"); status != statusBefore {
		t.Fatalf("the claimed worktree changed: before=%q after=%q", statusBefore, status)
	}
}

// TestPublishLessonRecordRefusesTraversalLessonIDsWithZeroEffects is the
// failing-first regression for the shard-path escape: the lesson id becomes
// the record and coverage shard file names, so the bounded path-safe
// vocabulary the public lesson_publish input schema declares is enforced
// before any path is derived, and a refused traversal writes nothing.
func TestPublishLessonRecordRefusesTraversalLessonIDsWithZeroEffects(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "../escape", Title: "Traversal refused", Summary: "A lesson id names shard files and is never a path.",
		Content: "# Traversal refused\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
		Coverage: lessonSatisfiedCoverage(), Now: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
	}
	head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD"))
	status := gitInWorktree(t, home.RepoPath, "status", "--porcelain")
	for _, id := range []string{"../escape", "a/../../escape", "..", "sub/dir/lesson"} {
		req := base
		req.LessonID = id
		if _, err := PublishLessonRecord(ctx, home, req); err == nil || !strings.Contains(err.Error(), "path-safe") {
			t.Fatalf("lesson id %q: expected path-safe refusal, got %v", id, err)
		}
	}
	requireLessonZeroEffects(t, home.RepoPath, head, status)
	if _, statErr := os.Stat(filepath.Join(home.RepoPath, ".concord/docs/knowledge/escape.json")); statErr == nil {
		t.Fatal("an escaped shard was written outside the shard trees")
	}
}

// TestPublishLessonRecordRefusesOccupiedLessonTargets holds the overwrite
// failure the repair closes: every write target — the note, the record
// shard, and the coverage shard — must be free before any byte moves, so a
// publication can never replace an occupied file.
func TestPublishLessonRecordRefusesOccupiedLessonTargets(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

	t.Run("note target", func(t *testing.T) {
		t.Parallel()
		_, home := lessonWorktreeFixture(t)
		occupiedPath := ".concord/docs/lessons/2026-09-05-occupied-note-target.md"
		occupied := filepath.Join(home.RepoPath, occupiedPath)
		if err := os.WriteFile(occupied, []byte("occupied\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInWorktree(t, home.RepoPath, "add", "--", occupiedPath)
		gitInWorktree(t, home.RepoPath, "commit", "--quiet", "-m", "occupied note")
		head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD"))
		status := gitInWorktree(t, home.RepoPath, "status", "--porcelain")
		_, err := PublishLessonRecord(ctx, home, LessonPublication{
			LessonID: "lesson-occupied-note", Title: "Occupied note target", Summary: "A publication refuses an occupied note path.",
			Content: "# Occupied note target\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
			Coverage: lessonSatisfiedCoverage(), Now: now,
		})
		if err == nil || !strings.Contains(err.Error(), "occupied") {
			t.Fatalf("expected occupied-target refusal, got %v", err)
		}
		if content, readErr := os.ReadFile(occupied); readErr != nil || string(content) != "occupied\n" {
			t.Fatalf("the occupied note changed: %q err=%v", content, readErr)
		}
		requireLessonZeroEffects(t, home.RepoPath, head, status)
	})

	t.Run("record shard target", func(t *testing.T) {
		t.Parallel()
		_, home := lessonWorktreeFixture(t)
		victimPath := lessonRecordDir + "/lesson-occupied-record.json"
		victim := filepath.Join(home.RepoPath, victimPath)
		victimShard := `{
  "id": "victim-record",
  "kind": "lesson",
  "path": ".concord/docs/lessons/2026-08-01-victim.md",
  "status": "published",
  "date": "2026-08-01T00:00:00Z",
  "title": "Victim record",
  "summary": "A committed record the publication must not overwrite.",
  "tags": [],
  "scopes": {"mode": "home", "product_ids": [], "project_ids": [], "domain_ids": [], "tag_ids": []},
  "sha256": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
}
`
		if err := os.WriteFile(victim, []byte(victimShard), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInWorktree(t, home.RepoPath, "add", "--", victimPath)
		gitInWorktree(t, home.RepoPath, "commit", "--quiet", "-m", "victim record shard")
		head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD"))
		status := gitInWorktree(t, home.RepoPath, "status", "--porcelain")
		_, err := PublishLessonRecord(ctx, home, LessonPublication{
			LessonID: "lesson-occupied-record", Title: "Occupied record shard", Summary: "A publication refuses an occupied record shard path.",
			Content: "# Occupied record shard\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
			Coverage: lessonSatisfiedCoverage(), Now: now,
		})
		if err == nil || !strings.Contains(err.Error(), "occupied") {
			t.Fatalf("expected occupied-target refusal, got %v", err)
		}
		if content, readErr := os.ReadFile(victim); readErr != nil || string(content) != victimShard {
			t.Fatalf("the occupied record shard changed: %q err=%v", content, readErr)
		}
		requireLessonZeroEffects(t, home.RepoPath, head, status)
	})

	t.Run("coverage shard target", func(t *testing.T) {
		t.Parallel()
		_, home := lessonWorktreeFixture(t)
		occupiedPath := lessonCoverageDir + "/lesson-occupied-coverage.json"
		occupied := filepath.Join(home.RepoPath, occupiedPath)
		if err := os.MkdirAll(filepath.Dir(occupied), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(occupied, []byte("{\"occupied\": true}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitInWorktree(t, home.RepoPath, "add", "--", occupiedPath)
		gitInWorktree(t, home.RepoPath, "commit", "--quiet", "-m", "occupied coverage shard")
		head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD"))
		status := gitInWorktree(t, home.RepoPath, "status", "--porcelain")
		_, err := PublishLessonRecord(ctx, home, LessonPublication{
			LessonID: "lesson-occupied-coverage", Title: "Occupied coverage shard", Summary: "A publication refuses an occupied coverage shard path.",
			Content: "# Occupied coverage shard\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
			Coverage: lessonSatisfiedCoverage(), Now: now,
		})
		if err == nil || !strings.Contains(err.Error(), "occupied") {
			t.Fatalf("expected occupied-target refusal, got %v", err)
		}
		if content, readErr := os.ReadFile(occupied); readErr != nil || string(content) != "{\"occupied\": true}\n" {
			t.Fatalf("the occupied coverage shard changed: %q err=%v", content, readErr)
		}
		requireLessonZeroEffects(t, home.RepoPath, head, status)
	})
}

// TestPublishLessonRecordRefusesSymlinkTargetsAndParents holds the link
// failure the repair closes: a publication never writes through a symlink,
// at a write target or in a target's parent directories, so no write can
// follow a link outside the claimed worktree.
func TestPublishLessonRecordRefusesSymlinkTargetsAndParents(t *testing.T) {
	ctx := context.Background()

	t.Run("symlink note target", func(t *testing.T) {
		t.Parallel()
		_, home := lessonWorktreeFixture(t)
		outside := filepath.Join(t.TempDir(), "outside-note.md")
		if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		notePath := ".concord/docs/lessons/2026-09-06-symlink-note-target.md"
		if err := os.Symlink(outside, filepath.Join(home.RepoPath, notePath)); err != nil {
			t.Fatal(err)
		}
		head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD"))
		status := gitInWorktree(t, home.RepoPath, "status", "--porcelain")
		_, err := PublishLessonRecord(ctx, home, LessonPublication{
			LessonID: "lesson-symlink-note", Title: "Symlink note target", Summary: "A publication refuses a symlinked note path.",
			Content: "# Symlink note target\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
			Coverage: lessonSatisfiedCoverage(), Now: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		})
		if err == nil || !strings.Contains(err.Error(), "occupied") {
			t.Fatalf("expected occupied-target refusal, got %v", err)
		}
		if content, readErr := os.ReadFile(outside); readErr != nil || string(content) != "outside\n" {
			t.Fatalf("the symlink destination changed: %q err=%v", content, readErr)
		}
		requireLessonZeroEffects(t, home.RepoPath, head, status)
	})

	t.Run("symlink coverage parent", func(t *testing.T) {
		t.Parallel()
		_, home := lessonWorktreeFixture(t)
		outsideDir := filepath.Join(t.TempDir(), "outside-coverage")
		if err := os.MkdirAll(outsideDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideDir, filepath.Join(home.RepoPath, lessonCoverageDir)); err != nil {
			t.Fatal(err)
		}
		head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD"))
		status := gitInWorktree(t, home.RepoPath, "status", "--porcelain")
		_, err := PublishLessonRecord(ctx, home, LessonPublication{
			LessonID: "lesson-symlink-parent", Title: "Symlink coverage parent", Summary: "A publication refuses a symlinked target parent.",
			Content: "# Symlink coverage parent\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
			Coverage: lessonSatisfiedCoverage(), Now: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		})
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlinked-parent refusal, got %v", err)
		}
		if entries, readErr := os.ReadDir(outsideDir); readErr != nil || len(entries) != 0 {
			t.Fatalf("the symlinked parent received writes: %v err=%v", entries, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(home.RepoPath, ".concord/docs/lessons/2026-09-06-symlink-coverage-parent.md")); statErr == nil {
			t.Fatal("the lesson note was written before the refusal")
		}
		requireLessonZeroEffects(t, home.RepoPath, head, status)
	})
}

// TestWriteConfinedLessonFileHoldsConfinementAtTheWriteBoundary is the
// deterministic half of the swap defense: it calls the write helper with
// hostile state already in place — a parent component that is a symlink to
// a directory outside the root, and an occupied target — and holds that the
// Root-bound write refuses both without creating or changing a byte outside
// the worktree. The preflight checks are absent here by design: the write
// boundary must confine on its own.
func TestWriteConfinedLessonFileHoldsConfinementAtTheWriteBoundary(t *testing.T) {
	t.Parallel()
	rootDir := t.TempDir()
	outside := t.TempDir()

	t.Run("symlink parent at write time", func(t *testing.T) {
		t.Parallel()
		r, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if err := os.MkdirAll(filepath.Join(rootDir, ".concord", "docs", "lessons"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(rootDir, ".concord", "docs", "lessons-swap")); err != nil {
			t.Fatal(err)
		}
		err = writeConfinedLessonFile(r, ".concord/docs/lessons-swap/2026-09-27-note.md", []byte("swapped\n"), "dir failure", "file failure")
		if err == nil {
			t.Fatal("expected the write boundary to refuse the swapped symlink parent")
		}
		if strings.Contains(err.Error(), "already occupied") {
			t.Fatalf("the swap refusal lost the confinement classification: %v", err)
		}
		if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
			t.Fatalf("the outside directory received writes: %v err=%v", entries, readErr)
		}
	})

	t.Run("occupied target at write time", func(t *testing.T) {
		t.Parallel()
		r, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if err := os.MkdirAll(filepath.Join(rootDir, ".concord", "docs", "shards"), 0o755); err != nil {
			t.Fatal(err)
		}
		occupied := filepath.Join(rootDir, ".concord", "docs", "shards", "occupied.json")
		if err := os.WriteFile(occupied, []byte("{\"committed\": true}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err = writeConfinedLessonFile(r, ".concord/docs/shards/occupied.json", []byte("{\"overwritten\": true}\n"), "dir failure", "file failure")
		if err == nil || !strings.Contains(err.Error(), "already occupied") {
			t.Fatalf("expected the occupied-target refusal at the write boundary, got %v", err)
		}
		if content, readErr := os.ReadFile(occupied); readErr != nil || string(content) != "{\"committed\": true}\n" {
			t.Fatalf("the occupied target changed: %q err=%v", content, readErr)
		}
	})

	t.Run("free target writes inside the root", func(t *testing.T) {
		t.Parallel()
		r, err := os.OpenRoot(rootDir)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if err := writeConfinedLessonFile(r, ".concord/docs/fresh/2026-09-27-free.md", []byte("fresh\n"), "dir failure", "file failure"); err != nil {
			t.Fatalf("expected the confined write to succeed, got %v", err)
		}
		content, readErr := os.ReadFile(filepath.Join(rootDir, ".concord", "docs", "fresh", "2026-09-27-free.md"))
		if readErr != nil || string(content) != "fresh\n" {
			t.Fatalf("the confined write did not land inside the root: %q err=%v", content, readErr)
		}
	})
}

// TestPublishLessonRecordProbeSymlinkSwapCannotEscapeTheWorktree is the
// targeted concurrency probe for the swap window the preflight checks cannot
// close: while publications run, one goroutine repeatedly replaces the note
// target's parent directory with a symlink to a directory outside the
// claimed worktree and restores it. Whatever the interleaving between the
// preflight checks and a write, no byte may land outside the worktree; the
// Root-bound writes refuse any name that resolves through the outside link.
func TestPublishLessonRecordProbeSymlinkSwapCannotEscapeTheWorktree(t *testing.T) {
	_, home := lessonWorktreeFixture(t)
	outside := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	noteDir := filepath.Join(home.RepoPath, ".concord", "docs", "lessons")
	stop := make(chan struct{})
	swapperDone := make(chan struct{})
	go func() {
		defer close(swapperDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Each swap step is best-effort: a lost race to another swap
			// step only skips one hostile or benign window.
			_ = os.RemoveAll(noteDir)
			_ = os.Symlink(outside, noteDir) // hostile window: the parent points outside
			_ = os.Remove(noteDir)
			_ = os.MkdirAll(noteDir, 0o755) // benign window: a real directory again
		}
	}()

	for i := 0; i < 120; i++ {
		if ctx.Err() != nil {
			break
		}
		req := LessonPublication{
			LessonID: fmt.Sprintf("probe-lesson-%d", i),
			Title:    fmt.Sprintf("Probe lesson %d", i),
			Summary:  "A confinement probe publication.",
			Content:  fmt.Sprintf("# Probe lesson %d\n", i),
			Scopes:   KnowledgeRecordScopes{Mode: "home"},
			Coverage: lessonSatisfiedCoverage(),
			Now:      time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		}
		_, _ = PublishLessonRecord(ctx, home, req)
	}
	close(stop)
	<-swapperDone

	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the outside directory received writes: %v err=%v", entries, err)
	}
}

// TestPublishLessonRecordRefusesSamePathSameHashDuplicateIDWithZeroEffects
// holds the partial-effect failure the repair closes: a different lesson id
// that claims one existing note path with identical content passes the
// path-conflict scan but breaks the manifest's canonical-path rule, so the
// prospective manifest must refuse before any file is written.
func TestPublishLessonRecordRefusesSamePathSameHashDuplicateIDWithZeroEffects(t *testing.T) {
	t.Parallel()
	_, home := lessonWorktreeFixture(t)
	ctx := context.Background()
	base := LessonPublication{
		LessonID: "lesson-alpha-dup", Title: "Same path same hash", Summary: "Two ids claiming one note path with one content hash refuse before any write.",
		Content: "# Same path same hash\n", Scopes: KnowledgeRecordScopes{Mode: "home"},
		Coverage: lessonSatisfiedCoverage(), Now: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
	}
	first, err := PublishLessonRecord(ctx, home, base)
	if err != nil {
		t.Fatal(err)
	}
	dup := base
	dup.LessonID = "lesson-beta-dup"
	if _, err := PublishLessonRecord(ctx, home, dup); err == nil || !strings.Contains(err.Error(), "duplicate canonical paths") {
		t.Fatalf("expected duplicate-path refusal before any write, got %v", err)
	}
	if head := strings.TrimSpace(gitInWorktree(t, home.RepoPath, "rev-parse", "HEAD")); head != first.CommitOID {
		t.Fatalf("the claimed branch head moved to %s, want the first prepared commit %s", head, first.CommitOID)
	}
	if status := gitInWorktree(t, home.RepoPath, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("the refused duplicate wrote files: %q", status)
	}
	for _, leftover := range []string{
		filepath.Join(home.RepoPath, lessonRecordDir, "lesson-beta-dup.json"),
		filepath.Join(home.RepoPath, lessonCoverageDir, "lesson-beta-dup.json"),
	} {
		if _, statErr := os.Stat(leftover); statErr == nil {
			t.Fatalf("the refused duplicate wrote %s", leftover)
		}
	}
}
