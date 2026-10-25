package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The SQLite file remains structurally readable, but its current membership
// rows violate foreign keys. History payloads do not decide this refusal.
func seedSnapshotForeignKeyCorruption(t *testing.T, path, guardTable string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO ` + guardTable + `(active) VALUES(1);
INSERT INTO work_projects(work_id,project_id,role) VALUES('missing-work','missing-project','primary'); DELETE FROM ` + guardTable); err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"integrity_check", "quick_check"} {
		var result string
		if err := db.QueryRow("PRAGMA " + check).Scan(&result); err != nil || result != "ok" {
			t.Fatalf("fixture must isolate foreign keys: %s=%q err=%v", check, result, err)
		}
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	var failures []string
	for rows.Next() {
		var table string
		var rowID, parent, key any
		if err := rows.Scan(&table, &rowID, &parent, &key); err != nil {
			t.Fatal(err)
		}
		failures = append(failures, fmt.Sprintf("%s:%v:%v:%v", table, rowID, parent, key))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(failures) != 2 {
		t.Fatalf("want both missing membership parents, got %v", failures)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return failures
}

func assertSnapshotIntegrityAttribution(t *testing.T, diagnostic string, failures []string) {
	t.Helper()
	for _, field := range append([]string{"verify_backup", "invariant_violation", "foreign_key_check"}, failures...) {
		if !strings.Contains(diagnostic, field) {
			t.Errorf("snapshot refusal %q omits %q", diagnostic, field)
		}
	}
}

func assertNoQualifiedSnapshot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused snapshot left files: %v", entries)
	}
}

func TestRestoreRefusesCurrentStateCorruptionWithAttribution(t *testing.T) {
	freshMigratedCLIDatabase(t)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	runCLIJSON(t, []string{"backup"}, map[string]any{"destination": snapshot})
	manifest, err := store.VerifyBackup(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	failures := seedSnapshotForeignKeyCorruption(t, snapshot, "write_guard")
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// Match the recorded checksum to isolate current-state validation from
	// manifest tamper detection. The verifier must still reject these rows.
	manifest.DBChecksum = fmt.Sprintf("%x", sha256.Sum256(raw))
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot+".manifest.json", manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	input, err := json.Marshal(map[string]string{"source": snapshot, "destination": destination})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, bytes.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("corrupt current state must refuse restore: exit=%d stderr=%s", code, &errOut)
	}
	assertSnapshotIntegrityAttribution(t, errOut.String(), failures)
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("refused restore left destination: %v", err)
	}
	if files, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".concord-restore-*")); err != nil || len(files) != 0 {
		t.Fatalf("refused restore left staging: %v err=%v", files, err)
	}
	for path, want := range map[string][]byte{snapshot: raw, snapshot + ".manifest.json": manifestRaw} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(want, got) {
			t.Fatalf("refusal changed %s: %v", path, err)
		}
	}
}
