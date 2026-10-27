package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Native snapshots deliberately contain no .git. These tests inspect committed
// recipes or invoke publication code that discovers its repository through Git.
// A missing prerequisite is a skip, never a passing required witness.
type nativeGitPrerequisiteTesting interface {
	Helper()
	Fatal(...any)
	Skip(...any)
}

func requireNativeRecipeGitMetadata(t nativeGitPrerequisiteTesting, root string) {
	t.Helper()
	if !nativeRecipeGitMetadataPresent(t, root) {
		t.Skip("repository Git metadata is unavailable in the controlled snapshot; prerequisite unproved")
	}
}

func nativeRecipeGitMetadataPresent(t nativeGitPrerequisiteTesting, root string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, ".git"))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func TestNativeRecipeGitMetadataPrerequisite(t *testing.T) {
	root := t.TempDir()
	if nativeRecipeGitMetadataPresent(t, root) {
		t.Fatal("missing metadata qualified")
	}
	missing := &nativeGitPrerequisiteRecorder{t: t}
	requireNativeRecipeGitMetadata(missing, root)
	if missing.skip != "repository Git metadata is unavailable in the controlled snapshot; prerequisite unproved" {
		t.Fatal("missing metadata was not reported as an unproved skip")
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if !nativeRecipeGitMetadataPresent(t, root) {
		t.Fatal("linked worktree metadata ignored")
	}
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if !nativeRecipeGitMetadataPresent(t, root) {
		t.Fatal("checkout metadata ignored")
	}
}

type nativeGitPrerequisiteRecorder struct {
	t    *testing.T
	skip string
}

func (*nativeGitPrerequisiteRecorder) Helper()             {}
func (r *nativeGitPrerequisiteRecorder) Fatal(args ...any) { r.t.Fatal(args...) }
func (r *nativeGitPrerequisiteRecorder) Skip(args ...any)  { r.skip = args[0].(string) }
