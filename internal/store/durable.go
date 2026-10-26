package store

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// writeTx keeps store failures at the domain-facing lifecycle boundary.
type writeTx struct{ *sqlitedb.WriteTx }

type writeAdmissionKind = sqlitedb.WriteAdmissionKind

const (
	stateWriteAdmission  = sqlitedb.StateWriteAdmission
	schemaWriteAdmission = sqlitedb.SchemaWriteAdmission
)

func beginWriteTx(ctx context.Context, db *sql.DB, durable *atomic.Uint64, kind writeAdmissionKind) (*writeTx, error) {
	tx, err := sqlitedb.BeginWrite(ctx, db, durable, kind)
	if err != nil {
		return nil, mechanismFailure(err)
	}
	return &writeTx{WriteTx: tx}, nil
}

func (w *writeTx) Commit() error   { return mechanismFailure(w.WriteTx.Commit()) }
func (w *writeTx) Rollback() error { return mechanismFailure(w.WriteTx.Rollback()) }

func (w *writeTx) finish(resultErr *error) {
	if err := w.Rollback(); err != nil {
		failure := wrapFailure(KindUnavailable, "transaction_cleanup", "cannot clean up the transaction", false, "inspect the transaction cleanup failure", errors.Join(*resultErr, err))
		*resultErr = failure
	}
}

// mechanismFailure translates every localized mechanism failure, including
// siblings in a joined rollback failure, without flattening driver causes.
func mechanismFailure(err error) error {
	switch e := err.(type) {
	case *sqlitedb.Error:
		f := wrapFailure(FailureKind(e.Kind), e.Op, e.Detail, e.RetrySafe, e.RecoveryAction, mechanismFailure(e.Err))
		f.EffectPossible = f.EffectPossible || e.EffectPossible
		return f
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		translated := make([]error, len(causes))
		for i, cause := range causes {
			translated[i] = mechanismFailure(cause)
		}
		return errors.Join(translated...)
	default:
		return err
	}
}

func (s *Store) beginStoreWriteTx(ctx context.Context, durable bool) (*writeTx, error) {
	if durable {
		return s.beginDurableTx(ctx)
	}
	return beginWriteTx(ctx, s.db, nil, stateWriteAdmission)
}

func (s *Store) beginDurableTx(ctx context.Context) (*writeTx, error) {
	return beginWriteTx(ctx, s.db, &s.durableCommits, stateWriteAdmission)
}

// DurableCommits reports successful durable commits since this handle opened.
func (s *Store) DurableCommits() uint64 {
	if s == nil {
		return 0
	}
	return s.durableCommits.Load()
}
