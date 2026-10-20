package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestOutsideRepairUsesCorrectedRemoteIdentity(t *testing.T) {
	for _, raw := range []string{"https://github.com/Owner/Repo.git", "git@github.com:Owner/Repo.git", "ssh://git@github.com/Owner/Repo.git"} {
		normalized, err := store.NormalizeProjectLocator(store.LocatorGitRemote, raw)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := store.GitHubRepositoryFromRemote(normalized)
		if !ok || got != "owner/repo" {
			t.Errorf("repository for %q = %q, %v", normalized, got, ok)
		}
	}
	for _, raw := range []string{"ssh://https/github.com/owner/repo", "ssh://http/github.com/owner/repo", "ssh://ssh/git@github.com/owner/repo", "https://github.com/owner/repo/extra", "https://elsewhere.invalid/owner/repo"} {
		if got, ok := store.GitHubRepositoryFromRemote(raw); ok || got != "" {
			t.Errorf("invalid GitHub identity %q accepted as %q", raw, got)
		}
	}
}
