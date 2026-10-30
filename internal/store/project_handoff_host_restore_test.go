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
	entry, err := activeWorktreeEntryForProject(ctx, f.store.db, "fixture", f.workID, f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, entry.RepositoryID, "worktree", "prune")
	gitRunStore(t, entry.RepositoryID, "worktree", "add", entry.Path, entry.Branch)
	if err := VerifyProjectHandoffArtifactPreservation(ctx, f.sourceTree, []string{f.artifactRef(t, f.sourceTree)}); err != nil {
		t.Fatal(err)
	}
	handoff := recordHandoff(t, f, f.sourceTree)
	if handoff.HandoffID == "" {
		t.Fatal("restored source recorded no handoff")
	}
	bindSessionToProjectWorktree(t, f.store, f.sourceTree, f.sourceSession)
	if err := f.store.Transact(ctx, func(tx *Transaction) error {
		project, err := verifiedSessionPlacementTx(ctx, tx.tx, f.workID, f.sourceSession)
		if err == nil && project != f.sourceProject {
			t.Fatalf("restored placement Project=%q want %q", project, f.sourceProject)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.targetTree, "tracked.txt")); err != nil {
		t.Fatalf("source restoration affected sibling: %v", err)
	}
}
