package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
// verdict, the recovery routes, the non-progress wall, the dispatch hold, and
// the outstanding evidence — and workflowAdmit consumes it, so every
// admission site that loads and admits answers identically for the same
// state instead of re-deriving the conditions per site.
type WorkflowAdmissionState struct {
	Review                *WorkflowReviewState
	ReviewLane            string
	ReviewGroup           string
	ReviewAcceptanceError *Failure
	ReviewCorrective      bool
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
	// refinement review shape: a step that declares checkpoint_refine. The
	// post-rejection review debt and its settling review belong to that
	// shape (CD-0197), not to a work kind.
	RefinementWorkflow bool
	// ReviewStep reports whether the current step's advance carries an
	// unreviewed repaired result toward delivery: the refinement step that
	// enters the delivery gate, and the gate itself.
	ReviewStep bool
	// ActiveContracts is the count of active workflow contracts.
	ActiveContracts int64
	// LateCriterionAssociation admits missing applicable proof at a terminal
	// or premise-question step without moving it back to a producer.
	LateCriterionAssociation bool
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
	// judges has gone stale: a bad verdict landed after the artifact was
	// last produced at the route target (CD-0201 D5). A supersession
	// invalidates only the Review and proof whose digests it changes
	// (CD-0242 D7). While set, record_verdict admits only a non-ok verdict
	// and confirm_premise refuses; only fresh production at the declared
	// route target clears it.
	ArtifactStale bool
	// AttemptState is the latest worker attempt's lifecycle state.
	AttemptState string
	// AttemptCapabilityClass is that attempt's dispatch capability class.
	AttemptCapabilityClass string
	// PendingWorkerAuthorization has no observed launch outcome yet. It must
	// be reconciled before the shared candidate can receive another writer.
	PendingWorkerAuthorization bool
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
	// UnconsumedUnboundRejection reports an unbound rejection at the
	// current step no materialized dispatch has consumed yet.
	UnconsumedUnboundRejection bool
	// OracleOpenFindings is the count of the derived open ranked findings
	// of an oracle-capable pin: the folded admission state stays
	// a comparable value, so it carries the bound the dispatch refusal
	// reads while the identity set itself is derived per surface from the
	// one lineage reader. Zero on every pin that predates the acceptance
	// oracle; dispatch refuses while the count exceeds
	// WorkflowOpenOracleFindingsLimit instead of truncating blockers.
	OracleOpenFindings int
	// DispatchHold reports a dispatched worker holding the step's advance.
	DispatchHold bool
	// EvidenceRecoveryRoute reports an outstanding contract evidence
	// requirement the recovery bind_evidence can settle.
	EvidenceRecoveryRoute bool
	// EvidenceRecovery retains the eligibility owner's snapshot facts for
	// readers; admission still decides through EvidenceRecoveryRoute.
	EvidenceRecovery *workflowEvidenceBindingRecoveryState
	// PendingOperatorDecision reports an open operator question at the
	// current step: the checkpoint declares an approval-required action and
	// the investigation artifact stands behind it. workflowAdmit refuses
	// confirm_premise on it — the question-open refusal is the admission's
	// answer, the one owner the work pin's advertisement reads — while the
	// operator-selection chain keeps the payload checks, the closed choice
	// and the decision-context digest, which the payload-blind admission
	// cannot judge. A folded engineering confirmation authority closes the
	// question whatever the step shape declares.
	PendingOperatorDecision bool
	// ApprovalPolicy is the pinned definition's approval policy: absence
	// selects the released legacy classification and consequence_v1 the
	// typed one. The consent consumers read this folded field instead of
	// re-reading the definition, so every surface answers from one fold.
	ApprovalPolicy string
	// ConfirmationAuthority is the folded confirmation authority of the one
	// active contract: consequence_v1 engineering work records the
	// authenticated non-worker coordinator's own confirmation, decision
	// work keeps the verified operator, and legacy pins fold the empty
	// authority whose shape the question machinery owns.
	ConfirmationAuthority WorkflowConfirmationAuthority
	// ConfirmationFactFailure is the typed refusal a consequence_v1 contract
	// folds when its declared decision-domain fact is absent or malformed:
	// the confirmation and completion refuse on it instead of classifying
	// the work as engineering. Nil on every legacy pin and every well-formed
	// fact.
	ConfirmationFactFailure *Failure
	// AcceptanceDeliverablesMissing is the acceptance gate's refusal while
	// an open premise question stands without its deliverables: the recorded
	// verdict, a verdict for every approved predicate, and every required
	// evidence kind bound. Nil when the deliverables stand or no premise
	// question is open. The field name is the common case; any typed refusal
	// of that gate lands here. workflowAdmit refuses confirm_premise on it, so the
	// preflight and the work pin answer what the confirmation enforces. At the
	// complete step it also carries the shared acceptance-subject refusal.
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
// one folded state: whether the advance is admitted, whether a supersede
// request classifies as contract recovery, whether the refusal is the
// consequential external-conditions one the owning boundary resolves and
// rechecks, the ready review the calling guard may bind the request's
// attempt identity against, and the typed refusal when the advance refuses.
type WorkflowAdmissionDecision struct {
	Admitted      bool
	RecoveryRoute bool
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
	// wall — the work pin drops the unadmitted
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

// WorkflowConfirmationAuthority names the folded confirmation authority of
// one active contract. A legacy pin folds the empty authority: the declared
// question machinery stays its confirmation path. consequence_v1 engineering
// work folds the coordinator authority — the authenticated non-worker
// coordinator records its own confirmation — and decision-bearing work folds
// the operator authority: the exact human choice, digest, and identity.
type WorkflowConfirmationAuthority string

const (
	// WorkflowConfirmationNone is the legacy fold: no typed authority, and
	// the question machinery keeps every answer it always gave.
	WorkflowConfirmationNone WorkflowConfirmationAuthority = ""
	// WorkflowConfirmationCoordinator records an engineering confirmation:
	// the executing or initiating non-worker coordinator confirms without a
	// human question, a human digest, or an operator substitution.
	WorkflowConfirmationCoordinator WorkflowConfirmationAuthority = "coordinator"
	// WorkflowConfirmationOperator records a decision confirmation: the
	// verified operator answers the open question with the closed choice and
	// the exact decision-context digest.
	WorkflowConfirmationOperator WorkflowConfirmationAuthority = "operator"
)

// workflowFailureOf converts a folded boundary error into its typed refusal.
func workflowFailureOf(err error) *Failure {
	var failure *Failure
	if failureAs(err, &failure) {
		return failure
	}
	return newFailure(KindUnavailable, "workflow_action", err.Error(), true, "retry once the projection is readable")
}

// loadWorkflowAdmissionStateTxForRegistry folds the same admission state with
// the caller's authoritative registry: correction routes resolve
// their pins through it. A nil registry
// selects the builtin registry.
func loadWorkflowAdmissionStateTxForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, registry DefinitionRegistry) (WorkflowAdmissionState, []workflowVerificationRun, []workflowVerificationRun, error) {
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
	l := workflowAdmissionLoad{ctx: ctx, q: q, workID: workID, definition: definition, currentStep: currentStep, subject: subject, duplicated: state.ActiveContracts > 1, registry: registry}
	if err := l.staleness(&state); err != nil {
		return WorkflowAdmissionState{}, nil, nil, err
	}
	if err := l.attemptAndReview(&state); err != nil {
		return WorkflowAdmissionState{}, nil, nil, err
	}
	// Duplicate active contracts leave no single criteria set to review
	// against, so no Review stands: every Review-gated action stays closed
	// while the supersede recovery the duplicates require stays reachable.
	review, reviewErr := loadWorkflowReviewState(ctx, q, workID, 0)
	if reviewErr != nil && l.duplicated && workflowDuplicateContractProjection(reviewErr) {
		review, reviewErr = WorkflowReviewState{Findings: []WorkerReviewFinding{}, PublicationOutstanding: true}, nil
	}
	if reviewErr != nil {
		return WorkflowAdmissionState{}, nil, nil, reviewErr
	}
	state.Review = &review
	// Authorization is not an observed start. The host may close an unstarted
	// authorization through abandonment, whose liveness guard owns that fact.
	// Every observed implementation start remains progress after termination.
	var progressed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND capability_class='implementation' AND lifecycle_state<>'in_flight' AND NOT(lifecycle_state='failed' AND failure_kind=? AND readback_model=''))`, workID, WorkerFailureAbandoned).Scan(&progressed); err != nil {
		return WorkflowAdmissionState{}, nil, nil, workflowProjectionError(err, "cannot read current execution progress")
	}
	review.ExecutionProgressed = review.ExecutionProgressed || progressed
	correctionRoute, err := l.recoveryRoutes(&state)
	if err != nil {
		return WorkflowAdmissionState{}, nil, nil, err
	}
	if err := l.holdsAndRecovery(&state); err != nil {
		return WorkflowAdmissionState{}, nil, nil, err
	}
	if err := loadWorkflowAcceptanceAdmission(ctx, q, workID, definition, &state); err != nil {
		return WorkflowAdmissionState{}, nil, nil, err
	}
	if state.ActiveContracts == 1 && workflowLateCriterionAssociationStep(definition, currentStep) {
		missing, err := workflowMissingCriterionAssociations(ctx, q, workID)
		if err != nil {
			return WorkflowAdmissionState{}, nil, nil, err
		}
		state.LateCriterionAssociation = len(missing) != 0
	}
	if atComplete := workflowCompleteStepCorrectionStep(definition, currentStep); atComplete {
		completeCorrection, completeErr := workflowCompleteStepCorrectionAvailable(ctx, q, workID, definition, currentStep, subject, 0)
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
	delivery, proofRuns, integrationRuns, deliveryErr := loadWorkflowDeliveryAdmission(ctx, q, workID, definition, currentStep, "")
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

// workflowAdmissionLoad carries the inputs every admission fold reads.
// duplicated reports a duplicated active-contract projection, under which the
// folds that resolve one active contract degrade instead of refusing.
type workflowAdmissionLoad struct {
	ctx         context.Context
	q           queryer
	workID      string
	definition  WorkflowDefinition
	currentStep string
	subject     string
	duplicated  bool
	// registry is the authoritative resolver the fold's progress-sensitive
	// derivations verify recorded selection pins through: nil selects the
	// builtin registry.
	registry DefinitionRegistry
}

// staleness folds the law-pin staleness, breaking notices, open conditions,
// design staleness, and artifact staleness.
func (l workflowAdmissionLoad) staleness(state *WorkflowAdmissionState) error {
	lawErr := checkWorkflowLawRevisionStalenessAdmittingTx(l.ctx, l.q, l.workID, workflowStalePinAdmitNone)
	// The staleness boundary's overlap half resolves the one active contract,
	// so under a duplicated projection it raises the duplicate invariant the
	// supersede recovery owns. The count itself classifies the recovery, so
	// the boundary's duplicate refusal degrades and the state folds on.
	if lawErr != nil && l.duplicated && workflowDuplicateContractProjection(lawErr) {
		lawErr = nil
	}
	state.LawPinStaleError = lawErr
	state.LawPinStale = lawErr != nil && workflowContractRecoveryStaleness(lawErr, l.workID)
	state.LawPinSelfError = checkWorkflowLawRevisionStalenessAdmittingTx(l.ctx, l.q, l.workID, workflowStalePinAdmitOwnMarker)
	if state.LawPinSelfError != nil && l.duplicated && workflowDuplicateContractProjection(state.LawPinSelfError) {
		state.LawPinSelfError = nil
	}
	if err := l.q.QueryRowContext(l.ctx, `SELECT count(*) FROM workflow_impact_notices n JOIN workflow_impact_edges e ON e.work_id=n.edge_owner_work_id AND e.edge_id=n.edge_id WHERE n.target_work_id=? AND n.severity='breaking' AND e.edge_class='hard'`, l.workID).Scan(&state.BreakingNotices); err != nil {
		return wrapFailure(KindUnavailable, l.subject, "cannot inspect workflow impact notices", true, "retry once the database is readable", err)
	}
	if openErr := l.q.QueryRowContext(l.ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, l.workID).Scan(&state.ExternalConditionsOpen); openErr != nil {
		return wrapFailure(KindUnavailable, l.subject, "cannot inspect consequential workflow conditions", true, "retry once the database is readable", openErr)
	}
	_, designErr := readCurrentWorkflowDesign(l.ctx, l.q, l.workID)
	if designErr != nil {
		return designErr
	}
	state.DesignStale = false
	artifactStale, staleFoldErr := workflowArtifactStale(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject)
	if staleFoldErr != nil {
		return staleFoldErr
	}
	state.ArtifactStale = artifactStale
	return nil
}

// attemptAndReview folds the latest worker attempt, the latest result
// disposition, and the refine workflow's post-rejection review debt.
func (l workflowAdmissionLoad) attemptAndReview(state *WorkflowAdmissionState) error {
	if err := l.q.QueryRowContext(l.ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND lifecycle_state='in_flight')`, l.workID).Scan(&state.PendingWorkerAuthorization); err != nil {
		return workflowProjectionError(err, "cannot read unresolved worker authorizations")
	}
	if err := l.q.QueryRowContext(l.ctx, `SELECT COALESCE(a.lifecycle_state,''),COALESCE((SELECT json_extract(d.payload,'$.capability_class') FROM domain_events d WHERE d.subject_type='work_item' AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id ORDER BY d.seq DESC LIMIT 1),'') FROM worker_attempts a WHERE a.work_id=? ORDER BY a.dispatched_at DESC,a.attempt_id DESC LIMIT 1`, WorkerDispatched, l.workID).Scan(&state.AttemptState, &state.AttemptCapabilityClass); err != nil && err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, l.subject, "cannot read the latest worker attempt", true, "retry once the worker attempt projection is readable", err)
	}
	if err := l.q.QueryRowContext(l.ctx, `SELECT COALESCE(json_extract(f.payload,'$.action_id'),'') FROM domain_events f WHERE f.subject_type='work_item' AND f.subject_id=? AND f.kind=? AND json_extract(f.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') ORDER BY f.seq DESC LIMIT 1`, l.workID, WorkflowActionCompleted).Scan(&state.LatestResultDisposition); err != nil && err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, l.subject, "cannot read the latest result disposition", true, "retry once the workflow projection is readable", err)
	}
	return nil
}

