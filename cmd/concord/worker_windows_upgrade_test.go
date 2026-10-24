package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerWindowMigrationTailReversal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "windows.db")
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := rewindMigrationTail(db, 143, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
}
