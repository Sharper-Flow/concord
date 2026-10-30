package store

import (
	"context"
	"database/sql"
	"testing"
)

// pinReaderSnapshot holds a read transaction from a separate connection, the
// way another Concord process does, and commits after it so the reader's WAL
// read mark trails the log. Neither an ordinary nor a durable write may wait
// on it.
func pinReaderSnapshot(t *testing.T, s *Store) {
	t.Helper()
	reader, err := sql.Open(driverName, SQLiteFileURI(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	readTx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readTx.Rollback() })
	var probe int
	if err := readTx.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&probe); err != nil {
		t.Fatalf("reader snapshot: %v", err)
	}
	if err := s.appendSeedEvent(context.Background()); err != nil {
		t.Fatalf("commit after the pinned snapshot: %v", err)
	}
}