// recoveryRoutes folds the recovery and correction routes. It returns the
// rejected-worker-result route before its dispatch-step restriction, which the
// mandate fold reads.
func (l workflowAdmissionLoad) recoveryRoutes(state *WorkflowAdmissionState) (bool, error) {
	workerFailureRoute, workerFailureErr := workflowWorkerFailureRecoveryAvailable(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject)
	if workerFailureErr != nil {
		return false, workerFailureErr
	}
	state.WorkerFailureRecovery = workerFailureRoute
	correctionRoute, correctionRouteErr := workflowRejectedWorkerResultAvailable(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject)
	if correctionRouteErr != nil {
		return false, correctionRouteErr
	}
	// The rejection recovery is a worker-dispatch-step route: off a step that
	// declares dispatch_worker the state carries no recovery, so a caller
	// cannot admit the rejection onto an unrelated step.
	state.CorrectionRecovery = correctionRoute && stepDeclaresAction(l.definition, l.currentStep, "dispatch_worker")
	// Both dispositions consume the same eligible completed result. A declared
	// acceptance action is not an available intent after that result is settled.
	if !correctionRoute && workflowDefinitionProducesDelivery(l.definition) && stepDeclaresAction(l.definition, l.currentStep, "accept_worker_result") {
		state.ReviewAcceptanceError = workflowFailureOf(reviewFailure(KindMissingEvidence,
			"result acceptance requires an undispositioned completed worker result",
			"finish an admitted worker assignment and have Main accept its exact completed result"))
	}
	reviewEvidenceAttempt, reviewEvidenceErr := workflowRefineReviewEvidenceAttempt(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject, 0)
	if reviewEvidenceErr != nil {
		return false, reviewEvidenceErr
	}
	state.RefineReviewEvidenceAttemptID = reviewEvidenceAttempt
	// The delivery gate's corrective return reads the review debt this fold
	// already carries, so the gate branch derives from the folded state and
	// never re-enters the loader; every other step folds the shared
	// correction-request admission directly. Under a duplicated projection
	// the singular active-contract reader refuses, and the duplicate
	// recovery owns the route out, so the conditional folds degrade.
	if workflowStepIsDeliveryGate(workflowStep(l.definition, l.currentStep)) {
		gateContext, gateErr := workflowDeliveryGateCorrectionContextFolded(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject, *state)
		if gateErr != nil && !(l.duplicated && workflowDuplicateContractProjection(gateErr)) {
			return false, gateErr
		}
		state.CorrectionRequestRecovery = gateErr == nil && gateContext != nil
		if gateErr == nil && gateContext == nil {
			state.CorrectionRequestMissing = workflowCorrectionMissingGateReview
		}
		if gateErr == nil {
			state.CorrectionRequestContext = gateContext
		}
	} else {
		correctionRequestContext, correctionRequestMissing, correctionRequestErr := workflowCorrectionRequestAdmissionForRegistry(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject, 0, l.registry)
		if correctionRequestErr != nil && !(l.duplicated && workflowDuplicateContractProjection(correctionRequestErr)) {
			return false, correctionRequestErr
		}
		state.CorrectionRequestRecovery, state.CorrectionRequestMissing = correctionRequestContext != nil && correctionRequestErr == nil, correctionRequestMissing
		if correctionRequestErr == nil {
			state.CorrectionRequestContext = correctionRequestContext
		}
	}
	return correctionRoute, nil
}

