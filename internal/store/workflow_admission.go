package store

import (
	"context"
	"database/sql"
)

// WorkflowReviewDebt names the unresolved refinement review state the admission
// fold derives from one work item's refinement history.
type WorkflowReviewDebt string

const (
	// ReviewDebtNone reports no rejected result or accepted no_ship review
	// awaits a fresh settling review.
	ReviewDebtNone WorkflowReviewDebt = "none"
	// ReviewDebtOutstanding reports a rejected result or accepted no_ship
	// review whose fresh review — verdict ship or absent — remains unaccepted.
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
	// OutsideRepairActive suspends every managed action, including continuity
	// and recovery actions. Only the operator-only outside-repair APIs clear it.
	OutsideRepairActive bool
	// RefinementWorkflow reports whether the pinned definition declares the
	// refinement review shape: a step that declares start_refine. The
	// post-rejection review debt and its settling review belong to that
	// shape (CD-0197), not to a work kind.
	RefinementWorkflow bool
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
	// ArtifactStale reports the artifact the current step's evaluation
	// judges has gone stale: a bad verdict or a successor contract landed
	// after the artifact was last produced at the route target
	// (CD-0201 D5). While set, record_verdict admits only a non-ok verdict
	// and confirm_premise refuses; only fresh production at the declared
	// route target clears it.
	ArtifactStale bool
	// AttemptState is the latest worker attempt's lifecycle state.
	AttemptState string
	// AttemptCapabilityClass is that attempt's dispatch capability class.
	AttemptCapabilityClass string
	// LatestResultDisposition is the latest disposition action id over the
	// worker results: accept_worker_result, accept_worker_evidence,
	// reject_worker_result, record_worker_failure, or "" when none stands.
	LatestResultDisposition string
	// ReviewDebt is the folded unresolved refinement review obligation.
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
	// RefineReviewEvidenceAttemptID names the review available for hold-mode acceptance at refine.
	RefineReviewEvidenceAttemptID string
	// CorrectionRequestRecovery reports the delivery-gate corrective return
	// stands open, with CorrectionRequestMissing naming the one missing
	// prerequisite when it does not.
	CorrectionRequestRecovery bool
	CorrectionRequestMissing  string
	// CorrectionRequestContext is the folded correction request the current
	// step admits, the one derivation the payload check consumes: the guard,
	// the preflight, the dispatch fold, and the event fold bind their
	// payloads against it instead of re-entering the loader. It is nil when
	// the step admits no correction request.
	CorrectionRequestContext *WorkflowCorrectionContext
	// CorrectionEscalated reports a recorded worker correction that reached
	// the attempt limit.
	CorrectionEscalated bool
	// SameStepFailedAttempts is the failed-attempt count the same-step wall
	// counts at the current step.
	SameStepFailedAttempts int64
	// FailedWorkerRetry names the current terminal authorization that requires
	// exact retry approval, including a failure without dispatch evidence or
	// a coordinator disposition. Identity fields are carried for the binding;
	// admission depends only on whether this current failure exists.
	FailedWorkerRetry *WorkflowRetryApprovalBinding
	// EscalatedRetryApproved carries the request's operator approval for a
	// failed retry or an escalation wall; the calling guard fills it from the
	// request identity before the pure decision runs.
	EscalatedRetryApproved bool
	// DispatchHold reports a dispatched worker holding the step's advance.
	DispatchHold bool
	// EvidenceRecoveryRoute reports an outstanding contract evidence
	// requirement the recovery bind_evidence can settle.
	EvidenceRecoveryRoute bool
	// PendingOperatorDecision reports an open operator question at the
	// current step: the checkpoint declares an approval-required action and
	// the investigation artifact stands behind it. workflowAdmit refuses
	// confirm_premise on it — the question-open refusal is the admission's
	// answer, the one owner the work pin's advertisement reads — while the
	// operator-selection chain keeps the payload checks, the closed choice
	// and the decision-context digest, which the payload-blind admission
	// cannot judge.
	PendingOperatorDecision bool
	// AcceptanceDeliverablesMissing is the acceptance gate's refusal while
	// an open premise question stands without its deliverables: the recorded
	// verdict, a verdict for every approved predicate, and every required
	// evidence kind bound. Nil when the deliverables stand or no premise
	// question is open. The field name is the common case; any typed refusal
	// of that gate lands here. workflowAdmit refuses confirm_premise on it, so the
	// preflight and the work pin answer what the confirmation enforces.
	AcceptanceDeliverablesMissing error
	// CompleteStepCorrection reports the shared complete-step correction
	// admission passes at the pinned complete step.
	CompleteStepCorrection bool
	// ContractCorrectionAvailable reports the ordinary contract correction
	// admission passes at the current step.
	ContractCorrectionAvailable bool
	Delivery                    workflowDeliveryAdmission
	Mandate                     workflowMandateAdmission
}

