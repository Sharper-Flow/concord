package store

import (
	"context"
	"testing"
)

func TestFreshnessPreflightPreservesNamedOriginTransport(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	runFreshnessGit(t, repo, "config", "remote.origin.uploadpack", "false")
	originURL := runFreshnessGit(t, repo, "config", "--get", "remote.origin.url")
	refspec := "+refs/heads/main:refs/remotes/origin/main"
	runFreshnessGit(t, repo, "fetch", "--no-tags", "--no-recurse-submodules", originURL, refspec)
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, repo)
	preflight.close()
	if err == nil {
		t.Fatal("preflight bypassed the configured origin transport")
	} else {
		assertFailureKind(t, err, KindGitUnreachable)
	}
}