// holdsAndRecovery folds rejection and dispatch holds, oracle admission,
// and evidence-binding recovery.
func (l workflowAdmissionLoad) holdsAndRecovery(state *WorkflowAdmissionState) error {
	// A current-step advance condition from the history's consumption answer.
	if workflowStepCarriesRejectionHold(l.definition, l.currentStep) {
		rejectionHold, rejectionHoldErr := workflowUnconsumedUnboundRejection(l.ctx, l.q, l.workID, l.currentStep, l.subject, 0)
		if rejectionHoldErr != nil {
			return rejectionHoldErr
		}
		state.UnconsumedUnboundRejection = rejectionHold
	}
	if err := loadWorkflowOracleAdmissionTx(l.ctx, l.q, l.workID, l.definition, state); err != nil {
		return err
	}
	dispatchHold, holdErr := workflowDispatchHoldsStepAdvance(l.ctx, l.q, l.definition, l.workID, l.currentStep, 0)
	if holdErr != nil {
		return holdErr
	}
	state.DispatchHold = dispatchHold
	if evidenceRecovery, evidenceErr := workflowEvidenceBindingRecoveryEligibility(l.ctx, l.q, l.definition, l.workID, l.currentStep, 0); evidenceErr != nil {
		if !l.duplicated || !workflowDuplicateContractProjection(evidenceErr) {
			return evidenceErr
		}
		state.EvidenceRecoveryRoute = false
	} else {
		state.EvidenceRecoveryRoute = evidenceRecovery.Refusal == nil && len(evidenceRecovery.Requirements) != 0
		state.EvidenceRecovery = &evidenceRecovery
	}
	return nil
}

func loadWorkflowAcceptanceAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *WorkflowAdmissionState) error {
	state.ApprovalPolicy = definition.ApprovalPolicy
	// The confirmation authority folds once from the pinned policy and the
	// contract's declared fact, and every consumer reads this fold: a folded
	// coordinator authority closes the human question whatever the step shape
	// declares, and a missing or malformed new-policy fact is a typed
	// refusal, never engineering.
	// The confirmation folds only for exactly one active contract: the
	// duplicated projection is the supersede recovery's subject, so the
	// singular active-contract read never runs there and the loader stays
	// total for workflowAdmitSupersede.
	if state.ActiveContracts == 1 {
		fold, foldErr := foldWorkflowConfirmationTx(ctx, q, workID, definition)
		if foldErr != nil {
			return foldErr
		}
		state.ConfirmationAuthority, state.ConfirmationFactFailure = fold.authority, fold.factFailure
	}
	action, _ := workflowOperatorQuestionAction(definition, state.Step)
	state.PendingOperatorDecision = state.ActiveContracts == 1 && state.ConfirmationFactFailure == nil && state.ConfirmationAuthority != WorkflowConfirmationCoordinator && action != ""
	if state.ActiveContracts == 1 {
		subjectErr := workflowAcceptedSubjectCurrent(ctx, q, workID)
		if subjectErr != nil {
			var failure *Failure
			if !failureAs(subjectErr, &failure) || failure.Kind != KindStaleRequiresReview {
				return subjectErr
			}
			state.AcceptanceDeliverablesMissing = subjectErr
		}
	}
	// The confirmation prerequisites fold once for both authorities at the
	// step that hosts the confirmation: the recorded investigation and the
	// acceptance deliverables stand behind the coordinator's engineering
	// confirmation exactly as they stand behind the operator's question
	// answer, so the preflight and the work pin answer what the confirmation
	// enforces. Unrelated steps run no prerequisite read.
	if action == "" || state.ConfirmationFactFailure != nil {
		return nil
	}
	if !state.PendingOperatorDecision && state.ConfirmationAuthority != WorkflowConfirmationCoordinator {
		return nil
	}
	return foldWorkflowConfirmationPrerequisites(ctx, q, workID, definition, action, state)
}

