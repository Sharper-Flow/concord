package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

const workflowCorrectionAttemptLimit int64 = 3

// WorkflowCorrectionContext is the bounded correction record projected for a
// work pin and for the next worker packet.
type WorkflowCorrectionContext struct {
	Disposition        string   `json:"disposition"`
	AttemptCount       int64    `json:"attempt_count"`
	AttemptLimit       int64    `json:"attempt_limit"`
	Escalated          bool     `json:"escalated"`
	PredicateIDs       []string `json:"predicate_ids"`
	EvidenceRefs       []string `json:"evidence_refs"`
	Diagnosis          string   `json:"diagnosis,omitempty"`
	Strategy           string   `json:"strategy,omitempty"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureDetail      string   `json:"failure_detail,omitempty"`
	FailedAttemptID    string   `json:"failed_attempt_id,omitempty"`
	FailedAttemptEpoch int64    `json:"failed_attempt_epoch,omitempty"`
}

func workflowCorrectionActionDefinition() WorkflowActionDefinition {
	return WorkflowActionDefinition{
		ID: "reject_worker_result", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionHold, RequiredCapability: "work_transition",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "attempt_epoch", ValueType: PayloadInteger, Required: true, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
			actionStringField("diagnosis", true, 4096), actionStringField("strategy", true, 4096), actionListField("predicate_ids", true, 1, 8), actionListField("evidence_refs", true, 1, 32),
		}},
	}
}

// workflowCorrectionContext reads the latest unconsumed failure or rejection.
// A later dispatch consumes the record, while all earlier worker attempts stay immutable.
func workflowCorrectionContext(ctx context.Context, q queryer, workID, stepID string) (*WorkflowCorrectionContext, error) {
	return workflowCorrectionContextForDispatch(ctx, q, workID, stepID, "")
}

func workflowCorrectionContextForDispatch(ctx context.Context, q queryer, workID, stepID, dispatchAttemptID string) (*WorkflowCorrectionContext, error) {
	query := `SELECT d.seq,d.payload FROM domain_events d
WHERE d.subject_type=? AND d.subject_id=? AND d.kind=?
  AND json_extract(d.payload,'$.action_id') IN ('record_worker_failure','reject_worker_result')
  AND NOT EXISTS (SELECT 1 FROM domain_events newer
    WHERE newer.subject_type=d.subject_type AND newer.subject_id=d.subject_id
      AND newer.kind=? AND newer.seq>d.seq
      AND json_extract(newer.payload,'$.action_id')='dispatch_worker'`
	args := []any{string(SubjectWorkItem), workID, WorkflowActionCompleted, WorkflowActionCompleted}
	if dispatchAttemptID != "" {
		query += ` AND json_extract(newer.payload,'$.worker_attempt_id')<>?`
		args = append(args, dispatchAttemptID)
	}
	query += `)
ORDER BY d.seq DESC LIMIT 1`
	var seq int64
	var raw []byte
	if err := q.QueryRowContext(ctx, query, args...).Scan(&seq, &raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read correction context", true, "retry once the workflow projection is readable", err)
	}
	var fields struct {
		ActionID             string   `json:"action_id"`
		AttemptID            string   `json:"worker_attempt_id"`
		AttemptEpoch         int64    `json:"attempt_epoch"`
		CorrectionDiagnosis  string   `json:"correction_diagnosis"`
		CorrectionStrategy   string   `json:"correction_strategy"`
		CorrectionPredicates []string `json:"correction_predicate_ids"`
		CorrectionEvidence   []string `json:"correction_evidence_refs"`
		ResultEvidence       []string `json:"result_evidence_refs"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction completion payload is malformed", false, "rebuild workflow projections from the event log")
	}
	if fields.AttemptID == "" {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction completion has no worker attempt", false, "rebuild workflow projections from the event log")
	}
	if stepID != "" {
		var actionStep string
		if err := q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.step_id') FROM domain_events WHERE seq=?`, seq).Scan(&actionStep); err == nil && actionStep != "" && actionStep != stepID {
			return nil, nil
		}
	}
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts a JOIN domain_events dispatch ON dispatch.subject_type=? AND dispatch.subject_id=a.work_id AND dispatch.kind=? AND json_extract(dispatch.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND dispatch.seq<=? AND dispatch.seq>COALESCE((SELECT MAX(accepted.seq) FROM domain_events accepted WHERE accepted.subject_type=dispatch.subject_type AND accepted.subject_id=dispatch.subject_id AND accepted.kind=? AND accepted.seq<? AND json_extract(accepted.payload,'$.action_id')='accept_worker_result'),0)`, string(SubjectWorkItem), WorkerDispatched, workID, seq, WorkflowActionCompleted, seq).Scan(&count); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot count worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	if count < 1 {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction has no worker attempt history", false, "rebuild the worker attempt projection")
	}
	var failureKind, failureDetail string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(failure_kind,''),COALESCE(failure_detail,'') FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, fields.AttemptID).Scan(&failureKind, &failureDetail); err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read worker attempt detail", true, "retry once the worker attempt projection is readable", err)
	}
	if fields.ActionID == "record_worker_failure" && fields.CorrectionDiagnosis == "" {
		fields.CorrectionDiagnosis = failureDetail
		if fields.CorrectionDiagnosis == "" {
			fields.CorrectionDiagnosis = "worker attempt failed"
		}
	}
	if fields.ActionID == "record_worker_failure" && fields.CorrectionStrategy == "" {
		fields.CorrectionStrategy = "retry with a fresh fenced attempt"
	}
	return &WorkflowCorrectionContext{
		Disposition: dispositionForCorrection(fields.ActionID), AttemptCount: count, AttemptLimit: workflowCorrectionAttemptLimit, Escalated: count >= workflowCorrectionAttemptLimit,
		PredicateIDs: nonNilStrings(fields.CorrectionPredicates), EvidenceRefs: nonNilStrings(append(fields.CorrectionEvidence, fields.ResultEvidence...)),
		Diagnosis: fields.CorrectionDiagnosis, Strategy: fields.CorrectionStrategy, FailureKind: failureKind, FailureDetail: failureDetail,
		FailedAttemptID: fields.AttemptID, FailedAttemptEpoch: fields.AttemptEpoch,
	}, nil
}

func dispositionForCorrection(actionID string) string {
	if actionID == "reject_worker_result" {
		return "rejected"
	}
	return "failed"
}

func workflowRejectedWorkerResultAvailable(ctx context.Context, q queryer, workID, currentStep, subject string) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	startSeq, _, found, err := latestWorkflowActionStart(ctx, q, workID, currentStep)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	var attemptID, lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT a.attempt_id,a.lifecycle_state FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND a.lifecycle_state='completed' AND d.seq>? AND NOT EXISTS (SELECT 1 FROM domain_events r WHERE r.subject_type=d.subject_type AND r.subject_id=d.subject_id AND r.kind=? AND r.seq<d.seq AND json_extract(r.payload,'$.worker_attempt_id')=a.attempt_id AND json_extract(r.payload,'$.action_id') IN ('accept_worker_result','reject_worker_result')) ORDER BY d.seq DESC LIMIT 1`, string(SubjectWorkItem), WorkerDispatched, workID, startSeq, WorkflowActionCompleted).Scan(&attemptID, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect completed worker results", true, "retry once the worker attempt projection is readable", err)
	}
	return attemptID != "" && lifecycle == "completed", nil
}

