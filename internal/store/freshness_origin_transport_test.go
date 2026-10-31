package store

import "testing"

func TestPublicationTransportPreservesNamedOriginTransport(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	runTestGit(t, repo, "config", "remote.origin.uploadpack", "false")
	originURL := runTestGit(t, repo, "config", "--get", "remote.origin.url")
	refspec := "+refs/heads/main:refs/remotes/origin/main"
	runTestGit(t, repo, "fetch", "--no-tags", "--no-recurse-submodules", originURL, refspec)
	if result := observeTransportPublication(t, repo); result.Comparison != "unknown" || result.Recovery != publicationAccessRecovery {
		t.Fatalf("publication bypassed the configured origin transport: %+v", result)
	}
}
