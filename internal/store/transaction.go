package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// beginTxSource is the minimal private transaction-begin surface. The pool
// (*sql.DB) and one held connection (*sql.Conn) both satisfy it, so the read
// owner can pin one snapshot on either without a second connection.
type beginTxSource interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// beginReadTx always opens a deferred snapshot, even when the database's DSN
// selects immediate write transactions. It also serves independent read-only
// readiness handles, without borrowing the live store's connection.
func beginReadTx(ctx context.Context, db beginTxSource) (*sql.Tx, error) {
	return db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

// beginOrdinaryTx uses the write owner without changing NORMAL commit or raw
// transaction lifecycle semantics. Only durable writes need a pinned connection.
func beginOrdinaryTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := beginWriteTx(ctx, db, nil, stateWriteAdmission)
	if err != nil {
		return nil, err
	}
	return tx.Tx, nil
}

// Transaction is an opaque unit of work owned by Store. Callers can pass it
// back to store-owned Tx methods, but cannot execute SQL or control its
// lifecycle directly.
type Transaction struct {
	tx    *sql.Tx
	clock func() time.Time
	path  string
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
	tx, err := s.beginStoreWriteTx(ctx, durable)
	if err != nil {
		return wrapFailure(KindUnavailable, "transaction", "cannot begin transaction", true, "retry once the database is writable", err)
	}
	transaction := &Transaction{tx: tx.Tx, clock: s.Clock, path: s.Path()}
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

// readSnapshot owns a deferred read scope for projection readers.
// The callback never commits; rollback releases its snapshot and
// invalidates the transaction even when the callback fails or panics.
func (s *Store) readSnapshot(ctx context.Context, fn func(queryer) error) error {
	var read func(*Transaction) error
	if fn != nil {
		read = func(tx *Transaction) error { return fn(tx.tx) }
	}
	return s.ReadTransaction(ctx, read)
}

// ReadTransaction owns a deferred read snapshot for transaction-scoped reads.
// It always rolls back and invalidates the opaque transaction after the callback.
// The callback must only read; rollback does not prevent attempted writes.
func (s *Store) ReadTransaction(ctx context.Context, fn func(*Transaction) error) (retErr error) {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "read_snapshot", "store is not open", false, "open the authority database")
	}
	if fn == nil {
		return newFailure(KindInvalidOperation, "read_snapshot", "transaction callback is required", false, "supply a read callback")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return wrapFailure(KindUnavailable, "read_snapshot", "cannot begin read snapshot", true, "retry once the database is readable", err)
	}
	transaction := &Transaction{tx: tx, clock: s.Clock, path: s.Path()}
	defer func() { transaction.tx = nil }()
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			retErr = errors.Join(retErr, wrapFailure(KindUnavailable, "read_snapshot", "cannot close read snapshot", false, "inspect the transaction cleanup failure", err))
		}
	}()
	return fn(transaction)
}
