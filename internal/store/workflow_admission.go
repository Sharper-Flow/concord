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

// WorkflowAdmissionState is the folded abstract state workflow action
// admission decides over. The tx-scoped loader derives it once from one work
// item's history — step, lifecycle, contract count, law pin staleness,
// impact and external conditions, design currency, attempt state and
// capability class, latest result disposition, review debt and its settling
// verdict, the recovery routes, the same-step wall, the dispatch hold, and
// the outstanding evidence — and workflowAdmit consumes it, so every
// admission site that loads and admits answers identically for the same
// state instead of re-deriving the conditions per site.
type WorkflowAdmissionState struct {
	// Step is the work item's current workflow step.
	Step string
	// Lifecycle is the work item's lifecycle.
	Lifecycle string
	// InstanceState is the workflow instance state.
	InstanceState string
	// CorrectionWorkflow reports whether the pinned definition carries the
	// correction review shape at all.
	CorrectionWorkflow bool
	// ReviewStep reports whether the current step's advance carries an
	// unreviewed repaired result toward delivery: the refinement step that
	// enters the delivery gate, and the gate itself.
	ReviewStep bool
	// ActiveContracts is the count of active workflow contracts.
	ActiveContracts int64
	// LawPinStale reports a staleness the contract recovery admits: a stale
	// law revision, an unresolved Domain overlap, or the subject's own stale
	// registry pin.
	LawPinStale bool
	// LawPinStaleError is the staleness boundary's refusal under the plain
	// admission, nil when the pinned law is current.
	LawPinStaleError error
	// LawPinSelfError is the staleness boundary's refusal when the subject's
	// own stale pin admits, nil when the boundary passes under that
	// admission.
	LawPinSelfError error
	// BreakingNotices is the count of breaking hard-edge impact notices
	// targeting the work.
	BreakingNotices int64
	// ExternalConditionsOpen is the count of open consequential external
	// conditions.
	ExternalConditionsOpen int64
	// DesignStale reports a recorded design a contract correction
	// invalidated.
	DesignStale bool
	// AttemptState is the latest worker attempt's lifecycle state.
	AttemptState string
	// AttemptCapabilityClass is that attempt's dispatch capability class.
	AttemptCapabilityClass string
	// LatestResultDisposition is the latest disposition action id over the
	// worker results: accept_worker_result, accept_worker_evidence,
	// reject_worker_result, record_worker_failure, or "" when none stands.
	LatestResultDisposition string
	// ReviewDebt is the folded post-rejection review debt.
	ReviewDebt WorkflowReviewDebt
	// ReadyReviewAttemptID names the completed review attempt whose
	// acceptance is the settling fresh review, or "" when none stands
	// ready: its dispatch postdates the debt's frontier and its typed
	// verdict settles (workflowReviewSettlesDebt).
	ReadyReviewAttemptID string
	// ReadyReviewVerdict is that attempt's typed review verdict, "" for the
	// pre-CD-0197 reports that carry none. A no_ship verdict stands ready like
	// any other: its acceptance binds the findings and settles nothing
	// (ReadyReviewSettles reports that).
	ReadyReviewVerdict string
	// ReadyReviewSettles reports whether that ready verdict settles the debt
	// (workflowReviewSettlesDebt): only a ship, or absent pre-CD-0197,
	// verdict settles, so a ready no_ship review keeps the debt outstanding.
	ReadyReviewSettles bool
	// LateVerdictRoute reports a late record_verdict recovery route the
	// pinned shape opens at the current step.
	LateVerdictRoute bool
	// WorkerFailureRecovery reports an unsettled failed attempt the
	// record_worker_failure recovery can record.
	WorkerFailureRecovery bool
	// CorrectionRecovery reports a completed result the reject_worker_result
	// recovery can reject.
	CorrectionRecovery bool
	// CorrectionRequestRecovery reports the delivery-gate corrective return
	// stands open, with CorrectionRequestMissing naming the one missing
	// prerequisite when it does not.
	CorrectionRequestRecovery bool
	CorrectionRequestMissing  string
	// CorrectionEscalated reports a recorded worker correction that reached
	// the attempt limit.
	CorrectionEscalated bool
	// SameStepFailedAttempts is the failed-attempt count the same-step wall
	// counts at the current step.
	SameStepFailedAttempts int64
	// EscalatedRetryApproved carries the request's operator approval for a
	// walled or escalated dispatch; the calling guard fills it from the
	// request identity before the pure decision runs.
	EscalatedRetryApproved bool
	// DispatchHold reports a dispatched worker holding the step's advance.
	DispatchHold bool
	// EvidenceRecoveryRoute reports an outstanding contract evidence
	// requirement the recovery bind_evidence can settle.
	EvidenceRecoveryRoute bool
	// PendingOperatorDecision reports an open operator question at the
	// current step: the checkpoint declares an approval-required action and
	// the investigation artifact stands behind it. The question-open refusal
	// itself stays in the operator-selection guard chain, whose checks the
	// payload-blind admission cannot reorder.
	PendingOperatorDecision bool
	// CompleteStepCorrection reports the shared complete-step correction
	// admission passes at the pinned complete step.
	CompleteStepCorrection bool
	// ContractCorrectionAvailable reports the ordinary contract correction
	// admission passes at the current step.
	ContractCorrectionAvailable bool
}

