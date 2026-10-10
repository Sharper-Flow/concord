package store

import (
	"context"
	"database/sql"
	"errors"
)

// CD-0213 D8 retires the planning-mirror event kinds. domain_events is
// append-only and replay refuses an unregistered kind, so each retired kind
// stays registered: its payload still validates and its fold still advances
// the subject version, so later historical events on the same subject fold at
// the version they were written against. The fold writes no planning state,
// and appendEvent refuses every new append of a retired kind.

func retiredEventAppendRefused(kind string) error {
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
// not of the event kind. A capture classifies by the prepared upcast payload,
// so a work.created recorded at an older payload version cannot enter the log
// as a new initiative either. Every other work-item event classifies by a
// kind lookup through the caller's transaction, never through the store pool:
// while this transaction holds the pooled connection, a nested s.db call
// parks forever. RebuildFromLog never calls appendEvent, so the folds stay
// free to replay historic initiative captures and version transitions.
func refuseRetiredPlanningAppend(ctx context.Context, tx *sql.Tx, event Event, prepared preparedRegisteredEvent) error {
	if event.SubjectType != SubjectWorkItem {
		return nil
	}
	if event.Kind == "work.created" {
		var payload workCreatedPayload
		if err := decodePayload(prepared.current, &payload); err != nil {
			return err
		}
		if payload.WorkKind == "initiative" {
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
	if kind == "initiative" {
		return retiredInitiativeWriteRefused(event.Kind)
	}
	return nil
}

type productPlanningModeSetPayload struct {
	ProductID        string `json:"product_id"`
	PlanningMode     string `json:"planning_mode"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

type initiativeEntryPayload struct {
	ChildWorkID      string `json:"child_work_id"`
	Position         int64  `json:"position"`
	Required         bool   `json:"required"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

type initiativeNarrativePayload struct {
	Narrative        string `json:"narrative"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

func retiredVersionEvidence(event Event, expected, resulting int64) error {
	if expected < 1 || resulting != expected+1 {
		return newFailure(KindInvalidPayload, "fold_event", event.Kind+" version evidence is not consecutive", false,
			"repair the historical event payload")
	}
	return nil
}

func foldRetiredProductPlanningModeSet(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p productPlanningModeSetPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if err := retiredVersionEvidence(event, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "products", event, p.ExpectedVersion, p.ResultingVersion, "Product")
}

func foldRetiredInitiativeEntry(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p initiativeEntryPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if err := retiredVersionEvidence(event, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item")
}

func foldRetiredInitiativeNarrativeRevised(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p initiativeNarrativePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if err := retiredVersionEvidence(event, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item")
}
