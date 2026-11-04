package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
)

// readWorkflowPlanTx reads the type and plan as one coherent projection.
// A nil plan denotes intake, before a type has been picked.
func readWorkflowPlanTx(ctx context.Context, q queryer, workID string) (*WorkflowStepPlan, error) {
	var typ, raw sql.NullString
	err := q.QueryRowContext(ctx, `SELECT workflow_type,step_plan FROM workflow_instances WHERE work_id=?`, workID).Scan(&typ, &raw)
	if err == sql.ErrNoRows {
		return nil, newFailure(KindProjectionNotFound, "workflow_step_plan", "workflow instance does not exist", false, "reread the work item")
	}
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read the workflow plan")
	}
	if !typ.Valid && !raw.Valid {
		return nil, nil
	}
	return decodeStoredPlanTx(typ, raw)
}

// decodeStoredPlanTx decodes one stored plan projection and refuses the
// half-written, malformed, or type-mismatched shapes as invariant violations.
func decodeStoredPlanTx(typ, raw sql.NullString) (*WorkflowStepPlan, error) {
	if !typ.Valid || !raw.Valid {
		return nil, newFailure(KindInvariantViolation, "workflow_step_plan", "workflow type and plan are inconsistent", false, "restore a verified backup")
	}
	var plan WorkflowStepPlan
	decoder := json.NewDecoder(bytes.NewBufferString(raw.String))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_step_plan", "stored workflow plan is malformed", false, "restore a verified backup")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || string(plan.Type) != typ.String || validateStepPlanShape(plan) != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_step_plan", "stored workflow plan is invalid or does not match its type", false, "restore a verified backup")
	}
	return &plan, nil
}

// writeWorkflowPlanTx is the single validated plan-write boundary. Callers
// must hold the work item's version fence in the same write transaction.
func writeWorkflowPlanTx(ctx context.Context, tx *sql.Tx, workID string, next WorkflowStepPlan) error {
	old, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if err := ValidateStepPlanWrite(old, next); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return workflowProjectionError(err, "cannot encode the workflow plan")
	}
	// The first plan write leaves intake: the picked type starts the run.
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET workflow_type=?,step_plan=?,instance_state=CASE instance_state WHEN 'intake' THEN 'running' ELSE instance_state END WHERE work_id=?`, string(next.Type), string(raw), workID)
	if err != nil {
		return workflowProjectionError(err, "cannot write the workflow plan")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return newFailure(KindInvariantViolation, "workflow_step_plan", "workflow plan write did not update one instance", false, "reread the work item")
	}
	return nil
}
