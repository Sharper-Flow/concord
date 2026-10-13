package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkResumeRestoresMissingWorktree(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	origin, err := s.BootstrapWorktree(context.Background(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(origin.Entry.Path, "committed.txt")
	if err := os.WriteFile(tracked, []byte("surviving committed work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, origin.Entry.Path, "add", "committed.txt")
	gitOutput(t, origin.Entry.Path, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture work")
	tip := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "refs/heads/"+origin.Entry.Branch))
	if err := os.RemoveAll(origin.Entry.Path); err != nil {
		t.Fatal(err)
	}
	code, output, stderr := resumeCLI(t, s, repo, origin.WorkID)
	if code != 0 {
		t.Fatalf("resume code=%d stderr=%q", code, stderr)
	}
	if got, err := os.ReadFile(tracked); err != nil || string(got) != "surviving committed work\n" {
		t.Fatalf("resume did not restore committed work: %q %v", got, err)
	}
	raw, err := json.Marshal(output.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]any
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	warning, _ := restored["restoration_warning"].(string)
	if restored["restored"] != true || restored["restored_tip"] != tip || !strings.Contains(strings.ToLower(warning), "uncommitted changes") {
		t.Fatalf("resume omitted restoration facts and loss warning: %s", raw)
	}
	entry, err := s.ResumeWorktreeLocation(context.Background(), "product-wl", "project-wl", origin.WorkID)
	if err != nil || entry.ClaimOpID != origin.Entry.ClaimOpID {
		t.Fatalf("restoration replaced the claim: %+v %v", entry, err)
	}
}
