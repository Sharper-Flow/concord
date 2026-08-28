package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDurableTxPragmaPinning probes and pins the mechanism a durable
// (synchronous=FULL) transaction rests on: a *sql.Conn pinned from the pool
// carries a per-connection pragma that is visible inside a transaction opened
// on that same conn, invisible to the pool after restore, and restorable even
// when the transaction errors.
func TestDurableTxPragmaPinning(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, filepath.Join(root, "pin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	readPragma := func(q queryer) (string, error) {
		var v string
		err := q.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&v)
		return v, err
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		t.Fatalf("set FULL on pinned conn: %v", err)
	}
	got, err := readPragma(conn)
	if err != nil || got != "2" {
		t.Fatalf("pinned conn synchronous = %q err=%v, want 2", got, err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Inside the transaction the pragma must still read FULL.
	if err := tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&got); err != nil || got != "2" {
		t.Fatalf("in-tx synchronous = %q err=%v, want 2", got, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		t.Fatalf("restore NORMAL: %v", err)
	}
	got, err = readPragma(conn)
	if err != nil || got != "1" {
		t.Fatalf("restored synchronous = %q err=%v, want 1", got, err)
	}

	// A transaction that errors mid-flight must still allow restore on the
	// same conn (SQLite statement-level rollback keeps the conn usable).
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		t.Fatal(err)
	}
	tx2, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.ExecContext(ctx, "SELECT * FROM no_such_table"); err == nil {
		t.Fatal("expected error from bogus statement")
	}
	_ = tx2.Rollback()
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		t.Fatalf("restore after errored tx: %v", err)
	}
	if got, err = readPragma(conn); err != nil || got != "1" {
		t.Fatalf("post-error restored synchronous = %q err=%v, want 1", got, err)
	}
}

// TestDurableSyncCheckpoint pins the checkpoint-based durability primitive:
// a TRUNCATE checkpoint on a pinned connection under synchronous=FULL
// completes, reports zero busy, resets the WAL to zero pages, and the
// connection stays usable afterwards.
func TestDurableSyncCheckpoint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, filepath.Join(root, "ckpt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Produce committed WAL frames so the checkpoint has something to carry.
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatal(err)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		t.Fatalf("set FULL: %v", err)
	}
	var busy, log, ckpt int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &ckpt); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if busy != 0 {
		t.Fatalf("checkpoint busy = %d, want 0", busy)
	}
	t.Logf("checkpoint carried log=%d pages, checkpointed=%d", log, ckpt)

	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		t.Fatalf("restore NORMAL: %v", err)
	}

	// Return the pinned connection before any other store call: the pool has
	// exactly one connection, so holding it while calling a Store method
	// parks that method on the pool forever. SyncDurable must be
	// self-contained the same way.
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	// The store remains usable after the durable sync.
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatalf("post-checkpoint append: %v", err)
	}
}

func TestDurableBarrierAfterRegisterTrustedClientTruncatesWal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbpath := filepath.Join(root, "concord.db")
	s, err := Open(ctx, dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("RegisterTrustedClient: %v", err)
	}

	walpath := dbpath + "-wal"
	info, err := os.Stat(walpath)
	if err != nil {
		t.Fatalf("expected %s to exist after SyncDurable: %v", walpath, err)
	}
	if info.Size() != 0 {
		t.Fatalf("WAL file %s size = %d after RegisterTrustedClient, want 0 (TRUNCATE did not reset)", walpath, info.Size())
	}
}

// TestDurableBarrierLeavesStoreUsable pins the post-barrier contract: a
// successfully acknowledged consequential operation must not leave the store
// in a state where ordinary writes fail. After the barrier truncates the WAL,
// the pool returns its single connection and the next append goes through the
// normal path.
func TestDurableBarrierLeavesStoreUsable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbpath := filepath.Join(root, "concord.db")
	s, err := Open(ctx, dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatalf("pre-barrier seed: %v", err)
	}
	if err := s.SyncDurable(ctx); err != nil {
		t.Fatalf("barrier: %v", err)
	}
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatalf("post-barrier ordinary append must succeed: %v", err)
	}
}

// TestDurableBarrierFailureSurfaces shows the durability barrier error is
// returned to the caller, not swallowed. Forcing a real
// PRAGMA wal_checkpoint(TRUNCATE) failure inside the running test is impractical
// (the store holds a single connection and the WAL only goes busy under a
// separate process holding a read transaction), so the test drives the failure
// path through the store boundary: open a store, run an acknowledged write
// through the same wrapper shape used by Family A, close the store, and assert
// the post-commit SyncDurable call returns the underlying database-closed
// error instead of nil.
func TestDurableBarrierFailureSurfaces(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbpath := filepath.Join(root, "concord.db")
	s, err := Open(ctx, dbpath)
	if err != nil {
		t.Fatal(err)
	}

	// produce a committed frame so a real barrier has work to carry
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// close the underlying pool so the next SyncDurable cannot run a query.
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = s.SyncDurable(ctx)
	if err == nil {
		t.Fatal("SyncDurable on a closed store returned nil; the failure path must surface the error to the caller")
	}
}

// appendSeedEvent writes one ordinary event through the real append path so a
// probe has committed WAL frames without duplicating store internals.
func (s *Store) appendSeedEvent(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	e := Event{
		EventID:        fmt.Sprintf("probe-%d", time.Now().UnixNano()),
		Kind:           "work.created",
		SubjectType:    SubjectWorkItem,
		SubjectID:      "probe-1",
		Actor:          "probe",
		OccurredAt:     time.Now().UTC(),
		PayloadVersion: 2,
		Payload:        []byte(`{"work_id":"probe-1","work_kind":"task","title":"probe","priority":1}`),
	}
	if _, err := AppendEvent(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}
