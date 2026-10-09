package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Transaction is an opaque unit of work owned by Store. Callers can pass it
// back to store-owned Tx methods, but cannot execute SQL or control its
// lifecycle directly.
type Transaction struct {
	tx         *sql.Tx
	clock      func() time.Time
	path       string
	navigation *workContextNavigationProof
	// fold is the transaction's fold scope while a fold region is open on it.
	// A region owner sets it after beginFold and mutation seams reuse it, so
	// nested folds stay depth-counted on one scope.
	fold *foldScope
}

func (t *Transaction) now() time.Time {
	if t == nil || t.clock == nil {
		return time.Now().UTC()
	}
	return t.clock().UTC()
}

func transactionSQL(tx *Transaction, op string) (*sql.Tx, error) {
	if tx == nil || tx.tx == nil {
		return nil, newFailure(KindInvalidOperation, op, "transaction is not open", false, "supply an active store transaction")
	}
	return tx.tx, nil
}

// Transact owns the complete lifecycle of a store transaction. Callers may
// pass the transaction to store-owned Tx methods, but never need to manage its
// commit or rollback boundary.
func (s *Store) Transact(ctx context.Context, fn func(*Transaction) error) error {
	return s.transact(ctx, false, fn)
}

// TransactDurable is Transact for a consequential operation (CD-0050 D3): its
// commit syncs the write-ahead log before it returns, so a nil result means
// the transaction and every earlier commit on the store are durable.
func (s *Store) TransactDurable(ctx context.Context, fn func(*Transaction) error) error {
	return s.transact(ctx, true, fn)
}

func (s *Store) transact(ctx context.Context, durable bool, fn func(*Transaction) error) (retErr error) {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "transaction", "store is not open", false, "open the authority database")
	}
	if fn == nil {
		return newFailure(KindInvalidOperation, "transaction", "transaction callback is required", false, "supply a transaction callback")
	}
	tx, err := beginWriteTx(ctx, s, durable)
	if err != nil {
		return wrapFailure(KindUnavailable, "transaction", "cannot begin transaction", true, "retry once the database is writable", err)
	}
	transaction := &Transaction{tx: tx.Tx, clock: s.Clock, path: s.Path()}
	transaction.navigation, _ = ctx.Value(workContextNavigationProofKey{}).(*workContextNavigationProof)
	defer tx.finish(&retErr)
	defer func() { transaction.tx = nil }()
	if err := fn(transaction); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			// The commit owner already classified this failure. A durable
			// commit error carries its own operation and possible-effect
			// state; re-wrapping it here would shadow both.
			return err
		}
		return wrapFailure(KindUnavailable, "transaction", "cannot commit transaction", true, "retry once the database is writable", err)
	}
	return nil
}
