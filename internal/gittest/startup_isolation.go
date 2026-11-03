package gittest

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

// TestStartupIsolation runs child in a re-executed test binary with Git pointed
// at an outer repository, then checks that the outer repository is unchanged.
func TestStartupIsolation(t *testing.T, child func(t *testing.T)) {
	t.Helper()
	if os.Getenv("TEST_CONCORD_FIXTURE_ISOLATION_CHILD") == "1" {
		child(t)
		return
	}
	outer := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", outer}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(outer, "README.md"), []byte("outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "outer")
	run("remote", "add", "origin", "https://outer.invalid/repo.git")
	snapshot := func() map[string]string {
		t.Helper()
		state := map[string]string{}
		err := filepath.WalkDir(outer, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(outer, path)
			if err != nil {
				return err
			}
			state[rel] = info.Mode().String()
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				state[rel] += ":" + target
			} else if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				state[rel] += fmt.Sprintf(":%x", sha256.Sum256(data))
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
	process := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitFixtureStartupIsolation$")
	process.WaitDelay = time.Second
	process.Dir = t.TempDir()
	process.Env = append(os.Environ(), "TEST_CONCORD_FIXTURE_ISOLATION_CHILD=1", "GIT_DIR="+filepath.Join(outer, ".git"), "GIT_WORK_TREE="+outer, "GIT_COMMON_DIR="+filepath.Join(outer, ".git"), "GIT_INDEX_FILE="+filepath.Join(outer, ".git", "index"), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0="+outer)
	out, err := process.CombinedOutput()
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("child changed outer repository: %v\n%s", err, out)
	}
	if err != nil {
		t.Fatalf("fixture child: %v\n%s", err, out)
	}
}
