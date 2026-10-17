package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenKeepsSQLiteURIPathLiteral(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"fragment", []string{"store.db#one", "store.db#two"}},
		{"query", []string{"store.db?one", "store.db?two"}},
		{"percent_escape", []string{"store%32.db", "store2.db"}},
		{"literal_percent", []string{"store%.db", "store%invalid.db"}},
		{"combined", []string{"store?query%32#one.db", "store?query%32#two.db"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			// Pre-existing files let Open reach the identity check even if
			// SQLite redirected its writes and finishOpen chmods this file.
			for _, name := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range tc.files {
				path := filepath.Join(dir, name)
				s, err := Open(ctx, path)
				if err != nil {
					t.Fatalf("Open(%q): %v", name, err)
				}
				t.Cleanup(func() {
					if err := s.Close(); err != nil {
						t.Error(err)
					}
				})
				var seq int
				var schema, actual string
				if err := s.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&seq, &schema, &actual); err != nil {
					t.Fatal(err)
				}
				if actual != path {
					t.Errorf("Open(%q) opened %q, want %q", name, actual, path)
				}
				if _, err := s.db.ExecContext(ctx, `CREATE TABLE path_identity (id INTEGER PRIMARY KEY)`); err != nil {
					t.Fatalf("Open(%q) aliases an earlier store: %v", name, err)
				}
			}
		})
	}
}

func TestDataSourceNameKeepsPlainPathBytes(t *testing.T) {
	const query = "?_txlock=immediate&_pragma=busy_timeout%285000%29&_pragma=journal_mode%28WAL%29&_pragma=synchronous%28NORMAL%29&_pragma=foreign_keys%28ON%29"
	for _, path := range []string{
		"store.db",
		filepath.Join(t.TempDir(), "store.db"),
		"relative dir/store + café.db",
		"/stores/@owner:1&name=value;other.db",
	} {
		if got, want := dataSourceName(path), "file:"+path+query; got != want {
			t.Errorf("dataSourceName(%q) = %q, want %q", path, got, want)
		}
	}
}

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

func TestBackupKeepsSQLiteURIPathLiteral(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot?query%32#fragment.db")
	if _, err := Backup(ctx, s, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot")); !os.IsNotExist(err) {
		t.Fatalf("backup created a query-truncated neighbor: %v", err)
	}
	restoredPath := filepath.Join(dir, "restored?query%32#fragment.db")
	if _, err := RestoreBackup(ctx, destination, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
}