// WorkflowAdmissionDecision is the pure admission answer for one action over
// one folded state: whether the advance is admitted, whether it stands only
// behind the operator's escalated retry approval, whether a supersede
// request classifies as contract recovery, whether the refusal is the
// consequential external-conditions one the owning boundary resolves and
// rechecks, the ready review the calling guard may bind the request's
// attempt identity against, and the typed refusal when the advance refuses.
type WorkflowAdmissionDecision struct {
	Admitted                bool
	ApprovalRequired        bool
	RecoveryRoute           bool
	ConsequentialConditions bool
	// OffStep marks the structural step-legality refusal. The callers apply
	// it at the engine's late position — after the payload and guard checks
	// whose specific refusals precede the generic off-step one — instead of
	// at the admission refusal point.
	OffStep bool
	// AdvanceHeld marks the dispatch-hold refusal over an advancing action.
	// The fold's own advance rule enforces it at event assembly, the
	// position whose timing the admission fold and the read-only preflight
	// do not own; the work pin's intent omission is its advertisement half.
	AdvanceHeld          bool
	ReadyReviewAttemptID string
	ReadyReviewSettles   bool
	Failure              *Failure
}

// workflowFailureOf converts a folded boundary error into its typed refusal.
func workflowFailureOf(err error) *Failure {
	var failure *Failure
	if failureAs(err, &failure) {
		return failure
	}
	return newFailure(KindUnavailable, "workflow_action", err.Error(), true, "retry once the projection is readable")
}

