package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const (
	WorkflowPlanRecorded         = "workflow.plan_recorded"
	WorkflowRemovalScopeRecorded = "workflow.removal_scope_recorded"
)

type workflowPlanRecordedPayload struct {
	WorkflowVersionFields
	Document json.RawMessage `json:"document"`
}

type workflowRemovalScopeRecordedPayload struct {
	WorkflowVersionFields
	Document json.RawMessage `json:"document"`
}

func workflowPlanDocumentEvents(request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64, kind string) []Event {
	return []Event{workflowTypedEvent(eventID, kind, request.WorkID, actor, request.Now, expected, map[string]any{"document": raw})}
}

func foldWorkflowPlanRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowPlanRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	fields, err := validateWorkflowPlanActionPayload("record_plan", p.Document)
	if err != nil {
		return err
	}
	if err := admitWorkflowPlanDocumentTx(ctx, tx, event, p.WorkflowVersionFields, "plan", "record_plan"); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_plan_records WHERE work_id=?)`, event.SubjectID).Scan(&exists); err != nil {
		return workflowProjectionError(err, "cannot read workflow plan record")
	}
	if exists != 0 && fields["reason"] == nil {
		return newFailure(KindInvalidPayload, "fold_event", "plan revision requires a reason", false, "supply the reason for the plan revision")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET work_version=excluded.work_version,payload=excluded.payload,recorded_at=excluded.recorded_at,actor=excluded.actor,event_ref=excluded.event_ref`, event.SubjectID, *p.ResultingVersion, string(p.Document), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, event.EventID)
	if err != nil {
		return workflowProjectionError(err, "cannot write workflow plan record")
	}
	return nil
}

func foldWorkflowRemovalScopeRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowRemovalScopeRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if _, err := validateWorkflowPlanActionPayload("record_removal_scope", p.Document); err != nil {
		return err
	}
	if err := admitWorkflowPlanDocumentTx(ctx, tx, event, p.WorkflowVersionFields, "list", "record_removal_scope"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_removal_scopes(work_id,work_version,payload,recorded_at,actor,event_ref) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET work_version=excluded.work_version,payload=excluded.payload,recorded_at=excluded.recorded_at,actor=excluded.actor,event_ref=excluded.event_ref`, event.SubjectID, *p.ResultingVersion, string(p.Document), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, event.EventID)
	if err != nil {
		return workflowProjectionError(err, "cannot write workflow removal scope")
	}
	return nil
}

func admitWorkflowPlanDocumentTx(ctx context.Context, tx *sql.Tx, event Event, version WorkflowVersionFields, step, action string) error {
	if err := workflowBase(event, version); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if _, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if !workflowPlanAllowsAction(plan, step, action) {
		retained, err := foldWorkflowRetainedRecordTx(ctx, tx, event.SubjectID, plan, step, action)
		if err != nil {
			return err
		}
		if !retained {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "document action is not declared on the frontier", false, "reread the workflow plan")
		}
	}
	return advanceWorkflowVersion(ctx, tx, event, version)
}

// requireWorkflowPlanDocumentTx gates completion of the document-owning step.
func requireWorkflowPlanDocumentTx(ctx context.Context, tx *sql.Tx, workID string, plan *WorkflowStepPlan, stepID string) error {
	if plan == nil {
		return nil
	}
	var raw string
	var err error
	var action string
	switch {
	case stepID == "clarify":
		action = "record_clarify"
		err = tx.QueryRowContext(ctx, `SELECT payload FROM workflow_clarify_records WHERE work_id=?`, workID).Scan(&raw)
	case stepID == "plan" && (plan.Type == WorkflowStepPlanChange || plan.Type == WorkflowStepPlanFix):
		action = "record_plan"
		err = tx.QueryRowContext(ctx, `SELECT payload FROM workflow_plan_records WHERE work_id=?`, workID).Scan(&raw)
	case stepID == "list" && plan.Type == WorkflowStepPlanRemoval:
		action = "record_removal_scope"
		err = tx.QueryRowContext(ctx, `SELECT payload FROM workflow_removal_scopes WHERE work_id=?`, workID).Scan(&raw)
	case stepID == "diagnose" && plan.Type == WorkflowStepPlanFix:
		// The diagnose step completes only with its recorded typed diagnosis.
		var exists int
		if existsErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_completed_exits WHERE work_id=? AND action_id='record_diagnosis')`, workID).Scan(&exists); existsErr != nil {
			return workflowProjectionError(existsErr, "cannot read required workflow document")
		}
		if exists == 0 {
			return newFailure(KindMissingEvidence, "fold_event", "step requires its current document", false, "record the required workflow document")
		}
		return nil
	default:
		return nil
	}
	if err == sql.ErrNoRows {
		return newFailure(KindMissingEvidence, "fold_event", "step requires its current document", false, "record the required workflow document")
	}
	if err != nil {
		return workflowProjectionError(err, "cannot read required workflow document")
	}
	if _, err := validateWorkflowPlanActionPayload(action, json.RawMessage(raw)); err != nil {
		return newFailure(KindInvariantViolation, "fold_event", "stored workflow document is invalid", false, "restore a verified backup")
	}
	return nil
}
