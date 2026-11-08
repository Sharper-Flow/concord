package sqlitedb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// cancelAfterFirstBackupStep interrupts the backup loop on its second check.
type cancelAfterFirstBackupStep struct {
	context.Context
	checks int
	done   chan struct{}
}

func (c *cancelAfterFirstBackupStep) Done() <-chan struct{} {
	c.checks++
	if c.checks == 2 {
		close(c.done)
	}
	return c.done
}

func (c *cancelAfterFirstBackupStep) Err() error {
	if c.checks >= 2 {
		return context.Canceled
	}
	return nil
}

func exclusiveTestTarget(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "target%?#.db")
	db, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE prior(value); INSERT INTO prior(rowid,value) VALUES(3,zeroblob(1500000)),(17,'keep')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenExclusiveRefusesOpenConnectionAndExcludesOthers(t *testing.T) {
	ctx := t.Context()
	path := exclusiveTestTarget(t)
	other, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := other.QueryRowContext(ctx, `SELECT value FROM prior WHERE rowid=17`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if exclusive, err := OpenExclusive(ctx, path); err == nil {
		_ = exclusive.Close()
		t.Fatal("exclusive open succeeded while another connection held the store")
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	exclusive, err := OpenExclusive(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, dsn := range []string{ReadOnlyDataSource(path) + "&_pragma=busy_timeout(10)", FileURI(path) + "?_pragma=busy_timeout(10)"} {
		blocked, err := Open(dsn)
		if err != nil {
			t.Fatal(err)
		}
		readErr := blocked.QueryRowContext(ctx, `SELECT value FROM prior WHERE rowid=17`).Scan(&value)
		_, writeErr := blocked.ExecContext(ctx, `INSERT INTO prior(value) VALUES('late')`)
		_ = blocked.Close()
		if readErr == nil || writeErr == nil {
			t.Fatalf("%s reached the locked store: read %v, write %v", dsn, readErr, writeErr)
		}
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	var rows int
	if err := after.QueryRowContext(ctx, `SELECT count(*) FROM prior`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("rows after release = %d: %v", rows, err)
	}
}

func TestExclusiveRestoreInterruptedAfterFirstStepKeepsPriorDatabase(t *testing.T) {
	ctx := t.Context()
	source, sourcePath := snapshotTestDB(t)
	if _, err := source.ExecContext(ctx, `INSERT INTO sample VALUES(zeroblob(700000))`); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := source.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil || pages <= int(BackupStepPages) {
		t.Fatalf("source pages = %d, error = %v", pages, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	path := exclusiveTestTarget(t)
	target, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	want, err := CurrentStateDigest(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	var priorPages int
	if err := target.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&priorPages); err != nil || priorPages <= pages {
		t.Fatalf("destination pages = %d, source = %d, error = %v", priorPages, pages, err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	exclusive, err := OpenExclusive(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cancel := &cancelAfterFirstBackupStep{Context: ctx, done: make(chan struct{})}
	err = exclusive.RestoreFrom(cancel, sourcePath)
	if !errors.Is(err, context.Canceled) || cancel.checks != 2 {
		t.Fatalf("interruption = %v, checks = %d", err, cancel.checks)
	}
	got, err := CurrentStateDigest(ctx, exclusive.Conn)
	if err != nil || got != want {
		t.Fatalf("interrupted destination state seen by the lock holder = %q, want %q: %v", got, want, err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	target, err = Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	got, err = CurrentStateDigest(ctx, target)
	if err != nil || got != want {
		t.Fatalf("interrupted destination state = %q, want %q: %v", got, want, err)
	}
	var value string
	if err := target.QueryRowContext(ctx, `SELECT value FROM prior WHERE rowid=17`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("interrupted destination rowid 17 = %q: %v", value, err)
	}
	after, err := os.Stat(path)
	if err != nil || after.Size() != before.Size() {
		t.Fatalf("interrupted destination size = %v, want %d: %v", after, before.Size(), err)
	}
	var afterPages int
	if err := target.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&afterPages); err != nil || afterPages != priorPages {
		t.Fatalf("interrupted destination pages = %d, want %d: %v", afterPages, priorPages, err)
	}
	integrity, quick, foreign, err := VerifySQLiteTriple(ctx, target)
	if err != nil || integrity != "ok" || quick != "ok" || len(foreign) != 0 {
		t.Fatalf("verification = %q / %q / %v: %v", integrity, quick, foreign, err)
	}
}

func TestExclusiveRestoreReplacesLockedDatabase(t *testing.T) {
	ctx := t.Context()
	source, sourcePath := snapshotTestDB(t)
	want, err := CurrentStateDigest(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	path := exclusiveTestTarget(t)
	exclusive, err := OpenExclusive(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := exclusive.RestoreFrom(ctx, sourcePath); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentStateDigest(ctx, exclusive.Conn)
	if err != nil || got != want {
		t.Fatalf("restored state = %q, want %q: %v", got, want, err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
}
