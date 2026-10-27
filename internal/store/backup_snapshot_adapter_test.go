package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestSnapshotStoreAdapters(t *testing.T) {
	db, err := sqlitedb.Open(sqlitedb.DSN(filepath.Join(t.TempDir(), "source.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE sample(value); INSERT INTO sample VALUES('state')`); err != nil {
		t.Fatal(err)
	}
	want, err := sqlitedb.CurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := backupCurrentStateDigest(t.Context(), db); err != nil || got != want {
		t.Fatalf("digest adapter = %q, %v; want %q", got, err, want)
	}
	integrity, quick, foreign, err := verifySQLiteTriple(t.Context(), db)
	if err != nil || integrity != "ok" || quick != "ok" || foreign == nil || len(foreign) != 0 {
		t.Fatalf("verification adapter = %q/%q/%v, %v", integrity, quick, foreign, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, digestErr := backupCurrentStateDigest(ctx, db)
	_, _, _, verifyErr := verifySQLiteTriple(ctx, db)
	restoreErr := runOnlineRestore(ctx, filepath.Join(t.TempDir(), "source.db"), filepath.Join(t.TempDir(), "stage.db"))
	for _, tc := range []struct {
		err                  error
		op, detail, recovery string
		retry                bool
	}{
		{digestErr, "verify_backup", "cannot read snapshot current state", "supply a readable Concord SQLite snapshot", false},
		{verifyErr, "verify_backup", "cannot run PRAGMA integrity_check", "supply a readable SQLite snapshot", false},
		{restoreErr, "restore_backup", "cannot acquire restore source connection", "retry the restore", true},
	} {
		f, ok := tc.err.(*Failure)
		var mechanism *sqlitedb.Error
		if !ok || f.Kind != KindUnavailable || f.Op != tc.op || f.Detail != tc.detail || f.RecoveryAction != tc.recovery || f.RetrySafe != tc.retry || f.EffectPossible || !errors.Is(tc.err, context.Canceled) || errors.As(tc.err, &mechanism) {
			t.Fatalf("store adapter error = %#v", tc.err)
		}
	}
	if err := contextErr(nil); err == nil || err.Error() != "nil context" {
		t.Fatalf("nil context adapter = %v", err)
	}
}

func TestSnapshotStoreCancellationCleansDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE sample(value); INSERT INTO sample VALUES('state')`); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, path: path}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications := 0
	ctx = context.WithValue(ctx, backupStartedContextKey{}, func() { notifications++; cancel() })
	_, err = BackupWithOptions(ctx, s, destination, BackupOptions{SnapshotID: "synthetic-snapshot"})
	f, ok := err.(*Failure)
	if !ok || f.Kind != KindUnavailable || f.Op != "backup" || f.Detail != "online backup was interrupted" || !f.RetrySafe || f.RecoveryAction != "discard the partial snapshot and retry" || f.EffectPossible || !errors.Is(err, context.Canceled) || notifications != 1 {
		t.Fatalf("cancelled backup = %#v, notifications = %d", err, notifications)
	}
	for _, suffix := range []string{"", "-wal", "-shm", ".manifest.json"} {
		if _, err := os.Stat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial destination %q: %v", suffix, err)
		}
	}
	readCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	var count int
	if err := db.QueryRowContext(readCtx, `SELECT count(*) FROM sample`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source pool after cancellation = %d, %v", count, err)
	}
}

func TestSnapshotStoreAcquisitionFailurePreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	s := &Store{db: db, path: path, Clock: func() time.Time {
		// A destination appearing after validation is not ours until acquisition.
		if err := os.WriteFile(destination, []byte("retained"), 0o600); err != nil {
			t.Fatal(err)
		}
		return time.Unix(0, 0)
	}}
	_, err = BackupWithOptions(t.Context(), s, destination, BackupOptions{SnapshotID: "synthetic-snapshot"})
	f, ok := err.(*Failure)
	if !ok || f.Kind != KindUnavailable || f.Detail != "cannot acquire a SQLite connection" || !f.RetrySafe {
		t.Fatalf("connection acquisition = %#v", err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "retained" {
		t.Fatalf("acquisition failure cleaned an unowned destination: %q, %v", got, err)
	}
}
