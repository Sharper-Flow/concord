package store

import (
	"database/sql"
	"encoding/json"
)

// A clean first editing result at its own dispatch commit changed nothing.
// Later attempts may deliver a retained draft from an earlier failed attempt;
// without a recorded job base, commit equality cannot judge those corrections.
func guardAcceptWorkerResultChanges(g *workflowActionGuardContext, fields map[string]json.RawMessage) error {
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	var raw []byte
	err := g.tx.QueryRowContext(g.ctx, `SELECT e.payload FROM domain_events e
		JOIN worker_attempts a ON a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.attempt_id')
		WHERE e.subject_type=? AND e.subject_id=? AND e.kind=? AND a.attempt_id=?
		AND a.capability_class='implementation' AND a.lifecycle_state='completed'
		ORDER BY e.seq DESC LIMIT 1`, string(SubjectWorkItem), g.request.WorkID, WorkerCompleted, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return workflowProjectionError(err, "cannot read the completed editing result")
	}
	var completed WorkerCompletedPayload
	if err := json.Unmarshal(raw, &completed); err != nil {
		return newFailure(KindInvariantViolation, "workflow_action", "the completed editing result is malformed", false, "rebuild workflow projections from the event log")
	}
	if completed.WorkerJob == nil || completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" {
		return nil
	}
	window, err := findAuthorizedDispatchWindowCore(g.ctx, g.tx, g.request.WorkID, attemptID)
	if err != nil {
		return err
	}
	if window.SubjectCommit == "" || window.SubjectCommit != completed.ObservedDelivery.Commit {
		return nil
	}
	var firstAttempt string
	err = g.tx.QueryRowContext(g.ctx, `SELECT a.attempt_id FROM domain_events e
		JOIN worker_attempts a ON a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.worker_attempt_id')
		WHERE e.subject_type=? AND e.subject_id=? AND e.kind=?
		AND json_extract(e.payload,'$.action_id')='dispatch_worker'
		AND json_extract(e.payload,'$.worker_job.job_id')=? AND a.capability_class='implementation'
		ORDER BY e.seq LIMIT 1`, string(SubjectWorkItem), g.request.WorkID, WorkflowActionCompleted, completed.WorkerJob.JobID).Scan(&firstAttempt)
	if err != nil {
		return workflowProjectionError(err, "cannot read the editing job's first authorization")
	}
	if firstAttempt != attemptID {
		return nil
	}
	return newFailure(KindInvalidOperation, "workflow_action", "accept_worker_result: the editing job's first result has no changed paths; its clean observed delivery equals its dispatch subject", false,
		"reject the result with reject_worker_result and dispatch a correction that delivers the recorded job")
}
