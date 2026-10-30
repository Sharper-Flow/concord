package store

import (
	"testing"
)

func seedChecksRepairFixture(t *testing.T, workID, step string) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, step, []string{"verification"}, []string{"verification", "review", "artifact"})
}

func checksRepairClaimGit(t *testing.T, s *Store, work, dir, head string) *fakeWorktreeGit {
	t.Helper()
	var branch string
	if err := s.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(work)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(dir)
	git.worktrees[dir] = branch
	git.worktreeRepos[dir] = dir
	git.branches[branch] = head
	return git
}

func passExitCode() *int { value := 0; return &value }
