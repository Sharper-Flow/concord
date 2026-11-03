package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommittedLawBlobReadStaysBounded proves the committed law reader keeps
// runGit's bounded-output behavior: a committed blob whose bytes exceed the
// package output cap fails with the bound refusal instead of draining
// unbounded through the cancellation-only exec runner.
func TestCommittedLawBlobReadStaysBounded(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		return runBlobFixtureGit(t, repo, args...)
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "t@e.invalid")
	run("config", "user.name", "t")
	// maxGitOutput is 4 MiB; the committed blob overruns it.
	if err := os.MkdirAll(filepath.Join(repo, ".concord", "law", "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord", "law", "decisions", "CD-9000-oversized.md"), bytes.Repeat([]byte("x"), 5*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "oversized blob")
	head := strings.TrimSpace(run("rev-parse", "HEAD"))
	_, err := committedRegularBlobBytes(context.Background(), lawFolderGitRunner{}, repo, head, ".concord/law/decisions/CD-9000-oversized.md")
	if err == nil {
		t.Fatal("the committed law read drained an over-cap blob: the bounded default was lost")
	}
	if !strings.Contains(err.Error(), "output exceeds") || !strings.Contains(err.Error(), "cannot read the committed blob") {
		t.Fatalf("committed law read error = %v, want the runGit output-cap refusal naming the drained read", err)
	}
}

func runBlobFixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd, err := newTestGitCommand(repo, args...)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
