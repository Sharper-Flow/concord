package testenv_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sharper-flow/concord/internal/testenv"
)

// Child Go commands must not collect telemetry while a scrubbed test
// environment is active. The supported hook is a private TEST_TELEMETRY_DIR
// whose mode file holds the exact bytes "off": Go then reports GOTELEMETRY
// as off and GOTELEMETRYDIR as that directory, writes no counters, and
// starts no uploader.
func TestScrubEnvQuarantinesGoTelemetry(t *testing.T) {
	// Keep host telemetry settings out of the scrubbed environment, and
	// register restore for every key this test and ScrubEnv rewrite.
	for _, key := range []string{"GOTELEMETRY", "GOTELEMETRYDIR", "TEST_TELEMETRY_DIR"} {
		t.Setenv(key, "")
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"} {
		t.Setenv(key, os.Getenv(key))
	}
	root := testenv.ScrubEnv()
	t.Cleanup(func() {
		if code := testenv.Cleanup(root, 0); code != 0 {
			t.Errorf("cleanup returned %d", code)
		}
	})

	home := os.Getenv("HOME")
	before := homeTree(t, home)

	// go version exercises the counter entrypoint that every Go command runs.
	if out, err := exec.Command("go", "version").CombinedOutput(); err != nil {
		t.Fatalf("go version: %v: %s", err, out)
	}
	output, err := exec.Command("go", "env", "-json", "GOTELEMETRY", "GOTELEMETRYDIR").Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	var reported map[string]string
	if err := json.Unmarshal(output, &reported); err != nil {
		t.Fatalf("decode go env output %s: %v", output, err)
	}
	if got := reported["GOTELEMETRY"]; got != "off" {
		t.Errorf("child go env GOTELEMETRY = %q, want %q", got, "off")
	}
	telemetryDir := os.Getenv("TEST_TELEMETRY_DIR")
	if telemetryDir == "" {
		t.Errorf("TEST_TELEMETRY_DIR is not set: ScrubEnv must quarantine Go telemetry in a private directory")
	} else {
		if got := reported["GOTELEMETRYDIR"]; got != telemetryDir {
			t.Errorf("child go env GOTELEMETRYDIR = %q, want the private TEST_TELEMETRY_DIR %q", got, telemetryDir)
		}
		entries, err := os.ReadDir(telemetryDir)
		if err != nil {
			t.Errorf("read telemetry directory: %v", err)
		} else {
			names := make([]string, len(entries))
			for i, entry := range entries {
				names[i] = entry.Name()
			}
			if !slices.Equal(names, []string{"mode"}) {
				t.Errorf("telemetry directory entries = %v, want [mode] only", names)
			}
			if mode, err := os.ReadFile(filepath.Join(telemetryDir, "mode")); err != nil {
				t.Errorf("read mode file: %v", err)
			} else if string(mode) != "off" {
				t.Errorf("mode file bytes = %q, want %q", mode, "off")
			}
		}
	}
	if after := homeTree(t, home); !slices.Equal(after, before) {
		t.Errorf("child Go commands changed the scrubbed home %s: added %v", home, addedPaths(before, after))
	}
}

// homeTree lists every path under home relative and sorted, so a before and
// after comparison holds even when earlier fixtures placed files there.
func homeTree(t *testing.T, home string) []string {
	t.Helper()
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("stat home: %v", err)
	}
	var paths []string
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		if rel != "." {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk home: %v", err)
	}
	slices.Sort(paths)
	return paths
}

func addedPaths(before, after []string) []string {
	known := make(map[string]bool, len(before))
	for _, path := range before {
		known[path] = true
	}
	var added []string
	for _, path := range after {
		if !known[path] {
			added = append(added, path)
		}
	}
	return added
}