// foldWorkflowConfirmationPrerequisites folds the recorded investigation and,
// at confirm_premise, the acceptance deliverables into the admission state. A
// typed refusal is the confirmation's answer; an unreadable projection fails
// the fold.
func foldWorkflowConfirmationPrerequisites(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, action string, state *WorkflowAdmissionState) error {
	if artifactErr := requireRecordedInvestigationArtifact(ctx, q, workID); artifactErr != nil {
		var failure *Failure
		if failureAs(artifactErr, &failure) && failure.Kind == KindMissingEvidence {
			if state.ConfirmationAuthority == WorkflowConfirmationCoordinator {
				// An earlier folded failure — the acceptance-subject
				// staleness above — keeps its place: the investigation
				// refusal never overwrites it.
				if state.AcceptanceDeliverablesMissing == nil {
					state.AcceptanceDeliverablesMissing = artifactErr
				}
				return nil
			}
			state.PendingOperatorDecision = false
			return nil
		}
		return artifactErr
	}
	if action != "confirm_premise" {
		return nil
	}
	if deliverablesErr := requireAcceptanceDeliverables(ctx, q, definition, workID); deliverablesErr != nil {
		// A typed refusal is the confirmation's answer; only an unreadable
		// projection fails the fold.
		var failure *Failure
		if !failureAs(deliverablesErr, &failure) || failure.Kind == KindUnavailable {
			return deliverablesErr
		}
		if state.AcceptanceDeliverablesMissing == nil {
			state.AcceptanceDeliverablesMissing = deliverablesErr
		}
	}
	return nil
}

// workflowConfirmationFold is the one classification answer the pinned
// policy and the active contract's declared fact fold to. Every consent
// consumer — the admission state, the question projection, the selection
// chain, and the continuity snapshot — reads this fold or the state it
// populated, never a second classifier.
type workflowConfirmationFold struct {
	authority   WorkflowConfirmationAuthority
	factFailure *Failure
}

// foldWorkflowConfirmationTx folds one active contract's confirmation
// authority from the pinned policy and its declared decision-domain fact,
// through the one pure classifier. A consequence_v1 contract whose fact is
// absent or malformed folds the typed refusal; legacy pins fold the empty
// authority and read nothing.
func foldWorkflowConfirmationTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition) (workflowConfirmationFold, error) {
	var fold workflowConfirmationFold
	if definition.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1 {
		return fold, nil
	}
	version, versionErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_admission")
	if versionErr != nil {
		if versionErr == sql.ErrNoRows {
			return fold, nil
		}
		return fold, versionErr
	}
	var stored sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, version).Scan(&stored); err != nil {
		if err == sql.ErrNoRows {
			return fold, nil
		}
		return fold, wrapFailure(KindUnavailable, "workflow_admission", "cannot read the workflow contract's declared decision fact", true, "retry once the database is readable", err)
	}
	var domains []string
	if stored.Valid {
		if err := json.Unmarshal([]byte(stored.String), &domains); err != nil {
			fold.factFailure = newFailure(KindInvalidInput, "workflow_admission", "the stored decision-domain fact is malformed", false, "rebuild projections from the event log")
			return fold, nil
		}
	}
	decision, decisionErr := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: domains})
	if decisionErr != nil {
		var failure *Failure
		if !failureAs(decisionErr, &failure) {
			return fold, decisionErr
		}
		fold.factFailure = failure
		return fold, nil
	}
	if decision.Consent == WorkflowConsentNone {
		fold.authority = WorkflowConfirmationCoordinator
		return fold, nil
	}
	fold.authority = WorkflowConfirmationOperator
	return fold, nil
}

