package store

import (
	"context"
	"testing"
)

func TestFreshnessPreflightPreservesNamedOriginTransport(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	runTestGit(t, repo, "config", "remote.origin.uploadpack", "false")
	originURL := runTestGit(t, repo, "config", "--get", "remote.origin.url")
	refspec := "+refs/heads/main:refs/remotes/origin/main"
	runTestGit(t, repo, "fetch", "--no-tags", "--no-recurse-submodules", originURL, refspec)
	preflight, err := startCreationPreflight(context.Background(), ExecGitRunner{}, repo)
	preflight.close()
	if err == nil {
		t.Fatal("preflight bypassed the configured origin transport")
	} else {
		assertFailureKind(t, err, KindGitUnreachable)
	}
}
