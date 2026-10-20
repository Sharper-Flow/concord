package store

import (
	"context"
	"slices"
)

// WorkflowProgressAnchor names the actual global event-log position, not the
// history page's per-kind version or a timestamp inferred from a report.
type WorkflowProgressAnchor struct {
	EventID       string `json:"event_id"`
	Kind          string `json:"kind"`
	Seq           int64  `json:"seq"`
	Qualification string `json:"qualification"`
	ObligationID  string `json:"obligation_id,omitempty"`
	// ActionID names the workflow action whose recorded completion is the
	// anchored boundary, where the anchored event carries one. A verdict,
	// evidence, or premise event anchors without one.
	ActionID string `json:"action_id,omitempty"`
}

// WorkflowRetryWindow is the CD-0164 D5 readable view of the retry walls at
// one work item. Every count is the admission derivation's own answer, read
// from the same immutable event-prefix interpretation; the window persists
// no independent counter or anchor authority.
type WorkflowRetryWindow struct {
	// Population is the whole-work nonprogress population (CD-0164 D1);
	// Count blocks at Count >= Limit.
	Population string `json:"population"`
	// Policy is the walked definition's progress policy (CD-0164 D2a), or
	// the legacy worker-acceptance policy when the recorded pin declares none.
	Policy string `json:"policy"`
	Count  int64  `json:"count"`
	Limit  int64  `json:"limit"`
	// Prefix is the shared counting prefix both attempt populations were
	// counted at: the walked event-log position.
	Prefix int64 `json:"prefix"`
	// NonprogressAnchor names the qualified event that opened the current
	// nonprogress window, or null when the window opens at the log start.
	NonprogressAnchor *WorkflowProgressAnchor `json:"nonprogress_anchor"`
	// WorkerSatisfactionAnchor names the productive acceptance that closed
	// the acceptance window (CD-0164 D2), or null. The dispatch population
	// counts from this boundary, not the nonprogress one: coordinator-side
	// satisfaction renews the nonprogress window alone (CD-0164 D2a).
	WorkerSatisfactionAnchor *WorkflowProgressAnchor `json:"worker_satisfaction_anchor"`
	// VerificationBaseline names the comparable-healthy verdict that opens
	// the verification-request window (CD-0164 D4), or null. A complete-step
	// cutoff above the latest healthy verdict is a boundary with no
	// qualifying verdict event, so the anchor stays null; the population
	// still counts from the cutoff.
	VerificationBaseline *WorkflowProgressAnchor `json:"verification_baseline"`
	// DispatchCount is the open correction window's dispatched attempts
	// since the last accepted productive result; it escalates at
	// DispatchCount >= DispatchLimit (CD-0164 D4).
	DispatchCount int64 `json:"dispatch_count"`
	DispatchLimit int64 `json:"dispatch_limit"`
	// VerificationCount is the recorded request_correction actions since
	// the baseline, including the open request; it escalates at
	// VerificationCount > VerificationLimit (CD-0164 D4).
	VerificationCount int64 `json:"verification_count"`
	VerificationLimit int64 `json:"verification_limit"`
	// Step is the workflow step the window's counts and blocking population
	// were derived for.
	Step string `json:"step,omitempty"`
	// CorrectionEscalated reports the correction wall alone; only worker
	// dispatch is walled.
	CorrectionEscalated bool   `json:"correction_escalated"`
	BlockingPopulation  string `json:"blocking_population"`
	// ConvergenceBasis is the one-use basis a convergence-admitted retry
	// consumes, or empty when none stands. A consumed basis renews no
	// window.
	ConvergenceBasis string `json:"convergence_basis"`
	// FundedConfirmingReviews counts the funded confirming-review
	// admissions (CD-0205 D4) beside the wall; each funds one dispatch.
	FundedConfirmingReviews      int64    `json:"funded_confirming_reviews"`
	UnresolvedObligations        []string `json:"unresolved_obligations"`
	UnresolvedObligationCount    int64    `json:"unresolved_obligation_count"`
	OmittedUnresolvedObligations int64    `json:"omitted_unresolved_obligations"`
}

const WorkflowRetryWindowObligationLimit = 32

// workflowRetryWindowRead derives the readable retry window from one
// correction walk plus the admission wall's own derivations. The workflow
// read projection calls it where no folded admission state exists.
func workflowRetryWindowRead(ctx context.Context, q queryer, workID, step, subject string) (*WorkflowRetryWindow, error) {
	return workflowRetryWindowForRegistry(ctx, q, workID, step, subject, nil)
}

// workflowRetryWindowForRegistry derives the same window with the caller's
// authoritative registry resolving the recorded selections the progress
// qualification verifies. A nil registry selects the builtin registry.
func workflowRetryWindowForRegistry(ctx context.Context, q queryer, workID, step, subject string, registry DefinitionRegistry) (*WorkflowRetryWindow, error) {
	h, err := workflowCorrectionWalkForRegistry(ctx, q, workID, int64(1)<<62, subject, registry)
	if err != nil {
		return nil, err
	}
	count, err := workflowNonProgressAttemptCountAfter(ctx, q, workID, subject, h.nonprogressBoundary)
	if err != nil {
		return nil, err
	}
	escalated, err := workflowCorrectionWindowEscalated(ctx, q, workID, step, subject)
	if err != nil {
		return nil, err
	}
	return workflowRetryWindowCore(ctx, q, workID, step, subject, h, count, escalated, nil, nil, registry)
}