// workflowAdmit decides one action's admission over one folded admission
// state. It is pure and total: no store access, no clock, no request payload,
// and a defined answer for every action the definitions declare. It decides
// the terminal immutability, the breaking-impact and consequential-condition
// boundaries, the supersede classification, the dispatch hold behind a stale
// design, the staleness
// classes, the review-debt advances, the recovery routes, and the step
// legality. The folded conditions hide only the advances they name — the
// recorded delivery at a review step and the refinement-step accept under
// review debt, a dispatch behind a stale design, and a consequential action behind a breaking impact
// notice or an open external condition — because each condition blocks a
// result, not the work: dispatch, recovery, and continuity actions stay
// admitted so a parked item always keeps a route. The payload and identity
// checks stay in the guards; the accept guard binds the request's attempt
// against the decision's ReadyReviewAttemptID.
// A group consumes continuation once. Its remaining members do not need an
// intermediate Review, but a changed depended-on revision still invalidates it.
func workflowAdmitReview(state WorkflowReviewState, group string) error {
	if admitted, found := state.Groups[group]; found && admitted.CorrectionRef != "" && state.Accepted != nil && state.Accepted.ReportRef == admitted.ReviewRef && state.DependenciesCurrent {
		return nil
	}
	if blocker := state.blocker(); blocker != "" {
		return reviewFailure(KindMissingEvidence, "open in-scope blocking finding "+blocker+" blocks execution progression", "obtain an evidenced closure of "+blocker+" and have Main accept that Review report")
	}
	if admitted, found := state.Groups[group]; group != "" && found {
		applicable := state.Accepted != nil && state.Candidate != nil && state.Accepted.Report.OutputCandidate == *state.Candidate
		if !applicable || admitted.ReviewRef == state.Accepted.ReportRef {
			if state.Accepted == nil || state.DependenciesCurrent {
				return nil
			}
		}
	}
	// First entry relies on the preceding stage exits and their binding guards
	// below. A missing Review is not evidence that execution has never run.
	if group != "" && !state.ExecutionProgressed && state.Accepted == nil {
		return nil
	}
	if group != "" && state.ContinueConsumed && state.GroupID == group && state.Accepted != nil && state.DependenciesCurrent {
		return nil
	}
	if state.Accepted == nil || state.Candidate == nil || state.Accepted.Report.OutputCandidate != *state.Candidate || !state.DependenciesCurrent {
		return reviewFailure(KindMissingEvidence, "execution progression requires an accepted Review on the current candidate and depended-on revisions", "obtain and accept a Review of the current integrated candidate; only valid preceding exits admit the first group")
	}
	disposition := state.Accepted.Report.Disposition
	if group != "" {
		if disposition != "continue" {
			return reviewFailure(KindMissingEvidence, "opening an ordinary assignment group requires accepted continue, not "+disposition, "obtain and accept continue, or follow the report's declared corrective/upstream obligation")
		}
		if state.ContinueConsumed && state.GroupID != group {
			return reviewFailure(KindOperationConflict, "this continue permission has already opened group "+state.GroupID, "finish that group, integrate its results, and obtain a new accepted continue")
		}
		return nil
	}
	if disposition != "ready_for_final_proof" {
		return reviewFailure(KindMissingEvidence, "leaving execution requires accepted ready_for_final_proof, not "+disposition, "obtain and accept ready_for_final_proof on the current candidate")
	}
	return nil
}

func workflowAdmit(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) WorkflowAdmissionDecision {
	decision := WorkflowAdmissionDecision{ReadyReviewAttemptID: state.ReadyReviewAttemptID, ReadyReviewSettles: state.ReadyReviewSettles}
	if workflowAdmitInstanceRefused(definition, state, actionID, &decision) {
		return decision
	}
	if actionID == "accept_worker_result" && state.ReviewAcceptanceError != nil {
		decision.Failure = state.ReviewAcceptanceError
		return decision
	}
	if workflowAdmitDispatchRefused(state, actionID, &decision) {
		return decision
	}
	if state.Review != nil && workflowDefinitionProducesDelivery(definition) {
		var reviewErr error
		switch actionID {
		case "dispatch_worker":
			if state.ReviewLane == "implementation" && !state.ReviewCorrective {
				reviewErr = workflowAdmitReview(*state.Review, state.ReviewGroup)
			}
		case "record_delivery":
			if state.Review.Candidate != nil || state.RefinementWorkflow {
				reviewErr = workflowAdmitReview(*state.Review, "")
			}
		case "complete":
			reviewErr = workflowAdmitReview(*state.Review, "")
		}
		if reviewErr != nil {
			decision.Failure = workflowFailureOf(reviewErr)
			return decision
		}
	}
	if actionID == "supersede_contract" {
		return workflowAdmitSupersede(definition, state, decision)
	}
	if err := state.stalenessRefusal(actionID); err != nil {
		decision.Failure = workflowFailureOf(err)
		return decision
	}
	if workflowAdmitDeliveryRefused(definition, state, actionID, &decision) {
		return decision
	}
	if workflowAdmitConfirmationRefused(state, actionID, &decision) {
		return decision
	}
	if workflowAdmitStepRefused(definition, state, actionID, &decision) {
		return decision
	}
	decision.Admitted = true
	return decision
}

