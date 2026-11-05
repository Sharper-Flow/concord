package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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

// findFailureByDetail returns the first typed failure with the given detail
// in the error tree, mirroring the store's failure-cause walks.
func findFailureByDetail(err error, detail string) *Failure {
	switch wrapped := err.(type) {
	case *Failure:
		if wrapped.Detail == detail {
			return wrapped
		}
		return findFailureByDetail(wrapped.Err, detail)
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			if found := findFailureByDetail(cause, detail); found != nil {
				return found
			}
		}
	case interface{ Unwrap() error }:
		return findFailureByDetail(wrapped.Unwrap(), detail)
	}
	return nil
}

// Cancelling the context before the commit boundary leaves three orderings,
// and each must report its own truth:
//
//   - the scope's rollback settles the refused commit, so the retry-safe
//     effect-free refusal envelope survives ("cannot commit transaction");
//   - the watchdog settles it, so cleanup reports the unproved settlement
//     and joins the refusal envelope;
//   - the watchdog marks the transaction done before Tx.Commit checks the
//     context, so Tx.Commit returns ErrTxDone, the commit reports the
//     possible effect ("cannot prove the transaction committed"), and
//     cleanup joins it with the unproved settlement.
//
// Nothing persists in any ordering.
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
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("canceled commit is not a typed failure: %v", err)
	}
	switch failure.Detail {
	case "cannot commit transaction":
		if failure.EffectPossible || !failure.RetrySafe {
			t.Fatalf("clean refusal claims an effect or an unsafe retry: %+v", failure)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("direct refusal lost its cause: %v", err)
		}
	case "cannot clean up the transaction":
		if !failure.EffectPossible {
			t.Fatalf("unproved settlement cleanup claims no possible effect: %+v", failure)
		}
		refusal := findFailureByDetail(err, "cannot commit transaction")
		landed := findFailureByDetail(err, "cannot prove the transaction committed")
		switch {
		case refusal != nil && landed == nil:
			if refusal.EffectPossible || !refusal.RetrySafe || !errors.Is(err, context.Canceled) {
				t.Fatalf("joined refusal envelope is untruthful: %v", err)
			}
		case refusal == nil && landed != nil:
			if !landed.EffectPossible || landed.RetrySafe || !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("joined watchdog-settled envelope is untruthful: %v", err)
			}
		default:
			t.Fatalf("cleanup joins neither a refusal nor an unproved settlement: %v", err)
		}
	default:
		t.Fatalf("canceled commit reported an unexpected envelope: %v", err)
	}
	if after := revisionValue(t, s.db); after != before {
		t.Fatalf("refusal persisted a revision: %d -> %d", before, after)
	}
	var bit int
	if err := s.db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("refusal persisted the callback write: bit=%d err=%v", bit, err)
	}
}

// The refusal classification is the boundary's one proof split, so every
// input class is pinned directly: only a context error with an already-done
// transaction context proves no driver commit ran (Tx.Commit checks the
// context before its done flag and before any driver commit). A watchdog
// ErrTxDone, a driver error, or a live context proves nothing, and a driver
// error with a done context still ran the commit — the context can be
// cancelled after the check, while the driver commit executes.
func TestCommitRefusedBeforeDriverClassification(t *testing.T) {
	t.Parallel()
	canceledCtx, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	deadlineCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancelDeadline()
	liveCtx := context.Background()
	driverErr := errors.New("injected driver commit failure")
	for _, row := range []struct {
		name string
		err  error
		ctx  context.Context
		want bool
	}{
		{"canceled context with canceled cause", context.Canceled, canceledCtx, true},
		{"deadline context with deadline cause", context.DeadlineExceeded, deadlineCtx, true},
		{"canceled context with wrapped canceled cause", fmt.Errorf("commit: %w", context.Canceled), canceledCtx, true},
		{"canceled context with watchdog settlement", sql.ErrTxDone, canceledCtx, false},
		{"canceled context with driver commit error", driverErr, canceledCtx, false},
		{"live context with canceled cause", context.Canceled, liveCtx, false},
		{"live context with driver commit error", driverErr, liveCtx, false},
	} {
		if got := commitRefusedBeforeDriver(row.err, row.ctx); got != row.want {
			t.Fatalf("%s: commitRefusedBeforeDriver = %t, want %t", row.name, got, row.want)
		}
	}
}
