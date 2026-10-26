package sqlitedb

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestWriteAdmissionAndRevisionOrder(t *testing.T) {
	ctx := t.Context()
	// Invalid admission is checked even before a handle is touched.
	_, err := BeginWrite(ctx, nil, nil, 0)
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != InvalidOperation {
		t.Fatalf("invalid admission: %v", err)
	}
	db, err := Open(DSN(filepath.Join(t.TempDir(), "admission.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = BeginWrite(ctx, db, nil, StateWriteAdmission)
	if !errors.As(err, &failure) || failure.Op != "store_revision" {
		t.Fatalf("state admission skipped revision: %v", err)
	}
	tx, err := BeginWrite(ctx, db, nil, SchemaWriteAdmission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE store_revision(id INTEGER PRIMARY KEY, revision INTEGER); INSERT INTO store_revision VALUES(1,0); CREATE TABLE durability_commits(id INTEGER PRIMARY KEY, bit INTEGER);`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, durable := range []bool{false, true} {
		var counter atomic.Uint64
		var count *atomic.Uint64
		if durable {
			count = &counter
		}
		write, err := BeginWrite(ctx, db, count, StateWriteAdmission)
		if err != nil {
			t.Fatal(err)
		}
		var mode int
		if err := write.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		wantMode := 1
		if durable {
			wantMode = 2
		}
		if mode != wantMode {
			t.Fatalf("synchronous=%d want %d", mode, wantMode)
		}
		if err := write.Rollback(); err != nil {
			t.Fatal(err)
		}
		if revision, err := Revision(ctx, db); err != nil || revision != 0 {
			t.Fatalf("rollback revision=%d err=%v", revision, err)
		}
		write, err = BeginWrite(ctx, db, count, StateWriteAdmission)
		if err != nil {
			t.Fatal(err)
		}
		if err := write.Commit(); err != nil {
			t.Fatal(err)
		}
		if revision, err := Revision(ctx, db); err != nil || revision != 1 {
			t.Fatalf("commit revision=%d err=%v", revision, err)
		}
		if durable && counter.Load() != 1 {
			t.Fatalf("durable commits=%d", counter.Load())
		}
		if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&mode); err != nil || mode != 1 {
			t.Fatalf("restored mode=%d err=%v", mode, err)
		}
		if _, err := db.ExecContext(ctx, "UPDATE store_revision SET revision=0"); err != nil {
			t.Fatal(err)
		}
	}
}
