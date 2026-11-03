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
	// RefinementWorkflow reports whether the definition declares checkpoint_refine.
	RefinementWorkflow bool
	// ActiveContracts is the count of active workflow contracts.
	ActiveContracts int64
	// LateCriterionAssociation admits missing applicable proof at a terminal
	// or confirmation step without moving it back to a producer.
	LateCriterionAssociation bool
	// DesignStale reports a recorded design a contract correction
	// invalidated.
	DesignStale bool
	// CurrentRecords names ordinary actions with a retained current record.
	CurrentRecords map[string]bool
	// AcceptanceDeliverablesMissing holds a confirmation prerequisite refusal.
	AcceptanceDeliverablesMissing error
	Delivery                      workflowDeliveryAdmission
	Mandate                       workflowMandateAdmission
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

// loadWorkflowAdmissionStateTxForRegistry folds the same admission state with
// the caller's authoritative registry: correction routes resolve
// their pins through it. A nil registry
// selects the builtin registry.
func loadWorkflowAdmissionStateTxForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, registry DefinitionRegistry) (WorkflowAdmissionState, error) {
	state := WorkflowAdmissionState{
		Step:               currentStep,
		RefinementWorkflow: workflowRefinementStepID(definition) != "",
	}
	if err := q.QueryRowContext(ctx, `SELECT wi.instance_state,(SELECT lifecycle FROM work_items WHERE id=wi.work_id),(SELECT count(*) FROM workflow_contracts wc WHERE wc.work_id=wi.work_id AND wc.superseded_by IS NULL) FROM workflow_instances wi WHERE wi.work_id=?`, workID).Scan(&state.InstanceState, &state.Lifecycle, &state.ActiveContracts); err != nil {
		if err == sql.ErrNoRows {
			return WorkflowAdmissionState{}, newFailure(KindProjectionNotFound, subject, "workflow instance is not recorded", false, "reread_entities")
		}
		return WorkflowAdmissionState{}, wrapFailure(KindUnavailable, subject, "cannot read workflow admission state", true, "retry once the database is readable", err)
	}
	// Keep corrupt duplicate projections readable without choosing a contract
	// or authorizing contract-dependent actions. Recovery requires a verified restore.
	l := workflowAdmissionLoad{ctx: ctx, q: q, workID: workID, definition: definition, currentStep: currentStep, subject: subject, duplicated: state.ActiveContracts > 1, registry: registry}
	if err := l.staleness(&state); err != nil {
		return WorkflowAdmissionState{}, err
	}
	records, recordsErr := workflowCurrentRecords(ctx, q, workID)
	if recordsErr != nil {
		return WorkflowAdmissionState{}, recordsErr
	}
	state.CurrentRecords = records
	// Duplicate active contracts leave no single criteria set to review
	// against, so no Review stands and every Review-gated action stays closed.
	review, reviewErr := loadWorkflowReviewState(ctx, q, workID)
	if reviewErr != nil && l.duplicated && workflowDuplicateContractProjection(reviewErr) {
		review, reviewErr = WorkflowReviewState{Findings: []ReviewFinding{}, PublicationOutstanding: true}, nil
	}
	if reviewErr != nil {
		return WorkflowAdmissionState{}, reviewErr
	}
	state.Review = &review
	if err := loadWorkflowAcceptanceAdmission(ctx, q, workID, definition, &state); err != nil {
		return WorkflowAdmissionState{}, err
	}
	if state.ActiveContracts == 1 && workflowLateCriterionAssociationStep(definition, currentStep) {
		missing, err := workflowMissingCriterionAssociations(ctx, q, workID)
		if err != nil {
			return WorkflowAdmissionState{}, err
		}
		state.LateCriterionAssociation = len(missing) != 0
	}
	delivery, deliveryErr := loadWorkflowDeliveryAdmission(ctx, q, workID, definition, currentStep)
	if deliveryErr != nil {
		return WorkflowAdmissionState{}, deliveryErr
	}
	state.Delivery = delivery
	mandate, mandateErr := loadWorkflowMandateAdmission(ctx, q, workID, definition, currentStep, subject, state)
	if mandateErr != nil {
		return WorkflowAdmissionState{}, mandateErr
	}
	state.Mandate = mandate
	return state, nil
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

// staleness folds design and artifact staleness.
func (l workflowAdmissionLoad) staleness(state *WorkflowAdmissionState) error {
	_, err := readCurrentWorkflowDesign(l.ctx, l.q, l.workID)
	return err
}

func loadWorkflowAcceptanceAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *WorkflowAdmissionState) error {
	if !stepDeclaresAction(definition, state.Step, "confirm_premise") || state.ActiveContracts != 1 {
		return nil
	}
	if workflowDefinitionProducesDelivery(definition) {
		subjectErr := workflowAcceptedSubjectCurrent(ctx, q, workID)
		if subjectErr != nil {
			var failure *Failure
			if !failureAs(subjectErr, &failure) || failure.Kind != KindStaleRequiresReview {
				return subjectErr
			}
			state.AcceptanceDeliverablesMissing = subjectErr
		}
	}
	return foldWorkflowConfirmationPrerequisites(ctx, q, workID, definition, state)
}

