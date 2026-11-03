package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/gittest"
)

// A fixture run under a Git hook inherits GIT_DIR, GIT_WORK_TREE, the index,
// and injected config from the hook. The package's environment scrub must
// keep every fixture repository write inside the fixture: the child process
// builds, commits, and configures its own repository while the hook
// variables point at an outer repository, and the outer tree stays
// byte-identical.
func TestGitFixtureIsolationFromHookEnvironment(t *testing.T) {
	gittest.TestStartupIsolation(t, func(t *testing.T) {
		repo := initKnowledgeRepo(t)
		writeKnowledgeFile(t, repo, "README.md", "isolated child\n")
		commitKnowledgeRepo(t, repo, "fixture seed")
		runKnowledgeGit(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "child-origin.git"))
		writeKnowledgeFile(t, repo, "README.md", "isolated child advance\n")
		commitKnowledgeRepo(t, repo, "fixture advance")
	})
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
	if helper := os.Getenv("TEST_CONCORD_FIXTURE_HELPER"); helper != "" {
		dir := os.Getenv("TEST_CONCORD_FIXTURE_DIR")
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
				before := gittest.SnapshotTree(t, outer)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFixtureHelpersRefuseInvalidDirectories$")
				child.WaitDelay = time.Second
				child.Dir = outer
				if dir == ".." {
					child.Dir = sub
				}
				child.Env = append(os.Environ(), "TEST_CONCORD_FIXTURE_HELPER="+helper, "TEST_CONCORD_FIXTURE_DIR="+dir)
				output, err := child.CombinedOutput()
				if after := gittest.SnapshotTree(t, outer); !reflect.DeepEqual(before, after) {
					t.Errorf("invalid fixture path changed outer repository: %v\n%s", err, output)
				}
				if err == nil || !strings.Contains(string(output), "test git requires an absolute fixture directory") {
					t.Errorf("invalid fixture path was not refused by guard: %v\n%s", err, output)
				}
			})
		}
	}
}
