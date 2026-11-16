package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func hideGitDir(t *testing.T, path string) func() {
	t.Helper()
	gitDir := filepath.Join(path, ".git")
	hidden := filepath.Join(t.TempDir(), "git")
	if err := os.Rename(gitDir, hidden); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Rename(hidden, gitDir); err != nil {
			t.Error(err)
		}
	}
}
