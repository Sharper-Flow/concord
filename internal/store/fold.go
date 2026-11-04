package store

import (
	"context"
	"database/sql"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// writeScope binds workflow write context and current-row protection to one
// transaction. Nested operations share the same scope.
type writeScope struct {
	tx    *sql.Tx
	guard sqlitedb.WriteGuard
	// eventIDs records this scope's successful appends in write order, including
	// nested operations. Callers copy only the slice their operation appended.
	eventIDs        []string
	operationEvents map[string]bool
}

// newWriteScope binds an unused scope to an open transaction. enter opens the
// first level and inserts the guard row.
func newWriteScope(tx *sql.Tx) *writeScope {
	return &writeScope{tx: tx, guard: sqlitedb.NewWriteGuard(tx)}
}

// beginWrite opens a write scope on tx and inserts the guard row. The caller
// closes the returned scope in the function that called beginWrite, before the
// transaction commits.
func beginWrite(ctx context.Context, tx *sql.Tx) (*writeScope, error) {
	scope := newWriteScope(tx)
	if err := scope.enter(ctx); err != nil {
		return nil, err
	}
	return scope, nil
}

func (s *writeScope) enter(ctx context.Context) error {
	return mechanismFailure(s.guard.Enter(ctx))
}

func (s *writeScope) close(ctx context.Context) error {
	return mechanismFailure(s.guard.Close(ctx))
}
