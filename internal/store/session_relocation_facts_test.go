package store

import (
	"database/sql"
	"testing"
)

func inRelocationTx(t *testing.T, s *Store, fn func(*Transaction, *sql.Tx) error) error {
	t.Helper()
	return s.Transact(t.Context(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "relocation_test")
		if err != nil {
			return err
		}
		return fn(transaction, tx)
	})
}
