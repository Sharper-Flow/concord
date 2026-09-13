package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

const workflowWorkerAttemptLimit = 3

// WorkflowCorrectionContext is the bounded durable context for a new worker
// attempt. It is derived from a recorded worker failure or a rejected result.
type WorkflowCorrectionContext struct {
	Disposition    string   `json:"disposition"`
	AttemptID      string   `json:"attempt_id"`
	AttemptEpoch   int64    `json:"attempt_epoch"`
	AttemptCount   int      `json:"attempt_count"`
	AttemptLimit   int      `json:"attempt_limit"`
	Escalated      bool     `json:"escalated"`
	FailureKind    string   `json:"failure_kind,omitempty"`
	FailureDetail  string   `json:"failure_detail,omitempty"`
	EvidenceRefs   []string `json:"evidence_refs,omitempty"`
	PredicateIDs   []string `json:"predicate_ids,omitempty"`
	Diagnosis      string   `json:"diagnosis,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
}

func workflowCorrectionContext(ctx context.Context, q queryer, workID string) (*WorkflowCorrectionContext, error) {
	var attempt struct {
		ID, State, FailureKind, FailureDetail string
	}
	var dispatchedAt string
	err := q.QueryRowContext(ctx, `SELECT attempt_id,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failure_detail,''),dispatched_at FROM worker_attempts WHERE work_id=? ORDER BY dispatched_at DESC,attempt_id DESC LIMIT 1`, workID).Scan(&attempt.ID, &attempt.State, &attempt.FailureKind, &attempt.FailureDetail, &dispatchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the latest worker attempt", true, "retry once the worker attempt projection is readable", err)
	}
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts WHERE work_id=?`, workID).Scan(&count); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot count worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	var actionID, raw string
	err = q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.action_id'),payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=? AND json_extract(payload,'$.action_id') IN ('record_worker_failure','reject_worker_result') ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attempt.ID).Scan(&actionID, &raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read worker correction evidence", true, "retry once the workflow history is readable", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "worker correction evidence is malformed", false, "rebuild workflow projections from the event log")
	}
	context := &WorkflowCorrectionContext{
		Disposition:   "failed",
		AttemptID:     attempt.ID,
		AttemptCount:  count,
		AttemptLimit:  workflowWorkerAttemptLimit,
		Escalated:     count >= workflowWorkerAttemptLimit,
		FailureKind:   attempt.FailureKind,
		FailureDetail: attempt.FailureDetail,
		EvidenceRefs:  workflowRawStrings(fields, "correction_evidence_refs"),
		PredicateIDs:  workflowRawStrings(fields, "correction_predicate_ids"),
		Diagnosis:     workflowRawString(fields, "correction_diagnosis"),
		Strategy:      workflowRawString(fields, "correction_strategy"),
	}
	if actionID == "reject_worker_result" {
		context.Disposition = "rejected"
		if context.FailureDetail == "" {
			context.FailureDetail = workflowRawString(fields, "rejection_reason")
		}
	}
	var epochRaw sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.attempt_epoch'),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attempt.ID, actionID).Scan(&epochRaw); err == nil && epochRaw.Valid {
		context.AttemptEpoch = epochRaw.Int64
	}
	return context, nil
}

func workflowRawString(fields map[string]json.RawMessage, name string) string {
	var value string
	if raw := fields[name]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

func workflowRawStrings(fields map[string]json.RawMessage, name string) []string {
	var values []string
	if raw := fields[name]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &values)
	}
	if values == nil {
		return []string{}
	}
	return values
}

func workflowCorrectionAvailable(ctx context.Context, q queryer, workID string) (bool, *WorkflowCorrectionContext, error) {
	correction, err := workflowCorrectionContext(ctx, q, workID)
	if err != nil || correction == nil {
		return false, correction, err
	}
	if correction.Escalated {
		return false, correction, nil
	}
	if correction.Diagnosis == "" {
		return false, correction, nil
	}
	return true, correction, nil
}

func workflowCompletedWorkerRejectionAvailable(ctx context.Context, q queryer, workID string) (bool, *WorkflowCorrectionContext, error) {
	var attemptID, state string
	if err := q.QueryRowContext(ctx, `SELECT attempt_id,lifecycle_state FROM worker_attempts WHERE work_id=? ORDER BY dispatched_at DESC,attempt_id DESC LIMIT 1`, workID).Scan(&attemptID, &state); err != nil {
		if err == sql.ErrNoRows {
			return false, nil, nil
		}
		return false, nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the latest worker attempt", true, "retry once the worker attempt projection is readable", err)
	}
	if state != "completed" {
		return false, nil, nil
	}
	var accepted, rejected int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN json_extract(payload,'$.action_id')='accept_worker_result' THEN 1 ELSE 0 END),0),COALESCE(sum(CASE WHEN json_extract(payload,'$.action_id')='reject_worker_result' THEN 1 ELSE 0 END),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&accepted, &rejected); err != nil {
		return false, nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot inspect worker result disposition", true, "retry once the workflow history is readable", err)
	}
	if accepted != 0 || rejected != 0 {
		correction, err := workflowCorrectionContext(ctx, q, workID)
		return false, correction, err
	}
	return true, nil, nil
}
