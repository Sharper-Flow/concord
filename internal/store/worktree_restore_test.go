package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func restoreFixture(t *testing.T) (*Store, string, BootstrapResult) {
	t.Helper()
	s := openTemp(t)
	repo := initBootstrapStoreRepo(t)
	seedBootstrapStoreAuthority(t, s, repo)
	claimed, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, claimed
}

func TestResumeRestoresMissingWorktreeOnRecordedBranch(t *testing.T) {
	for _, prune := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "pruned"}[prune], func(t *testing.T) {
			s, repo, claimed := restoreFixture(t)
			ctx := context.Background()
			tracked := filepath.Join(claimed.Entry.Path, "committed.txt")
			if err := os.WriteFile(tracked, []byte("committed work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runTestGit(t, claimed.Entry.Path, "add", "committed.txt")
			runTestGit(t, claimed.Entry.Path, "commit", "-qm", "fixture work")
			tip := runTestGit(t, repo, "rev-parse", "refs/heads/"+claimed.Entry.Branch)
			var before int
			if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(claimed.Entry.Path); err != nil {
				t.Fatal(err)
			}
			if prune {
				runTestGit(t, repo, "worktree", "prune", "--expire", "now")
			}
			restored, err := s.RestoreResumeWorktree(ctx, claimed.WorkID, claimed.Entry, nil)
			if err != nil || !restored.Restored || restored.RestoredTip != tip || !strings.Contains(restored.RestorationWarning, "not recoverable") {
				t.Fatalf("restored=%+v err=%v", restored, err)
			}
			if content, err := os.ReadFile(tracked); err != nil || string(content) != "committed work\n" {
				t.Fatalf("committed content=%q err=%v", content, err)
			}
			entry, err := s.ResumeWorktreeLocation(ctx, claimed.ProductID, claimed.ProjectID, claimed.WorkID)
			if err != nil || entry.ClaimOpID != claimed.Entry.ClaimOpID || entry.Path != claimed.Entry.Path || entry.BaseSHA != claimed.Entry.BaseSHA {
				t.Fatalf("entry=%+v err=%v", entry, err)
			}
			var after int
			if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&after); err != nil || after != before {
				t.Fatalf("restoration wrote store events: before=%d after=%d err=%v", before, after, err)
			}
			replay, err := s.RestoreResumeWorktree(ctx, claimed.WorkID, claimed.Entry, nil)
			if err != nil || replay.Restored {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			if got := runTestGit(t, repo, "rev-parse", "refs/heads/"+entry.Branch); got != tip {
				t.Fatalf("replay moved branch: %s != %s", got, tip)
			}
		})
	}
}