func validateWorkerPacketCorrection(ctx context.Context, q queryer, workID, currentStep string, packetRaw json.RawMessage) error {
	var packet struct {
		AttemptID string `json:"attempt_id"`
		Inputs    struct {
			Correction *WorkflowCorrectionContext `json:"correction"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, currentStep, packet.AttemptID)
	if err != nil {
		return err
	}
	if correction == nil {
		if packet.Inputs.Correction != nil {
			return newFailure(KindInvalidPayload, "workflow_action", "worker packet carries correction context without a durable correction", false, "build the packet from the current work pin")
		}
		return nil
	}
	if correction.Escalated {
		return newFailure(KindApprovalRequired, "workflow_action", "worker retry reached the three-attempt limit", false, "escalate the correction to the operator")
	}
	if packet.Inputs.Correction == nil || !sameWorkflowCorrection(packet.Inputs.Correction, correction) {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet does not consume the current correction context", false, "build a fresh packet from the current work pin")
	}
	return nil
}

func sameWorkflowCorrection(left, right *WorkflowCorrectionContext) bool {
	return left.Disposition == right.Disposition && left.AttemptCount == right.AttemptCount && left.AttemptLimit == right.AttemptLimit && left.Escalated == right.Escalated && left.Diagnosis == right.Diagnosis && left.Strategy == right.Strategy && sameCorrectionStrings(left.PredicateIDs, right.PredicateIDs) && sameCorrectionStrings(left.EvidenceRefs, right.EvidenceRefs)
}

func sameCorrectionStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
