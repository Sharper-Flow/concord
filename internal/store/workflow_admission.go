package store

import (
	"context"
	"database/sql"
)

// WorkflowReviewDebt names the post-rejection review debt state the admission
// fold derives from one work item's refinement history.
type WorkflowReviewDebt string

const (
	// ReviewDebtNone reports no rejected refinement result awaits a fresh
	// accepted review.
	ReviewDebtNone WorkflowReviewDebt = "none"
	// ReviewDebtOutstanding reports a rejected refinement result whose fresh
	// accepted review — verdict ship or absent — has not been accepted.
	ReviewDebtOutstanding WorkflowReviewDebt = "outstanding"
)

// WorkflowAdmissionState is the folded abstract state the post-rejection
// review admission decides over. The tx-scoped loader derives it once from
// the work item's history, and workflowAdmit consumes it, so every admission
// site that loads and admits answers identically for the same state instead
// of re-deriving the debt per site.
type WorkflowAdmissionState struct {
	// Step is the work item's current workflow step.
	Step string
	// CorrectionWorkflow reports whether the pinned definition carries the
	// correction review shape at all.
	CorrectionWorkflow bool
	// ReviewStep reports whether the current step's advance carries an
	// unreviewed repaired result toward delivery: the refinement step that
	// enters the delivery gate, and the gate itself.
	ReviewStep bool
	// ReviewDebt is the folded post-rejection review debt.
	ReviewDebt WorkflowReviewDebt
	// ReadyReviewAttemptID names the completed review attempt whose
	// acceptance is the settling fresh review, or "" when none stands
	// ready: its dispatch postdates the debt's frontier and its typed
	// verdict settles (workflowReviewSettlesDebt).
	ReadyReviewAttemptID string
	// ReadyReviewVerdict is that attempt's typed review verdict, "" for the
	// pre-CD-0197 reports that carry none.
	ReadyReviewVerdict string
}

// WorkflowAdmissionDecision is the pure admission answer for one action over
// one folded state: whether the advance is admitted, the ready review the
// calling guard may bind the request's attempt identity against, and the
// typed refusal when the advance refuses.
type WorkflowAdmissionDecision struct {
	Admitted             bool
	ReadyReviewAttemptID string
	Failure              *Failure
}

// loadWorkflowAdmissionStateTx folds one work item's post-rejection review
// admission state in the caller's transaction. It takes a queryer rather than
// a *sql.Tx so a caller holding the pooled connection's transaction passes the
// tx itself and never a pool-backed store: a nested s.db call inside the
// transaction parks on the single pooled connection forever.
func loadWorkflowAdmissionStateTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (WorkflowAdmissionState, error) {
	state := WorkflowAdmissionState{
		Step:               currentStep,
		CorrectionWorkflow: workflowCorrectionWorkflow(definition),
		ReviewStep:         workflowPostRejectionReviewStep(definition, currentStep),
		ReviewDebt:         ReviewDebtNone,
	}
	if !state.CorrectionWorkflow || !state.ReviewStep {
		return state, nil
	}
	outstanding, err := workflowPostRejectionReviewOutstanding(ctx, q, workID, definition, subject)
	if err != nil || !outstanding {
		return state, err
	}
	state.ReviewDebt = ReviewDebtOutstanding
	readyAttemptID, readyVerdict, readyErr := workflowReadySettlingReviewAttemptTx(ctx, q, workID, definition, subject)
	if readyErr != nil {
		return state, readyErr
	}
	state.ReadyReviewAttemptID, state.ReadyReviewVerdict = readyAttemptID, readyVerdict
	return state, nil
}

// workflowAdmit decides one action's admission over one folded admission
// state. It is pure and total: no store access, no clock, no request payload,
// and a defined answer for every action the definitions declare. Outstanding
// post-rejection review debt hides only the advances toward delivery — the
// recorded delivery at a review step, and the refinement-step accept whose
// completion would carry the unreviewed result off refine — because the debt
// blocks a result, not the work: dispatch, recovery, and continuity actions
// stay admitted so a parked item always keeps a route. The payload and
// identity checks stay in the guards; the accept guard binds the request's
// attempt against the decision's ReadyReviewAttemptID.
func workflowAdmit(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) WorkflowAdmissionDecision {
	decision := WorkflowAdmissionDecision{ReadyReviewAttemptID: state.ReadyReviewAttemptID}
	if !state.CorrectionWorkflow || state.ReviewDebt != ReviewDebtOutstanding {
		decision.Admitted = true
		return decision
	}
	refineStep := stepDeclaresAction(definition, state.Step, "start_refine")
	switch actionID {
	case "record_delivery":
		if state.ReviewStep {
			return workflowAdmitFreshReviewRefusal(decision)
		}
	case "accept_worker_result":
		if refineStep {
			return workflowAdmitFreshReviewRefusal(decision)
		}
	}
	decision.Admitted = true
	return decision
}

// workflowAdmitFreshReviewRefusal is the typed refusal both debt-hidden
// advances carry: the work waits for a fresh accepted review of the repaired
// result before delivery.
func workflowAdmitFreshReviewRefusal(decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "the advance toward delivery requires a fresh accepted review of the repaired result", false, "dispatch a review attempt at the refinement step, accept its result, then advance")
	return decision
}

// workflowReadySettlingReviewAttemptTx names the latest completed review
// attempt whose dispatch postdates the debt's frontier and whose typed
// verdict settles, with that verdict, or "" when none stands ready. It is the
// loader's read of the ready-review predicate the work pin and the accept
// guard share.
func workflowReadySettlingReviewAttemptTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, subject string) (string, string, error) {
	frontier, err := workflowPostRejectionFrontier(ctx, q, workID, definition, subject)
	if err != nil || frontier == 0 {
		return "", "", err
	}
	var attemptID, verdict string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(wc.payload,'$.attempt_id'),''), COALESCE(json_extract(wc.payload,'$.review.verdict'),'') FROM domain_events wc JOIN domain_events wd ON wd.subject_type=wc.subject_type AND wd.subject_id=wc.subject_id AND wd.kind=? AND json_extract(wd.payload,'$.attempt_id')=json_extract(wc.payload,'$.attempt_id') AND json_extract(wd.payload,'$.capability_class')='review' WHERE wc.subject_type=? AND wc.subject_id=? AND wc.kind=? AND wc.seq>? AND `+workflowReviewSettlesDebtSQL+` ORDER BY wc.seq DESC LIMIT 1`, WorkerDispatched, string(SubjectWorkItem), workID, WorkerCompleted, frontier).Scan(&attemptID, &verdict); err != nil {
		if err == sql.ErrNoRows {
			return "", "", nil
		}
		return "", "", wrapFailure(KindUnavailable, subject, "cannot read the completed review history", true, "retry once the worker delivery projection is readable", err)
	}
	return attemptID, verdict, nil
}