// workflowAdmitInstanceRefused answers the refusals no action can pass:
// outside repair, a terminal or archived instance, a pending size rerun, and a
// breaking notice or open condition at a consequential boundary. Each phase
// below reports true when it decided the action, with decision filled.
func workflowAdmitInstanceRefused(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	if state.OutsideRepairActive {
		decision.Failure = newOutsideRepairRouteFailure("workflow_action", "managed workflow action refused: outside-repair disposition is active")
		return true
	}
	if workflowCompletedInstanceActionImmutable(state.InstanceState, actionID, state.Lifecycle) {
		decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "terminal workflow instance is immutable", false, "start a successor workflow")
		return true
	}
	// A nonterminal instance under an archived family refuses every public
	// action here — the recovery routes included — before any write. Terminal
	// instances keep the terminal-immutability answer above.
	if workflowFamilyArchived(definition.Ref) {
		decision.Failure = newFailure(KindInvalidOperation, "workflow_action",
			"workflow family "+definition.Ref+" is archived: new public workflow actions are refused", false,
			"read the recorded history, or start retained-family work")
		return true
	}
	consequence := workflowActionConsequence(definition, actionID)
	if workflowImpactBoundary(actionID, consequence) {
		if state.BreakingNotices != 0 {
			decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "breaking workflow impact notice blocks consequential execution", false, "reread_entities")
			return true
		}
		if state.ExternalConditionsOpen != 0 {
			decision.Failure = newFailure(KindNotTerminal, "workflow_action", "consequential action has unresolved external conditions", false, "reread_entities")
			return true
		}
	}
	return false
}

// workflowAdmitDispatchRefused answers dispatch_worker's own bounds: a stale
// design and the open oracle blocker bound.
func workflowAdmitDispatchRefused(state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	if actionID == "dispatch_worker" {
		if state.PendingWorkerAuthorization {
			decision.Failure = newFailure(KindOperationConflict, "worker_dispatch", "a prior worker authorization has an unresolved launch outcome", false, "reconcile the current assignment's launch outcome before authorizing another attempt")
			return true
		}
		if state.DesignStale {
			decision.Failure = newFailure(KindMissingEvidence, "worker_dispatch", "contract correction invalidated the recorded design", false, "use supersede_contract with design_record before worker dispatch")
			return true
		}
		// More than 32 genuine open oracle blockers is an
		// explicit no-dispatch bound refusal. Blockers are never silently
		// omitted to satisfy the bound; recovery is an approved scope or
		// contract revision, or stopping the work.
		if state.OracleOpenFindings > WorkflowOpenOracleFindingsLimit {
			decision.Failure = newFailure(KindLimitExceeded, "worker_dispatch", fmt.Sprintf("the work holds %d open oracle blockers above the %d-ID bound; dispatch is refused rather than truncating the set", state.OracleOpenFindings, WorkflowOpenOracleFindingsLimit), false, "close findings through supported closures or an approved scope/contract revision, or stop the work")
			return true
		}
	}
	return false
}

// workflowAdmitDeliveryRefused answers delivery admission, the mandate, and
// the refine workflow's fresh-review debt.
func workflowAdmitDeliveryRefused(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	// On a job-capable pin (CD-0205) the payload-blind accept is admissible
	// as local acceptance of one worker job, which holds the step; the
	// delivery-asserting accept applies workflowAdmitDelivery at its
	// payload-bound guard instead.
	if actionID == "accept_worker_result" && workflowAcceptDeliveryAdmissionActive(definition, state.Step) && !workflowWorkerJobsActive(definition) && !(state.ReadyReviewAttemptID != "" && !state.ReadyReviewSettles) {
		*decision = workflowAdmitDelivery(state, *decision)
		if decision.Failure != nil {
			return true
		}
	}
	if failure := workflowAdmitMandate(definition, state.Step, state.Mandate, actionID, "workflow_action"); failure != nil {
		decision.Failure = failure
		return true
	}
	if actionID == "record_delivery" {
		*decision = workflowAdmitDelivery(state, *decision)
		if decision.Failure != nil {
			return true
		}
	}
	return false
}

// workflowAdmitConfirmationRefused answers premise confirmation, completion
// facts, and the correction request route.
func workflowAdmitConfirmationRefused(state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
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
		return true
	}
	if (actionID == "confirm_premise" || actionID == "complete") && state.ConfirmationFactFailure != nil {
		// A missing or malformed new-policy decision fact is the fold's own
		// typed refusal: the confirmation and completion refuse on it instead
		// of classifying the work as engineering or waiting on a question.
		decision.Failure = workflowFailureOf(state.ConfirmationFactFailure)
		return true
	}
	if (actionID == "confirm_premise" || actionID == "complete") && state.AcceptanceDeliverablesMissing != nil {
		decision.Failure = workflowFailureOf(state.AcceptanceDeliverablesMissing)
		return true
	}
	// The question-open answer is the admission's: confirm_premise is
	// answered only through an open operator question, so the decision
	// marks the question closed and refuses advertisement. The work pin
	// reads this same decision and drops the unadmitted intent. A folded
	// coordinator confirmation is the engineering exit: the executing or
	// initiating non-worker coordinator records it without a question.
	if actionID == "confirm_premise" && !state.PendingOperatorDecision && state.ConfirmationAuthority != WorkflowConfirmationCoordinator {
		decision.OperatorQuestionClosed = true
		decision.Failure = newFailure(KindStaleRequiresReview, "workflow_action", "no operator question is open at the current workflow step", false, "record the investigation artifact the question requires, or take a declared route")
		return true
	}
	if actionID == "request_correction" && !state.CorrectionRequestRecovery {
		decision.Failure = workflowCorrectionRequestUnavailableFailure("workflow_action", state.CorrectionRequestMissing)
		return true
	}
	return false
}

