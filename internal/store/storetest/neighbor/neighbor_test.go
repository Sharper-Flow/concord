package neighbor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestSQLiteNeighborProcess(t *testing.T) { Child(t, store.SQLiteFileURI) }

// The fixture itself must prove it pins WAL frames, takes the write lock,
// and releases that lock at cleanup. These checks use SQLite, not elapsed time.
func TestNeighborProcessLocks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "neighbor#?%23.db")
	s, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`PRAGMA busy_timeout=50`); err != nil {
		t.Fatal(err)
	}
	appendReceipt := func(t *testing.T, id string) {
		t.Helper()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = store.AppendEvent(t.Context(), tx, store.Event{
			EventID: id, Kind: "product.created", SubjectType: store.SubjectProduct,
			SubjectID: "neighbor-product", Actor: "principal:neighbor-fixture", OccurredAt: time.Unix(1, 0).UTC(),
			PayloadVersion: store.CurrentEventPayloadVersion("product.created"),
			Payload:        []byte(`{"display_name":"Neighbor Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	appendReceipt(t, "neighbor-first")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Run("reader", func(t *testing.T) {
		Start(t, dbPath, Reader)
		appendReceipt(t, "neighbor-second")
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
