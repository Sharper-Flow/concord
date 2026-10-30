package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// seedCLIReceipt records synthetic current fixture identity at insert time.
// Callers own the matching current row and hold the write guard in this tx.
func seedCLIReceipt(t *testing.T, tx *sql.Tx, event store.Event) int64 {
	t.Helper()
	digest := cliPayloadDigest(t, event.Payload)
	var seq int64
	if err := tx.QueryRow(`UPDATE store_sequences SET last=last+1 WHERE name='effect' RETURNING last`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, seq, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.PayloadVersion, digest); err != nil {
		t.Fatal(err)
	}
	return seq
}

func cliPayloadDigest(t *testing.T, raw []byte) string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func hideGitDir(t *testing.T, path string) func() {
	t.Helper()
	gitDir := filepath.Join(path, ".git")
	hidden := filepath.Join(t.TempDir(), "git")
	if err := os.Rename(gitDir, hidden); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Rename(hidden, gitDir); err != nil {
			t.Error(err)
		}
	}
}