// foldWorkflowConfirmationPrerequisites folds acceptance deliverables into the
// admission state. A typed refusal is the confirmation's answer; an unreadable
// projection fails the fold.
func foldWorkflowConfirmationPrerequisites(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, state *WorkflowAdmissionState) error {
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
	if state.AcceptanceDeliverablesMissing == nil {
		err := workflowForwardConfirmationReview(ctx, q, workID, definition, state.Step, state.Review)
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
				return err
			}
			state.AcceptanceDeliverablesMissing = err
		}
	}
	return nil
}

// A failure-edge confirmation returns to production; only forward confirmation
// requires the Review that will authorize final proof.
func workflowForwardConfirmationReview(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step string, review *WorkflowReviewState) error {
	if !workflowDefinitionProducesDelivery(definition) {
		return nil
	}
	if workflowConfirmationFailureEdge(definition, step) != "" {
		version, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
		if err != nil {
			return err
		}
		target, err := workflowConfirmationFailureTarget(ctx, q, workID, definition, step, version, 0)
		if err != nil || target != "" {
			return err
		}
	}
	if review == nil {
		state, err := loadWorkflowReviewState(ctx, q, workID)
		if err != nil {
			return err
		}
		review = &state
	}
	return workflowAdmitReview(*review)
}

func workflowAdmit(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) WorkflowAdmissionDecision {
	decision := WorkflowAdmissionDecision{}
	if workflowAdmitInstanceRefused(definition, state, actionID, &decision) {
		return decision
	}
	if workflowAdmitDeliveryRefused(definition, state, actionID, &decision) {
		return decision
	}
	if workflowAdmitConfirmationRefused(definition, state, actionID, &decision) {
		return decision
	}
	if workflowAdmitStepRefused(definition, state, actionID, &decision) {
		return decision
	}
	decision.Admitted = true
	return decision
}

// workflowAdmitInstanceRefused answers the refusals no action can pass:
// a terminal or archived instance. Each phase below reports
// true when it decided the action, with decision filled.
func workflowAdmitInstanceRefused(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	if workflowCompletedInstanceActionImmutable(state.InstanceState) && !(state.InstanceState == "completed" && actionID == "record_delivery" && state.CurrentRecords[actionID]) {
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
	if actionID == "bind_evidence" {
		if isTerminalLifecycle(state.Lifecycle) {
			decision.Failure = newFailure(KindInvalidOperation, "workflow_action", "terminal work cannot bind evidence", false, "start a successor workflow")
			return true
		}
		if state.ActiveContracts != 1 {
			decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "evidence binding requires exactly one active workflow contract", false, "restore the active workflow contract")
			return true
		}
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

// workflowAdmitConfirmationRefused answers premise confirmation and the correction request route.
func workflowAdmitConfirmationRefused(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string, decision *WorkflowAdmissionDecision) bool {
	// Confirmation records against the one active contract; without exactly
	// one, the confirmation write refuses, so admission and the pin refuse too.
	// An undeclared confirmation keeps the step-legality refusal.
	declared := actionID == "confirm_premise" && stepDeclaresAction(definition, state.Step, actionID)
	if declared && state.ActiveContracts == 0 {
		decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "premise confirmation requires an active workflow contract", false, "restore a verified backup into a new path with concord restore, then swap")
		return true
	}
	if declared && state.ActiveContracts > 1 {
		decision.Failure = newFailure(KindInvariantViolation, "workflow_action", "premise confirmation requires exactly one active workflow contract", false, "restore a verified backup into a new path with concord restore, then swap")
		return true
	}
	if actionID == "confirm_premise" && state.AcceptanceDeliverablesMissing != nil {
		decision.Failure = workflowFailureOf(state.AcceptanceDeliverablesMissing)
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
	if state.ActiveContracts == 0 && actionID != "approve_contract" && workflowCompletedActionAdvances(definition, workflowActionCompletedPayload{StepID: state.Step, ActionID: actionID}) {
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

// workflowAdmissionStepAllows decides structural step legality, including
// ordinary records and evidence binding after their declared steps.
func workflowAdmissionStepAllows(definition WorkflowDefinition, state WorkflowAdmissionState, actionID string) bool {
	if definitionStepAllows(definition, state.Step, actionID) || workflowRecordAction(actionID) && state.CurrentRecords[actionID] {
		return true
	}
	switch actionID {
	case "record_verdict":
		return state.ActiveContracts == 1 && state.LateCriterionAssociation && workflowLateCriterionAssociationStep(definition, state.Step)
	case "bind_evidence":
		return state.ActiveContracts == 1 && workflowEvidenceBindingAvailable(definition, state.Step)
	}
	return false
}