// loadWorkflowAdmissionStateTx folds one work item's admission state in the
// caller's transaction. It takes a queryer rather than a *sql.Tx so a caller
// holding the pooled connection's transaction passes the tx itself and never
// a pool-backed store: a nested s.db call inside the transaction parks on the
// single pooled connection forever.
func loadWorkflowAdmissionStateTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (WorkflowAdmissionState, error) {
	state := WorkflowAdmissionState{
		Step:               currentStep,
		CorrectionWorkflow: workflowCorrectionWorkflow(definition),
		ReviewStep:         workflowPostRejectionReviewStep(definition, currentStep),
		ReviewDebt:         ReviewDebtNone,
	}
	if err := q.QueryRowContext(ctx, `SELECT wi.instance_state,(SELECT lifecycle FROM work_items WHERE id=wi.work_id),(SELECT count(*) FROM workflow_contracts wc WHERE wc.work_id=wi.work_id AND wc.superseded_by IS NULL) FROM workflow_instances wi WHERE wi.work_id=?`, workID).Scan(&state.InstanceState, &state.Lifecycle, &state.ActiveContracts); err != nil {
		if err == sql.ErrNoRows {
			return WorkflowAdmissionState{}, newFailure(KindProjectionNotFound, subject, "workflow instance is not recorded", false, "reread_entities")
		}
		return WorkflowAdmissionState{}, wrapFailure(KindUnavailable, subject, "cannot read workflow admission state", true, "retry once the database is readable", err)
	}
	// A duplicated contract projection is the supersede recovery's subject:
	// workflowAdmitSupersede admits its route on the count alone, and the
	// singular active-contract reader refuses duplicates. The conditional
	// folds below that resolve one active contract degrade instead of
	// refusing, so the loader stays total over the projection the recovery
	// owns and every action still reaches its typed route.
	duplicatedProjection := state.ActiveContracts > 1
	// The staleness boundary reads through the mutation transaction the
	// admission sites already hold; a pool-backed queryer cannot answer it
	// inside one fold, so the loader refuses rather than read past the tx.
	lawTx, isTx := q.(*sql.Tx)
	if !isTx {
		return WorkflowAdmissionState{}, newFailure(KindUnavailable, subject, "workflow action admission folds in the caller's transaction", false, "run the admission fold inside the mutation transaction")
	}
	lawErr := checkWorkflowLawRevisionStalenessAdmittingTx(ctx, lawTx, workID, workflowStalePinAdmitNone)
	// The staleness boundary's overlap half resolves the one active contract,
	// so under a duplicated projection it raises the duplicate invariant the
	// supersede recovery owns. The count itself classifies the recovery, so
	// the boundary's duplicate refusal degrades and the state folds on.
	if lawErr != nil && duplicatedProjection && workflowDuplicateContractProjection(lawErr) {
		lawErr = nil
	}
	state.LawPinStaleError = lawErr
	state.LawPinStale = lawErr != nil && workflowContractRecoveryStaleness(lawErr, workID)
	state.LawPinSelfError = checkWorkflowLawRevisionStalenessAdmittingTx(ctx, lawTx, workID, workflowStalePinAdmitOwnMarker)
	if state.LawPinSelfError != nil && duplicatedProjection && workflowDuplicateContractProjection(state.LawPinSelfError) {
		state.LawPinSelfError = nil
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_impact_notices n JOIN workflow_impact_edges e ON e.work_id=n.edge_owner_work_id AND e.edge_id=n.edge_id WHERE n.target_work_id=? AND n.severity='breaking' AND e.edge_class='hard'`, workID).Scan(&state.BreakingNotices); err != nil {
		return WorkflowAdmissionState{}, wrapFailure(KindUnavailable, subject, "cannot inspect workflow impact notices", true, "retry once the database is readable", err)
	}
	if openErr := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, workID).Scan(&state.ExternalConditionsOpen); openErr != nil {
		return WorkflowAdmissionState{}, wrapFailure(KindUnavailable, subject, "cannot inspect consequential workflow conditions", true, "retry once the database is readable", openErr)
	}
	if _, designStale, designErr := readCurrentWorkflowDesign(ctx, q, workID); designErr != nil {
		return WorkflowAdmissionState{}, designErr
	} else {
		state.DesignStale = designStale
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(a.lifecycle_state,''),COALESCE((SELECT json_extract(d.payload,'$.capability_class') FROM domain_events d WHERE d.subject_type='work_item' AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.worker_attempt_id')=a.attempt_id ORDER BY d.seq DESC LIMIT 1),'') FROM worker_attempts a WHERE a.work_id=? ORDER BY a.dispatched_at DESC,a.attempt_id DESC LIMIT 1`, WorkerDispatched, workID).Scan(&state.AttemptState, &state.AttemptCapabilityClass); err != nil && err != sql.ErrNoRows {
		return WorkflowAdmissionState{}, wrapFailure(KindUnavailable, subject, "cannot read the latest worker attempt", true, "retry once the worker attempt projection is readable", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(f.payload,'$.action_id'),'') FROM domain_events f WHERE f.subject_type='work_item' AND f.subject_id=? AND f.kind=? AND json_extract(f.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') ORDER BY f.seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&state.LatestResultDisposition); err != nil && err != sql.ErrNoRows {
		return WorkflowAdmissionState{}, wrapFailure(KindUnavailable, subject, "cannot read the latest result disposition", true, "retry once the workflow projection is readable", err)
	}
	if state.CorrectionWorkflow && state.ReviewStep {
		outstanding, err := workflowPostRejectionReviewOutstanding(ctx, q, workID, definition, subject)
		if err != nil {
			return WorkflowAdmissionState{}, err
		}
		if outstanding {
			state.ReviewDebt = ReviewDebtOutstanding
			readyAttemptID, readyVerdict, readyErr := workflowReadyReviewAttemptTx(ctx, q, workID, definition, subject)
			if readyErr != nil {
				return WorkflowAdmissionState{}, readyErr
			}
			state.ReadyReviewAttemptID, state.ReadyReviewVerdict = readyAttemptID, readyVerdict
			state.ReadyReviewSettles = workflowReviewSettlesDebt(readyVerdict)
		}
	}
	lateRoute, lateErr := workflowLateVerdictRecoveryAvailable(ctx, q, workID, definition, currentStep)
	if lateErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(lateErr)) {
		return WorkflowAdmissionState{}, lateErr
	}
	state.LateVerdictRoute = lateRoute && lateErr == nil
	workerFailureRoute, workerFailureErr := workflowWorkerFailureRecoveryAvailable(ctx, q, workID, definition, currentStep, subject)
	if workerFailureErr != nil {
		return WorkflowAdmissionState{}, workerFailureErr
	}
	state.WorkerFailureRecovery = workerFailureRoute
	correctionRoute, correctionRouteErr := workflowRejectedWorkerResultAvailable(ctx, q, workID, definition, currentStep, subject, 0)
	if correctionRouteErr != nil {
		return WorkflowAdmissionState{}, correctionRouteErr
	}
	// The rejection recovery is a worker-dispatch-step route: off a step that
	// declares dispatch_worker the state carries no recovery, so a caller
	// cannot admit the rejection onto an unrelated step.
	state.CorrectionRecovery = correctionRoute && stepDeclaresAction(definition, currentStep, "dispatch_worker")
	// The delivery gate's corrective return reads the review debt this fold
	// already carries, so the gate branch derives from the folded state and
	// never re-enters the loader; every other step folds the shared
	// correction-request admission directly. Under a duplicated projection
	// the singular active-contract reader refuses, and the duplicate
	// recovery owns the route out, so the conditional folds degrade.
	if workflowCorrectionWorkflow(definition) && workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		gateContext, gateErr := workflowDeliveryGateCorrectionContextFolded(ctx, q, workID, definition, currentStep, subject, state)
		if gateErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(gateErr)) {
			return WorkflowAdmissionState{}, gateErr
		}
		state.CorrectionRequestRecovery = gateErr == nil && gateContext != nil
		if gateErr == nil && gateContext == nil {
			state.CorrectionRequestMissing = workflowCorrectionMissingGateReview
		}
	} else {
		correctionRequestRoute, correctionRequestMissing, correctionRequestErr := workflowCorrectionRequestAdmissionState(ctx, q, workID, definition, currentStep, subject, 0)
		if correctionRequestErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(correctionRequestErr)) {
			return WorkflowAdmissionState{}, correctionRequestErr
		}
		state.CorrectionRequestRecovery, state.CorrectionRequestMissing = correctionRequestRoute && correctionRequestErr == nil, correctionRequestMissing
	}
	correction, correctionErr := workflowCorrectionContext(ctx, q, workID, currentStep)
	if correctionErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(correctionErr)) {
		return WorkflowAdmissionState{}, correctionErr
	} else if correctionErr == nil && correction != nil {
		state.CorrectionEscalated = correction.Escalated
	}
	sameStepFailed, wallErr := workflowSameStepFailedAttemptCount(ctx, q, definition, workID, currentStep, subject)
	if wallErr != nil {
		return WorkflowAdmissionState{}, wallErr
	}
	state.SameStepFailedAttempts = sameStepFailed
	dispatchHold, holdErr := workflowDispatchHoldsStepAdvance(ctx, q, definition, workID, currentStep, 0)
	if holdErr != nil {
		return WorkflowAdmissionState{}, holdErr
	}
	state.DispatchHold = dispatchHold
	if evidenceRecovery, evidenceErr := workflowEvidenceBindingRecoveryAvailable(ctx, q, definition, workID, currentStep); evidenceErr != nil {
		if !duplicatedProjection || !workflowDuplicateContractProjection(evidenceErr) {
			return WorkflowAdmissionState{}, evidenceErr
		}
		state.EvidenceRecoveryRoute = false
	} else {
		state.EvidenceRecoveryRoute = evidenceRecovery
	}
	state.PendingOperatorDecision = workflowOperatorDecisionPending(definition, currentStep)
	if state.PendingOperatorDecision {
		if artifactErr := requireRecordedInvestigationArtifact(ctx, q, workID); artifactErr != nil {
			var failure *Failure
			if failureAs(artifactErr, &failure) && failure.Kind == KindMissingEvidence {
				state.PendingOperatorDecision = false
			} else {
				return WorkflowAdmissionState{}, artifactErr
			}
		}
	}
	if atComplete := workflowCompleteStepCorrectionStep(definition, currentStep); atComplete {
		completeCorrection, completeErr := workflowCompleteStepCorrectionAvailable(ctx, q, workID, definition, currentStep, subject)
		if completeErr != nil {
			return WorkflowAdmissionState{}, completeErr
		}
		state.CompleteStepCorrection = completeCorrection
	}
	if contractCorrection, contractCorrectionErr := workflowContractCorrectionAvailable(ctx, q, workID, definition, currentStep, subject); contractCorrectionErr != nil {
		return WorkflowAdmissionState{}, contractCorrectionErr
	} else {
		state.ContractCorrectionAvailable = contractCorrection
	}
	return state, nil
}