// workflowRetryWindowForState derives the same window from the caller's
// already-folded admission state, so the work pin reuses the loader's wall
// answer instead of re-entering the loader.
func workflowRetryWindowForState(ctx context.Context, q queryer, workID, step, subject string, state WorkflowAdmissionState, registry DefinitionRegistry) (*WorkflowRetryWindow, error) {
	h, err := workflowCorrectionWalkForRegistry(ctx, q, workID, int64(1)<<62, subject, registry)
	if err != nil {
		return nil, err
	}
	convergence, funded := state.RetryConvergence, state.FundedChecksRepairReviews
	return workflowRetryWindowCore(ctx, q, workID, step, subject, h, state.NonProgressAttempts, state.CorrectionEscalated, &convergence, &funded, registry)
}

// workflowRetryWindowCore fills the window over one walked correction
// history. A non-nil convergence or funded pointer carries the caller's
// folded answer; a nil one derives it here, only while the wall stands.
func workflowRetryWindowCore(ctx context.Context, q queryer, workID, step, subject string, h *workflowCorrectionHistory, count int64, escalated bool, convergence *WorkflowRetryConvergence, funded *int, registry DefinitionRegistry) (*WorkflowRetryWindow, error) {
	out := &WorkflowRetryWindow{
		Population: "distinct_failed_rejected_no_ship", Policy: h.progress.definition.ProgressPolicy,
		Count: count, Limit: workflowCorrectionAttemptLimit, Prefix: h.progress.prefix,
		DispatchCount: 0, DispatchLimit: workflowCorrectionAttemptLimit,
		VerificationCount: 0, VerificationLimit: workflowCorrectionAttemptLimit,
		Step: step, BlockingPopulation: "none", UnresolvedObligations: []string{},
	}
	if out.Policy == "" {
		out.Policy = "worker_acceptance"
	}
	anchor := func(seq int64, qualification, obligation string) (*WorkflowProgressAnchor, error) {
		if seq == 0 {
			return nil, nil
		}
		v := &WorkflowProgressAnchor{Seq: seq, Qualification: qualification, ObligationID: obligation}
		var actionID string
		if err := q.QueryRowContext(ctx, `SELECT event_id,kind,COALESCE(json_extract(payload,'$.action_id'),'') FROM domain_events WHERE seq=? AND subject_type='work_item' AND subject_id=?`, seq, workID).Scan(&v.EventID, &v.Kind, &actionID); err != nil {
			return nil, workflowProjectionError(err, "cannot read the retry window anchor")
		}
		if actionID != "" {
			v.ActionID = actionID
		}
		return v, nil
	}
	var err error
	if out.NonprogressAnchor, err = anchor(h.nonprogressBoundary, h.progress.qualification, h.progress.obligation); err != nil {
		return nil, err
	}
	if out.WorkerSatisfactionAnchor, err = anchor(h.boundary, "satisfying_worker_acceptance", "worker_result"); err != nil {
		return nil, err
	}
	// The verification population's counting prefix and anchor. A missing
	// active contract opens the baseline at the log start, so every recorded
	// request counts; a duplicated projection degrades the same way instead
	// of refusing, and invents no anchor.
	baseline, baselineErr := workflowCorrectionActiveHealthyBaseline(ctx, q, workID, h.progress.prefix+1, subject)
	if baselineErr != nil && !workflowDuplicateContractProjection(baselineErr) {
		return nil, baselineErr
	}
	if baseline != 0 {
		v, anchorErr := anchor(baseline, "comparable_healthy_verdict_baseline", "")
		if anchorErr != nil {
			return nil, anchorErr
		}
		// A complete-step cutoff can win the baseline. The boundary then
		// sits on a supersession event, and no comparable-healthy verdict
		// exists to name: the explicit null is the truthful answer.
		if v.Kind == WorkflowVerdictRecorded {
			out.VerificationBaseline = v
		}
	}
	if out.VerificationCount, err = workflowVerificationRequestCountSince(ctx, q, workID, subject, baseline, h.progress.prefix); err != nil {
		return nil, err
	}
	// The dispatch population counts from this fold's acceptance boundary,
	// so the count and the worker-satisfaction anchor name one window
	// (CD-0164 D2) with no second fold.
	if out.DispatchCount, err = workflowCorrectionAttemptCountSinceBoundary(ctx, q, workID, h.boundary, h.progress.prefix, subject); err != nil {
		return nil, err
	}
	nonprogress := out.Count >= out.Limit
	switch {
	case escalated && nonprogress:
		out.BlockingPopulation = "both"
	case escalated:
		out.BlockingPopulation = "correction"
	case nonprogress:
		out.BlockingPopulation = "nonprogress"
	}
	out.CorrectionEscalated = escalated
	if out.BlockingPopulation != "none" {
		if convergence == nil {
			basis, basisErr := workflowRetryConvergenceForRegistry(ctx, q, workID, step, registry)
			if basisErr != nil {
				return nil, basisErr
			}
			convergence = &basis
		}
		out.ConvergenceBasis = convergence.Basis
		if funded == nil {
			fundings, fundingErr := workerChecksRepairReviewFundingsTx(ctx, q, workID)
			if fundingErr != nil {
				return nil, fundingErr
			}
			funded = new(int)
			*funded = len(fundings)
		}
		out.FundedConfirmingReviews = int64(*funded)
	}
	for key := range h.unresolved {
		out.UnresolvedObligations = append(out.UnresolvedObligations, key)
	}
	slices.Sort(out.UnresolvedObligations)
	out.UnresolvedObligationCount = int64(len(out.UnresolvedObligations))
	if len(out.UnresolvedObligations) > WorkflowRetryWindowObligationLimit {
		out.OmittedUnresolvedObligations = int64(len(out.UnresolvedObligations) - WorkflowRetryWindowObligationLimit)
		out.UnresolvedObligations = out.UnresolvedObligations[:WorkflowRetryWindowObligationLimit]
	}
	return out, nil
}
