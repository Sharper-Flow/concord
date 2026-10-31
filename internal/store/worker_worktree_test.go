package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeWorktreePathCanonicalizesSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	claimed := filepath.Join(root, "claimed")
	if err := os.Mkdir(claimed, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(claimed, alias); err != nil {
		t.Fatal(err)
	}
	path, err := canonicalNativeWorktreePath(alias)
	if err != nil || path != claimed {
		t.Fatalf("canonical path = %q, %v; want %q", path, err, claimed)
	}
}
