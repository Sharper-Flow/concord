package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerCompleteCLIBindsDirectoryToAttemptProject(t *testing.T) {
	for _, tc := range []struct {
		name       string
		foreign    bool
		symlink    bool
		wantRefuse bool
	}{
		{name: "attempt Project claim"},
		{name: "attempt Project claim through symlink", symlink: true},
		{name: "another active Project claim", foreign: true, wantRefuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			nonce := "nonce-complete-project-claim-0001"
			request, assertion, key := completeImplementationAttempt(t, dbPath, nonce)
			ownDirectory := cliFixtureWorktree(t, dbPath)
			otherDirectory := seedWorkerCompletionOtherProjectClaim(t, dbPath)
			directory := ownDirectory
			if tc.foreign {
				directory = otherDirectory
			}
			if tc.symlink {
				alias := filepath.Join(t.TempDir(), "claimed-alias")
				if err := os.Symlink(directory, alias); err != nil {
					t.Fatal(err)
				}
				directory = alias
			}
			request["event_id"] = "complete-project-directory"
			request["worker_directory"] = directory
			code, out, stderr := runCompleteJSON(t, request, assertion, key)
			if !tc.wantRefuse {
				if code != 0 {
					t.Fatalf("worker-complete at its attempt's Project claim exit=%d stderr=%s out=%s", code, stderr, out)
				}
				return
			}
			if code == 0 {
				t.Fatalf("worker-complete accepted another Project's active claim: exit=%d stderr=%s out=%s", code, stderr, out)
			}
			kind, effect, _ := typedWorkerEvidenceError(t, out)
			if kind != string(store.KindUnauthorizedDispatch) || effect != "none" {
				t.Fatalf("worker-complete refusal = %s/%s, want unauthorized_dispatch/none: %s", kind, effect, out)
			}
			assertZeroTerminalEffects(t, dbPath, nonce)
		})
	}
}

func seedWorkerCompletionOtherProjectClaim(t *testing.T, dbPath string) string {
	t.Helper()
	directory := t.TempDir()
	seedGitFixtureWorktree(t, directory)
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO write_guard(active) VALUES(1)`},
		{query: `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-2','Second repository',1,'2026-08-22T00:00:00Z','2026-08-22T00:00:00Z')`},
		{query: `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-1','project-2','secondary')`},
		{query: `INSERT INTO work_projects(work_id,project_id,role) VALUES('work-1','project-2','secondary')`},
		{query: `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,'project-2','claim-project-2','work/second-project',?,?,'repo-2','active','2026-08-22T00:00:00Z','{}')`, args: []any{store.WorktreeSetID("work-1"), strings.Repeat("b", 40), directory}},
		{query: `DELETE FROM write_guard`},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var claims int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active' AND project_id IN ('project-1','project-2')`, store.WorktreeSetID("work-1")).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 2 {
		t.Fatalf("active Project claims = %d, want 2", claims)
	}
	return directory
}
