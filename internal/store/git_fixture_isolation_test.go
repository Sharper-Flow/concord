package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A fixture run under a Git hook inherits GIT_DIR, GIT_WORK_TREE, the index,
// and injected config from the hook. The package's environment scrub must
// keep every fixture repository write inside the fixture: the child process
// builds, commits, and configures its own repository while the hook
// variables point at an outer repository, and the outer tree stays
// byte-identical.
func TestGitFixtureIsolationFromHookEnvironment(t *testing.T) {
	if os.Getenv("TEST_CONCORD_GIT_FIXTURE_CHILD") == "1" {
		repo := initKnowledgeRepo(t)
		writeKnowledgeFile(t, repo, "README.md", "isolated child\n")
		commitKnowledgeRepo(t, repo, "fixture seed")
		runKnowledgeGit(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "child-origin.git"))
		writeKnowledgeFile(t, repo, "README.md", "isolated child advance\n")
		commitKnowledgeRepo(t, repo, "fixture advance")
		return
	}
	outer := initKnowledgeRepo(t)
	runKnowledgeGit(t, outer, "config", "user.email", "outer@example.invalid")
	writeKnowledgeFile(t, outer, "README.md", "outer repository\n")
	commitKnowledgeRepo(t, outer, "outer seed")
	runTestGit(t, outer, "remote", "add", "origin", filepath.Join(t.TempDir(), "unused-origin.git"))
	snapshot := func() map[string]string {
		t.Helper()
		state := map[string]string{}
		err := filepath.WalkDir(outer, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(outer, path)
			if err != nil {
				return err
			}
			state[relative] = info.Mode().String()
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				state[relative] += ":" + target
			} else if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				state[relative] += fmt.Sprintf(":%x", sha256.Sum256(data))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitFixtureIsolationFromHookEnvironment$")
	child.WaitDelay = time.Second
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(),
		"TEST_CONCORD_GIT_FIXTURE_CHILD=1",
		"GIT_DIR="+filepath.Join(outer, ".git"),
		"GIT_WORK_TREE="+outer,
		"GIT_COMMON_DIR="+filepath.Join(outer, ".git"),
		"GIT_INDEX_FILE="+filepath.Join(outer, ".git/index"),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0="+outer)
	output, err := child.CombinedOutput()
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture child changed the outer repository tree: %v\n%s", err, output)
	}
	if err != nil {
		t.Fatalf("isolated fixture child: %v\n%s", err, output)
	}
}

func TestTestGitCommandRefusesImplicitWorkingDirectory(t *testing.T) {
	for _, repo := range []string{"", ".", "..", "relative/repo"} {
		command, err := newTestGitCommand(repo, "remote", "remove", "origin")
		if err == nil || command != nil {
			t.Fatalf("directory %q produced command %v, error %v", repo, command, err)
		}
	}
	repo := t.TempDir()
	command, err := newTestGitCommand(repo, "status", "--porcelain")
	if err != nil || command == nil {
		t.Fatalf("absolute fixture command: %v", err)
	}
	if command.Dir != repo || !reflect.DeepEqual(command.Args[1:], []string{"status", "--porcelain"}) {
		t.Fatalf("command must chdir explicitly: dir=%q args=%q", command.Dir, command.Args)
	}
}