// workflowAdmit decides one action's admission over one folded admission
// state. It is pure and total: no store access, no clock, no request payload,
// and a defined answer for every action the definitions declare. It decides
// the terminal immutability, the breaking-impact and consequential-condition
// boundaries, the supersede classification, the dispatch hold behind a stale
// design, an escalated correction, or the same-step wall, the staleness
// classes, the review-debt advances, the recovery routes, and the step
// legality. The folded conditions hide only the advances they name — the
// recorded delivery at a review step and the refinement-step accept under
// review debt, a dispatch behind a stale design, the same-step wall, or an
// escalated correction, and a consequential action behind a breaking impact
// notice or an open external condition — because each condition blocks a
// result, not the work: dispatch, recovery, and continuity actions stay
// admitted so a parked item always keeps a route. The payload and identity
// checks stay in the guards; the accept guard binds the request's attempt
// against the decision's ReadyReviewAttemptID.
func workflowAdmit(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) WorkflowAdmissionDecision {
	decision := WorkflowAdmissionDecision{ReadyReviewAttemptID: state.ReadyReviewAttemptID, ReadyReviewSettles: state.ReadyReviewSettles}
	if workflowCompletedInstanceActionImmutable(state.InstanceState, actionID, state.Lifecycle) {
		decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "terminal workflow instance is immutable", false, "start a successor workflow")
		return decision
	}
	consequence := workflowActionConsequence(definition, actionID)
	if workflowImpactBoundary(actionID, consequence) {
		if state.BreakingNotices != 0 {
			decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "breaking workflow impact notice blocks consequential execution", false, "reread_entities")
			return decision
		}
		if state.ExternalConditionsOpen != 0 {
			decision.ConsequentialConditions = true
			decision.Failure = newFailure(KindNotTerminal, "workflow_action", "consequential action has unresolved external conditions", false, "reread_entities")
			return decision
		}
	}
	if actionID == "supersede_contract" {
		return workflowAdmitSupersede(definition, state, decision)
	}
	if actionID == "dispatch_worker" {
		if state.DesignStale {
			decision.Failure = newFailure(KindMissingEvidence, "worker_dispatch", "contract correction invalidated the recorded design", false, "use supersede_contract with design_record before worker dispatch")
			return decision
		}
		if state.CorrectionEscalated && !state.EscalatedRetryApproved {
			decision.Failure = newFailure(KindApprovalRequired, "workflow_action", "worker correction reached the three-attempt limit", false, "escalate the failed or rejected result to the operator")
			decision.ApprovalRequired = true
			return decision
		}
		if state.SameStepFailedAttempts >= workflowCorrectionAttemptLimit && !state.EscalatedRetryApproved {
			decision.Failure = workflowSameStepWallFailure(state.Step, state.SameStepFailedAttempts)
			decision.ApprovalRequired = true
			return decision
		}
	}
	if err := state.stalenessRefusal(actionID); err != nil {
		decision.Failure = workflowFailureOf(err)
		return decision
	}
	if state.CorrectionWorkflow && state.ReviewDebt == ReviewDebtOutstanding {
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
	}
	if actionID == "request_correction" && !state.CorrectionRequestRecovery {
		decision.Failure = workflowCorrectionRequestUnavailableFailure("workflow_action", state.CorrectionRequestMissing)
		return decision
	}
	// The dispatch hold is the fold's own advance rule: while a dispatched
	// worker holds the step's advance, every advancing action refuses except
	// the accept that dispositions its report. Hold-moded actions — another
	// fresh dispatch among them — keep their route. The refusal is the
	// fold's advance-rule answer, enforced at event assembly.
	if state.DispatchHold && actionID != "accept_worker_result" {
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
			decision.AdvanceHeld = true
			decision.Failure = newFailure(KindIllegalLifecycleTransition, "workflow_action", "a dispatched worker attempt must advance through accept_worker_result", false, "accept the exact completed worker attempt, record the failed attempt at a human checkpoint, or dispatch a fresh worker")
			return decision
		}
	}
	if !workflowAdmissionStepAllows(definition, state, actionID) {
		decision.OffStep = true
		decision.Failure = newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared on the current step", false, "reread_entities")
		return decision
	}
	decision.Admitted = true
	return decision
}

