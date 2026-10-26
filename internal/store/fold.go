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
	if s == nil || s.tx != tx || !s.guard.Active() || s.outsideRepairLifecycleEvent == nil {
		return false
	}
	authorized := s.outsideRepairLifecycleEvent
	return lifecycle == "completed" && event.Kind == WorkflowOutsideRepairReconciled &&
		authorized.EventID == event.EventID && authorized.SubjectID == event.SubjectID && authorized.Seq == event.Seq
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
