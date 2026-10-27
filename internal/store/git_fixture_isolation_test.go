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

func TestGitFixtureIsolationFromHookEnvironment(t *testing.T) {
	if os.Getenv("TEST_CONCORD_GIT_FIXTURE_CHILD") == "1" {
		repo := initKnowledgeRepo(t)
		writeKnowledgeFile(t, repo, "README.md", "isolated child\n")
		commitKnowledgeRepo(t, repo, "fixture seed")
		queryPublicationSourceOrigin(t, repo)
		queryPublicationAdvance(t, repo, "fixture advance")
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

func queryPublicationFixtureDirectory(repo string) (string, error) {
	if !filepath.IsAbs(repo) {
		return "", fmt.Errorf("publication fixture requires an absolute repository path: %q", repo)
	}
	fixture, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	temporaryRoot := os.Getenv("GOTMPDIR")
	if temporaryRoot == "" {
		temporaryRoot = os.TempDir()
	}
	root, err := filepath.EvalSymlinks(temporaryRoot)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, fixture)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("publication fixture is outside a temporary subdirectory: %q", fixture)
	}
	info, err := os.Lstat(filepath.Join(fixture, ".git"))
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("publication fixture requires its own .git directory: %q", fixture)
	}
	return fixture, nil
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

func TestPublicationFixtureDirectoryRefusesUnownedGitState(t *testing.T) {
	for _, repo := range []string{"", ".", "..", os.TempDir(), t.TempDir()} {
		if got, err := queryPublicationFixtureDirectory(repo); err == nil || got != "" {
			t.Fatalf("directory %q accepted: %q, %v", repo, got, err)
		}
	}
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: ../external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := queryPublicationFixtureDirectory(linked); err == nil {
		t.Fatal("linked worktree accepted as an isolated publication fixture")
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := queryPublicationFixtureDirectory(repo); err != nil || got != want {
		t.Fatalf("owned fixture = %q, %v; want %q", got, err, want)
	}
	symlinked := t.TempDir()
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(symlinked, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := queryPublicationFixtureDirectory(symlinked); err == nil {
		t.Fatal("symlinked Git directory accepted as owned fixture state")
	}
}

func TestPublicationFixtureDirectoryUsesGoTemporaryRoot(t *testing.T) {
	goRoot, systemRoot := t.TempDir(), t.TempDir()
	goRepo, systemRepo := filepath.Join(goRoot, "repo"), filepath.Join(systemRoot, "repo")
	for _, repo := range []string{goRepo, systemRepo} {
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOTMPDIR", goRoot)
	t.Setenv("TMPDIR", systemRoot)
	want, err := filepath.EvalSymlinks(goRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := queryPublicationFixtureDirectory(goRepo); err != nil || got != want {
		t.Fatalf("Go temporary fixture = %q, %v; want %q", got, err, want)
	}
	if _, err := queryPublicationFixtureDirectory(systemRepo); err == nil {
		t.Fatal("accepted a system temporary fixture outside the configured Go root")
	}
	t.Setenv("GOTMPDIR", "")
	want, err = filepath.EvalSymlinks(systemRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := queryPublicationFixtureDirectory(systemRepo); err != nil || got != want {
		t.Fatalf("default temporary fixture = %q, %v; want %q", got, err, want)
	}
}
