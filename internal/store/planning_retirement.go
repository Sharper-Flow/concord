package store

import (
	"context"
	"database/sql"
	"errors"
)

const retiredInitiativeWorkKind = "initiative"

func actionableWorkKind(kind string) bool {
	return kind != retiredInitiativeWorkKind
}

// RequireActionableWork checks existence and publication before execution.
func (s *Store) RequireActionableWork(ctx context.Context, workID, operation string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, operation, "store is not open", false, "open the authority database")
	}
	return requireActionableWork(ctx, s.db, workID, operation)
}

func requireActionableWork(ctx context.Context, q queryer, workID, operation string) error {
	if err := publishedWorkAbsenceQ(ctx, q, workID, operation); err != nil {
		return err
	}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, workID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return newFailure(KindUnknownScope, operation, "work item does not exist", false, "check the work identity before execution")
		}
		return wrapFailure(KindUnavailable, operation, "cannot read the work item", true, "retry once the database is readable", err)
	}
	return nil
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
// work-event kinds whose retirement is a property of their payload.
func refuseRetiredPlanningAppend(event Event) error {
	if event.SubjectType != SubjectWorkItem || event.Kind != "work.created" {
		return nil
	}
	var payload workCreatedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if !actionableWorkKind(payload.WorkKind) {
		return retiredInitiativeWriteRefused(event.Kind)
	}
	return nil
}
