package sqlitedb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func guardDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(DSN(filepath.Join(t.TempDir(), "guard.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE write_guard(active INTEGER PRIMARY KEY CHECK(active=1))`); err != nil {
		t.Fatal(err)
	}
	return db
}

func guardRows(t *testing.T, q Queryer) int {
	t.Helper()
	var count int
	if err := q.QueryRowContext(t.Context(), `SELECT count(*) FROM write_guard`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWriteGuardNestingAndRollback(t *testing.T) {
	db := guardDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	g := NewWriteGuard(tx)
	if g.Active() || g.Close(ctx) != nil {
		t.Fatal("unused guard is active or cannot close")
	}
	for range 2 {
		if err := g.Enter(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !g.Active() || guardRows(t, tx) != 1 {
		t.Fatal("nested entry did not retain exactly one guard")
	}
	if err := g.Close(ctx); err != nil || !g.Active() || guardRows(t, tx) != 1 {
		t.Fatalf("nested close dropped its caller's guard: %v", err)
	}
	if err := g.Close(ctx); err != nil || g.Active() || guardRows(t, tx) != 0 {
		t.Fatalf("outer close retained its guard: %v", err)
	}
	if err := g.Enter(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if guardRows(t, db) != 0 {
		t.Fatal("rollback left a committed guard")
	}
}

func TestWriteGuardFlatAndScopedModes(t *testing.T) {
	db := guardDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for range 2 {
		if err := EnterWrite(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	g := NewWriteGuard(tx)
	var failure *Error
	if err := g.Enter(ctx); !errors.As(err, &failure) || failure.Kind != InvariantViolation || failure.Op != "fold" || failure.RetrySafe || failure.EffectPossible || failure.Err != nil || g.Active() {
		t.Fatalf("scoped collision must refuse the flat writer's row: %v", err)
	}
	if guardRows(t, tx) != 1 {
		t.Fatal("collision changed the existing guard")
	}
	if err := LeaveWrite(ctx, tx); err != nil || guardRows(t, tx) != 0 {
		t.Fatalf("flat leave did not remove the shared row: %v", err)
	}
	if err := LeaveWrite(ctx, tx); err != nil {
		t.Fatalf("empty flat leave: %v", err)
	}
}

func TestWriteGuardFailureFields(t *testing.T) {
	db := guardDatabase(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := NewWriteGuard(tx)
	if err := g.Enter(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	unopened := NewWriteGuard(tx)
	for _, tc := range []struct {
		name, detail string
		call         func() error
	}{
		{"scoped enter", "cannot enable current-row write guard", func() error { return unopened.Enter(ctx) }},
		{"scoped close", "cannot disable current-row write guard", func() error { return g.Close(ctx) }},
		{"flat enter", "cannot enable current-row write guard", func() error { return EnterWrite(ctx, tx) }},
		{"flat leave", "cannot disable current-row write guard", func() error { return LeaveWrite(ctx, tx) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != Unavailable || failure.Op != "fold" || failure.Detail != tc.detail || !failure.RetrySafe || failure.EffectPossible || failure.RecoveryAction != "retry once the database is writable" || !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("changed failure fields or nested cause: %v", err)
			}
		})
	}
	if g.Active() || unopened.Active() {
		t.Fatal("failed enter or outer close changed depth mutation timing")
	}
}

func TestWriteGuardOpenRefusal(t *testing.T) {
	db := guardDatabase(t)
	ctx := t.Context()
	if err := RefuseStrandedWriteGuard(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	var failure *Error
	err := RefuseStrandedWriteGuard(ctx, db)
	if !errors.As(err, &failure) || failure.Kind != InvariantViolation || failure.Op != "open" || failure.RetrySafe || failure.EffectPossible || failure.Err != nil || failure.Detail != "1 committed write_guard row(s) guard the current rows, and no write scope owns them" || failure.RecoveryAction != "stop concord processes and run concord restore from a verified backup into a new path, then swap the restored database into place" {
		t.Fatalf("changed stranded guard refusal: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE write_guard`); err != nil {
		t.Fatal(err)
	}
	err = RefuseStrandedWriteGuard(ctx, db)
	if !errors.As(err, &failure) || failure.Kind != Unavailable || failure.Op != "open" || failure.Detail != "cannot inspect the write guard" || !failure.RetrySafe || failure.EffectPossible || failure.RecoveryAction != "confirm the database is readable" || failure.Err == nil {
		t.Fatalf("changed guard inspection failure: %v", err)
	}
}