// workflowAdmissionStepAllows decides the structural step legality: the step
// declares the action, or the folded state owns a recovery route that admits
// it off-step. The payload-bound halves of the late-verdict and
// evidence-binding recoveries stay guard checks beside the payloads they
// read; this answer is the route the state proves.
func workflowAdmissionStepAllows(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) bool {
	if definitionStepAllows(definition, state.Step, actionID) {
		return true
	}
	switch actionID {
	case "record_verdict":
		return state.LateVerdictRoute
	case "bind_evidence":
		return state.EvidenceRecoveryRoute
	case "record_worker_failure":
		return state.WorkerFailureRecovery
	case "reject_worker_result":
		return state.CorrectionRecovery
	case "request_correction":
		return state.CorrectionRequestRecovery
	}
	return false
}

// workflowOperatorDecisionPending reports whether the step's shape can carry
// an open operator question at all: a human checkpoint declaring an
// approval-required action. The investigation artifact's presence — the one
// condition the shape cannot answer — is the caller's queryer read, so the
// fold stays inside the caller's transaction.
func workflowOperatorDecisionPending(definition WorkflowDefinition, currentStep string) bool {
	step := workflowStep(definition, currentStep)
	if step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return false
	}
	for _, candidate := range step.Actions {
		for _, action := range definition.ActionDefinitions {
			if action.ID == candidate && action.Approval == ActionApprovalRequired {
				return true
			}
		}
	}
	return false
}