// WorkflowAdmissionDecision is the pure admission answer for one action over
// one folded state: whether the advance is admitted, whether it stands only
// behind the operator's exact retry approval, whether a supersede
// request classifies as contract recovery, whether the refusal is the
// consequential external-conditions one the owning boundary resolves and
// rechecks, the ready review the calling guard may bind the request's
// attempt identity against, and the typed refusal when the advance refuses.
type WorkflowAdmissionDecision struct {
	Admitted bool
	// ApprovalRequired classifies the mutation boundary's exact retry
	// approval. An escalation wall also carries Failure; an ordinary retry
	// keeps its declared route while the boundary obtains that approval.
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
	AdvanceHeld bool
	// FreshReviewRequired marks the debt-hidden advance refusal the
	// claim-phase post-rejection review guard owns. The fold and the
	// preflight defer exactly this refusal to that guard, whose ready-review
	// carve-out is payload-bound; every other refusal — staleness, impact,
	// the wall, step legality — applies at the caller's own decision point
	// and is never deferred past it.
	FreshReviewRequired  bool
	DeliveryProofMissing bool
	// OperatorQuestionClosed marks the closed-question answer over
	// confirm_premise: the step declares the approval-required confirmation
	// and no operator question stands open behind it. It is an advertisement
	// wall like an escalated approval refusal — the work pin drops the unadmitted
	// intent, and the interactive refusal plus the payload checks (the
	// closed choice, the decision-context digest, the operator identity)
	// stay with the operator-selection chain the payload-blind admission
	// cannot judge — so the execution path leaves confirm_premise to those
	// checks instead of refusing at the admission point.
	OperatorQuestionClosed bool
	ReadyReviewAttemptID   string
	ReadyReviewSettles     bool
	Failure                *Failure
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
// single pooled connection forever. Ordered proof candidates are a separate
// result for the guard's external tooling check, not part of the abstract state.
func loadWorkflowAdmissionStateTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (WorkflowAdmissionState, []workflowVerificationRun, []workflowVerificationRun, error) {
	state := WorkflowAdmissionState{
		Step:               currentStep,
		RefinementWorkflow: workflowRefinementStepID(definition) != "",
		ReviewStep:         workflowPostRejectionReviewStep(definition, currentStep),
		ReviewDebt:         ReviewDebtNone,
	}
	if err := q.QueryRowContext(ctx, `SELECT wi.instance_state,(SELECT lifecycle FROM work_items WHERE id=wi.work_id),(SELECT count(*) FROM workflow_contracts wc WHERE wc.work_id=wi.work_id AND wc.superseded_by IS NULL) FROM workflow_instances wi WHERE wi.work_id=?`, workID).Scan(&state.InstanceState, &state.Lifecycle, &state.ActiveContracts); err != nil {
		if err == sql.ErrNoRows {
			return WorkflowAdmissionState{}, nil, nil, newFailure(KindProjectionNotFound, subject, "workflow instance is not recorded", false, "reread_entities")
		}
		return WorkflowAdmissionState{}, nil, nil, wrapFailure(KindUnavailable, subject, "cannot read workflow admission state", true, "retry once the database is readable", err)
	}
	active, err := outsideRepairActiveTx(ctx, q, workID)
	if err != nil {
		return WorkflowAdmissionState{}, nil, nil, err
	}
	state.OutsideRepairActive = active
	if active || state.InstanceState == "outside_repair" {
		return state, nil, nil, nil
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
		return WorkflowAdmissionState{}, nil, nil, newFailure(KindUnavailable, subject, "workflow action admission folds in the caller's transaction", false, "run the admission fold inside the mutation transaction")
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
		return WorkflowAdmissionState{}, nil, nil, wrapFailure(KindUnavailable, subject, "cannot inspect workflow impact notices", true, "retry once the database is readable", err)
	}
	if openErr := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, workID).Scan(&state.ExternalConditionsOpen); openErr != nil {
		return WorkflowAdmissionState{}, nil, nil, wrapFailure(KindUnavailable, subject, "cannot inspect consequential workflow conditions", true, "retry once the database is readable", openErr)
	}
	_, designStale, designErr := readCurrentWorkflowDesign(ctx, q, workID)
	if designErr != nil {
		return WorkflowAdmissionState{}, nil, nil, designErr
	}
	state.DesignStale = designStale
	artifactStale, staleFoldErr := workflowArtifactStale(ctx, q, workID, definition, currentStep, subject)
	if staleFoldErr != nil {
		return WorkflowAdmissionState{}, nil, nil, staleFoldErr
	}
	state.ArtifactStale = artifactStale
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(a.lifecycle_state,''),COALESCE((SELECT json_extract(d.payload,'$.capability_class') FROM domain_events d WHERE d.subject_type='work_item' AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id ORDER BY d.seq DESC LIMIT 1),'') FROM worker_attempts a WHERE a.work_id=? ORDER BY a.dispatched_at DESC,a.attempt_id DESC LIMIT 1`, WorkerDispatched, workID).Scan(&state.AttemptState, &state.AttemptCapabilityClass); err != nil && err != sql.ErrNoRows {
		return WorkflowAdmissionState{}, nil, nil, wrapFailure(KindUnavailable, subject, "cannot read the latest worker attempt", true, "retry once the worker attempt projection is readable", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(f.payload,'$.action_id'),'') FROM domain_events f WHERE f.subject_type='work_item' AND f.subject_id=? AND f.kind=? AND json_extract(f.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') ORDER BY f.seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&state.LatestResultDisposition); err != nil && err != sql.ErrNoRows {
		return WorkflowAdmissionState{}, nil, nil, wrapFailure(KindUnavailable, subject, "cannot read the latest result disposition", true, "retry once the workflow projection is readable", err)
	}
	if state.RefinementWorkflow && state.ReviewStep {
		outstanding, err := workflowPostRejectionReviewOutstanding(ctx, q, workID, definition, subject)
		if err != nil {
			return WorkflowAdmissionState{}, nil, nil, err
		}
		if outstanding {
			state.ReviewDebt = ReviewDebtOutstanding
			readyAttemptID, readyVerdict, readyErr := workflowReadyReviewAttemptTx(ctx, q, workID, definition, subject)
			if readyErr != nil {
				return WorkflowAdmissionState{}, nil, nil, readyErr
			}
			state.ReadyReviewAttemptID, state.ReadyReviewVerdict = readyAttemptID, readyVerdict
			state.ReadyReviewSettles = workflowReviewSettlesDebt(readyVerdict)
		}
	}
	lateRoute, lateErr := workflowLateVerdictRecoveryAvailable(ctx, q, workID, definition, currentStep)
	if lateErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(lateErr)) {
		return WorkflowAdmissionState{}, nil, nil, lateErr
	}
	state.LateVerdictRoute = lateRoute && lateErr == nil
	workerFailureRoute, workerFailureErr := workflowWorkerFailureRecoveryAvailable(ctx, q, workID, definition, currentStep, subject)
	if workerFailureErr != nil {
		return WorkflowAdmissionState{}, nil, nil, workerFailureErr
	}
	state.WorkerFailureRecovery = workerFailureRoute
	correctionRoute, correctionRouteErr := workflowRejectedWorkerResultAvailable(ctx, q, workID, definition, currentStep, subject, 0)
	if correctionRouteErr != nil {
		return WorkflowAdmissionState{}, nil, nil, correctionRouteErr
	}
	// The rejection recovery is a worker-dispatch-step route: off a step that
	// declares dispatch_worker the state carries no recovery, so a caller
	// cannot admit the rejection onto an unrelated step.
	state.CorrectionRecovery = correctionRoute && stepDeclaresAction(definition, currentStep, "dispatch_worker")
	reviewEvidenceAttempt, reviewEvidenceErr := workflowRefineReviewEvidenceAttempt(ctx, q, workID, definition, currentStep, subject, 0)
	if reviewEvidenceErr != nil {
		return WorkflowAdmissionState{}, nil, nil, reviewEvidenceErr
	}
	state.RefineReviewEvidenceAttemptID = reviewEvidenceAttempt
	// The delivery gate's corrective return reads the review debt this fold
	// already carries, so the gate branch derives from the folded state and
	// never re-enters the loader; every other step folds the shared
	// correction-request admission directly. Under a duplicated projection
	// the singular active-contract reader refuses, and the duplicate
	// recovery owns the route out, so the conditional folds degrade.
	if workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		gateContext, gateErr := workflowDeliveryGateCorrectionContextFolded(ctx, q, workID, definition, currentStep, subject, state)
		if gateErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(gateErr)) {
			return WorkflowAdmissionState{}, nil, nil, gateErr
		}
		state.CorrectionRequestRecovery = gateErr == nil && gateContext != nil
		if gateErr == nil && gateContext == nil {
			state.CorrectionRequestMissing = workflowCorrectionMissingGateReview
		}
		if gateErr == nil {
			state.CorrectionRequestContext = gateContext
		}
	} else {
		correctionRequestContext, correctionRequestMissing, correctionRequestErr := workflowCorrectionRequestAdmission(ctx, q, workID, definition, currentStep, subject, 0)
		if correctionRequestErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(correctionRequestErr)) {
			return WorkflowAdmissionState{}, nil, nil, correctionRequestErr
		}
		state.CorrectionRequestRecovery, state.CorrectionRequestMissing = correctionRequestContext != nil && correctionRequestErr == nil, correctionRequestMissing
		if correctionRequestErr == nil {
			state.CorrectionRequestContext = correctionRequestContext
		}
	}
	correction, correctionErr := workflowCorrectionContext(ctx, q, workID, currentStep)
	if correctionErr != nil && !(duplicatedProjection && workflowDuplicateContractProjection(correctionErr)) {
		return WorkflowAdmissionState{}, nil, nil, correctionErr
	} else if correctionErr == nil && correction != nil {
		state.CorrectionEscalated = correction.Escalated
	}
	sameStepFailed, wallErr := workflowSameStepFailedAttemptCount(ctx, q, definition, workID, currentStep, subject)
	if wallErr != nil {
		return WorkflowAdmissionState{}, nil, nil, wallErr
	}
	state.SameStepFailedAttempts = sameStepFailed
	failedRetry, retryErr := workflowCurrentFailedWorkerRetryBinding(ctx, q, definition, workID, currentStep)
	if retryErr != nil {
		return WorkflowAdmissionState{}, nil, nil, retryErr
	}
	state.FailedWorkerRetry = failedRetry
	dispatchHold, holdErr := workflowDispatchHoldsStepAdvance(ctx, q, definition, workID, currentStep, 0)
	if holdErr != nil {
		return WorkflowAdmissionState{}, nil, nil, holdErr
	}
	state.DispatchHold = dispatchHold
	if evidenceRecovery, evidenceErr := workflowEvidenceBindingRecoveryAvailable(ctx, q, definition, workID, currentStep); evidenceErr != nil {
		if !duplicatedProjection || !workflowDuplicateContractProjection(evidenceErr) {
			return WorkflowAdmissionState{}, nil, nil, evidenceErr
		}
		state.EvidenceRecoveryRoute = false
	} else {
		state.EvidenceRecoveryRoute = evidenceRecovery
	}
	// The operator question reads the active contract's premise, so a
	// contractless or ambiguous projection has no question to answer.
	state.PendingOperatorDecision = state.ActiveContracts == 1 && workflowOperatorDecisionPending(definition, currentStep)
	if state.PendingOperatorDecision {
		if artifactErr := requireRecordedInvestigationArtifact(ctx, q, workID); artifactErr != nil {
			var failure *Failure
			if failureAs(artifactErr, &failure) && failure.Kind == KindMissingEvidence {
				state.PendingOperatorDecision = false
			} else {
				return WorkflowAdmissionState{}, nil, nil, artifactErr
			}
		}
	}
	if state.PendingOperatorDecision {
		if action, _ := workflowOperatorQuestionAction(definition, currentStep); action == "confirm_premise" {
			if deliverablesErr := requireAcceptanceDeliverables(ctx, q, workID); deliverablesErr != nil {
				// A typed refusal is the confirmation's answer; only an
				// unreadable projection fails the fold.
				var failure *Failure
				if !failureAs(deliverablesErr, &failure) || failure.Kind == KindUnavailable {
					return WorkflowAdmissionState{}, nil, nil, deliverablesErr
				}
				state.AcceptanceDeliverablesMissing = deliverablesErr
			}
		}
	}
	if atComplete := workflowCompleteStepCorrectionStep(definition, currentStep); atComplete {
		completeCorrection, completeErr := workflowCompleteStepCorrectionAvailable(ctx, q, workID, definition, currentStep, subject)
		if completeErr != nil {
			return WorkflowAdmissionState{}, nil, nil, completeErr
		}
		state.CompleteStepCorrection = completeCorrection
	}
	contractCorrection, contractCorrectionErr := workflowContractCorrectionAvailable(ctx, q, workID, definition, currentStep, subject)
	if contractCorrectionErr != nil {
		return WorkflowAdmissionState{}, nil, nil, contractCorrectionErr
	}
	state.ContractCorrectionAvailable = contractCorrection
	delivery, proofRuns, integrationRuns, deliveryErr := loadWorkflowDeliveryAdmission(ctx, q, workID, definition, currentStep)
	if deliveryErr != nil {
		return WorkflowAdmissionState{}, nil, nil, deliveryErr
	}
	state.Delivery = delivery
	mandate, mandateErr := loadWorkflowMandateAdmission(ctx, q, workID, definition, currentStep, subject, state, correctionRoute)
	if mandateErr != nil {
		return WorkflowAdmissionState{}, nil, nil, mandateErr
	}
	state.Mandate = mandate
	return state, proofRuns, integrationRuns, nil
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
	if state.OutsideRepairActive {
		decision.Failure = newOutsideRepairRouteFailure("workflow_action", "managed workflow action refused: outside-repair disposition is active")
		return decision
	}
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
	// On a job-capable pin (CD-0205) the payload-blind accept is admissible
	// as local acceptance of one worker job, which holds the step; the
	// delivery-asserting accept applies workflowAdmitDelivery at its
	// payload-bound guard instead.
	if actionID == "accept_worker_result" && workflowAcceptDeliveryAdmissionActive(definition, state.Step) && !workflowWorkerJobsActive(definition) && !(state.ReadyReviewAttemptID != "" && !state.ReadyReviewSettles) {
		decision = workflowAdmitDelivery(state, decision)
		if decision.Failure != nil {
			return decision
		}
	}
	if failure := workflowAdmitMandate(definition, state.Step, state.Mandate, actionID, "workflow_action"); failure != nil {
		decision.Failure = failure
		return decision
	}
	if state.RefinementWorkflow && state.ReviewDebt == ReviewDebtOutstanding {
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
	if actionID == "record_delivery" {
		decision = workflowAdmitDelivery(state, decision)
		if decision.Failure != nil {
			return decision
		}
	}
	// The artifact-staleness answer is the admission's (CD-0201 D5): while
	// the artifact the step evaluates is stale, the premise confirmation
	// cannot step past the verdict. The declared unhealthy_verdict route —
	// request_correction back to the producer — is the one admitted return;
	// families whose steps carry no failure edge keep that route instead of
	// a forward escape. The refusal precedes the question-closed answer:
	// staleness is the state of the artifact itself, prior to the operator
	// question machinery.
	if actionID == "confirm_premise" && state.ArtifactStale {
		decision.Failure = newFailure(KindStaleRequiresReview, "workflow_action", "the artifact the premise confirms is stale: it was not re-produced at its producer step", false, "request_correction to return to the declared producer step, or record a non-ok verdict")
		return decision
	}
	// The question-open answer is the admission's: confirm_premise is
	// answered only through an open operator question, so the decision
	// marks the question closed and refuses advertisement. The work pin
	// reads this same decision and drops the unadmitted intent.
	if actionID == "confirm_premise" && !state.PendingOperatorDecision {
		decision.OperatorQuestionClosed = true
		decision.Failure = newFailure(KindStaleRequiresReview, "workflow_action", "no operator question is open at the current workflow step", false, "record the investigation artifact the question requires, or take a declared route")
		return decision
	}
	if actionID == "confirm_premise" && state.AcceptanceDeliverablesMissing != nil {
		decision.Failure = workflowFailureOf(state.AcceptanceDeliverablesMissing)
		return decision
	}
	if actionID == "request_correction" && !state.CorrectionRequestRecovery {
		decision.Failure = workflowCorrectionRequestUnavailableFailure("workflow_action", state.CorrectionRequestMissing)
		return decision
	}
	// The contract step is left only through the approval it hosts. An
	// advancing action there with no active contract would move the instance
	// past the only step that binds one, and completion refuses without a
	// contract, so the work could never finish. Version-1 research declares
	// frame_research in advance mode beside approve_contract on its frame step.
	if state.ActiveContracts == 0 && actionID != "approve_contract" && workflowActionAdvancesStep(definition, actionID) {
		if contractStep, err := workflowDefinitionContractStep(definition); err == nil && contractStep == state.Step {
			decision.Failure = newFailure(KindIllegalLifecycleTransition, "workflow_action", "the contract step advances only through its contract approval", false, "approve the workflow contract before advancing")
			return decision
		}
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
	if actionID == "dispatch_worker" && state.FailedWorkerRetry != nil && !state.EscalatedRetryApproved {
		// The ordinary retry stays a declared route only after the other
		// admission gates pass. Its exact approval belongs to the mutation
		// boundary; an unrelated refusal must never become an approval route.
		decision.ApprovalRequired = true
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
	case "accept_worker_evidence":
		return state.RefineReviewEvidenceAttemptID != ""
	case "request_correction":
		return state.CorrectionRequestRecovery
	}
	return false
}

// workflowOperatorDecisionPending reports whether the step's shape can carry
// an open operator question at all. The investigation artifact's presence —
// the one condition the shape cannot answer — is the caller's queryer read,
// so the fold stays inside the caller's transaction.
func workflowOperatorDecisionPending(definition WorkflowDefinition, currentStep string) bool {
	_, ok := workflowOperatorQuestionAction(definition, currentStep)
	return ok
}

// workflowOperatorQuestionAction names the approval-required action the
// operator question at one step serves. A human checkpoint opens the question
// for its first approval-required action. confirm_premise is answered only
// through that question, so a step that declares it opens the question
// whatever its kind: version-1 to version-4 ops runbooks declare it on an
// internal-SQLite cleanup step, and without the question their only edge out
// of cleanup could never be taken (CD-0203 D2). Every other approval-required
// action off a human checkpoint, such as request_correction at a delivery
// gate, opens no question.
func workflowOperatorQuestionAction(definition WorkflowDefinition, currentStep string) (string, bool) {
	step := workflowStep(definition, currentStep)
	if step == nil {
		return "", false
	}
	for _, candidate := range step.Actions {
		if step.Kind != WorkflowStepHumanCheckpoint && candidate != "confirm_premise" {
			continue
		}
		for _, action := range definition.ActionDefinitions {
			if action.ID == candidate && action.Approval == ActionApprovalRequired {
				return candidate, true
			}
		}
	}
	return "", false
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
	if state.ActiveContracts == 0 {
		decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "contract recovery requires an active workflow contract", false, "rebuild the workflow contract projection")
		return decision
	}
	atCompleteStep := workflowCompleteStepCorrectionStep(definition, state.Step)
	if atCompleteStep {
		// A duplicated projection at this step recovers through the same
		// complete-step correction: the successor names every active version
		// and the instance returns to its external-effect step (CD-0203 D1).
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
// result before delivery. It is the one refusal the claim-phase review guard
// owns, so the decision marks it FreshReviewRequired for the deferring sites.
func workflowAdmitFreshReviewRefusal(decision WorkflowAdmissionDecision) WorkflowAdmissionDecision {
	decision.FreshReviewRequired = true
	decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "the advance toward delivery requires a fresh accepted review of the repaired result", false, "dispatch a review attempt at the refinement step, accept its result, then advance")
	return decision
}

// workflowAdmissionDefersToReviewGate reports whether one action's refusal
// belongs to the claim-phase post-rejection review guard instead of the
// caller's own decision application. Only the guard's own refusal over the
// accept defers: its ready-review carve-out is the one payload-bound member
// of the debt family — the accept whose attempt identity names the ready
// review. The same refusal over record_delivery admits nothing payload-blind,
// so every site applies it. A staleness, impact, wall,
// or step-legality refusal is never deferred, so an unrelated cause cannot
// ride the review gate's acceptance route.
func workflowAdmissionDefersToReviewGate(decision WorkflowAdmissionDecision, actionID string) bool {
	return decision.FreshReviewRequired && actionID == "accept_worker_result"
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
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(wc.payload,'$.attempt_id'),''), COALESCE(json_extract(wc.payload,'$.review.verdict'),'') FROM domain_events wc JOIN domain_events wd ON wd.subject_type=wc.subject_type AND wd.subject_id=wc.subject_id AND wd.kind=? AND json_extract(wd.payload,'$.attempt_id')=json_extract(wc.payload,'$.attempt_id') AND json_extract(wd.payload,'$.capability_class')='review' AND wd.seq>? WHERE wc.subject_type=? AND wc.subject_id=? AND wc.kind=? AND wc.seq>? AND NOT EXISTS(SELECT 1 FROM domain_events ax WHERE ax.subject_type=wc.subject_type AND ax.subject_id=wc.subject_id AND ax.kind=? AND json_extract(ax.payload,'$.action_id')='accept_worker_result' AND json_extract(ax.payload,'$.worker_attempt_id')=json_extract(wc.payload,'$.attempt_id') AND ax.seq>?) ORDER BY wc.seq DESC LIMIT 1`, WorkerDispatched, frontier, string(SubjectWorkItem), workID, WorkerCompleted, frontier, WorkflowActionCompleted, frontier).Scan(&attemptID, &verdict); err != nil {
		if err == sql.ErrNoRows {
			return "", "", nil
		}
		return "", "", wrapFailure(KindUnavailable, subject, "cannot read the completed review history", true, "retry once the worker delivery projection is readable", err)
	}
	return attemptID, verdict, nil
}
