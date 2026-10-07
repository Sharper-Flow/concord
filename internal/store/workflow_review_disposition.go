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

func workflowRefineReviewEvidenceAttempt(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step, subject string, beforeSeq int64) (string, error) {
	if !workflowRefineReviewEvidenceStep(definition, step) {
		return "", nil
	}
	var boundary int64
	var started bool
	var err error
	if beforeSeq == 0 {
		boundary, started, err = workflowStepPassBoundary(ctx, q, definition, workID, step, subject)
	} else {
		boundary, _, started, err = latestWorkflowActionStartAt(ctx, q, workID, step, beforeSeq)
	}
	if err != nil || !started {
		return "", err
	}
	var attemptID string
	err = q.QueryRowContext(ctx, `SELECT a.attempt_id
FROM worker_attempts a JOIN domain_events d ON d.subject_type='work_item'
 AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id
JOIN domain_events completed ON completed.subject_type=d.subject_type AND completed.subject_id=d.subject_id
 AND completed.kind=? AND json_extract(completed.payload,'$.attempt_id')=a.attempt_id
WHERE a.work_id=? AND a.capability_class='review' AND a.lifecycle_state='completed'
 AND a.readback_model<>'' AND d.seq>? AND (?=0 OR completed.seq<?)
 AND EXISTS (SELECT 1 FROM domain_events authorization WHERE authorization.subject_type=d.subject_type
  AND authorization.subject_id=d.subject_id AND authorization.kind=? AND authorization.seq>=?
  AND json_extract(authorization.payload,'$.action_id')='dispatch_worker'
  AND json_extract(authorization.payload,'$.step_id')=?
  AND json_extract(authorization.payload,'$.worker_attempt_id')=a.attempt_id)
 AND NOT EXISTS (SELECT 1 FROM domain_events r WHERE r.subject_type=d.subject_type
 AND r.subject_id=d.subject_id AND r.kind=? AND (?=0 OR r.seq<?)
  AND json_extract(r.payload,'$.worker_attempt_id')=a.attempt_id
  AND json_extract(r.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result'))
ORDER BY completed.seq DESC LIMIT 1`, WorkerDispatched, WorkerCompleted, workID, boundary, beforeSeq, beforeSeq, WorkflowActionCompleted, boundary, step, WorkflowActionCompleted, beforeSeq, beforeSeq).Scan(&attemptID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, subject, "cannot inspect refinement review evidence", true, "retry once the worker projection is readable", err)
	}
	stale, err := workflowStaleReviewAcceptBeforeTx(ctx, q, workID, attemptID, subject, beforeSeq)
	if err != nil || stale {
		return "", err
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
	_, epoch, started, err := latestWorkflowActionStartAt(ctx, q, workID, step, beforeSeq)
	if err != nil {
		return err
	}
	if !started || requestedEpoch != epoch {
		return newFailure(KindIllegalLifecycleTransition, subject, "review evidence attempt epoch does not match the current refinement pass", false, "use the epoch that dispatched the current review")
	}
	return nil
}

// An accepted refinement review reopens operator correction only when every
// authorization in this pass has a real terminal disposition. Authorization
// events count before native dispatch evidence exists.
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
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events e JOIN worker_attempts a
 ON a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.worker_attempt_id')
 WHERE e.subject_type='work_item' AND e.subject_id=? AND e.kind=? AND e.seq>?
 AND json_extract(e.payload,'$.step_id')=? AND json_extract(e.payload,'$.action_id')='accept_worker_evidence'
 AND a.capability_class='review' AND a.lifecycle_state='completed')`, workID, WorkflowActionCompleted, boundary, step).Scan(&accepted); err != nil {
		return workflowRefineReviewCorrectionNotApplicable, wrapFailure(KindUnavailable, subject, "cannot inspect accepted refinement review evidence", true, "retry once the workflow projection is readable", err)
	}
	if accepted == 0 {
		return workflowRefineReviewCorrectionNotApplicable, nil
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events opening
 WHERE opening.subject_type='work_item' AND opening.subject_id=? AND opening.kind=? AND opening.seq>=?
 AND json_extract(opening.payload,'$.step_id')=? AND json_extract(opening.payload,'$.action_id')='dispatch_worker'
 AND NOT EXISTS(SELECT 1 FROM domain_events disposition WHERE disposition.subject_type=opening.subject_type
  AND disposition.subject_id=opening.subject_id AND disposition.kind=? AND disposition.seq>opening.seq
  AND json_extract(disposition.payload,'$.worker_attempt_id')=json_extract(opening.payload,'$.worker_attempt_id')
  AND json_extract(disposition.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure')))`, workID, WorkflowActionCompleted, boundary, step, WorkflowActionCompleted).Scan(&unsettled); err != nil {
		return workflowRefineReviewCorrectionNotApplicable, wrapFailure(KindUnavailable, subject, "cannot inspect refinement authorization dispositions", true, "retry once the workflow projection is readable", err)
	}
	if unsettled != 0 {
		return workflowRefineReviewCorrectionClosed, nil
	}
	return workflowRefineReviewCorrectionOpen, nil
}