// workflowAdmitSupersede classifies one supersede_contract request's recovery
// route over the folded state. The payload's route declaration stays a
// payload check at the calling guard; the state answer is the shape refusal,
// the recovery classification, and the typed refusals.
func workflowAdmitSupersede(definition WorkflowDefinition, state WorkflowAdmissionState, decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	if workflowCompletedInstanceSupersedeOffShape(state.InstanceState, definition, state.Step) {
		// The stale-law and duplicate recoveries belong to running earlier
		// steps. A completed instance off the supported shape would fold
		// without the return that reopens the work.
		decision.Failure = workflowCompletedInstanceOffShapeFailure("workflow_action")
		return decision
	}
	atCompleteStep := workflowCompleteStepCorrectionStep(definition, state.Step)
	if atCompleteStep {
		if state.ActiveContracts > 1 {
			decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "duplicate contract recovery is unavailable at the pinned complete step", false, "run duplicate recovery on a declared earlier step")
			return decision
		}
		decision.RecoveryRoute = state.CompleteStepCorrection
		if !decision.RecoveryRoute {
			decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
			return decision
		}
		decision.Admitted = true
		return decision
	}
	if state.ActiveContracts > 1 {
		// Recovery owns the ambiguous projection. The normal authority check
		// cannot run first because it deliberately refuses duplicate state.
		decision.RecoveryRoute = true
		decision.Admitted = true
		return decision
	}
	if state.LawPinStale {
		decision.RecoveryRoute = true
		decision.Admitted = true
		return decision
	}
	if state.LawPinStaleError != nil {
		decision.Failure = workflowFailureOf(state.LawPinStaleError)
		return decision
	}
	decision.RecoveryRoute = state.ContractCorrectionAvailable
	if !decision.RecoveryRoute {
		decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
		return decision
	}
	decision.Admitted = true
	return decision
}

