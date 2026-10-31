package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Inspect reads one active same-Project worktree without touching the target.

func claimFixtureWorktree(t *testing.T, s *Store, git *fakeWorktreeGit) WorktreeEntry {
	t.Helper()
	result, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err != nil {
		t.Fatal(err)
	}
	return result.Entry
}

func inspectRequest(git *fakeWorktreeGit, mode, path string) WorktreeInspectRequest {
	return WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: mode, Path: path, Runner: git}
}

func TestInspectWorktreeReadsStatusDiffWithoutLeaseOrTarget(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	clean, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, ""))
	if err != nil {
		t.Fatal(err)
	}
	if clean.Content != "" || clean.Truncated || clean.Branch != entry.Branch || clean.Path != entry.Path {
		t.Fatalf("clean status=%+v", clean)
	}
	git.dirty[entry.Path] = true
	dirty, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, ""))
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Content != "M file\n" {
		t.Fatalf("dirty status=%q", dirty.Content)
	}
	diff, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeDiff, ""))
	if err != nil {
		t.Fatal(err)
	}
	if diff.Content != "M file\n" {
		t.Fatalf("dirty diff=%q", diff.Content)
	}
}

func TestInspectWorktreeRequiresActiveEntryInSessionProject(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)

	_, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, ""))
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found before any claim", err)
	}
	claimFixtureWorktree(t, s, git)

	// The entry lives in project-w; a session anchored in another Project
	// cannot see it, which is the same-Project tier boundary.
	_, err = s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-other", Mode: WorktreeInspectModeStatus, Runner: git})
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found across Projects", err)
	}
}

func TestInspectWorktreeValidatesModeAndSelector(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)

	if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, "log", "")); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want invalid mode refusal", err)
	}
	if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, "README.md")); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want path refusal outside file mode", err)
	}
	if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeFile, "")); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want empty selector refusal", err)
	}
	for _, selector := range []string{"/etc/passwd", "../escape", "a/../../escape"} {
		if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeFile, selector)); failureKind(err) != KindInvalidOperation {
			t.Fatalf("selector %q err=%v, want traversal refusal", selector, err)
		}
	}
}

// realGitTiersFixture seeds one Project and work item against a real git
// repository, claims the canonical worktree through the real runner, and
// returns the store plus the worktree path.
func realGitTiersFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "work-w-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Tiers Work","priority":1}`)},
		{EventID: "work-w-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	gitRunStore(t, repoRoot, "init", "-b", "main")
	gitRunStore(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRunStore(t, repoRoot, "config", "user.name", "Concord Tiers Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repoRoot, "add", "tracked.txt")
	gitRunStore(t, repoRoot, "commit", "-m", "base")
	seedRealGitOrigin(t, repoRoot)
	if err := s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	runner := ExecGitRunner{}
	baseOut, err := runner.Run(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "tiers-op-1", WorkID: "work-w", ProjectID: "project-w",
		BaseSHA:      strings.TrimSpace(string(baseOut)),
		PrincipalRef: "principal-1", RequestID: "req-tiers-1",
		ExpectedVersion: 2, Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, result.Entry.Path
}

func gitRunStore(t *testing.T, dir string, args ...string) {
	t.Helper()
	_, err := ExecGitRunner{}.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// seedRealGitOrigin registers a local bare origin and mirrors the default
// branch into it, so the fixture repository exercises the shared preflight
// the way a real Project repository does: origin/HEAD names the fetched
// default branch and the tracking ref answers the claim's base resolution.
func seedRealGitOrigin(t *testing.T, repoRoot string) {
	t.Helper()
	origin := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"-origin.git")
	gitRunStore(t, filepath.Dir(repoRoot), "init", "-q", "--bare", "-b", "main", origin)
	gitRunStore(t, repoRoot, "remote", "add", "origin", origin)
	gitRunStore(t, repoRoot, "push", "-q", "origin", "main")
	gitRunStore(t, repoRoot, "fetch", "-q", "origin")
	gitRunStore(t, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}

func TestTiersAgainstRealGitInspectFile(t *testing.T) {
	t.Parallel()
	s, worktreePath := realGitTiersFixture(t)
	ctx := context.Background()
	if !filepath.IsAbs(worktreePath) {
		t.Fatalf("worktree path=%q, want absolute", worktreePath)
	}

	file, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeFile, Path: "tracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if file.Content != "base\n" {
		t.Fatalf("file content=%q", file.Content)
	}
	// A file above the cap returns exactly the cap and reports truncation.
	if err := os.WriteFile(filepath.Join(worktreePath, "large.txt"), []byte(strings.Repeat("x", 9)), 0o644); err != nil {
		t.Fatal(err)
	}
	large, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeFile, Path: "large.txt", MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(large.Content) != 8 || !large.Truncated {
		t.Fatalf("over-cap file content=%d bytes truncated=%v, want 8 bytes truncated", len(large.Content), large.Truncated)
	}
	missing, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeFile, Path: "absent.txt"})
	if failureKind(err) != KindProjectionNotFound || missing.Path != "" {
		t.Fatalf("err=%v, want typed missing-file refusal", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeStatus})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Content, "tracked.txt") {
		t.Fatalf("status=%q, want the mutated file visible", status.Content)
	}
}
