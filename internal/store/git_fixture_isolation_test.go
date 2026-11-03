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
	"strings"
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
	before := snapshotGitFixtureTree(t, outer)
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
	if after := snapshotGitFixtureTree(t, outer); !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture child changed the outer repository tree: %v\n%s", err, output)
	}
	if err != nil {
		t.Fatalf("isolated fixture child: %v\n%s", err, output)
	}
}

func snapshotGitFixtureTree(t *testing.T, outer string) map[string]string {
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

func TestFixtureHelpersRefuseInvalidDirectories(t *testing.T) {
	if helper := os.Getenv("TEST_FIXTURE_HELPER"); helper != "" {
		dir := os.Getenv("TEST_FIXTURE_DIR")
		switch helper {
		case "locator":
			runLocatorFixtureGit(t, dir, "remote", "remove", "origin")
		case "blob":
			runBlobFixtureGit(t, dir, "remote", "remove", "origin")
		default:
			t.Fatalf("unknown helper %q", helper)
		}
		return
	}
	for _, helper := range []string{"locator", "blob"} {
		for _, dir := range []string{"", ".", "..", "sub"} {
			t.Run(fmt.Sprintf("%s/%q", helper, dir), func(t *testing.T) {
				outer := initKnowledgeRepo(t)
				writeKnowledgeFile(t, outer, "README.md", "outer repository\n")
				commitKnowledgeRepo(t, outer, "outer seed")
				runTestGit(t, outer, "remote", "add", "origin", filepath.Join(t.TempDir(), "origin.git"))
				sub := filepath.Join(outer, "sub")
				if err := os.Mkdir(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				before := snapshotGitFixtureTree(t, outer)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFixtureHelpersRefuseInvalidDirectories$")
				child.WaitDelay = time.Second
				child.Dir = outer
				if dir == ".." {
					child.Dir = sub
				}
				child.Env = append(os.Environ(), "TEST_FIXTURE_HELPER="+helper, "TEST_FIXTURE_DIR="+dir)
				output, err := child.CombinedOutput()
				if after := snapshotGitFixtureTree(t, outer); !reflect.DeepEqual(before, after) {
					t.Errorf("invalid fixture path changed outer repository: %v\n%s", err, output)
				}
				if err == nil || !strings.Contains(string(output), "test git requires an absolute fixture directory") {
					t.Errorf("invalid fixture path was not refused by guard: %v\n%s", err, output)
				}
			})
		}
	}
}
