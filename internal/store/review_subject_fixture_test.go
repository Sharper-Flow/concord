package store

import (
	"os"
	"testing"
)

// reviewSubjectClaim creates a candidate repository for Review fixtures.
func reviewSubjectClaim(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	return reviewSubjectClaimObserved(t, s, workID, true)
}

func reviewSubjectClaimObserved(t *testing.T, s *Store, workID string, _ bool) (string, string) {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	var projectVersion int64
	if err := s.db.QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project", ProjectLocator{ID: "review-repo-" + workID, Kind: LocatorCanonicalPath, Value: root}, projectVersion); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(root)
	claim := baseClaim(git)
	claim.WorkID, claim.ProjectID, claim.ExpectedVersion = workID, "project", readWorkVersion(t, s, workID)
	claim.OpID, claim.RequestID = "review-claim-"+workID, "review-claim-"+workID
	claimed, err := s.ClaimWorktree(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	dir := claimed.Entry.Path
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "init", "-b", claimed.Entry.Branch)
	gitRunStore(t, dir, "config", "user.email", "concord@example.invalid")
	gitRunStore(t, dir, "config", "user.name", "Concord Review Subject Test")
	if err := os.WriteFile(dir+"/seed.txt", []byte("review subject seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "add", "-A")
	gitRunStore(t, dir, "commit", "-m", "review subject seed")
	head := runTestGit(t, dir, "rev-parse", "--verify", "HEAD^{commit}")
	return dir, head
}
