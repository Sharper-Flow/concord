package store

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyCommittedNoteDefaultRunnerStaysBounded proves the committed-read
// default wrapper keeps runGit's bounded-output behavior: a committed blob
// whose bytes exceed the package output cap fails with the bound refusal
// instead of draining unbounded through the cancellation-only exec runner.
func TestVerifyCommittedNoteDefaultRunnerStaysBounded(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...) //nolint:gosec // test-only git argv, fixed binary
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "t@e.invalid")
	run("config", "user.name", "t")
	// maxGitOutput is 4 MiB; the committed blob overruns it. The canonical
	// note path check runs before the byte proof, so the blob commits at
	// the eligible note directory.
	if err := os.MkdirAll(filepath.Join(repo, ".concord", "docs", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".concord", "docs", "work", "note.md"), bytes.Repeat([]byte("x"), 5*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "oversized blob")
	head := strings.TrimSpace(run("rev-parse", "HEAD"))
	_, err := VerifyCommittedNote(context.Background(), repo, head, ".concord/docs/work/note.md", "")
	if err == nil {
		t.Fatal("VerifyCommittedNote drained an over-cap blob: the bounded default was lost")
	}
	// The runGit bound refuses at the 4 MiB output cap, before the 256 KiB
	// note-size check ever sees the bytes; the cancellation-only exec runner
	// would drain the blob and refuse with the note-size wording instead.
	if !strings.Contains(err.Error(), "output exceeds") || !strings.Contains(err.Error(), "cannot read the committed blob") {
		t.Fatalf("VerifyCommittedNote error = %v, want the runGit output-cap refusal naming the drained read", err)
	}
}
