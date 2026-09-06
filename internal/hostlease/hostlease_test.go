package hostlease

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeaseRoundTripKeepsALiveProcessAndPrunesAStaleOne(t *testing.T) {
	root := t.TempDir()
	start, err := ProcessStart(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessStart(self) error = %v", err)
	}
	lease := Lease{
		PID:            os.Getpid(),
		PidStart:       start,
		ReleaseRoot:    "/releases/v1.2.3",
		CoreBinary:     "/releases/v1.2.3/bin/concord",
		SchemaVersion:  73,
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		RecordedAt:     "2026-09-06T00:00:00Z",
	}
	if err := Write(root, lease); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	live, err := List(root)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(live) != 1 || live[0] != lease {
		t.Fatalf("List() = %+v, want the written lease", live)
	}

	// A recycled pid: the same pid with the start time it no longer has is
	// stale and must be pruned, and the replacement lease is live.
	recycled := lease
	recycled.PidStart = start + 1
	if err := Write(root, recycled); err == nil || !strings.Contains(err.Error(), "not live") {
		t.Fatalf("Write() for a recycled pid = %v, want a stale-process refusal", err)
	}
	if err := os.WriteFile(filepath.Join(Directory(root), "424242.json"), mustJSON(t, Lease{PID: 424242, PidStart: 1, ReleaseRoot: "/releases/v0.0.9", SchemaVersion: 1}), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err = List(root)
	if err != nil {
		t.Fatalf("List() with a stale entry error = %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("List() = %d leases, want 1 after pruning", len(live))
	}
	if _, err := os.Stat(filepath.Join(Directory(root), "424242.json")); !os.IsNotExist(err) {
		t.Fatalf("stale lease was not pruned: %v", err)
	}

	// A malformed lease is an observation failure, never a silent prune.
	if err := os.WriteFile(filepath.Join(Directory(root), "99.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(root); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("List() with a malformed lease = %v, want a refusal", err)
	}
}

func TestListReadsAnAbsentDirectoryAsEmpty(t *testing.T) {
	live, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("List() = %+v, want none", live)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
