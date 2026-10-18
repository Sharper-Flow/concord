package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenURIPathUsesImmediateTransactions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store?query#fragment%32.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	other, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.SetMaxOpenConns(1)
	if _, err := other.ExecContext(ctx, `PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	second, err := other.BeginTx(ctx, nil)
	if err == nil {
		_ = second.Rollback()
		t.Fatal("BEGIN did not acquire the immediate write lock")
	}
	if !migrationLockContended(err) {
		t.Fatalf("second BEGIN error = %v, want write-lock contention", err)
	}
}
