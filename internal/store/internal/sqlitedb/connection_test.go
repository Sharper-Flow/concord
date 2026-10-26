package sqlitedb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectionDSNBytes(t *testing.T) {
	path := "/synthetic/store%?# with space/日本.db"
	uri := "file:/synthetic/store%25%3F%23 with space/日本.db"
	for _, pair := range [][2]string{
		{FileURI(path), uri},
		{DSN(path), uri + "?_txlock=immediate&_pragma=busy_timeout%285000%29&_pragma=journal_mode%28WAL%29&_pragma=synchronous%28NORMAL%29&_pragma=foreign_keys%28ON%29"},
		{ReadOnlyDataSource(path), uri + "?mode=ro&_pragma=foreign_keys(1)"},
		{ReadOnlyReadinessDSN(path), uri + "?mode=ro&_pragma=busy_timeout(5000)"},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("DSN bytes: got %q want %q", pair[0], pair[1])
		}
	}
}

func TestConnectionOpenIsLazyAndOneConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "store.db")
	db, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if stats := db.Stats(); stats.MaxOpenConnections != 1 || stats.OpenConnections != 0 {
		t.Fatalf("opener reached the driver or changed pool size: %+v", stats)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opener touched file: %v", err)
	}
	if err := db.PingContext(t.Context()); err == nil {
		t.Fatal("invalid parent unexpectedly opened")
	}
}

func TestConnectionPragmaVerification(t *testing.T) {
	db, err := Open(DSN(filepath.Join(t.TempDir(), "settings.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := VerifyPragmas(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	err = VerifyPragmas(t.Context(), db)
	var failure *Error
	if !errors.As(err, &failure) || failure.Op != "open" || failure.RetrySafe || failure.Detail != `PRAGMA foreign_keys is "0", want "1"` {
		t.Fatalf("pragma mismatch: %v", err)
	}
}
