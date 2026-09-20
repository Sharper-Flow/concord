package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBareConcordOffTTYPrintsUsageAndExits2 proves the non-TTY half of
// check:launcher.bare_command_starts_launcher: with no arguments and no
// terminal there is no interactive surface to hand over, so bare concord
// prints usage and exits 2. It must not read stdin as JSON.
func TestBareConcordOffTTYPrintsUsageAndExits2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runBareInvocation(strings.NewReader("not json"), &out, &errOut, false); code != 2 {
		t.Fatalf("bare exit code = %d, want 2; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("bare non-TTY diagnostic = %q, want usage", errOut.String())
	}
	if strings.Contains(errOut.String(), "invalid character") {
		t.Fatalf("bare invocation was routed through JSON handling: %q", errOut.String())
	}
}

// TestBareConcordOnTTYStartsLauncher proves the TTY half of
// check:launcher.bare_command_starts_launcher: with no arguments and a
// terminal, bare concord starts the launcher. A "q" quits the TUI, so the
// run exits 0 having rendered; a JSON stdin parse of "q" would have failed
// with an unreadable-input diagnostic instead.
func TestBareConcordOnTTYStartsLauncher(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut bytes.Buffer
	if code := runBareInvocation(strings.NewReader("q"), &out, &errOut, true); code != 0 {
		t.Fatalf("bare TTY exit code = %d; stderr=%q", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("bare TTY run printed usage instead of starting the launcher: %q", errOut.String())
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("bare launcher run changed the authority path: stat=%v", err)
	}
}
