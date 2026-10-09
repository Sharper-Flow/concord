package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// WorkflowRetryConvergence records the stored facts that admit one escalated
// dispatch. Sequence references point into this work item's event history.
type WorkflowRetryConvergence struct {
	Basis             string `json:"basis"`
	PreviousRecordSeq int64  `json:"previous_record_seq,omitempty"`
	LatestRecordSeq   int64  `json:"latest_record_seq,omitempty"`
	SupersededSeq     int64  `json:"superseded_seq,omitempty"`
}

func (s WorkflowAdmissionState) retryEscalated() bool {
	return s.CorrectionEscalated || s.NonProgressAttempts >= workflowCorrectionAttemptLimit
}

func workflowRetryConvergenceFailure(state WorkflowAdmissionState) *Failure {
	detail := "worker correction reached the three-attempt limit without a convergence basis"
	if state.NonProgressAttempts >= workflowCorrectionAttemptLimit {
		detail += fmt.Sprintf(": %d non-progress attempts since the last accepted productive result (step %s)", state.NonProgressAttempts, state.Step)
	}
	return newFailure(KindMissingEvidence, "worker_dispatch", detail, false,
		"record shrinking open_finding_ids on the next rejected result, supersede_contract with a changed approach, or stop the work")
}

// workflowRetryConvergence derives a basis rather than trusting a caller's
// assessment. The latest dispatch consumes both bases, including an authorized
// dispatch whose host worker never materialized.
func workflowRetryConvergence(ctx context.Context, q queryer, workID, stepID string) (WorkflowRetryConvergence, error) {
	var lastDispatch, superseded int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker'`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&lastDispatch); err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot read the latest dispatch for convergence")
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>?`, string(SubjectWorkItem), workID, WorkflowContractSuperseded, lastDispatch).Scan(&superseded); err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot read contract supersession for convergence")
	}
	if superseded != 0 {
		return WorkflowRetryConvergence{Basis: "approach_changed", SupersededSeq: superseded}, nil
	}
	history, err := workflowCorrectionWalk(ctx, q, workID, int64(1)<<62, "worker_convergence")
	if err != nil || len(history.records) == 0 {
		return WorkflowRetryConvergence{}, err
	}
	latest := history.records[len(history.records)-1]
	if latest.seq <= lastDispatch || latest.fields.ActionID == "record_worker_failure" {
		return WorkflowRetryConvergence{}, nil
	}
	boundary := history.boundary
	if latest.fields.ActionID == "request_correction" {
		boundary, err = workflowCorrectionActiveHealthyBaseline(ctx, q, workID, latest.seq, "worker_convergence")
		if err != nil {
			return WorkflowRetryConvergence{}, err
		}
	} else if latest.fields.StepID != stepID {
		return WorkflowRetryConvergence{}, nil
	}
	if latest.seq <= boundary {
		return WorkflowRetryConvergence{}, nil
	}
	for i := len(history.records) - 2; i >= 0; i-- {
		previous := history.records[i]
		if previous.seq <= boundary {
			break
		}
		if previous.fields.ActionID != latest.fields.ActionID || previous.fields.StepID != latest.fields.StepID {
			continue
		}
		if workflowFindingsShrink(previous.fields.openFindings(), latest.fields.openFindings()) {
			return WorkflowRetryConvergence{Basis: "findings_shrinking", PreviousRecordSeq: previous.seq, LatestRecordSeq: latest.seq}, nil
		}
		break
	}
	return WorkflowRetryConvergence{}, nil
}

func (f workflowCorrectionCompletionFields) openFindings() []string {
	if f.ActionID == "request_correction" {
		return f.CorrectionPredicates
	}
	return f.OpenFindingIDs
}

func workflowFindingsShrink(previous, latest []string) bool {
	if !validWorkflowOpenFindings(previous) || !validWorkflowOpenFindings(latest) || len(latest) >= len(previous) {
		return false
	}
	for _, id := range latest {
		if !contains(previous, id) {
			return false
		}
	}
	return true
}

func validWorkflowOpenFindings(ids []string) bool {
	if len(ids) < 1 || len(ids) > 32 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] || !workflowCorrectionID(id) {
			return false
		}
		seen[id] = true
	}
	return true
}

func validateWorkflowOpenFindingsPayload(fields map[string]json.RawMessage) error {
	raw, present := fields["open_finding_ids"]
	if !present {
		return nil
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || !validWorkflowOpenFindings(ids) {
		return newFailure(KindInvalidPayload, "workflow_action", "open_finding_ids must contain 1 to 32 unique finding IDs", false, "supply the complete open finding set with stable IDs")
	}
	return nil
}

func (c WorkflowRetryConvergence) valid() bool {
	switch c.Basis {
	case "findings_shrinking":
		return c.PreviousRecordSeq > 0 && c.LatestRecordSeq > c.PreviousRecordSeq && c.SupersededSeq == 0
	case "approach_changed":
		return c.SupersededSeq > 0 && c.PreviousRecordSeq == 0 && c.LatestRecordSeq == 0
	}
	return false
}
