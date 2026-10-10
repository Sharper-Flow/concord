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

func TestRemovedLauncherVerbPrintsUsageWithoutOpeningAuthority(t *testing.T) {
	database := filepath.Join(t.TempDir(), "authority", "concord.db")
	t.Setenv(dbOverrideEnv, database)
	for _, args := range [][]string{{"launcher"}, {"launcher", "--list"}, {"launcher", "--resume-last"}} {
		var out, diagnostic bytes.Buffer
		if code := runWithInput(args, strings.NewReader("not JSON"), &out, &diagnostic); code != 2 {
			t.Fatalf("%v exit=%d, want 2; stderr=%q", args, code, diagnostic.String())
		}
		if out.Len() != 0 || !strings.Contains(diagnostic.String(), "unsupported arguments:") || !strings.Contains(diagnostic.String(), "Usage:") {
			t.Fatalf("%v stdout=%q stderr=%q", args, out.String(), diagnostic.String())
		}
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatalf("removed verb created authority directory: %v", err)
	}
	var usage bytes.Buffer
	writeUsage(&usage)
	if strings.Contains(usage.String(), "concord launcher") {
		t.Fatal("help still advertises the deleted launcher")
	}
}
