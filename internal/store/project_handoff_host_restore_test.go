package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectHandoffRecordsAfterSourceWorktreeRestore(t *testing.T) {
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	if err := os.RemoveAll(f.sourceTree); err != nil {
		t.Fatal(err)
	}
	if err := VerifyProjectHandoffArtifactPreservation(ctx, f.sourceTree, nil); err == nil {
		t.Fatal("missing source worktree admitted handoff")
	}
	gitRunStore(t, f.sourceRepo, "worktree", "prune")
	gitRunStore(t, f.sourceRepo, "worktree", "add", f.sourceTree, "work/"+f.workID)
	if err := VerifyProjectHandoffArtifactPreservation(ctx, f.sourceTree, []string{f.artifactRef(t, f.sourceTree)}); err != nil {
		t.Fatal(err)
	}
	handoff := recordHandoff(t, f, f.sourceTree)
	if handoff.HandoffID == "" {
		t.Fatal("restored source recorded no handoff")
	}
	if _, err := os.Stat(filepath.Join(f.targetTree, "tracked.txt")); err != nil {
		t.Fatalf("source restoration affected sibling: %v", err)
	}
}
