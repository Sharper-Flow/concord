package store

import (
	"context"
	"database/sql"
	"strings"
)

// foldScope is a transaction-owned, depth-counted fold guard. One scope
// travels through one transaction's call graph; its enter and close calls
// must pair. The first level inserts the fold_guard row, deeper levels only
// count depth, and the row leaves when the outermost level closes. The row
// lives inside the caller's transaction, so a rollback removes it with every
// other uncommitted write and no committed guard can survive a scope.
type foldScope struct {
	tx    *sql.Tx
	depth int
	// eventIDs records this scope's successful appends in write order, including
	// nested operations. Callers copy only the slice their operation appended.
	eventIDs []string
	// Only the validated outside-repair reconciliation fold grants this exact
	// event permission to complete held work. It is local to that fold call.
	outsideRepairLifecycleEvent *Event
	// registry is the definition source the transaction's workflow folds
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
func (s *foldScope) bindRegistry(tx *sql.Tx, registry DefinitionRegistry) error {
	if s == nil {
		return newFailure(KindInvariantViolation, "fold", "fold scope is required", false, "open the fold scope with beginFold")
	}
	if s.tx != tx {
		return newFailure(KindInvariantViolation, "fold", "fold scope belongs to another transaction", false, "open the fold scope on the transaction that folds")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	if s.registry == nil {
		s.registry = registry
		return nil
	}
	if s.registry != registry {
		return newFailure(KindInvariantViolation, "fold", "fold scope is already bound to another definition registry", false, "reuse the transaction's bound definition registry")
	}
	return nil
}

// scopeDefinitionRegistry is the one registry the transaction's workflow
// folds resolve pinned definitions through. A builtin entry selects builtins;
// a caller-selected registry never retries a missing pin against builtins.
func scopeDefinitionRegistry(scope *foldScope) DefinitionRegistry {
	if scope == nil || scope.registry == nil {
		return BuiltinWorkflowRegistry()
	}
	return scope.registry
}

func (s *foldScope) permitsOutsideRepairLifecycle(tx *sql.Tx, event Event, lifecycle string) bool {
	if s == nil || s.tx != tx || s.depth == 0 || s.outsideRepairLifecycleEvent == nil {
		return false
	}
	authorized := s.outsideRepairLifecycleEvent
	return lifecycle == "completed" && event.Kind == WorkflowOutsideRepairReconciled &&
		authorized.EventID == event.EventID && authorized.SubjectID == event.SubjectID && authorized.Seq == event.Seq
}

// newFoldScope binds an unused scope to an open transaction. enter opens the
// first level and inserts the guard row.
func newFoldScope(tx *sql.Tx) *foldScope {
	return &foldScope{tx: tx}
}

// beginFold opens a fold scope on tx and inserts the guard row. The caller
// closes the returned scope in the function that called beginFold, before the
// transaction commits.
func beginFold(ctx context.Context, tx *sql.Tx) (*foldScope, error) {
	scope := newFoldScope(tx)
	if err := scope.enter(ctx); err != nil {
		return nil, err
	}
	return scope, nil
}

// enter opens one fold level. At depth zero the guard row is inserted, and a
// committed fold_guard row already in the table is an invariant failure: the
// row can only be a stranded guard, and ordinary writes must stay refused
// until a verified backup is restored into a new path and swapped into place.
func (s *foldScope) enter(ctx context.Context) error {
	if s.depth > 0 {
		s.depth++
		return nil
	}
	if _, err := s.tx.ExecContext(ctx, `INSERT INTO fold_guard (active) VALUES (1)`); err != nil {
		if isFoldGuardCollision(err) {
			return newFailure(KindInvariantViolation, "fold",
				"a committed fold_guard row already guards the projections, so no fold scope owns it",
				false,
				"stop concord processes and run concord restore from a verified backup into a new path, then swap the restored database into place")
		}
		return wrapFailure(KindUnavailable, "fold", "cannot enable projection fold guard", true,
			"retry once the database is writable", err)
	}
	s.depth = 1
	return nil
}

// close releases one fold level. Deeper levels keep the guard row in place,
// so a nested fold cannot drop its caller's protection early; the row leaves
// only when the outermost level closes.
func (s *foldScope) close(ctx context.Context) error {
	if s.depth == 0 {
		return nil
	}
	s.depth--
	if s.depth > 0 {
		return nil
	}
	if _, err := s.tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active = 1`); err != nil {
		return wrapFailure(KindUnavailable, "fold", "cannot disable projection fold guard", true,
			"retry once the database is writable", err)
	}
	return nil
}

// isFoldGuardCollision reports whether err is the fold_guard primary-key
// collision, the only way the insert can refuse while the database is
// writable.
func isFoldGuardCollision(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") && strings.Contains(message, "fold_guard.active")
}
