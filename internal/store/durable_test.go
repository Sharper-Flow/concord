package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// readSynchronous reads the safety level of whichever connection q runs on.
func readSynchronous(t *testing.T, ctx context.Context, q queryer) int {
	t.Helper()
	var level int
	if err := q.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&level); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	return level
}

// TestDurableTxCommitsUnderFull pins the CD-0050 D1 mechanism: a durable
// transaction runs under synchronous=FULL (2), an ordinary one under NORMAL
// (1), and both commit and rollback return the pool's connection at NORMAL so
// the next ordinary write does not pay a per-commit sync.
func TestDurableTxCommitsUnderFull(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ordinary, err := beginWriteTx(ctx, s.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSynchronous(t, ctx, ordinary); got != 1 {
		t.Fatalf("ordinary transaction synchronous = %d, want 1", got)
	}
	if err := ordinary.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := s.DurableCommits(); got != 0 {
		t.Fatalf("durable commits after an ordinary commit = %d, want 0", got)
	}

	durable, err := s.beginDurableTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSynchronous(t, ctx, durable); got != 2 {
		t.Fatalf("durable transaction synchronous = %d, want 2", got)
	}
	if err := durable.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := s.DurableCommits(); got != 1 {
		t.Fatalf("durable commits = %d, want 1", got)
	}
	if got := readSynchronous(t, ctx, s.db); got != 1 {
		t.Fatalf("pool synchronous after a durable commit = %d, want 1", got)
	}
	// A second Rollback after Commit is the deferred-cleanup shape callers
	// use; it must not touch the released connection.
	_ = durable.Rollback()

	rolledBack, err := s.beginDurableTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := s.DurableCommits(); got != 1 {
		t.Fatalf("durable commits after a rollback = %d, want 1", got)
	}
	if got := readSynchronous(t, ctx, s.db); got != 1 {
		t.Fatalf("pool synchronous after a durable rollback = %d, want 1", got)
	}
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatalf("ordinary append after durable transactions: %v", err)
	}
}

// TestDurableCommitIgnoresPinnedReader pins the reason the durability barrier
// moved into the commit: another process holding a WAL read snapshot cannot
// fail a consequential acknowledgement. The replaced TRUNCATE checkpoint
// reported busy here after the effect had committed.
func TestDurableCommitIgnoresPinnedReader(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pinReaderSnapshot(t, s)

	if err := s.RegisterTrustedClient(ctx, TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientKeyRecord{ClientRef: "client-1", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"}, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("RegisterTrustedClient under a pinned reader: %v", err)
	}
	if got := s.DurableCommits(); got != 1 {
		t.Fatalf("durable commits after RegisterTrustedClient = %d, want 1", got)
	}
}

// TestDurableTransactFailureSurfaces shows a durable transaction on a closed
// store returns a typed, retry-safe failure and no durable commit.
func TestDurableTransactFailureSurfaces(t *testing.T) {
	t.Parallel()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	err = s.TransactDurable(context.Background(), func(*Transaction) error { return nil })
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("durable transaction on a closed store = %v, want a typed failure", err)
	}
	if failure.Kind != KindUnavailable || !failure.RetrySafe {
		t.Fatalf("durable transaction failure = %+v, want a retry-safe unavailable failure", failure)
	}
	if got := s.DurableCommits(); got != 0 {
		t.Fatalf("durable commits = %d, want 0", got)
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