// workflowAdmitStepRefused answers step advancement: the contract step, the
// dispatch hold, the unconsumed rejection, and step legality.
func workflowAdmitStepRefused(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	// The contract step is left only through the approval it hosts. An
	// advancing action there with no active contract would move the instance
	// past the only step that binds one, and completion refuses without a
	// contract, so the work could never finish. Version-1 research declares
	// frame_research in advance mode beside approve_contract on its frame step.
	if state.ActiveContracts == 0 && actionID != "approve_contract" && workflowActionAdvancesStep(definition, actionID) {
		if contractStep, err := workflowDefinitionContractStep(definition); err == nil && contractStep == state.Step {
			decision.Failure = newFailure(KindIllegalLifecycleTransition, "workflow_action", "the contract step advances only through its contract approval", false, "approve the workflow contract before advancing")
			return true
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
			return true
		}
	}
	// The accept-side sibling of the dispatch hold; the payload-omitted held
	// shapes defer to the guard and the fold.
	if state.UnconsumedUnboundRejection && actionID == "accept_worker_result" {
		mode, modeOK := workflowActionExecutionMode(definition, actionID)
		advances := modeOK && mode == ActionAdvance &&
			!workflowAcceptDeliveryAdmissionActive(definition, state.Step) &&
			!workflowWorkerJobsActive(definition)
		if failure := workflowRejectionAdvanceFailure(true, actionID, advances); failure != nil {
			decision.Failure = failure
			return true
		}
	}
	if !workflowAdmissionStepAllows(definition, state, actionID) {
		decision.OffStep = true
		decision.Failure = newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared on the current step", false, "reread_entities")
		return true
	}
	return false
}

// workflowAdmissionStepAllows decides the structural step legality: the step
// declares the action, or the folded state owns a recovery route that admits
// it off-step. Payload-bound evidence-binding recoveries stay beside the
// payloads they read; this answer is the route the state proves.
func workflowAdmissionStepAllows(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) bool {
	if definitionStepAllows(definition, state.Step, actionID) {
		return true
	}
	switch actionID {
	case "record_verdict":
		return state.ActiveContracts == 1 && state.LateCriterionAssociation && workflowLateCriterionAssociationStep(definition, state.Step)
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
// attempt-disposition actions admit the subject's own stale pin (CD-0222 D7).
func (s WorkflowAdmissionState) stalenessRefusal(actionID string) error {
	if workflowActionStalePinAdmission(actionID) != workflowStalePinAdmitNone {
		return s.LawPinSelfError
	}
	return s.LawPinStaleError
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

// workflowStepCarriesRejectionHold: a named non-checkpoint step carries the hold.
func workflowStepCarriesRejectionHold(definition WorkflowDefinition, currentStep string) bool {
	step := workflowStep(definition, currentStep)
	return step != nil && step.Kind != WorkflowStepHumanCheckpoint
}

// workflowUnconsumedUnboundRejection reports whether an unbound
// reject_worker_result record at the current step stands unconsumed in the
// prefix beforeSeq names (exclusive; 0 reads the current prefix), reusing the
// correction walk's consumption rule over a shallow copy of its records.
func workflowUnconsumedUnboundRejection(ctx context.Context, q queryer, workID, currentStep, subject string, beforeSeq int64) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	prefix := beforeSeq - 1
	if beforeSeq <= 0 {
		prefix = int64(1) << 62
	}
	history, err := workflowCorrectionWalkWorkerSatisfaction(ctx, q, workID, prefix, subject)
	if err != nil {
		return false, err
	}
	filtered := *history
	filtered.records = make([]workflowCorrectionRecordState, 0, len(history.records))
	for _, record := range history.records {
		if record.fields.ActionID != "reject_worker_result" || record.jobKey != "" || record.fields.StepID != currentStep {
			continue
		}
		filtered.records = append(filtered.records, record)
	}
	return filtered.openRecord("") != nil, nil
}

// workflowRejectionAdvanceFailure is the shared unconsumed-rejection advance
// refusal; non-advancing held shapes keep their routes.
func workflowRejectionAdvanceFailure(held bool, actionID string, advances bool) *Failure {
	if !held || actionID != "accept_worker_result" || !advances {
		return nil
	}
	return newFailure(KindIllegalLifecycleTransition, "workflow_action",
		"an unconsumed rejected worker result holds the step's advance", false,
		"dispatch a fresh worker whose materialized completion consumes the correction, then advance")
}
