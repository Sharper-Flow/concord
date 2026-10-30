package store

import (
	"context"
	"database/sql"
)

// WorkflowAdmissionState is the abstract state workflow action admission
// decides over. The tx-scoped loader derives it once, and workflowAdmit
// consumes it so every admission site answers identically for the same state.
type WorkflowAdmissionState struct {
	Review *WorkflowReviewState
	// Step is the work item's current workflow step.
	Step string
	// Lifecycle is the work item's lifecycle.
	Lifecycle string
	// InstanceState is the workflow instance state.
	InstanceState string
	// OutsideRepairActive suspends every managed action, including continuity
	// and recovery actions. Only the operator-only outside-repair APIs clear it.
	OutsideRepairActive bool
	// RefinementWorkflow reports whether the definition declares checkpoint_refine.
	RefinementWorkflow bool
	// ActiveContracts is the count of active workflow contracts.
	ActiveContracts int64
	// LateCriterionAssociation admits missing applicable proof at a terminal
	// or premise-question step without moving it back to a producer.
	LateCriterionAssociation bool
	// LawPinStale reports a staleness the contract recovery admits: a
	// deleted-law pin.
	LawPinStale bool
	// LawPinStaleError is the staleness boundary's refusal under the plain
	// admission, nil when the pinned law is current.
	LawPinStaleError error
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
	// EvidenceRecoveryRoute reports an outstanding contract evidence
	// requirement the recovery bind_evidence can settle.
	EvidenceRecoveryRoute bool
	// EvidenceRecovery retains the eligibility owner's snapshot facts for
	// readers; admission still decides through EvidenceRecoveryRoute.
	EvidenceRecovery *workflowEvidenceBindingRecoveryState
	// PendingOperatorDecision reports an open operator question at the
	// current step: the checkpoint declares a question-bearing action and
	// the investigation artifact stands behind it. workflowAdmit refuses
	// confirm_premise on it — the question-open refusal is the admission's
	// answer, the one owner the work pin's advertisement reads — while the
	// operator-selection chain keeps the payload checks, the closed choice
	// and the decision-context digest, which the payload-blind admission
	// cannot judge. A folded engineering confirmation authority closes the
	// question whatever the step shape declares.
	PendingOperatorDecision bool
	// ConfirmationAuthority is the folded confirmation authority of the one
	// active contract: any active contract records the authenticated
	// non-worker coordinator's own confirmation.
	ConfirmationAuthority WorkflowConfirmationAuthority
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
// one state, including recovery classification and the typed refusal.
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
	AdvanceHeld          bool
	DeliveryProofMissing bool
	// OperatorQuestionClosed marks the closed-question answer over
	// confirm_premise: the step declares the checkpoint confirmation
	// and no operator question stands open behind it. It is an advertisement
	// wall — the work pin drops the unadmitted
	// intent, and the interactive refusal plus the payload checks (the
	// closed choice, the decision-context digest, the operator identity)
	// stay with the operator-selection chain the payload-blind admission
	// cannot judge — so the execution path leaves confirm_premise to those
	// checks instead of refusing at the admission point.
	OperatorQuestionClosed bool
	Failure                *Failure
}

// WorkflowConfirmationAuthority names the folded confirmation authority of
// one active contract. A legacy pin folds the empty authority: the declared
// question machinery stays its confirmation path. Every active contract —
// engineering and decision-bearing alike — folds the coordinator authority:
// the authenticated non-worker coordinator records its own confirmation.
type WorkflowConfirmationAuthority string

