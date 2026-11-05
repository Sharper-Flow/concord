package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// A commit error at the ordinary transaction boundary is not a refusal
// proof: the fault connector lands the real SQLite commit and then fails, so
// the callback's write and the revision advance persist while Transact
// reports failure. The envelope must report the possible effect and the
// reconcile duty instead of a provably effect-free retry.
func TestTransactCommitFailureReportsLandedEffect(t *testing.T) {
	t.Parallel()
	path := copyTestDatabase(t)
	seed, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	before := revisionValue(t, seed.db)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(path)}
	connector.failCommit.Store(true)
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	s := &Store{db: db, path: path}
	err = s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := tx.tx.ExecContext(context.Background(), "UPDATE durability_commits SET bit=1 WHERE id=1")
		return err
	})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.EffectPossible || failure.RetrySafe {
		t.Fatalf("landed commit reported a provably effect-free retry: %v", err)
	}
	if failure.Detail != "cannot prove the transaction committed" {
		t.Fatalf("landed commit did not keep its commit envelope: %+v", failure)
	}
	if failure.RecoveryAction != "reconcile database state before retrying" {
		t.Fatalf("landed commit recovery action: %q", failure.RecoveryAction)
	}
	if !strings.Contains(err.Error(), "injected failure after SQLite commit") {
		t.Fatalf("landed commit lost its cause: %v", err)
	}
	if after := revisionValue(t, db); after != before+1 {
		t.Fatalf("landed commit did not advance the revision: %d -> %d", before, after)
	}
	var bit int
	if err := db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 1 {
		t.Fatalf("landed commit did not persist the callback write: bit=%d err=%v", bit, err)
	}
}

// Cancelling the context before the commit boundary proves no driver commit
// ran: Tx.Commit returns the context error unchanged, before its done flag
// and before any driver commit. The surviving refusal envelope stays
// retry-safe and effect-free; when database/sql's watchdog settles the
// transaction first, cleanup reports the unproved settlement instead. Nothing
// persists either way.
func TestTransactCanceledContextRefusalReportsNoEffect(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	before := revisionValue(t, s.db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Transact(ctx, func(tx *Transaction) error {
		if _, err := tx.tx.ExecContext(ctx, "UPDATE durability_commits SET bit=1 WHERE id=1"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("canceled commit reported success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit lost its cause: %v", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("canceled commit is not a typed failure: %v", err)
	}
	if failure.Detail == "cannot commit transaction" && (failure.EffectPossible || !failure.RetrySafe) {
		t.Fatalf("clean refusal claims an effect or an unsafe retry: %+v", failure)
	}
	if after := revisionValue(t, s.db); after != before {
		t.Fatalf("refusal persisted a revision: %d -> %d", before, after)
	}
	var bit int
	if err := s.db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("refusal persisted the callback write: bit=%d err=%v", bit, err)
	}
}
