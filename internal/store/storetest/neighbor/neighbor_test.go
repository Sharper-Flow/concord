package neighbor

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteNeighborProcess(t *testing.T) { Child(t) }

// The fixture itself must prove it pins WAL frames, takes the write lock,
// and releases that lock at cleanup. These checks use SQLite, not elapsed time.
func TestNeighborProcessLocks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "neighbor.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=50; CREATE TABLE domain_events(seq INTEGER); INSERT INTO domain_events VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	t.Run("reader", func(t *testing.T) {
		Start(t, dbPath, Reader)
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(2)`); err != nil {
			t.Fatalf("reader blocked writer: %v", err)
		}
		var busy, log, checkpointed int
		if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
			t.Fatal(err)
		}
		if busy != 1 || log <= checkpointed {
			t.Fatalf("reader did not pin WAL: busy=%d log=%d checkpointed=%d", busy, log, checkpointed)
		}
	})
	t.Run("writer", func(t *testing.T) {
		Start(t, dbPath, Writer)
		if _, err := db.Exec(`BEGIN IMMEDIATE`); err == nil {
			_, _ = db.Exec(`ROLLBACK`)
			t.Fatal("writer neighbor did not hold the lock")
		} else if !strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Fatalf("writer lock: %v", err)
		}
	})
	if _, err := db.Exec(`BEGIN IMMEDIATE; ROLLBACK`); err != nil {
		t.Fatalf("neighbor cleanup left a lock: %v", err)
	}
}
