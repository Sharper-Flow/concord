package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Refinement evidence disposition uses the existing hold-mode acceptance;
// neither the frozen definition nor the combined delivery route changes.
func workflowRefineReviewEvidenceStep(definition WorkflowDefinition, step string) bool {
	return workflowAcceptDeliveryAdmissionActive(definition, step) &&
		workflowActionDefinitionByID(definition, "accept_worker_evidence").ID != ""
}

func workflowRefineReviewEvidenceAttempt(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step, subject string, _ int64) (string, error) {
	if !workflowRefineReviewEvidenceStep(definition, step) {
		return "", nil
	}
	boundary, started, err := workflowStepPassBoundary(ctx, q, definition, workID, step, subject)
	if err != nil || !started {
		return "", err
	}
	var attemptID string
	err = q.QueryRowContext(ctx, `SELECT a.attempt_id
FROM worker_attempts a JOIN worker_authorizations authorization
 ON authorization.work_id=a.work_id AND authorization.attempt_id=a.attempt_id
WHERE a.work_id=? AND a.capability_class='review' AND a.lifecycle_state='completed'
 AND a.readback_model<>'' AND a.dispatch_seq>? AND a.terminal_seq>0
 AND authorization.seq>=? AND json_extract(authorization.payload,'$.step_id')=?
 AND NOT EXISTS (SELECT 1 FROM worker_attempt_dispositions r
  WHERE r.work_id=a.work_id AND r.attempt_id=a.attempt_id
  AND r.action_id IN ('accept_worker_result','accept_worker_evidence','reject_worker_result'))
ORDER BY a.terminal_seq DESC LIMIT 1`, workID, boundary, boundary, step).Scan(&attemptID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, subject, "cannot inspect refinement review evidence", true, "retry once the worker projection is readable", err)
	}
	return attemptID, nil
}

func validateRefineReviewEvidenceDisposition(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step string, fields map[string]json.RawMessage, subject string, beforeSeq int64) error {
	if workflowAcceptCarriesDeliveryAssertion(fields) {
		return newFailure(KindInvalidPayload, subject, "review evidence acceptance cannot carry delivery fields", false, "assert delivery through the declared delivery route")
	}
	return validateRefineReviewEvidenceAttempt(ctx, q, workID, definition, step, workflowFieldStringDefault(fields, "attempt_id", ""), workflowFieldInt(fields, "attempt_epoch", 0), subject, beforeSeq)
}

func validateRefineReviewEvidenceAttempt(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step, requestedAttempt string, requestedEpoch int64, subject string, beforeSeq int64) error {
	attemptID, err := workflowRefineReviewEvidenceAttempt(ctx, q, workID, definition, step, subject, beforeSeq)
	if err != nil {
		return err
	}
	if attemptID == "" || requestedAttempt != attemptID {
		return newFailure(KindIllegalLifecycleTransition, subject, "review evidence acceptance requires the current undispositioned completed review", false, "accept the exact current review attempt")
	}
	_, epoch, started, err := latestWorkflowActionStart(ctx, q, workID, step)
	if err != nil {
		return err
	}
	if !started || requestedEpoch != epoch {
		return newFailure(KindIllegalLifecycleTransition, subject, "review evidence attempt epoch does not match the current refinement pass", false, "use the epoch that dispatched the current review")
	}
	return workflowAcceptanceSubjectCurrent(ctx, q, workID, []string{attemptID}, nil)
}

// An accepted refinement review reopens operator correction only when every
// authorization in this pass has a real terminal disposition. Authorizations
// count before native dispatch evidence exists.
type workflowRefineReviewCorrectionState uint8

const (
	workflowRefineReviewCorrectionNotApplicable workflowRefineReviewCorrectionState = iota
	workflowRefineReviewCorrectionClosed
	workflowRefineReviewCorrectionOpen
)

func workflowRefineReviewCorrectionDecision(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step, subject string, boundary int64) (workflowRefineReviewCorrectionState, error) {
	if !workflowRefineReviewEvidenceStep(definition, step) {
		return workflowRefineReviewCorrectionNotApplicable, nil
	}
	var accepted, unsettled int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempt_dispositions e JOIN worker_attempts a
 ON a.work_id=e.work_id AND a.attempt_id=e.attempt_id
 WHERE e.work_id=? AND e.seq>?
 AND json_extract(e.payload,'$.step_id')=? AND e.action_id='accept_worker_evidence'
 AND a.capability_class='review' AND a.lifecycle_state='completed')`, workID, boundary, step).Scan(&accepted); err != nil {
		return workflowRefineReviewCorrectionNotApplicable, wrapFailure(KindUnavailable, subject, "cannot inspect accepted refinement review evidence", true, "retry once the workflow projection is readable", err)
	}
	if accepted == 0 {
		return workflowRefineReviewCorrectionNotApplicable, nil
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_authorizations opening
 WHERE opening.work_id=? AND opening.seq>=?
 AND json_extract(opening.payload,'$.step_id')=?
 AND NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions disposition
  WHERE disposition.work_id=opening.work_id AND disposition.attempt_id=opening.attempt_id
  AND disposition.seq>opening.seq))`, workID, boundary, step).Scan(&unsettled); err != nil {
		return workflowRefineReviewCorrectionNotApplicable, wrapFailure(KindUnavailable, subject, "cannot inspect refinement authorization dispositions", true, "retry once the workflow projection is readable", err)
	}
	if unsettled != 0 {
		return workflowRefineReviewCorrectionClosed, nil
	}
	return workflowRefineReviewCorrectionOpen, nil
}
