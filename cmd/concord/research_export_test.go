package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResearchExportCLI(t *testing.T) {
	freshMigratedCLIDatabase(t)
	destination := filepath.Join(t.TempDir(), "research")
	raw := runCLIJSON(t, []string{"research-export"}, map[string]any{"destination": destination})
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result["pack_count"] != float64(0) || result["destination"] != destination {
		t.Fatalf("result = %v", result)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("export = %v, %v", entries, err)
	}
}

func TestResearchExportCLIRefusesInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		input string
		code  int
	}{
		{`{}`, inputRefusalExit},
		{`{"destination":"/unused","unknown":true}`, 1},
	} {
		t.Run(tc.input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.db")
			t.Setenv(dbOverrideEnv, path)
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"research-export"}, bytes.NewBufferString(tc.input), &out, &errOut); code != tc.code {
				t.Fatalf("exit = %d, stderr = %s", code, &errOut)
			}
			if out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("stdout = %s, stderr = %s", &out, &errOut)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid input created source: %v", err)
			}
		})
	}
}

func TestResearchExportCLIMissingSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	t.Setenv(dbOverrideEnv, path)
	destination := filepath.Join(t.TempDir(), "research")
	input, err := json.Marshal(map[string]string{"destination": destination})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"research-export"}, bytes.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, stderr = %s", code, &errOut)
	}
	for _, candidate := range []string{path, destination, filepath.Join(filepath.Dir(path), "host-leases")} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Errorf("export created %s: %v", candidate, err)
		}
	}
}

func TestResearchExportCLIPendingUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source ?#%.db")
	pendingBreakingStore(t, path, true)
	t.Setenv(dbOverrideEnv, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runCLIJSON(t, []string{"research-export"}, map[string]any{"destination": t.TempDir()})
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("export modified source with pending migrations: %v", err)
	}
}
