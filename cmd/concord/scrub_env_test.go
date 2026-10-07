package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	gittest.DisableBackgroundMaintenance()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestDefaultDatabasePathIsIsolated(t *testing.T) {
	if _, present := os.LookupEnv(dbOverrideEnv); present {
		t.Fatal("TestMain retained CONCORD_DB_PATH")
	}
	root := os.Getenv("HOME")
	if root == "" || os.Getenv("XDG_DATA_HOME") != root {
		t.Fatal("TestMain did not isolate HOME and XDG_DATA_HOME together")
	}
	path, err := databasePath()
	if err != nil || path != filepath.Join(root, "concord", "concord.db") {
		t.Fatalf("default database path = %q, err = %v", path, err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	path, err = databasePath()
	if err != nil || path != filepath.Join(root, ".local", "share", "concord", "concord.db") {
		t.Fatalf("HOME fallback database path = %q, err = %v", path, err)
	}
}