const (
	// WorkflowConfirmationNone is the legacy fold: no typed authority, and
	// the question machinery keeps every answer it always gave.
	WorkflowConfirmationNone WorkflowConfirmationAuthority = ""
	// WorkflowConfirmationCoordinator records an engineering confirmation:
	// the executing or initiating non-worker coordinator confirms without a
	// human question, a human digest, or an operator substitution.
	WorkflowConfirmationCoordinator WorkflowConfirmationAuthority = "coordinator"
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
func loadWorkflowAdmissionStateTxForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, registry DefinitionRegistry) (WorkflowAdmissionState, []workflowVerificationRun, error) {
	state := WorkflowAdmissionState{
		Step:               currentStep,
		RefinementWorkflow: workflowRefinementStepID(definition) != "",
	}
	if err := q.QueryRowContext(ctx, `SELECT wi.instance_state,(SELECT lifecycle FROM work_items WHERE id=wi.work_id),(SELECT count(*) FROM workflow_contracts wc WHERE wc.work_id=wi.work_id AND wc.superseded_by IS NULL) FROM workflow_instances wi WHERE wi.work_id=?`, workID).Scan(&state.InstanceState, &state.Lifecycle, &state.ActiveContracts); err != nil {
		if err == sql.ErrNoRows {
			return WorkflowAdmissionState{}, nil, newFailure(KindProjectionNotFound, subject, "workflow instance is not recorded", false, "reread_entities")
		}
		return WorkflowAdmissionState{}, nil, wrapFailure(KindUnavailable, subject, "cannot read workflow admission state", true, "retry once the database is readable", err)
	}
	active, err := outsideRepairActiveTx(ctx, q, workID)
	if err != nil {
		return WorkflowAdmissionState{}, nil, err
	}
	state.OutsideRepairActive = active
	if active || state.InstanceState == "outside_repair" {
		return state, nil, nil
	}
	// A duplicated contract projection is the supersede recovery's subject:
	// workflowAdmitSupersede admits its route on the count alone, and the
	// singular active-contract reader refuses duplicates. The conditional
	// folds below that resolve one active contract degrade instead of
	// refusing, so the loader stays total over the projection the recovery
	// owns and every action still reaches its typed route.
	l := workflowAdmissionLoad{ctx: ctx, q: q, workID: workID, definition: definition, currentStep: currentStep, subject: subject, duplicated: state.ActiveContracts > 1, registry: registry}
	if err := l.staleness(&state); err != nil {
		return WorkflowAdmissionState{}, nil, err
	}
	// Duplicate active contracts leave no single criteria set to review
	// against, so no Review stands: every Review-gated action stays closed
	// while the supersede recovery the duplicates require stays reachable.
	review, reviewErr := loadWorkflowReviewState(ctx, q, workID)
	if reviewErr != nil && l.duplicated && workflowDuplicateContractProjection(reviewErr) {
		review, reviewErr = WorkflowReviewState{Findings: []ReviewFinding{}, PublicationOutstanding: true}, nil
	}
	if reviewErr != nil {
		return WorkflowAdmissionState{}, nil, reviewErr
	}
	state.Review = &review
	if err := l.recoveryRoutes(&state); err != nil {
		return WorkflowAdmissionState{}, nil, err
	}
	if err := l.holdsAndRecovery(&state); err != nil {
		return WorkflowAdmissionState{}, nil, err
	}
	if err := loadWorkflowAcceptanceAdmission(ctx, q, workID, definition, &state); err != nil {
		return WorkflowAdmissionState{}, nil, err
	}
	if state.ActiveContracts == 1 && workflowLateCriterionAssociationStep(definition, currentStep) {
		missing, err := workflowMissingCriterionAssociations(ctx, q, workID)
		if err != nil {
			return WorkflowAdmissionState{}, nil, err
		}
		state.LateCriterionAssociation = len(missing) != 0
	}
	if atComplete := workflowCompleteStepCorrectionStep(definition, currentStep); atComplete {
		completeCorrection, completeErr := workflowCompleteStepCorrectionAvailable(ctx, q, workID, definition, currentStep, subject, 0)
		if completeErr != nil {
			return WorkflowAdmissionState{}, nil, completeErr
		}
		state.CompleteStepCorrection = completeCorrection
	}
	contractCorrection, contractCorrectionErr := workflowContractCorrectionAvailable(ctx, q, workID, definition, currentStep, subject)
	if contractCorrectionErr != nil {
		return WorkflowAdmissionState{}, nil, contractCorrectionErr
	}
	state.ContractCorrectionAvailable = contractCorrection
	delivery, proofRuns, deliveryErr := loadWorkflowDeliveryAdmission(ctx, q, workID, definition, currentStep)
	if deliveryErr != nil {
		return WorkflowAdmissionState{}, nil, deliveryErr
	}
	state.Delivery = delivery
	mandate, mandateErr := loadWorkflowMandateAdmission(ctx, q, workID, definition, currentStep, subject, state)
	if mandateErr != nil {
		return WorkflowAdmissionState{}, nil, mandateErr
	}
	state.Mandate = mandate
	return state, proofRuns, nil
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

// staleness folds law-pin, design, and artifact staleness.
func (l workflowAdmissionLoad) staleness(state *WorkflowAdmissionState) error {
	lawErr := checkWorkflowLawRevisionStalenessTx(l.ctx, l.q, l.workID)
	state.LawPinStaleError = lawErr
	state.LawPinStale = lawErr != nil && workflowContractRecoveryStaleness(lawErr)
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

// recoveryRoutes folds the coordinator correction route.
func (l workflowAdmissionLoad) recoveryRoutes(state *WorkflowAdmissionState) error {
	// The delivery gate's corrective return reads the Review state this fold
	// already carries, so the gate branch derives from the folded state and
	// never re-enters the loader; every other step folds the shared
	// correction-request admission directly. Under a duplicated projection
	// the singular active-contract reader refuses, and the duplicate
	// recovery owns the route out, so the conditional folds degrade.
	if workflowStepIsDeliveryGate(workflowStep(l.definition, l.currentStep)) {
		gateContext, gateErr := workflowDeliveryGateCorrectionContextFolded(l.ctx, l.q, l.workID, l.definition, l.currentStep, l.subject, *state)
		if gateErr != nil && !(l.duplicated && workflowDuplicateContractProjection(gateErr)) {
			return gateErr
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
			return correctionRequestErr
		}
		state.CorrectionRequestRecovery, state.CorrectionRequestMissing = correctionRequestContext != nil && correctionRequestErr == nil, correctionRequestMissing
		if correctionRequestErr == nil {
			state.CorrectionRequestContext = correctionRequestContext
		}
	}
	return nil
}

// holdsAndRecovery folds evidence-binding recovery.
func (l workflowAdmissionLoad) holdsAndRecovery(state *WorkflowAdmissionState) error {
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
		fold, foldErr := foldWorkflowConfirmationTx(ctx, q, workID)
		if foldErr != nil {
			return foldErr
		}
		state.ConfirmationAuthority = fold.authority
	}
	action, _ := workflowOperatorQuestionAction(definition, state.Step)
	state.PendingOperatorDecision = state.ActiveContracts == 1 && state.ConfirmationAuthority != WorkflowConfirmationCoordinator && action != ""
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
	if action == "" {
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

// workflowConfirmationFold is the one classification answer the active
// contract's existence folds to. Every confirmation consumer — the admission
// state, the question projection, the selection chain, and the continuity
// snapshot — reads this fold or the state it populated, never a second
// classification.
type workflowConfirmationFold struct {
	authority WorkflowConfirmationAuthority
}

// foldWorkflowConfirmationTx folds one active contract's confirmation
// authority. An active contract folds the coordinator authority; a legacy
// pin with no active contract folds the empty authority and reads nothing.
func foldWorkflowConfirmationTx(ctx context.Context, q queryer, workID string) (workflowConfirmationFold, error) {
	var fold workflowConfirmationFold
	_, versionErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_admission")
	if versionErr != nil {
		if versionErr == sql.ErrNoRows {
			return fold, nil
		}
		return fold, versionErr
	}
	fold.authority = WorkflowConfirmationCoordinator
	return fold, nil
}

func workflowAdmit(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) WorkflowAdmissionDecision {
	decision := WorkflowAdmissionDecision{}
	if workflowAdmitInstanceRefused(definition, state, actionID, &decision) {
		return decision
	}
	if state.Review != nil && workflowDefinitionProducesDelivery(definition) && actionID == "complete" {
		if reviewErr := workflowAdmitReview(*state.Review); reviewErr != nil {
			decision.Failure = workflowFailureOf(reviewErr)
			return decision
		}
	}
	if actionID == "supersede_contract" {
		return workflowAdmitSupersede(definition, state, decision)
	}
	if err := state.LawPinStaleError; err != nil {
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
// outside repair or a terminal or archived instance. Each phase below reports
// true when it decided the action, with decision filled.
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
	return false
}

// workflowAdmitDeliveryRefused answers delivery admission and the mandate.
func workflowAdmitDeliveryRefused(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	reviewOnly := actionID == "record_review"
	if failure := workflowAdmitMandate(definition, state.Step, state.Mandate, actionID, "workflow_action", reviewOnly); failure != nil {
		decision.Failure = failure
		return true
	}
	if actionID == "record_delivery" {
		*decision = workflowAdmitDelivery(definition, state, *decision)
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

// workflowOperatorQuestionActions names the checkpoint actions whose step
// exit is gated behind the operator question. They are the fixed declared
// set; every other advancing action runs without a question wall.
var workflowOperatorQuestionActions = map[string]bool{
	"confirm_premise":   true,
	"approve_contract":  true,
	"accept_decision":   true,
	"approve_operation": true,
}

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
			if action.ID == candidate && workflowOperatorQuestionActions[candidate] {
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
		decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "contract recovery requires an active workflow contract", false, "restore a verified backup into a new path with concord restore, then swap")
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
