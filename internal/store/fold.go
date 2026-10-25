package store

import (
	"context"
	"database/sql"
	"strings"
)

// writeScope is a transaction-owned, depth-counted write guard. One scope
// travels through one transaction's call graph; its enter and close calls
// must pair. The first level inserts the write_guard row, deeper levels only
// count depth, and the row leaves when the outermost level closes. The row
// lives inside the caller's transaction, so a rollback removes it with every
// other uncommitted write and no committed guard can survive a scope.
type writeScope struct {
	tx    *sql.Tx
	depth int
	// eventIDs records this scope's successful appends in write order, including
	// nested operations. Callers copy only the slice their operation appended.
	eventIDs []string
	// Only the validated outside-repair reconciliation writer grants this exact
	// event permission to complete held work. It is local to that writer call.
	outsideRepairLifecycleEvent *Event
	// registry is the definition source the transaction's workflow writers
	// resolve pinned definitions through. A nil registry selects the builtin
	// registry; a caller-selected registry is bound before any write and is
	// never silently widened with a builtin retry.
	registry DefinitionRegistry
}

// bindRegistry binds the caller's definition registry to this scope before
// any write. The scope must belong to the transaction that binds it, and one
// scope carries one registry for its whole lifetime: a second binding with a
// different registry refuses instead of mixing definition sources inside one
// transaction.
func (s *writeScope) bindRegistry(tx *sql.Tx, registry DefinitionRegistry) error {
	if s == nil {
		return newFailure(KindInvariantViolation, "fold", "write scope is required", false, "open the write scope with beginWrite")
	}
	if s.tx != tx {
		return newFailure(KindInvariantViolation, "fold", "write scope belongs to another transaction", false, "open the write scope on the transaction that writes")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	if s.registry == nil {
		s.registry = registry
		return nil
	}
	if s.registry != registry {
		return newFailure(KindInvariantViolation, "fold", "write scope is already bound to another definition registry", false, "reuse the transaction's bound definition registry")
	}
	return nil
}

// scopeDefinitionRegistry is the one registry the transaction's workflow
// writers resolve pinned definitions through. A builtin entry selects builtins;
// a caller-selected registry never retries a missing pin against builtins.
func scopeDefinitionRegistry(scope *writeScope) DefinitionRegistry {
	if scope == nil || scope.registry == nil {
		return BuiltinWorkflowRegistry()
	}
	return scope.registry
}

func (s *writeScope) permitsOutsideRepairLifecycle(tx *sql.Tx, event Event, lifecycle string) bool {
	if s == nil || s.tx != tx || s.depth == 0 || s.outsideRepairLifecycleEvent == nil {
		return false
	}
	authorized := s.outsideRepairLifecycleEvent
	return lifecycle == "completed" && event.Kind == WorkflowOutsideRepairReconciled &&
		authorized.EventID == event.EventID && authorized.SubjectID == event.SubjectID && authorized.Seq == event.Seq
}

// newWriteScope binds an unused scope to an open transaction. enter opens the
// first level and inserts the guard row.
func newWriteScope(tx *sql.Tx) *writeScope {
	return &writeScope{tx: tx}
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

// enter opens one write level. At depth zero the guard row is inserted, and a
// committed write_guard row already in the table is an invariant failure: the
// row can only be a stranded guard, and ordinary writes must stay refused
// until a verified backup is restored into a new path and swapped into place.
func (s *writeScope) enter(ctx context.Context) error {
	if s.depth > 0 {
		s.depth++
		return nil
	}
	if _, err := s.tx.ExecContext(ctx, `INSERT INTO write_guard (active) VALUES (1)`); err != nil {
		if isWriteGuardCollision(err) {
			return newFailure(KindInvariantViolation, "fold",
				"a committed write_guard row already guards the current rows, so no write scope owns it",
				false,
				"stop concord processes and run concord restore from a verified backup into a new path, then swap the restored database into place")
		}
		return wrapFailure(KindUnavailable, "fold", "cannot enable current-row write guard", true,
			"retry once the database is writable", err)
	}
	s.depth = 1
	return nil
}

// close releases one write level. Deeper levels keep the guard row in place,
// so a nested write cannot drop its caller's protection early; the row leaves
// only when the outermost level closes.
func (s *writeScope) close(ctx context.Context) error {
	if s.depth == 0 {
		return nil
	}
	s.depth--
	if s.depth > 0 {
		return nil
	}
	if _, err := s.tx.ExecContext(ctx, `DELETE FROM write_guard WHERE active = 1`); err != nil {
		return wrapFailure(KindUnavailable, "fold", "cannot disable current-row write guard", true,
			"retry once the database is writable", err)
	}
	return nil
}

// isWriteGuardCollision reports whether err is the write_guard primary-key
// collision, the only way the insert can refuse while the database is
// writable.
func isWriteGuardCollision(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") && strings.Contains(message, "write_guard.active")
}
