package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// historicalWitnessLogEnv names the file a release restore coverage run
// collects historical corpus rows into. Unset, recording does nothing.
const historicalWitnessLogEnv = "CONCORD_HISTORICAL_WITNESS_LOG"

var historicalWitnessMu sync.Mutex

// recordHistoricalWitness logs one historical row a corpus test inserted. The
// line is appended only when the test passes, so a failing test witnesses
// nothing; the row's bytes are recorded exactly as inserted.
func recordHistoricalWitness(t *testing.T, kind string, payloadVersion int, payload []byte) {
	t.Helper()
	path := os.Getenv(historicalWitnessLogEnv)
	if path == "" {
		return
	}
	line, err := json.Marshal(struct {
		Test           string `json:"test"`
		Kind           string `json:"kind"`
		PayloadVersion int    `json:"payload_version"`
		Payload        string `json:"payload"`
	}{t.Name(), kind, payloadVersion, string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		historicalWitnessMu.Lock()
		defer historicalWitnessMu.Unlock()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("historical witness log: %v", err)
		}
		_, err = file.Write(append(line, '\n'))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("historical witness log: %v", err)
		}
	})
}

func TestHistoricalWitnessRecorderIsInertWhenUnset(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(historicalWitnessLogEnv, "")
	passed := t.Run("unset", func(t *testing.T) {
		recordHistoricalWitness(t, "work.created", 2, []byte(`{"title":"x"}`))
	})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !passed || len(entries) != 0 {
		t.Fatalf("unset recorder: subtest passed=%v, wrote %d entries", passed, len(entries))
	}
}

func TestHistoricalWitnessRecorderAppendsPassingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witnesses.jsonl")
	t.Setenv(historicalWitnessLogEnv, path)
	t.Run("row", func(t *testing.T) {
		recordHistoricalWitness(t, "work.created", 2, []byte(`{"title":"x"}`))
		recordHistoricalWitness(t, "work.created", 1, []byte(`{`))
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last-registered first.
	want := `{"test":"TestHistoricalWitnessRecorderAppendsPassingRows/row","kind":"work.created","payload_version":1,"payload":"{"}` + "\n" +
		`{"test":"TestHistoricalWitnessRecorderAppendsPassingRows/row","kind":"work.created","payload_version":2,"payload":"{\"title\":\"x\"}"}` + "\n"
	if string(data) != want {
		t.Fatalf("witness log =\n%s\nwant\n%s", data, want)
	}
}
