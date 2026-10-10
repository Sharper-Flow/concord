package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBareConcordOnTerminalPrintsUsage(t *testing.T) {
	terminal, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	if !terminalStreams(terminal, terminal) {
		t.Fatal("fixture must exercise character-device stdin and stdout")
	}
	database := filepath.Join(t.TempDir(), "authority", "concord.db")
	t.Setenv(dbOverrideEnv, database)
	var diagnostic bytes.Buffer
	if code := runWithInput(nil, terminal, terminal, &diagnostic); code != 2 {
		t.Fatalf("bare concord exit=%d, want 2; stderr=%q", code, diagnostic.String())
	}
	if !strings.HasPrefix(diagnostic.String(), "Usage:\n") {
		t.Fatalf("bare concord stderr=%q, want usage", diagnostic.String())
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatalf("bare concord created authority directory: %v", err)
	}
}
