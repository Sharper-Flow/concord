package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkerDispatchWorktreeCanonicalizesSymlinks(t *testing.T) {
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
	if err := validateWorkerDispatchWorktree(context.Background(), s.db, "work-claim", alias); err != nil {
		t.Fatalf("canonical worktree match refused: %v", err)
	}
}

func TestValidateWorkerDispatchWorktreeRefusesMismatchWithoutPathLeak(t *testing.T) {
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
	err := validateWorkerDispatchWorktree(context.Background(), s.db, "work-mismatch", session)
	if err == nil {
		t.Fatal("mismatched worktree was accepted")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure = %v, want unauthorized_dispatch", err)
	}
	if strings.Contains(failure.Detail, claimed) || strings.Contains(failure.Detail, session) {
		t.Fatalf("failure leaked a machine path: %q", failure.Detail)
	}
	if !strings.Contains(failure.Detail, "expected") || !strings.Contains(failure.Detail, "session boundary") {
		t.Fatalf("failure does not identify both worktree boundaries: %q", failure.Detail)
	}
}

func TestValidateWorkerDispatchWorktreeRequiresActiveClaim(t *testing.T) {
	s := openTemp(t)
	root := t.TempDir()
	err := validateWorkerDispatchWorktree(context.Background(), s.db, "work-without-claim", root)
	if err == nil {
		t.Fatal("dispatch without an active claim was accepted")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure = %v, want unauthorized_dispatch", err)
	}
}

func insertWorkerWorktreeEntry(t *testing.T, s *Store, workID, path string) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`
INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
VALUES(?,?,?,?,?,?,?,'active',?,'{}');
DELETE FROM fold_guard;`, WorktreeSetID(workID), "project-1", "claim-1", "change/claim", strings.Repeat("a", 40), path, "repo-1", "2026-09-05T00:00:00Z")
	if err != nil {
		t.Fatalf("insert worktree entry: %v", err)
	}
}
