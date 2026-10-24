package store

import (
	"context"
	"database/sql"
	"errors"
)

// CD-0213 D8 keeps Initiative records and edges readable but gives them no
// execution behavior: neither selectable work nor operative blockers.
// Queries qualify this constant predicate with their fixed table alias.
const retiredInitiativeWorkKind = "initiative"
const actionableWorkKindSQL = "kind <> '" + retiredInitiativeWorkKind + "'"

func actionableWorkKind(kind string) bool {
	return kind != retiredInitiativeWorkKind
}

func requireActionableWorkKind(kind, operation string) error {
	if !actionableWorkKind(kind) {
		return newFailure(KindInvalidOperation, operation,
			"initiative work kind is read-only under CD-0213 D4/D8 and cannot enter execution", false,
			"group the work in Linear through the Linear MCP server")
	}
	return nil
}

// RequireActionableWork refuses execution of a historical read-only kind before
// a caller probes or changes native state. Historical reads do not use this gate.
func (s *Store) RequireActionableWork(ctx context.Context, workID, operation string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, operation, "store is not open", false, "open the authority database")
	}
	return requireActionableWork(ctx, s.db, workID, operation)
}

func requireActionableWork(ctx context.Context, q queryer, workID, operation string) error {
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return newFailure(KindUnknownScope, operation, "work item does not exist", false, "check the work identity before execution")
		}
		return wrapFailure(KindUnavailable, operation, "cannot read the work item kind", true, "retry once the database is readable", err)
	}
	return requireActionableWorkKind(kind, operation)
}

func planningRetiredEventAppendRefused(kind string) error {
	return newFailure(KindInvalidOperation, "apply_operation",
		"event kind "+kind+" is retired by CD-0213 D8: Linear is the only planning authority", false,
		"make the planning change in Linear through the Linear MCP server")
}

func retiredInitiativeWriteRefused(kind string) error {
	return newFailure(KindInvalidOperation, "apply_operation",
		"event kind "+kind+" writes the initiative work kind, retired by CD-0213 D4: the initiative work kind receives no new writes", false,
		"group the work in Linear through the Linear MCP server")
}

// refuseRetiredPlanningAppend owns the CD-0213 D4 control for the generic
// work-event kinds whose retirement is a property of their payload or subject,
// not of the event kind. A capture classifies by its validated payload.
// Every other work-item event classifies by a kind lookup through the
// caller's transaction, never through the store pool:
// while this transaction holds the pooled connection, a nested s.db call
// parks forever.
func refuseRetiredPlanningAppend(ctx context.Context, tx *sql.Tx, event Event) error {
	if event.SubjectType != SubjectWorkItem {
		return nil
	}
	if event.Kind == "work.created" {
		var payload workCreatedPayload
		if err := decodePayload(event, &payload); err != nil {
			return err
		}
		if !actionableWorkKind(payload.WorkKind) {
			return retiredInitiativeWriteRefused(event.Kind)
		}
		return nil
	}
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, event.SubjectID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "append_event", "cannot read the subject work kind", true,
			"retry once the database is readable", err)
	}
	if !actionableWorkKind(kind) {
		return retiredInitiativeWriteRefused(event.Kind)
	}
	return nil
}
