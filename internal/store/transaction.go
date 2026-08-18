package store

import (
	"context"
	"database/sql"
)

// Transaction is an opaque unit of work owned by Store. Callers can pass it
// back to store-owned operations, but cannot execute SQL or control its
// lifecycle directly.
type Transaction struct {
	tx *sql.Tx
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
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "transaction", "store is not open", false, "open the authority database")
	}
	if fn == nil {
		return newFailure(KindInvalidOperation, "transaction", "transaction callback is required", false, "supply a transaction callback")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "transaction", "cannot begin transaction", true, "retry once the database is writable", err)
	}
	transaction := &Transaction{tx: tx}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		transaction.tx = nil
	}()
	if err := fn(transaction); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "transaction", "cannot commit transaction", true, "retry once the database is writable", err)
	}
	committed = true
	return nil
}
