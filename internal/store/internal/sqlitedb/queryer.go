package sqlitedb

import (
	"context"
	"database/sql"
)

// Queryer inherits the caller's read handle. A transaction owns the pool's
// single connection, so transaction-scoped reads must not re-enter the pool.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
