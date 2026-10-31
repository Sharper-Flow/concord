package store

import (
	"context"
	"path/filepath"
	"testing"
)

func gitRunStore(t *testing.T, dir string, args ...string) {
	t.Helper()
	_, err := ExecGitRunner{}.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// seedRealGitOrigin registers a local bare origin and mirrors the default
// branch into it, so origin/HEAD names the fetched default branch.
func seedRealGitOrigin(t *testing.T, repoRoot string) {
	t.Helper()
	origin := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"-origin.git")
	gitRunStore(t, filepath.Dir(repoRoot), "init", "-q", "--bare", "-b", "main", origin)
	gitRunStore(t, repoRoot, "remote", "add", "origin", origin)
	gitRunStore(t, repoRoot, "push", "-q", "origin", "main")
	gitRunStore(t, repoRoot, "fetch", "-q", "origin")
	gitRunStore(t, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}
