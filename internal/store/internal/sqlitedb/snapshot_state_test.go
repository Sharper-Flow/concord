package sqlitedb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"testing"
)

func TestSnapshotCurrentStateDigestStableFraming(t *testing.T) {
	db, _ := snapshotTestDB(t)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE "current ""state"(value COLLATE NOCASE);
CREATE TABLE store_revision(id INTEGER PRIMARY KEY, revision INTEGER);
INSERT INTO store_revision VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	values := []any{nil, "a", "A", "a\x00b", []byte{}, []byte{0, 255}, int64(1), float64(1), math.Inf(1), "duplicate", "duplicate"}
	replace := func(reverse bool, omit int) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), `DELETE FROM "current ""state"`); err != nil {
			t.Fatal(err)
		}
		for i := range values {
			if reverse {
				i = len(values) - 1 - i
			}
			if i != omit {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO "current ""state" VALUES(?)`, values[i]); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	digest := func() string {
		t.Helper()
		got, err := CurrentStateDigest(t.Context(), db)
		if err != nil {
			t.Fatal(err)
		}
		if decoded, err := hex.DecodeString(got); err != nil || len(decoded) != sha256.Size {
			t.Fatalf("digest = %q, %v", got, err)
		}
		return got
	}
	replace(false, -1)
	want := digest()
	replace(true, -1)
	if got := digest(); got != want {
		t.Fatalf("row order changed digest: %s != %s", got, want)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE store_revision SET revision=revision+1`); err != nil {
		t.Fatal(err)
	}
	if got := digest(); got != want {
		t.Fatalf("publication counter changed digest: %s != %s", got, want)
	}
	for i := range values {
		replace(false, i)
		if digest() == want {
			t.Fatalf("missing %T:%v did not change digest", values[i], values[i])
		}
	}
	replace(false, -1)
	if _, err := db.ExecContext(t.Context(), `CREATE INDEX state_values ON "current ""state"(value)`); err != nil {
		t.Fatal(err)
	}
	if digest() == want {
		t.Fatal("schema change did not change digest")
	}
	// Assert exact framing separately from the database driver's value decoding.
	h := sha256.New()
	for _, value := range []any{nil, "a\x00b", []byte{0, 255}, int64(1), float64(1)} {
		writeBackupHashValue(h, value)
	}
	framed := []byte("11:<nil>:<nil>10:string:a\x00b15:[]uint8:[0 255]7:int64:19:float64:1")
	wantHash := sha256.Sum256(framed)
	if !bytes.Equal(h.Sum(nil), wantHash[:]) {
		t.Fatal("storage-type or length framing changed")
	}
}

func TestSnapshotStateFailureFieldsAndCauses(t *testing.T) {
	db, _ := snapshotTestDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := CurrentStateDigest(ctx, db)
	assertSnapshotFailure(t, err, "verify_backup", "cannot read snapshot current state", false, "supply a readable Concord SQLite snapshot", context.Canceled)
	integrity, quick, foreign, err := VerifySQLiteTriple(ctx, db)
	assertSnapshotFailure(t, err, "verify_backup", "cannot run PRAGMA integrity_check", false, "supply a readable SQLite snapshot", context.Canceled)
	if integrity != "" || quick != "" || foreign != nil {
		t.Fatalf("failed verification returned results: %q/%q/%v", integrity, quick, foreign)
	}
	cause := errors.New("synthetic state read failure")
	assertSnapshotFailure(t, backupStateReadFailure(cause), "verify_backup", "cannot read snapshot current state", false, "supply a readable Concord SQLite snapshot", cause)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = CurrentStateDigest(t.Context(), db)
	f, ok := err.(*Error)
	if !ok || f.Err == nil {
		t.Fatalf("closed-pool error lost its cause: %v", err)
	}
	assertSnapshotFailure(t, err, "verify_backup", "cannot read snapshot current state", false, "supply a readable Concord SQLite snapshot", f.Err)
}

func TestSnapshotSQLiteVerificationForeignKeyRows(t *testing.T) {
	db, _ := snapshotTestDB(t)
	if _, err := db.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF;
CREATE TABLE parent(id INTEGER PRIMARY KEY);
CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id REFERENCES parent(id));
INSERT INTO child VALUES(7,99)`); err != nil {
		t.Fatal(err)
	}
	integrity, quick, foreign, err := VerifySQLiteTriple(t.Context(), db)
	if err != nil || integrity != "ok" || quick != "ok" || len(foreign) != 1 || foreign[0] != "child:7:parent:0" {
		t.Fatalf("verification = %q/%q/%v, %v", integrity, quick, foreign, err)
	}
	// The mechanism reports rows; the caller, not this probe, rejects them.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO parent VALUES(99)`); err != nil {
		t.Fatal(err)
	}
	_, _, foreign, err = VerifySQLiteTriple(t.Context(), db)
	if err != nil || foreign == nil || len(foreign) != 0 {
		t.Fatalf("repaired verification = %v, %v", foreign, err)
	}
}
