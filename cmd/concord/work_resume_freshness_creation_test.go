package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkResumeMissingWorktreeUsesFetchedDefault(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	localHead := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD"))
	advanced := pushLocatorOriginCommit(t, repo, "advanced-before-resume")
	if advanced == localHead {
		t.Fatal("fixture did not advance the remote default branch")
	}
	code, output, stderr := resumeCLI(t, s, repo, "work-wl")
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if output.Worktree.BaseSHA != advanced {
		t.Fatalf("missing-worktree resume base=%s want fetched default=%s", output.Worktree.BaseSHA, advanced)
	}
	if head := strings.TrimSpace(gitOutput(t, output.Worktree.Path, "rev-parse", "HEAD")); head != advanced {
		t.Fatalf("created worktree HEAD=%s want fetched default=%s", head, advanced)
	}
}
