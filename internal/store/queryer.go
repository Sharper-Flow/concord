package store

import (
	"context"
	"database/sql"
)

// queryer is the read handle shared by *sql.DB and *sql.Tx.
//
// The store pools one connection (SetMaxOpenConns(1), store.go). While a
// transaction is open it holds that connection, so a nested read through the
// pool parks there forever: no error, no timeout, and a test that only hangs.
// A core taking a queryer inherits whichever handle its caller already holds
// rather than reaching for the pool, which is what makes the read safe inside
// transaction scope.
//
// Both methods are declared because both handles provide both. Splitting the
// interface per call site is what produced five near-identical copies and a
// hand-rolled row adapter that returned nil for any handle it failed to match.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
