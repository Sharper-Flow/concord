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
	seedCLIMigrationPrefix(t, path, 142)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
}
