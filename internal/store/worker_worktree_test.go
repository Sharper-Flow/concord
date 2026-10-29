package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree claim recorded under one path must accept the same directory
// reached through a symlink: worktree identity canonicalizes both sides.
func TestActiveWorkerClaimedWorktreeCanonicalizesSymlinks(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	root := t.TempDir()
	claimed := filepath.Join(root, "claimed")
	if err := os.Mkdir(claimed, 0o755); err != nil {
		t.Fatalf("create claimed worktree: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(claimed, alias); err != nil {
		t.Fatalf("create worktree alias: %v", err)
	}
	insertWorkerWorktreeEntry(t, s, "work-claim", claimed)
	path, err := activeWorkerClaimedWorktree(context.Background(), s.db, "work-claim", alias)
	if err != nil {
		t.Fatalf("canonical worktree match refused: %v", err)
	}
	if path != claimed {
		t.Fatalf("canonical path = %q, want %q", path, claimed)
	}
}

// A session outside every active claim is refused with the ownership conflict,
// and the refusal names both boundaries without pretending a match.
func TestActiveWorkerClaimedWorktreeRefusesMismatch(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	root := t.TempDir()
	claimed := filepath.Join(root, "claimed")
	session := filepath.Join(root, "session")
	for _, path := range []string{claimed, session} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("create worktree %s: %v", path, err)
		}
	}
	insertWorkerWorktreeEntry(t, s, "work-mismatch", claimed)
	err := activeWorkerClaimedWorktree(context.Background(), s.db, "work-mismatch", session)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("failure = %v, want worktree_ownership_conflict", err)
	}
	if strings.Contains(failure.Detail, claimed) || strings.Contains(failure.Detail, session) {
		t.Fatalf("failure leaked a machine path: %q", failure.Detail)
	}
	if !strings.Contains(failure.Detail, "active claim") || !strings.Contains(failure.Detail, "session boundary") {
		t.Fatalf("failure does not identify both worktree boundaries: %q", failure.Detail)
	}
	if failure.RecoveryAction == "" {
		t.Fatal("refusal carries no recovery action")
	}
}

// A work identity without an active claim never resolves a session directory.
func TestActiveWorkerClaimedWorktreeRequiresActiveClaim(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	root := t.TempDir()
	_, err := activeWorkerClaimedWorktree(context.Background(), s.db, "work-without-claim", root)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("absent claim failure = %v, want worktree_ownership_conflict", err)
	}
}

func insertWorkerWorktreeEntry(t *testing.T, s *Store, workID, path string) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`
INSERT INTO write_guard(active) VALUES(1);
INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
VALUES(?,?,?,?,?,?,?,'active',?,'{}');
DELETE FROM write_guard;`, WorktreeSetID(workID), "project-1", "claim-1", "change/claim", strings.Repeat("a", 40), path, "repo-1", "2026-09-05T00:00:00Z")
	if err != nil {
		t.Fatalf("insert worktree entry: %v", err)
	}
}