func TestResumeRestoreRefusesUnsafeNativeState(t *testing.T) {
	for _, scenario := range []string{"branch-gone", "foreign-directory", "empty-directory", "symlink-directory", "symlink-parent", "symlink-root", "locked", "other-checkout", "outside-root", "branch-replaced"} {
		t.Run(scenario, func(t *testing.T) {
			s, repo, claimed := restoreFixture(t)
			entry := claimed.Entry
			root := filepath.Join(filepath.Dir(s.Path()), "worktrees")
			if scenario == "locked" {
				runTestGit(t, repo, "worktree", "lock", entry.Path)
			}
			if err := os.RemoveAll(entry.Path); err != nil {
				t.Fatal(err)
			}
			wantKind := KindProjectionConflict
			switch scenario {
			case "branch-gone":
				runTestGit(t, repo, "update-ref", "-d", "refs/heads/"+entry.Branch)
				wantKind = KindProjectionNotFound
			case "foreign-directory", "empty-directory":
				if err := os.Mkdir(entry.Path, 0o700); err != nil {
					t.Fatal(err)
				}
				if scenario == "foreign-directory" {
					if err := os.WriteFile(filepath.Join(entry.Path, "keep"), []byte("foreign content"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink-directory":
				if err := os.Symlink(t.TempDir(), entry.Path); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				if err := os.Remove(filepath.Dir(entry.Path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Dir(entry.Path)); err != nil {
					t.Fatal(err)
				}
			case "other-checkout":
				runTestGit(t, repo, "worktree", "prune", "--expire", "now")
				runTestGit(t, repo, "worktree", "add", filepath.Join(t.TempDir(), "other\ncheckout"), entry.Branch)
			case "symlink-root":
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			case "branch-replaced":
				otherRepo := initBootstrapStoreRepo(t)
				runTestGit(t, otherRepo, "checkout", "--orphan", "unrelated")
				runTestGit(t, otherRepo, "commit", "-qm", "unrelated root")
				runTestGit(t, repo, "fetch", otherRepo, "unrelated")
				runTestGit(t, repo, "update-ref", "refs/heads/"+entry.Branch, "FETCH_HEAD")
			case "outside-root":
				entry.Path = filepath.Join(t.TempDir(), "outside")
			}
			got, err := restoreResumeWorktree(context.Background(), root, repo, entry, ExecGitRunner{})
			var failure *Failure
			if got.Restored || !errors.As(err, &failure) || failure.Kind != wantKind {
				t.Fatalf("restored=%+v err=%v, want %s", got, err, wantKind)
			}
			if scenario == "foreign-directory" {
				content, err := os.ReadFile(filepath.Join(entry.Path, "keep"))
				if err != nil || string(content) != "foreign content" {
					t.Fatalf("foreign content changed: %q %v", content, err)
				}
			}
			if scenario == "outside-root" || scenario == "branch-gone" || scenario == "locked" || scenario == "other-checkout" || scenario == "symlink-parent" || scenario == "symlink-root" || scenario == "branch-replaced" {
				if _, err := os.Lstat(entry.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refusal created worktree path: %v", err)
				}
			}
		})
	}
}

type restoreDirectoryAppearsRunner struct {
	path string
}

func (r restoreDirectoryAppearsRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "show-ref" {
		if err := os.Mkdir(r.path, 0o700); err != nil {
			return nil, err
		}
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

type restoreAncestorFailureRunner struct{}

func (restoreAncestorFailureRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "merge-base" {
		return (ExecGitRunner{}).Run(ctx, dir, "rev-parse", "--verify", "refs/heads/missing-restore^{commit}")
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func TestResumeRestoreDistinguishesGitFailureFromDivergence(t *testing.T) {
	s, _, claimed := restoreFixture(t)
	if err := os.RemoveAll(claimed.Entry.Path); err != nil {
		t.Fatal(err)
	}
	got, err := s.RestoreResumeWorktree(context.Background(), claimed.WorkID, claimed.Entry, restoreAncestorFailureRunner{})
	var failure *Failure
	if got.Restored || !errors.As(err, &failure) || failure.Kind != KindGitUnreachable {
		t.Fatalf("restoration=%+v err=%v, want git_unreachable rather than a divergence diagnosis", got, err)
	}
	if _, err := os.Lstat(claimed.Entry.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Git probe created the worktree path: %v", err)
	}
}

func TestResumeRestoreNeverOverwritesDirectoryAppearingDuringProbe(t *testing.T) {
	s, _, claimed := restoreFixture(t)
	if err := os.RemoveAll(claimed.Entry.Path); err != nil {
		t.Fatal(err)
	}
	got, err := s.RestoreResumeWorktree(context.Background(), claimed.WorkID, claimed.Entry, restoreDirectoryAppearsRunner{path: claimed.Entry.Path})
	var failure *Failure
	if got.Restored || !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("restored=%+v err=%v", got, err)
	}
	entries, err := os.ReadDir(claimed.Entry.Path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("existing empty directory was modified: %v %v", entries, err)
	}
}

func TestResumeRestoreRefusesStaleClaimSnapshot(t *testing.T) {
	s, _, claimed := restoreFixture(t)
	entry := claimed.Entry
	entry.ClaimOpID = "another-claim"
	got, err := s.RestoreResumeWorktree(context.Background(), claimed.WorkID, entry, nil)
	var failure *Failure
	if got.Restored || !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("stale claim restoration=%+v err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(claimed.Entry.Path, "README.md")); err != nil {
		t.Fatalf("stale resume disturbed the real claim: %v", err)
	}
}

func TestProjectHandoffRecordsAfterSourceWorktreeRestore(t *testing.T) {
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	if err := os.RemoveAll(f.sourceTree); err != nil {
		t.Fatal(err)
	}
	if err := VerifyProjectHandoffArtifactPreservation(ctx, f.sourceTree, nil); err == nil {
		t.Fatal("missing source worktree admitted handoff")
	}
	entry, err := activeWorktreeEntryForProject(ctx, f.store.db, "fixture", f.workID, f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	if restored, err := f.store.RestoreResumeWorktree(ctx, f.workID, entry, nil); err != nil || !restored.Restored {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	if err := VerifyProjectHandoffArtifactPreservation(ctx, f.sourceTree, []string{f.artifactRef(t, f.sourceTree)}); err != nil {
		t.Fatal(err)
	}
	handoff := recordHandoff(t, f, f.sourceTree)
	if handoff.HandoffID == "" {
		t.Fatal("restored source recorded no handoff")
	}
	if _, err := f.store.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: f.workID, SessionRef: f.sourceSession, LandedDirectory: f.sourceTree, HostPID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Transact(ctx, func(tx *Transaction) error {
		sqlTx, err := transactionSQL(tx, "fixture")
		if err != nil {
			return err
		}
		project, err := verifiedSessionPlacementTx(ctx, sqlTx, f.workID, f.sourceSession)
		if err == nil && project != f.sourceProject {
			t.Fatalf("restored landing resolves Project %q, want %q", project, f.sourceProject)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.targetTree, "tracked.txt")); err != nil {
		t.Fatalf("source restoration affected the sibling worktree: %v", err)
	}
}
