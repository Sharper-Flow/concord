package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerDiffIncludesIgnoredGitlinkChanges(t *testing.T) {
	for _, setting := range []string{"config", "gitmodules"} {
		t.Run(setting, func(t *testing.T) {
			repo := t.TempDir()
			gitRunStore(t, repo, "init", "-q", "-b", "main")
			gitRunStore(t, repo, "config", "user.name", "Synthetic Author")
			gitRunStore(t, repo, "config", "user.email", "author@example.invalid")
			gitRunStore(t, repo, "commit", "-q", "--allow-empty", "-m", "first child")
			runner := ExecGitRunner{}
			git := func(args ...string) string {
				t.Helper()
				raw, err := runner.Run(t.Context(), repo, args...)
				if err != nil {
					t.Fatalf("git %v: %v", args, err)
				}
				return strings.TrimSpace(string(raw))
			}
			first := git("rev-parse", "HEAD")
			gitRunStore(t, repo, "commit", "-q", "--allow-empty", "-m", "second child")
			second := git("rev-parse", "HEAD")
			modules := "[submodule \"child\"]\n\tpath = child\n\turl = https://example.invalid/child.git\n"
			if setting == "gitmodules" {
				modules += "\tignore = all\n"
			} else {
				gitRunStore(t, repo, "config", "submodule.child.ignore", "all")
			}
			if err := os.WriteFile(filepath.Join(repo, ".gitmodules"), []byte(modules), 0o600); err != nil {
				t.Fatal(err)
			}
			gitRunStore(t, repo, "add", ".gitmodules")
			gitRunStore(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+first+",child")
			gitRunStore(t, repo, "commit", "-q", "-m", "initial gitlink")
			base := git("rev-parse", "HEAD")
			gitRunStore(t, repo, "update-index", "--cacheinfo", "160000,"+second+",child")
			gitRunStore(t, repo, "commit", "-q", "-m", "changed gitlink")
			head := git("rev-parse", "HEAD")
			paths := git(append(workerDiffSharedDiffArgs(), "--name-only", base, head, "--")...)
			if paths != "child" {
				t.Fatalf("scope observation hid the changed gitlink: %q", paths)
			}
			patch := git(append(workerDiffSharedDiffArgs(), base, head, "--")...)
			if !strings.Contains(patch, "-Subproject commit "+first) || !strings.Contains(patch, "+Subproject commit "+second) {
				t.Fatalf("Review diff hid the changed gitlink: %s", patch)
			}
		})
	}
}
