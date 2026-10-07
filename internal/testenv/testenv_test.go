package testenv_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/testenv"
)

func TestScrubEnv(t *testing.T) {
	for _, key := range []string{"CONCORD_DB_PATH", "CONCORD_SELECTED_PRODUCT_ID", "CONCORD_TEST_SUBPROCESS", "CONCORD_FUTURE_INPUT"} {
		t.Setenv(key, "poison")
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"} {
		t.Setenv(key, os.Getenv(key))
	}
	before, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := testenv.ScrubEnv()
	t.Cleanup(func() {
		if code := testenv.Cleanup(root, 0); code != 0 {
			t.Errorf("cleanup returned %d", code)
		}
	})
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CONCORD_") {
			t.Errorf("session variable survived: %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if got := os.Getenv(key); got != root {
			t.Errorf("%s = %q, want %q", key, got, root)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary home must start empty: entries=%v err=%v", entries, err)
	}
	after, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV").Output()
	if err != nil || string(after) != string(before) {
		t.Fatalf("child Go paths changed: before=%s after=%s err=%v", before, after, err)
	}
	t.Setenv("CONCORD_DB_PATH", filepath.Join(root, "fixture.db"))
	if os.Getenv("CONCORD_DB_PATH") == "" {
		t.Fatal("explicit test input was not retained")
	}
}

func TestCleanupAndFreshHome(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, os.Getenv(key))
	}
	first := testenv.ScrubEnv()
	second := testenv.ScrubEnv()
	if first == second {
		t.Fatal("temporary homes collided")
	}
	if code := testenv.Cleanup(first, 7); code != 7 {
		t.Fatalf("test failure changed: %d", code)
	}
	if code := testenv.Cleanup(second, 0); code != 0 {
		t.Fatalf("cleanup failed: %d", code)
	}
	for _, root := range []string{first, second} {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("temporary home still exists: %v", err)
		}
	}
}