// stalenessRefusal returns the staleness boundary's refusal for one action's
// stale-pin admission class, or nil when the action's class admits. The
// attempt-disposition actions admit the subject's own stale pin (CD-0041 D7).
func (s WorkflowAdmissionState) stalenessRefusal(actionID string) error {
	if workflowActionStalePinAdmission(actionID) != workflowStalePinAdmitNone {
		return s.LawPinSelfError
	}
	return s.LawPinStaleError
}

// workflowAdmitFreshReviewRefusal is the typed refusal both debt-hidden
// advances carry: the work waits for a fresh accepted review of the repaired
// result before delivery.
func workflowAdmitFreshReviewRefusal(decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "the advance toward delivery requires a fresh accepted review of the repaired result", false, "dispatch a review attempt at the refinement step, accept its result, then advance")
	return decision
}

// workflowAdmissionDefersToReviewGate reports whether one action's refusal
// belongs to the claim-phase post-rejection review guard instead of the
// caller's own decision application. The guard owns the one payload-bound
// carve-out in the debt family — the accept whose attempt identity names the
// ready settling review is itself the fresh review — so the mutating fold and
// the preflight defer these advances to it rather than refuse them payload-
// blind, and every other site still answers from the same folded state.
func workflowAdmissionDefersToReviewGate(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) bool {
	if !state.CorrectionWorkflow || state.ReviewDebt != ReviewDebtOutstanding {
		return false
	}
	switch actionID {
	case "record_delivery":
		return state.ReviewStep
	case "accept_worker_result":
		return stepDeclaresAction(definition, state.Step, "start_refine")
	default:
		return false
	}
}

// workflowReadyReviewAttemptTx names the latest completed review attempt
// that stands ready for acceptance: its review-class dispatch postdates the
// debt's frontier, so it covers the rejected result, and no accept has
// dispositioned it yet. The verdict does not qualify it — a no_ship review
// stands ready exactly like a ship one, binds its findings on acceptance,
// and settles nothing, because the settlement query owns that rule. It is
// the loader's read of the ready-review predicate the work pin and the
// accept guard share.
func workflowReadyReviewAttemptTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, subject string) (string, string, error) {
	frontier, err := workflowPostRejectionFrontier(ctx, q, workID, definition, subject)
	if err != nil || frontier == 0 {
		return "", "", err
	}
	var attemptID, verdict string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(wc.payload,'$.attempt_id'),''), COALESCE(json_extract(wc.payload,'$.review.verdict'),'') FROM domain_events wc JOIN domain_events wd ON wd.subject_type=wc.subject_type AND wd.subject_id=wc.subject_id AND wd.kind=? AND json_extract(wd.payload,'$.attempt_id')=json_extract(wc.payload,'$.attempt_id') AND json_extract(wd.payload,'$.capability_class')='review' AND wd.seq>? WHERE wc.subject_type=? AND wc.subject_id=? AND wc.kind=? AND wc.seq>? AND NOT EXISTS(SELECT 1 FROM domain_events ax WHERE ax.subject_type=wc.subject_type AND ax.subject_id=wc.subject_id AND ax.kind=? AND json_extract(ax.payload,'$.action_id')='accept_worker_result' AND json_extract(ax.payload,'$.attempt_id')=json_extract(wc.payload,'$.attempt_id') AND ax.seq>?) ORDER BY wc.seq DESC LIMIT 1`, WorkerDispatched, frontier, string(SubjectWorkItem), workID, WorkerCompleted, frontier, WorkflowActionCompleted, frontier).Scan(&attemptID, &verdict); err != nil {
		if err == sql.ErrNoRows {
			return "", "", nil
		}
		return "", "", wrapFailure(KindUnavailable, subject, "cannot read the completed review history", true, "retry once the worker delivery projection is readable", err)
	}
	return attemptID, verdict, nil
}
