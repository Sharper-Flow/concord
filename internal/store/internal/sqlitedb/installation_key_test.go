package sqlitedb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func installationKeyDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.db")
	db, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE store_revision(id INTEGER PRIMARY KEY, revision INTEGER);
		INSERT INTO store_revision VALUES(1,0);
		CREATE TABLE agent_installation_keys(key_name TEXT PRIMARY KEY, key_bytes BLOB NOT NULL CHECK(length(key_bytes)=32), created_at TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func TestInstallationKeyLifecycle(t *testing.T) {
	t.Parallel()
	db, path := installationKeyDatabase(t)
	ctx := t.Context()
	when := time.Date(2026, 10, 1, 12, 0, 0, 123, time.FixedZone("fixture", 3600))
	if err := EnsureInstallationKey(ctx, db, when); err != nil {
		t.Fatal(err)
	}
	key, err := InstallationKey(ctx, db)
	if err != nil || len(key) != 32 {
		t.Fatalf("created key length=%d err=%v", len(key), err)
	}
	var createdAt string
	if err := db.QueryRow(`SELECT created_at FROM agent_installation_keys`).Scan(&createdAt); err != nil || createdAt != when.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("created_at=%q err=%v", createdAt, err)
	}
	if revision, err := Revision(ctx, db); err != nil || revision != 1 {
		t.Fatalf("creation revision=%d err=%v", revision, err)
	}
	key[0] ^= 0xff
	stored, err := InstallationKey(ctx, db)
	if err != nil || bytes.Equal(key, stored) {
		t.Fatalf("returned bytes mutated storage: %v", err)
	}
	writer, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := BeginWrite(ctx, writer, nil, StateWriteAdmission)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := EnsureInstallationKey(readCtx, db, when.Add(time.Hour)); err != nil {
		t.Fatalf("existing key competed with a foreign writer: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := InstallationKey(ctx, db)
	if err != nil || !bytes.Equal(after, stored) {
		t.Fatalf("reopen replaced key: %v", err)
	}
	if revision, err := Revision(ctx, db); err != nil || revision != 1 {
		t.Fatalf("reopen revision=%d err=%v", revision, err)
	}
}

func TestInstallationKeyConcurrentFirstOpen(t *testing.T) {
	t.Parallel()
	first, path := installationKeyDatabase(t)
	second, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for _, db := range []*sql.DB{first, second} {
		workers.Go(func() {
			<-start
			errors <- EnsureInstallationKey(t.Context(), db, time.Now())
		})
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	one, err := InstallationKey(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := InstallationKey(t.Context(), second)
	if err != nil || len(one) != 32 || !bytes.Equal(one, two) {
		t.Fatalf("first opens did not converge: %v", err)
	}
	if revision, err := Revision(t.Context(), first); err != nil || revision != 1 {
		t.Fatalf("first opens revision=%d err=%v", revision, err)
	}
}

func TestInstallationKeyReadFailures(t *testing.T) {
	t.Parallel()
	db, _ := installationKeyDatabase(t)
	key, err := InstallationKey(t.Context(), db)
	var failure *Error
	if key != nil || !errors.Is(err, sql.ErrNoRows) || !errors.As(err, &failure) || failure.Kind != Unavailable || failure.Op != "cursor_key" {
		t.Fatalf("missing key: key length=%d err=%v", len(key), err)
	}
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON; INSERT INTO agent_installation_keys VALUES('cursor',zeroblob(31),'fixture')`); err != nil {
		t.Fatal(err)
	}
	key, err = InstallationKey(t.Context(), db)
	if key != nil || !errors.As(err, &failure) || failure.Kind != InvariantViolation || failure.RetrySafe || failure.Err != nil {
		t.Fatalf("invalid length: key length=%d err=%v", len(key), err)
	}
}
