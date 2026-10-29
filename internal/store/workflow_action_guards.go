package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// workflowActionGuardPhase names the point in the dispatcher's sequence where
// a guard runs. The phases are separate because the guards are not
// independent: stale recovery selects payload validation and lifecycle
// legality, and the claim guard runs after the durable operation is claimed.
type workflowActionGuardPhase int

const (
	guardPhaseRecovery workflowActionGuardPhase = iota
	guardPhaseBoundary
	guardPhasePostValidation
	guardPhaseClaim
)

// workflowActionGuardContext carries one action's execution state through the
// guard phases.
type workflowActionGuardContext struct {
	ctx           context.Context
	tx            *sql.Tx
	request       WorkflowActionExecutionRequest
	entry         RegisteredDefinition
	currentStep   string
	instanceState string
	// registry is the authoritative definition source the dispatch verified
	// the entry through: nil selects the builtin registry.
	registry DefinitionRegistry

	staleRecovery             bool
	recoveryBind              bool
	workerFailureRecovery     bool
	correctionRecovery        bool
	correctionRequestRecovery bool
	correctionRequestMissing  string
	// admissionState and admissionDecision carry the fold's one admission
	// derivation: the tx-scoped loader's folded state and workflowAdmit's
	// decision over it. Guards consume them instead of re-deriving the
	// conditions per site.
	admissionState          *WorkflowAdmissionState
	admissionDecision       *WorkflowAdmissionDecision
	deliveryProofRuns       []workflowVerificationRun
	deliveryIntegrationRuns []workflowVerificationRun
	actorRef                string
	eventActor              string
	operatorRef             string
	actorRecordPrefix       *workflowActorRecordPrefix
	operatorNeedsRecord     bool
}

type workflowActionGuardFunc func(*workflowActionGuardContext) error

type workflowActionGuard struct {
	phase workflowActionGuardPhase
	run   workflowActionGuardFunc
}

// workflowActionGuards declares every action-specific guard and the phase it
// runs in. An action acquires a guard only by appearing here; the dispatcher
// consults this table at each phase point in its sequence.
var workflowActionGuards = map[string]workflowActionGuard{
	"supersede_contract":     {guardPhaseRecovery, guardSupersedeContractRecovery},
	"reject_worker_result":   {guardPhaseRecovery, guardRejectWorkerResultRecovery},
	"request_correction":     {guardPhaseRecovery, guardRequestCorrectionRecovery},
	"complete":               {guardPhaseBoundary, guardCompleteBoundary},
	"record_verdict":         {guardPhaseBoundary, guardRecordVerdictArtifactFreshness},
	"accept_worker_result":   {guardPhaseClaim, guardAcceptWorkerResultDeliveryRoute},
	"accept_worker_evidence": {guardPhaseClaim, guardAcceptWorkerEvidenceRoute},
	"link_successor":         {guardPhasePostValidation, guardForwardLinkOnly},
	"cross_context_boundary": {guardPhaseClaim, guardNoRestartDispatch},
	"record_delivery":        {guardPhaseClaim, guardDeliveryAdmission},
}

func guardRejectWorkerResultRecovery(g *workflowActionGuardContext) error {
	if !g.correctionRecovery {
		return newFailure(KindInvalidOperation, "workflow_action", "worker result rejection is unavailable without a completed result", false, "accept or reject the completed worker result")
	}
	// The refusal binds the exact attempt pair the payload names; no
	// availability boolean for another open result authorizes it.
	fields, fieldsErr := workflowActionObject(g.defaultedPayload())
	if fieldsErr != nil {
		return fieldsErr
	}
	attemptID, attemptOK := workflowFieldString(fields, "attempt_id")
	epoch, epochOK := workflowFieldIntOK(fields, "attempt_epoch")
	if !attemptOK || attemptID == "" || !epochOK {
		return newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result requires the exact attempt_id and attempt_epoch of a completed result", false, "supply the attempt pair the work pin advertises")
	}
	if _, err := validateWorkerResultRejection(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, "workflow_action", attemptID, epoch, 0); err != nil {
		return err
	}
	// On an oracle-capable history the rejection's open finding
	// set must exactly equal the derived open set — omission never closes
	// a finding. Oracle-free histories keep the legacy optional field.
	return g.validateCorrectionOpenFindings()
}

// validateCorrectionOpenFindings is the derived-set equality both correction
// surfaces share: the payload's open_finding_ids must equal the
// lineage's derived open set whenever the work's history ever dispatched
// under an oracle-bearing job revision.
func (g *workflowActionGuardContext) validateCorrectionOpenFindings() error {
	fields, fieldsErr := workflowActionObject(g.defaultedPayload())
	if fieldsErr != nil {
		return fieldsErr
	}
	supplied := workflowFieldStrings(fields, "open_finding_ids")
	_, present := fields["open_finding_ids"]
	return validateCorrectionOpenFindingsTx(g.ctx, g.tx, g.request.WorkID, supplied, present)
}

func guardRequestCorrectionRecovery(g *workflowActionGuardContext) error {
	if !g.correctionRequestRecovery {
		return workflowCorrectionRequestUnavailableFailure("workflow_action", g.correctionRequestMissing)
	}
	// The payload check binds against the folded state's one correction
	// request derivation; it never re-enters the loader.
	state, err := g.foldedAdmissionState()
	if err != nil {
		return err
	}
	if state.CorrectionRequestContext == nil {
		return workflowCorrectionRequestUnavailableFailure("workflow_action", state.CorrectionRequestMissing)
	}
	if err := validateCorrectionRequestPayload(g.ctx, g.tx, g.request.WorkID, g.request.Payload, "workflow_action", state.CorrectionRequestContext); err != nil {
		return err
	}
	// A correction request on an oracle-capable history carries
	// the same derived open finding set a rejection does, and the folded
	// convergence comparison prefers it over the legacy predicate list.
	return g.validateCorrectionOpenFindings()
}

// runWorkflowActionGuard runs the request's guard when one is declared for
// this phase, and does nothing otherwise.
func runWorkflowActionGuard(g *workflowActionGuardContext, phase workflowActionGuardPhase) error {
	guard, ok := workflowActionGuards[g.request.ActionID]
	if !ok || guard.phase != phase {
		return nil
	}
	return guard.run(g)
}

// workflowContractAddsLaw reports whether the active contract itself declares
// the law id among its law additions, the only scope where an unbound mandate
// can name a law this branch never authored.
func workflowContractAddsLaw(ctx context.Context, q queryer, workID string, contractVersion int64, lawID string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_law_additions WHERE work_id=? AND contract_version=? AND law_id=?`, workID, contractVersion, lawID).Scan(&count); err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect the contract's law additions", true, "retry once the workflow projection is readable", err)
	}
	return count != 0, nil
}

func workflowSpecMandate(ctx context.Context, q queryer, workID, subject string) ([]string, int64, error) {
	var mandateJSON string
	version, activeErr := activeWorkflowContractVersion(ctx, q, workID, subject)
	if activeErr == sql.ErrNoRows {
		return nil, 0, nil
	}
	if activeErr != nil {
		return nil, 0, activeErr
	}
	if err := q.QueryRowContext(ctx, `SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, version).Scan(&mandateJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, 0, nil
		}
		return nil, 0, wrapFailure(KindUnavailable, subject, "cannot read the workflow spec mandate", true, "retry once the workflow contract is readable", err)
	}
	var mandate []string
	if err := json.Unmarshal([]byte(mandateJSON), &mandate); err != nil {
		return nil, 0, newFailure(KindInvariantViolation, subject, "workflow spec mandate is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return mandate, version, nil
}

// workflowEvidenceReferenceBound reports a durable evidence binding naming
// the reference inside the accepted history. afterSeq bounds that history:
// bindings at or before it do not count, and zero admits the whole history.
func workflowEvidenceReferenceBound(ctx context.Context, q queryer, workID, reference, subject string, afterSeq int64) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND binding_seq>? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, afterSeq, reference).Scan(&count); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect spec-mandate evidence", true, "retry once the workflow evidence projection is readable", err)
	}
	return count != 0, nil
}

func stepDeclaresAction(definition WorkflowDefinition, stepID, actionID string) bool {
	step := workflowStep(definition, stepID)
	if step == nil {
		return false
	}
	for _, declared := range step.Actions {
		if declared == actionID {
			return true
		}
	}
	return false
}

// workflowWorkerFailureRecovery reports whether the engine admits the
// hold-mode record_worker_failure recovery at the current step.
//
// A frozen definition that predates record_worker_failure (version 4 and
// earlier) admits it wherever a failed dispatched attempt stands. The
// recovery action is not added to the pinned definition, so the query
// preserves the definition digest.
//
// CD-0193 D1: the checkpoint pair keeps record_worker_failure off the
// human_checkpoint steps' declared actions, so a failed attempt there admits
// the same hold-mode record as an engine recovery. The record leaves the
// pinned definition digest unchanged, dispositions the attempt, and the
// checkpoint stops holding, so the operator's own gate opens.
func workflowWorkerFailureRecovery(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeRecorded bool) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	if !slices.Contains(definition.AvailableActions, "record_worker_failure") {
		return workflowFailedWorkerAttempt(ctx, q, workID, currentStep, subject, excludeRecorded)
	}
	if stepDeclaresAction(definition, currentStep, "record_worker_failure") {
		return false, nil
	}
	if step := workflowStep(definition, currentStep); step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return false, nil
	}
	failedAttemptID, bindingErr := workflowLatestFailedWorkerAuthorization(ctx, q, workID, currentStep)
	if bindingErr != nil {
		return false, bindingErr
	}
	attemptID, lifecycle, found, err := latestDispatchedAttemptAtStep(ctx, q, workID, currentStep, 0)
	if failedAttemptID != "" {
		attemptID, lifecycle, found = failedAttemptID, "failed", true
	}
	if err != nil || !found {
		return false, err
	}
	if lifecycle != "failed" {
		return false, nil
	}
	if !excludeRecorded {
		return true, nil
	}
	recorded, err := workerRecordedFailureCountTx(ctx, q, workID, attemptID)
	if err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect recorded worker failures", true, "retry once the worker attempt projection is readable", err)
	}
	return recorded == 0, nil
}

// workflowLatestFailedWorkerAuthorization also finds terminal launches without
// worker dispatch evidence. Only the latest authorization at this step may
// supply recovery identity; an older failure cannot replace a newer launch.
func workflowLatestFailedWorkerAuthorization(ctx context.Context, q queryer, workID, stepID string) (string, error) {
	var attemptID, lifecycle string
	err := q.QueryRowContext(ctx, `SELECT a.attempt_id,a.lifecycle_state FROM worker_authorizations c JOIN worker_attempts a ON a.work_id=c.work_id AND a.attempt_id=c.attempt_id WHERE c.work_id=? AND json_extract(c.payload,'$.step_id')=? ORDER BY c.seq DESC LIMIT 1`, workID, stepID).Scan(&attemptID, &lifecycle)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_failure_recovery", "cannot inspect the latest authorized worker attempt", true, "retry once the worker authorization is readable", err)
	}
	if lifecycle != "failed" {
		return "", nil
	}
	return attemptID, nil
}

// workflowFailedWorkerAttempt reports a failed worker attempt opened after
// the step's latest action start. The step-start bound is the failure
// recovery bound: a fresh fenced attempt at the step supersedes every earlier
// failure there.
func workflowFailedWorkerAttempt(ctx context.Context, q queryer, workID, currentStep, subject string, excludeRecorded bool) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	start, err := readWorkflowActionStartTx(ctx, q, workID, currentStep, false)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect the current workflow action start", true, "retry once the workflow projection is readable", err)
	}
	return workflowFailedWorkerAttemptSince(ctx, q, workID, start.Seq, subject, excludeRecorded)
}

// workflowFailedWorkerAttemptSince reports a failed worker attempt whose
// authorization or dispatch evidence follows sinceSeq. Callers pass their
// attempt window's bound: contract correction passes the step's pass boundary, so a
// failure recorded in a previous pass cannot open correction in the pass that
// returned to the step (CD-0133 D1).
func workflowFailedWorkerAttemptSince(ctx context.Context, q queryer, workID string, sinceSeq int64, subject string, excludeRecorded bool) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='failed'
 AND ((a.dispatch_seq>0 AND a.dispatch_seq>?) OR EXISTS (
  SELECT 1 FROM worker_authorizations d WHERE d.work_id=a.work_id AND d.attempt_id=a.attempt_id AND d.seq>?))`
	args := []any{workID, sinceSeq, sinceSeq}
	if excludeRecorded {
		query += ` AND NOT EXISTS (SELECT 1 FROM worker_attempt_dispositions f WHERE f.work_id=a.work_id AND f.attempt_id=a.attempt_id AND f.action_id='record_worker_failure')`
	}
	query += `)`
	var available int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&available); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect failed worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	return available != 0, nil
}

// workflowContractCorrectionAvailable reports whether operator-approved
// contract correction is open on the current step. A human checkpoint carries
// the route unconditionally. A worker-dispatch step admits correction before
// dispatch, after a worker failure, or after a worker result rejection. An
// authorized dispatch window counts even before a worker report exists. The
// pinned complete step admits correction only when the complete-step state
// gate passes.
func workflowContractCorrectionAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	if !workflowContractCorrectionCheckpoint(definition, currentStep) {
		return false, nil
	}
	if stepDeclaresAction(definition, currentStep, "complete") {
		return workflowCompleteStepCorrectionAvailable(ctx, q, workID, definition, currentStep, subject, 0)
	}
	step := workflowStep(definition, currentStep)
	if step.Kind == WorkflowStepHumanCheckpoint {
		return true, nil
	}
	var contracts int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contracts); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect the active workflow contract", true, "retry once the workflow projection is readable", err)
	}
	if contracts > 1 {
		return true, nil
	}
	if contracts != 1 {
		return false, nil
	}
	correction, correctionErr := workflowCorrectionContextForDispatch(ctx, q, workID, currentStep, "")
	if correctionErr != nil {
		return false, correctionErr
	}
	// A rejected job's unresolved obligations keep contract correction open;
	// accepting a different revision does not satisfy those obligations.
	if correction != nil && correction.Disposition == "rejected" {
		return true, nil
	}
	boundary, started, err := workflowStepPassBoundary(ctx, q, definition, workID, currentStep, subject)
	if err != nil {
		return false, err
	}
	if !started {
		return true, nil
	}
	var dispatched int
	if err := q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND dispatch_seq>=?)
 OR EXISTS(SELECT 1 FROM worker_authorizations WHERE work_id=? AND seq>=?)
 OR EXISTS(SELECT 1 FROM workflow_step_epochs WHERE work_id=? AND latest_dispatch_start_seq>=?)`, workID, boundary, workID, boundary, workID, boundary).Scan(&dispatched); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect worker dispatch authorization", true, "retry once the workflow projection is readable", err)
	}
	if dispatched == 0 {
		return true, nil
	}
	if decision, err := workflowRefineReviewCorrectionDecision(ctx, q, workID, definition, currentStep, subject, boundary); err != nil || decision != workflowRefineReviewCorrectionNotApplicable {
		return decision == workflowRefineReviewCorrectionOpen, err
	}
	// The failure check reads the same pass boundary as the dispatch check,
	// so both answers describe one pass: a failure recorded before the
	// return edge cannot reopen correction while the new pass's worker is
	// live (CD-0133 D1).
	failed, err := workflowFailedWorkerAttemptSince(ctx, q, workID, boundary, subject, false)
	if err != nil || !failed {
		return false, err
	}
	unrecorded, err := workflowFailedWorkerAttemptSince(ctx, q, workID, boundary, subject, true)
	if err != nil {
		return false, err
	}
	return !unrecorded, nil
}

func workflowWorkerFailureRecoveryAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	return workflowWorkerFailureRecovery(ctx, q, workID, definition, currentStep, subject, true)
}

func workflowWorkerFailureRecoveryMayFold(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	return workflowWorkerFailureRecovery(ctx, q, workID, definition, currentStep, subject, false)
}

func workflowEvidenceBindingStep(definition WorkflowDefinition, currentStep string) string {
	if stepDeclaresAction(definition, currentStep, "bind_evidence") {
		return currentStep
	}
	for _, step := range definition.StepGraph.Steps {
		if stepDeclaresAction(definition, step.ID, "bind_evidence") {
			return step.ID
		}
	}
	return ""
}

func workflowEvidenceRecoveryBindingStep(definition WorkflowDefinition, currentStep string) string {
	// Close declares the final reviewed judgement beside ordinary recovery;
	// it does not reopen unrestricted binding from the earlier evidence step.
	if stepDeclaresAction(definition, currentStep, "bind_evidence") && !stepDeclaresAction(definition, currentStep, "complete") {
		return ""
	}
	for _, step := range definition.StepGraph.Steps {
		if stepDeclaresAction(definition, step.ID, "bind_evidence") {
			return step.ID
		}
	}
	return ""
}

func workflowStepFollows(definition WorkflowDefinition, earlierStep, currentStep string) bool {
	seen := map[string]bool{}
	for step := earlierStep; step != "" && !seen[step]; step = workflowNextStep(definition, step) {
		seen[step] = true
		if step != earlierStep && step == currentStep {
			return true
		}
	}
	return false
}

type workflowEvidenceBindingRecoveryState struct {
	Requirements []workflowEvidenceRequirement
	// Refusal belongs to bind_evidence, not to another action whose admission
	// happens to probe binding availability in the same snapshot.
	Refusal *Failure
}

// workflowEvidenceBindingRecoveryEligibility is the tx-scoped owner for
// recovery calls, discovery, and completed-action validation. Read failures
// remain errors; a predicate refusal is a fact about this action's eligibility.
func workflowEvidenceBindingRecoveryEligibility(ctx context.Context, q queryer, definition WorkflowDefinition, workID, currentStep string, beforeSeq int64) (workflowEvidenceBindingRecoveryState, error) {
	var state workflowEvidenceBindingRecoveryState
	bindingStep := workflowEvidenceRecoveryBindingStep(definition, currentStep)
	if bindingStep == "" || !workflowStepFollows(definition, bindingStep, currentStep) {
		return state, nil
	}
	required, mandates, obligations, cutoff, err := workflowEvidenceRequirementInputs(ctx, q, workID)
	if err != nil {
		if workflowMissingContractProjection(err) {
			return state, nil
		}
		return state, err
	}
	requirements := workflowEvidenceRequirementDefinitions(required, mandates, definition, obligations)
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if err != nil {
		return state, err
	}
	predicates, err := readWorkflowContractPredicates(ctx, q, workID, contractVersion)
	if err != nil {
		return state, err
	}
	verdicts, err := latestWorkflowVerdictsBefore(ctx, q, workID, contractVersion, beforeSeq)
	if err != nil {
		return state, err
	}
	verdicted := make(map[string]bool, len(verdicts))
	for _, verdict := range verdicts {
		verdicted[verdict.PredicateID] = true
	}
	for _, predicate := range predicates {
		if verdicted[predicate.PredicateID] {
			continue
		}
		approved, decodeErr := DecodeWorkflowPredicate([]byte(predicate.OutcomePayload))
		if decodeErr != nil || string(approved.Kind) != predicate.OutcomeKind {
			state.Refusal = newFailure(KindInvariantViolation, "workflow_action", "approved outcome predicate is malformed or mismatched", false, "reread_entities")
			return state, nil
		}
		if approved.Kind == PredicateCheck {
			requirements = append(requirements, workflowEvidenceRequirement{Kind: string(EvidenceVerification), Reference: approved.ImmutableSubjectRef})
		}
	}
	state.Requirements, err = unboundWorkflowEvidenceRequirements(ctx, q, workID, requirements, cutoff, beforeSeq)
	return state, err
}

// guardRecoveryEvidenceBind admits an outstanding requirement or verification
// of an unverdicted active check, without reopening an already bound tuple.
func guardRecoveryEvidenceBind(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string, payload json.RawMessage, subject string, evidenceKinds ...string) (bool, error) {
	bindingStep := workflowEvidenceRecoveryBindingStep(definition, currentStep)
	if bindingStep == "" || !workflowStepFollows(definition, bindingStep, currentStep) {
		return false, nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return false, err
	}
	reference := workflowFieldStringDefault(fields, "evidence_ref", "")
	reference = workflowFieldStringDefault(fields, "immutable_subject_ref", reference)
	kind := workflowFieldStringDefault(fields, "evidence_kind", "verification")
	if stepDeclaresAction(definition, currentStep, "bind_evidence") && stepDeclaresAction(definition, currentStep, "complete") && reviewedClosureSubject(kind, reference) {
		kinds, err := workflowEvidenceBindingKinds("bind_evidence", evidenceKinds, fields, reference)
		if err != nil {
			return false, err
		}
		for _, emittedKind := range kinds {
			if !reviewedClosureSubject(emittedKind, reference) {
				return false, newFailure(KindIllegalLifecycleTransition, subject, "reviewed closure binding cannot include ordinary evidence kinds", false, "bind ordinary evidence separately through recovery before binding reviewed closure")
			}
		}
		return false, nil
	}
	_, _, _, cutoff, inputsErr := workflowEvidenceRequirementInputs(ctx, q, workID)
	if inputsErr != nil {
		return false, inputsErr
	}
	if reference != "" {
		exactBound, exactErr := workflowEvidenceTupleBound(ctx, q, workID, kind, reference, cutoff)
		if exactErr != nil {
			return false, exactErr
		}
		if exactBound {
			return false, newFailure(KindIllegalLifecycleTransition, subject, "recovery bind_evidence cannot reopen an already bound evidence tuple", false, fmt.Sprintf("use bind_evidence on step %q before advancing", bindingStep))
		}
	}
	recovery, requirementsErr := workflowEvidenceBindingRecoveryEligibility(ctx, q, definition, workID, currentStep, 0)
	if requirementsErr != nil {
		return false, requirementsErr
	}
	if recovery.Refusal != nil {
		return false, recovery.Refusal
	}
	for _, requirement := range recovery.Requirements {
		if requirement.Kind != "" && requirement.Kind != kind {
			continue
		}
		if requirement.Reference != "" && requirement.Reference != reference {
			continue
		}
		return true, nil
	}
	return false, newFailure(KindIllegalLifecycleTransition, subject, "recovery bind_evidence requires an outstanding contract evidence requirement or an unbound verification subject for an unverdicted active check", false, fmt.Sprintf("use bind_evidence on step %q before advancing", bindingStep))
}

// guardSupersedeContractRecovery keeps the supersede payload checks the
// recovery classification needs: the reserved route convention is payload
// state, and the recovery classification itself is the folded admission
// decision the dispatch fold already ran, so the guard records it for the
// later validation stages instead of re-deriving the stale-law and duplicate
// conditions.
func guardSupersedeContractRecovery(g *workflowActionGuardContext) error {
	fields, fieldsErr := workflowActionObject(g.defaultedPayload())
	if fieldsErr != nil {
		return fieldsErr
	}
	atCompleteStep := workflowCompleteStepCorrectionStep(g.entry.Definition, g.currentStep)
	declaresRoute := slices.Contains(workflowFieldStrings(fields, "route_conventions"), workflowCompleteStepCorrectionRoute)
	if declaresRoute && !atCompleteStep {
		return newFailure(KindInvalidPayload, "workflow_action", "route convention complete_step_correction is reserved for correction at the pinned complete step", false, "drop the reserved route convention")
	}
	if atCompleteStep && !declaresRoute {
		// The payload satisfies its own declaration; what refuses is the step
		// state, so the refusal is an operation refusal, not a payload one.
		return newFailure(KindInvalidPayload, "workflow_action", "complete-step correction requires complete_step_correction in fields.route_conventions", false, "declare complete_step_correction in the successor route conventions")
	}
	g.staleRecovery = g.admissionDecision != nil && g.admissionDecision.RecoveryRoute
	return nil
}

func workflowContractCorrectionCheckpoint(definition WorkflowDefinition, currentStep string) bool {
	step := workflowStep(definition, currentStep)
	if step == nil {
		return false
	}
	if stepDeclaresAction(definition, currentStep, "complete") {
		// The pinned complete step is a correction checkpoint whose admission
		// the complete-step state gate owns, so every surface that asks the
		// shared predicate evaluates the same conditions.
		return true
	}
	if slices.Contains(definition.StepGraph.TerminalSteps, currentStep) {
		return false
	}
	if workflowUnhealthyVerdictRouteTarget(definition, currentStep) != "" {
		return true
	}
	if step.Kind == WorkflowStepHumanCheckpoint {
		return slices.Contains(step.Actions, "confirm_premise")
	}
	return slices.Contains(step.Actions, "dispatch_worker")
}

// workflowCompleteStepCorrectionAvailable reports whether the pinned complete
// step of a break-fix or implementation workflow admits operator-approved
// contract correction. Every condition is state the action boundary, work pin,
// and preflight can read without the successor payload: the work item is
// nonterminal, at least one approved contract is active, no worker attempt is
// unsettled, and a same-work observation was recorded after the latest
// recorded verdict. Any other work kind or pinned shape refuses.
// A zero beforeSeq reads the current log; a fold bounds history before its
// supersession so later events cannot change that event's admission.
func workflowCompleteStepCorrectionAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, beforeSeq int64) (bool, error) {
	if workflowDisprovedPremiseAtCompleteRouteTarget(definition, currentStep) == "" {
		// The declared complete-step correction route is the shape: only a
		// disproved_premise_at_complete route seats the CD-0172 return, and
		// registration pins it on a step that declares the complete action.
		return false, nil
	}
	var lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot read the work item lifecycle", true, "retry once the work item is readable", err)
	}
	if lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded" {
		return false, nil
	}
	var contracts int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contracts); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect the active workflow contract", true, "retry once the workflow projection is readable", err)
	}
	// A duplicated projection takes the same route: the successor names
	// every active version, so the correction both repairs the projection
	// and returns the instance (CD-0203 D1).
	if contracts == 0 {
		return false, nil
	}
	unsettled, err := workflowUnsettledWorkerAttempt(ctx, q, workID, subject, beforeSeq)
	if err != nil || unsettled {
		return false, err
	}
	return workflowContradictionObservationRecorded(ctx, q, workID, subject, beforeSeq)
}

// workflowUnsettledWorkerAttempt reports a worker attempt that still holds
// the work: a dispatched attempt, a completed report without an accepted or
// rejected disposition or split replacement, or a failed attempt without a
// recorded failure.
// Correction at the complete step never strands one.
func workflowUnsettledWorkerAttempt(ctx context.Context, q queryer, workID, subject string, beforeSeq int64) (bool, error) {
	var unsettled int
	if err := q.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='dispatched') +
 (SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='completed' AND NOT EXISTS (
		SELECT 1 FROM worker_attempt_dispositions f WHERE f.work_id=a.work_id
		  AND f.action_id IN (`+workerAttemptDisposalActions+`)
		  AND f.attempt_id=a.attempt_id AND (?=0 OR f.seq<?))) +
 (SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='failed' AND NOT EXISTS (
		SELECT 1 FROM worker_attempt_dispositions f WHERE f.work_id=a.work_id
		  AND f.action_id='record_worker_failure'
		  AND f.attempt_id=a.attempt_id AND (?=0 OR f.seq<?)))`, workID, workID, beforeSeq, beforeSeq, workID, beforeSeq, beforeSeq).Scan(&unsettled); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect unsettled worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	return unsettled != 0, nil
}

// workflowContradictionObservationRecorded reports a same-work observation
// recorded after the latest verdict. Verdicts closed verification when they
// were recorded, so a record that postdates the newest verdict carries
// information verification did not hold: the contradiction that justifies
// replacing an otherwise satisfied contract. The observation kind carries
// generic append authority, so a public route can record it at the pinned
// complete step, where no declared action binds workflow evidence. The
// predicate reads the record's existence and ordering, never its content:
// the contradiction claim itself rides the successor's audit evidence and
// the verified operator approval (CD-0172).
func workflowContradictionObservationRecorded(ctx context.Context, q queryer, workID, subject string, beforeSeq int64) (bool, error) {
	var recorded int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_observations e
		WHERE e.work_id=? AND (?=0 OR e.recorded_seq<?)
		AND e.recorded_seq > COALESCE((SELECT MAX(v.verdict_seq) FROM workflow_predicate_verdicts v
			WHERE v.work_id=? AND (?=0 OR v.verdict_seq<?)), 0))`, workID, beforeSeq, beforeSeq, workID, beforeSeq, beforeSeq).Scan(&recorded); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect work observations", true, "retry once the work observation projection is readable", err)
	}
	return recorded != 0, nil
}

func guardCompleteBoundary(g *workflowActionGuardContext) error {
	return workflowCompletionBoundaryPreflight(g.request.Payload)
}

// guardForwardLinkOnly rejects nested or non-forward workflow composition.
func guardForwardLinkOnly(g *workflowActionGuardContext) error {
	fields, fieldErr := workflowActionObject(g.request.Payload)
	if fieldErr != nil {
		return fieldErr
	}
	relationKind := workflowFieldStringDefault(fields, "relation", "forward_link")
	if relationData := workflowFieldRaw(fields, "relation_data"); len(relationData) != 0 {
		var relation map[string]json.RawMessage
		if relationKind != "nested" && json.Unmarshal(relationData, &relation) == nil && workflowFieldStringDefault(relation, "kind", "") == "forward_link" {
			relationKind = "forward_link"
		}
	}
	if relationKind != "forward_link" {
		return newFailure(KindInvalidRelation, "workflow_action", "nested or non-forward workflow composition is forbidden", false, "use relation=forward_link")
	}
	if relationData := workflowFieldRaw(fields, "relation_data"); len(relationData) != 0 {
		var relation map[string]json.RawMessage
		if json.Unmarshal(relationData, &relation) != nil || workflowFieldStringDefault(relation, "kind", "") != "forward_link" {
			return newFailure(KindInvalidRelation, "workflow_action", "nested or non-forward workflow composition is forbidden", false, "use relation_data.kind=forward_link")
		}
	}
	return nil
}

// guardRecordedActorTuple verifies the authenticated actor against the durable
// workflow actor record, or retains the shared actor-recording draft when no
// row exists. It applies to every action rather than to one action ID, so the
// dispatcher calls it directly instead of through the per-action table, as it
// does for guardOperatorPremiseActor. Any action the current step declares is
// some session's possible first action, and the folds that call requireActor
// refuse until the row exists.
func guardRecordedActorTuple(g *workflowActionGuardContext) error {
	prefix, err := workflowActorRecordPrefixTx(g.ctx, g.tx, g.request.Actor)
	if err != nil {
		return err
	}
	g.actorRecordPrefix = prefix
	return nil
}

// workflowActorRecordPrefixTx is the shared actor-prefix owner for every
// writer that may meet an unrecorded authenticated session: it derives the
// canonical actor_ref and reads the durable tuple once, returning nil for a
// matching row or the absent-actor draft for recording. The draft carries no
// event metadata; the caller materializes its canonical event at its own
// version cursor with its own normalized time. A recorded row that disagrees
// with the authenticated tuple refuses; this owner never writes.
func workflowActorRecordPrefixTx(ctx context.Context, q queryer, actor WorkflowActor) (*workflowActorRecordPrefix, error) {
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		return nil, err
	}
	var recordedPrincipal, recordedClient, recordedAgent, recordedSession string
	recordErr := q.QueryRowContext(ctx, `SELECT principal_ref,client_ref,agent_ref,session_ref FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&recordedPrincipal, &recordedClient, &recordedAgent, &recordedSession)
	switch {
	case recordErr == sql.ErrNoRows:
		return &workflowActorRecordPrefix{actorRef: actorRef, actor: actor}, nil
	case recordErr != nil:
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow actor", true, "retry once the workflow actor authority is readable", recordErr)
	}
	if recordedPrincipal != actor.PrincipalRef || recordedClient != actor.ClientRef || recordedAgent != actor.AgentRef || recordedSession != actor.SessionRef {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "recorded workflow actor tuple does not match the authenticated actor", false, "reread workflow actor authority")
	}
	return nil, nil
}

type workflowActorRecordPrefix struct {
	actorRef string
	actor    WorkflowActor
}

// event materializes the exact canonical WorkflowActorRecorded event. It
// mutates nothing, so one lookup can serve repeated materializations.
func (p workflowActorRecordPrefix) event(eventID, workID string, now time.Time, expectedVersion int64) Event {
	return workflowTypedEvent(eventID, WorkflowActorRecorded, workID, p.actorRef, now, expectedVersion, map[string]any{"actor_ref": p.actorRef, "principal_ref": p.actor.PrincipalRef, "client_ref": p.actor.ClientRef, "agent_ref": p.actor.AgentRef, "session_ref": p.actor.SessionRef, "actor_class": string(p.actor.ActorClass)})
}

// Only these operator-decision actions can carry a verified operator identity.
// A worker cannot acquire that authority through its report.
func workflowActionAllowsOperatorIdentity(actionID string) bool {
	switch actionID {
	case "confirm_premise", "record_verdict", "complete", "supersede_contract", "request_correction":
		return true
	default:
		return false
	}
}

func guardOperatorPremiseActor(g *workflowActionGuardContext) error {
	newPolicy := g.entry.Definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1
	if g.request.OperatorActor == nil {
		return guardUnsignedPremiseRecovery(g, newPolicy)
	}
	return guardSignedPremiseActor(g, newPolicy)
}

func guardUnsignedPremiseRecovery(g *workflowActionGuardContext, newPolicy bool) error {
	if g.request.ActionID != "supersede_contract" && g.request.ActionID != "request_correction" {
		return nil
	}
	// The availability half is the shared derivation: the fold's state
	// carries the correction-request and contract-correction routes, so
	// this guard consumes it instead of re-deriving the conditions. The
	// operator identity requirement stays a guard check.
	state, err := g.foldedAdmissionState()
	if err != nil {
		return err
	}
	if g.request.ActionID == "request_correction" {
		if !state.CorrectionRequestRecovery {
			return workflowCorrectionRequestUnavailableFailure("workflow_action", state.CorrectionRequestMissing)
		}
		if newPolicy {
			// A repair request is routine: no operator approval identity,
			// and no self-evaluation predicate — the requesting
			// coordinator may have executed the failed delivery.
			return nil
		}
		return newFailure(KindApprovalRequired, "workflow_action", "correction request requires the verified operator approval identity", false, "request_approval")
	}
	if state.ContractCorrectionAvailable {
		if newPolicy {
			// Contract correction is routine under consequence_v1: the
			// authenticated coordinator supersedes unsigned, and the
			// fold keeps the duplicate-recovery, stale-law, and
			// audit-evidence walls it always ran.
			return nil
		}
		return newFailure(KindApprovalRequired, "workflow_action", "contract correction requires the verified operator approval identity", false, "request_approval")
	}
	return nil
}

func guardSignedPremiseActor(g *workflowActionGuardContext, newPolicy bool) error {
	if newPolicy && workflowActionAllowsOperatorIdentity(g.request.ActionID) && g.request.ActionID != "confirm_premise" {
		return newFailure(KindUnauthorized, "workflow_action", "consequence_v1 takes no operator substitution for "+g.request.ActionID+"; invoke under the authenticated evaluator or coordinator identity", false, "act under the verified caller identity")
	}
	if !workflowActionAllowsOperatorIdentity(g.request.ActionID) || g.request.OperatorActor.ActorClass != ActorOperator {
		return newFailure(KindUnauthorized, "workflow_action", "operator actor is only valid for signed premise confirmation, contract correction, conditioned verdict, and completion", false, "use the verified approval identity")
	}
	ref, err := WorkflowActorRef(*g.request.OperatorActor)
	if err != nil {
		return err
	}
	if ref == g.actorRef {
		return newFailure(KindUnauthorized, "workflow_action", "operator actor cannot relabel the invoking agent", false, "approve from an independent operator identity")
	}
	authority, err := workflowEvaluationAuthority(g.ctx, g.tx, g.request.WorkID, g.actorRef)
	if err != nil {
		return err
	}
	if authority == WorkflowWorkerEvaluator {
		return newFailure(KindUnauthorized, "workflow_action", "worker actors cannot submit operator decisions", false, "return the worker report to the coordinator")
	}
	var recordedPrincipal, recordedClient, recordedAgent, recordedSession string
	var recordedClass ActorClass
	recordErr := g.tx.QueryRowContext(g.ctx, `SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, ref).Scan(&recordedPrincipal, &recordedClient, &recordedAgent, &recordedSession, &recordedClass)
	switch {
	case recordErr == sql.ErrNoRows:
		g.operatorNeedsRecord = true
	case recordErr != nil:
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read operator actor", true, "retry once the database is readable", recordErr)
	default:
		if recordedPrincipal != g.request.OperatorActor.PrincipalRef || recordedClient != g.request.OperatorActor.ClientRef || recordedAgent != g.request.OperatorActor.AgentRef || recordedSession != g.request.OperatorActor.SessionRef || recordedClass != ActorOperator {
			return newFailure(KindInvariantViolation, "workflow_action", "recorded operator actor tuple does not match the signed assertion", false, "reread workflow actor authority")
		}
	}
	g.operatorRef = ref
	g.eventActor = ref
	return nil
}

// guardNoRestartDispatch keeps restart dispatch closed: CD-0027 excludes it,
// and the payload may not request it.
func guardNoRestartDispatch(g *workflowActionGuardContext) error {
	fields, fieldErr := workflowActionObject(g.defaultedPayload())
	if fieldErr != nil {
		return fieldErr
	}
	if workflowFieldStringDefault(fields, "mode", "summary") == "restart" || workflowFieldStringDefault(fields, "boundary_kind", "summary") == "restart" || fields["restart"] != nil {
		return newFailure(KindUnavailable, "workflow_action", "restart dispatch is not implemented and fails closed pending Concord issue #120", false, "contact_operator")
	}
	return nil
}

// guardDeliveryAdmission consumes the shared decision and binds the proof
// to the request's external tool declaration.
func guardDeliveryAdmission(g *workflowActionGuardContext) error {
	state, err := g.foldedAdmissionState()
	if err != nil {
		return err
	}
	decision, err := g.foldedAdmissionDecision()
	if err != nil {
		return err
	}
	if !decision.Admitted {
		return workflowExecutionAdmissionFailure(*decision, g.request.ProjectTooling)
	}
	// On a job-capable pin the payload-blind accept admission is local
	// acceptance and carries no delivery facet (CD-0205), so the
	// delivery-asserting accept applies the one delivery admission here,
	// where the payload names the assertion. The accepting attempt's own
	// dispatched revision counts as satisfied for the population check; the
	// core derives that binding from the attempt's dispatch record, so the
	// caller never asserts it. Its pending satisfaction supplies no
	// integration: missing integration evidence refuses the combined route
	// exactly as it refuses record_delivery.
	if g.request.ActionID == "accept_worker_result" && workflowWorkerJobsActive(g.entry.Definition) {
		fields, fieldsErr := workflowActionObject(g.defaultedPayload())
		if fieldsErr != nil {
			return fieldsErr
		}
		accepting, acceptingErr := workflowDispatchedJobForAttempt(g.ctx, g.tx, g.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
		if acceptingErr != nil {
			return acceptingErr
		}
		// The combined action integrates its pending result before delivery.
		// Judge both proof facets against that output, using the fold's candidate
		// selection rather than the pre-acceptance candidate cached above.
		delivery, proofRuns, integrationRuns, err := loadWorkflowDeliveryAdmission(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, workflowFieldStringDefault(fields, "attempt_id", ""))
		if err != nil {
			return err
		}
		projected := *state
		projected.Delivery = delivery
		if state.Review != nil {
			auth, result, err := readReviewAttempt(g.ctx, g.tx, g.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
			if err != nil {
				return err
			}
			review, err := projectWorkflowReviewAcceptance(g.ctx, g.tx, g.request.WorkID, *state.Review, auth, result, g.actorRef)
			if err != nil {
				return err
			}
			projected.Review = &review
		}
		state = &projected
		g.deliveryProofRuns, g.deliveryIntegrationRuns = proofRuns, integrationRuns
		if delivery := workflowAdmitDeliveryForAccept(g.entry.Definition, *state, WorkflowAdmissionDecision{}, accepting); delivery.Failure != nil {
			return workflowExecutionAdmissionFailure(delivery, g.request.ProjectTooling)
		}
	}
	if state.Delivery.ProofRequired && g.request.ProjectTooling != nil {
		qualifying, why, err := workflowVerificationProof(g.deliveryProofRuns, g.request.ProjectTooling)
		if err != nil {
			return err
		}
		if !qualifying {
			failure := workflowRefineProofFailure(why)
			failure.Detail += " " + ProjectToolingDeclaredText(g.request.ProjectTooling)
			return failure
		}
	}
	// The integration facet re-validates the same runs against the request's
	// declared tooling, the split the refine proof keeps: a qualifying run of
	// an undeclared tool covers no required Project.
	if failure := workflowIntegrationToolingFailure(state.Delivery, g.deliveryIntegrationRuns, g.request.ProjectTooling); failure != nil {
		failure.Detail += " " + ProjectToolingDeclaredText(g.request.ProjectTooling)
		return failure
	}
	return nil
}

// workflowAcceptDeliveryAdmissionActive reports whether the pinned definition
// and step admit the combined accept-and-delivery route (CD-0198 D4): a
// definition declaring the accept-delivery capability, at its refinement step.
// The CD-0166 delivery gate stays record_delivery-only.
func workflowAcceptDeliveryAdmissionActive(definition WorkflowDefinition, currentStep string) bool {
	if currentStep == "" || workflowRefinementStepID(definition) != currentStep {
		return false
	}
	return workflowCapabilities(definition).has(WorkflowCapabilityAcceptDelivery)
}

func guardAcceptWorkerEvidenceRoute(g *workflowActionGuardContext) error {
	fields, err := workflowActionObject(g.defaultedPayload())
	if err != nil {
		return err
	}
	if workflowAcceptCarriesDeliveryAssertion(fields) {
		return newFailure(KindInvalidPayload, "workflow_action", "review evidence acceptance cannot carry delivery fields", false, "assert delivery through the declared delivery route")
	}
	if definitionStepAllows(g.entry.Definition, g.currentStep, "accept_worker_evidence") {
		return nil
	}
	return validateRefineReviewEvidenceDisposition(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, fields, "workflow_action", 0)
}

// guardAcceptWorkerResultDeliveryRoute composes the accept_worker_result
// claim guard (CD-0198 D4). At the delivery-admitting refinement step the
// accept carries its delivery fields and runs guardDeliveryAdmission;
// everywhere else the delivery fields refuse. A plain accept at the admitting step refuses with the
// delivery-admission remedy: the refine step exits only through an admitted
// delivery assertion, so the CD-0192 refine-exit proof can no longer be
// crossed by an advancing accept that carries no artifact.
func guardAcceptWorkerResultDeliveryRoute(g *workflowActionGuardContext) error {
	fields, fieldsErr := workflowActionObject(g.defaultedPayload())
	if fieldsErr != nil {
		return fieldsErr
	}
	if err := guardAcceptWorkerResultChanges(g, fields); err != nil {
		return err
	}
	artifact := workflowFieldStringDefault(fields, "delivery_artifact", "")
	state := workflowFieldStringDefault(fields, "delivery_state", "")
	// Payload-bound advance derivation while the folded hold stands.
	if g.admissionState.UnconsumedUnboundRejection {
		mode, modeOK := workflowActionExecutionMode(g.entry.Definition, "accept_worker_result")
		advances := modeOK && mode == ActionAdvance
		if advances && artifact == "" && state == "" && workflowWorkerJobsActive(g.entry.Definition) {
			job, jobErr := workflowDispatchedJobForAttempt(g.ctx, g.tx, g.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
			if jobErr != nil {
				return jobErr
			}
			advances = job == nil
		}
		if failure := workflowRejectionAdvanceFailure(true, "accept_worker_result", advances); failure != nil {
			return failure
		}
	}
	if workflowAcceptDeliveryAdmissionActive(g.entry.Definition, g.currentStep) {
		// On a job-capable pin a plain accept of a job-bound attempt is local
		// acceptance of that one worker job: the step stays held (CD-0205).
		// An attempt dispatched without a job binding has no local
		// disposition to record, and the refine step still exits only through
		// an admitted delivery assertion.
		if artifact == "" && state == "" && workflowWorkerJobsActive(g.entry.Definition) {
			job, jobErr := workflowDispatchedJobForAttempt(g.ctx, g.tx, g.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
			if jobErr != nil {
				return jobErr
			}
			if job != nil {
				return nil
			}
		}
		if artifact == "" || state == "" {
			return newFailure(KindInvalidOperation, "workflow_action",
				"the refine step exits only through an admitted delivery assertion: record_delivery, or an accept_worker_result carrying delivery_artifact and delivery_state asserted",
				false, "run worktree_verify on the refined work, bind the run's evidence, then accept the result with delivery_artifact and delivery_state asserted")
		}
		return guardDeliveryAdmission(g)
	}
	if artifact != "" || state != "" {
		return newFailure(KindInvalidPayload, "workflow_action",
			"accept_worker_result carries fields.delivery_artifact and fields.delivery_state only at the delivery-admitting refinement step",
			false, "drop the delivery fields here, or assert the delivery through record_delivery")
	}
	return nil
}

// workflowRefineProofGateActive reports whether the refine-exit proof guard
// governs record_delivery on this step. The guard is active only at the
// refinement step of a definition declaring the refine-proof capability.
func workflowRefineProofGateActive(definition WorkflowDefinition, currentStep string) bool {
	if currentStep == "" || workflowRefinementStepID(definition) != currentStep {
		return false
	}
	return workflowCapabilities(definition).has(WorkflowCapabilityRefineProof)
}

// workflowVerificationBindingsAfter lists the immutable subject refs bound as
// verification evidence after afterSeq, the current refine epoch's start, in
// binding order.
func workflowVerificationBindingsAfter(ctx context.Context, q queryer, workID string, afterSeq int64, subject string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings
		WHERE work_id=? AND binding_seq>?
		AND json_extract(payload,'$.evidence_kind')='verification'
		ORDER BY binding_seq`, workID, afterSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the refine epoch's evidence bindings", true, "retry once the workflow evidence projection is readable", err)
	}
	defer rows.Close()
	refs := make([]string, 0, 4)
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, wrapFailure(KindUnavailable, subject, "cannot scan the refine epoch's evidence bindings", true, "retry once the workflow evidence projection is readable", err)
		}
		if !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot enumerate the refine epoch's evidence bindings", true, "retry once the workflow evidence projection is readable", err)
	}
	return refs, nil
}

// foldedAdmissionState returns the fold's admission state, folding it on the
// guard's transaction when the caller did not carry one. The dispatch fold
// always folds before the guard phases run; the fallback keeps a directly
// constructed guard context on the same single derivation.
func (g *workflowActionGuardContext) foldedAdmissionState() (*WorkflowAdmissionState, error) {
	if g.admissionState != nil {
		return g.admissionState, nil
	}
	state, proofRuns, integrationRuns, err := loadWorkflowAdmissionStateTxForRegistry(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, "workflow_action", g.registry)
	if err != nil {
		return nil, err
	}
	if err := bindWorkflowReviewRequest(g.ctx, g.tx, &state, g.request.WorkID, g.request.ActionID, g.request.Payload, g.request.Actor); err != nil {
		return nil, err
	}
	g.admissionState = &state
	g.deliveryProofRuns = proofRuns
	g.deliveryIntegrationRuns = integrationRuns
	return &state, nil
}

func (g *workflowActionGuardContext) foldedAdmissionDecision() (*WorkflowAdmissionDecision, error) {
	if g.admissionDecision != nil {
		return g.admissionDecision, nil
	}
	state, err := g.foldedAdmissionState()
	if err != nil {
		return nil, err
	}
	decision := workflowAdmit(g.entry.Definition, *state, g.request.ActionID)
	g.admissionDecision = &decision
	return &decision, nil
}

// workflowAcceptCarriesDeliveryAssertion reports whether one accept payload
// carries the CD-0198 D4 delivery fields, the shape whose acceptance advances
// the refinement step.
func workflowAcceptCarriesDeliveryAssertion(fields map[string]json.RawMessage) bool {
	return workflowFieldStringDefault(fields, "delivery_artifact", "") != "" || workflowFieldStringDefault(fields, "delivery_state", "") != ""
}

// workflowAcceptAdvancesStep reports whether an accept_worker_result
// completion leaves its step: only the combined accept-and-delivery at the
// refinement step does (CD-0198 D4).
func workflowAcceptAdvancesStep(definition WorkflowDefinition, stepID string, fields map[string]json.RawMessage) bool {
	return workflowAcceptDeliveryAdmissionActive(definition, stepID) && workflowAcceptCarriesDeliveryAssertion(fields)
}

// defaultedPayload returns the action payload with an empty payload
// normalized to an empty JSON object, the form the later stages process.
func (g *workflowActionGuardContext) defaultedPayload() json.RawMessage {
	if len(g.request.Payload) == 0 {
		return json.RawMessage(`{}`)
	}
	return g.request.Payload
}

// guardWorkflowActionStepMatch rejects a nested payload that restates a
// current step other than the pinned one.
func guardWorkflowActionStepMatch(payload json.RawMessage, currentStep string) error {
	if actionFields, fieldsErr := workflowActionObject(payload); fieldsErr == nil {
		if nestedRaw := workflowFieldRaw(actionFields, "payload"); len(nestedRaw) != 0 {
			var nested map[string]json.RawMessage
			if json.Unmarshal(nestedRaw, &nested) == nil {
				if declaredStep := workflowFieldStringDefault(nested, "current_step", ""); declaredStep != "" && declaredStep != currentStep {
					return newFailure(KindInvariantViolation, "workflow_action", "workflow action payload step does not match the pinned current step", false, "reread_entities")
				}
			}
		}
	}
	return nil
}

// normalizeWorkflowActionRequest applies the shared request defaults and
// bounds. The mutation is deliberate: the normalized values feed the durable
// claim and the assembled events.
func normalizeWorkflowActionRequest(request *WorkflowActionExecutionRequest) error {
	if request.OperationID == "" {
		return newFailure(KindInvalidOperation, "workflow_action", "durable operation ID is required", false, "retry with a stable idempotency identity")
	}
	if request.IdempotencyIdentity == "" {
		request.IdempotencyIdentity = request.IdempotencyKey
	}
	if len(request.IdempotencyIdentity) < 2 || len(request.IdempotencyIdentity) > 128 {
		return newFailure(KindInvalidOperation, "workflow_action", "idempotency identity is out of bounds", false, "retry with a bounded idempotency key")
	}
	if request.Now.IsZero() {
		request.Now = nowFromClock(nil)
	}
	if !validDigest(request.ContractDigest) {
		return newFailure(KindSchemaUnsupported, "workflow_action", "contract_digest is not a SHA-256 digest", false, "supply the current manifest digest")
	}
	if request.AcceptedInputsDigest == "" {
		return newFailure(KindInvalidOperation, "workflow_action", "accepted input digest is required", false, "retry with the canonical request digest")
	}
	if request.Tool == "" {
		request.Tool = "concord_work_transition"
	}
	if request.RequestID == "" {
		return newFailure(KindInvalidOperation, "workflow_action", "request ID is required", false, "retry with the transport request ID")
	}
	return nil
}

// claimDurableWorkflowOperationTx resolves the current step, claims the
// durable operation, and prepares its evidence authority. The returned step
// and evidence refs feed event assembly.
func claimDurableWorkflowOperationTx(ctx context.Context, tx *sql.Tx, entry RegisteredDefinition, request WorkflowActionExecutionRequest, currentStep string) (*WorkflowStep, []string, error) {
	step := workflowStep(entry.Definition, currentStep)
	if step == nil {
		return nil, nil, newFailure(KindInvariantViolation, "workflow_action", "current workflow step is not registered", false, "reread_entities")
	}
	durableStepKind := string(step.Kind)
	if step.Kind == WorkflowStepHumanCheckpoint {
		// durable_operations predates human checkpoints and has a closed
		// three-value step_kind. The workflow event retains the checkpoint kind;
		// the durable claim uses its owning SQLite transaction as authority.
		durableStepKind = string(WorkflowStepInternalSQLite)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.OperationID, 1, request.WorkID, entry.Definition.Ref, entry.Digest, currentStep, durableStepKind, request.AcceptedInputsDigest, nullableWorkflowText(request.AcceptedScope), request.PrincipalRef, request.RequestID, request.Now.UTC().Format(time.RFC3339Nano), request.ContractDigest); err != nil {
		return nil, nil, wrapFailure(KindIdempotencyConflict, "workflow_action", "durable workflow operation identity is already claimed: "+err.Error(), false, "retry the same request or reconcile the operation", err)
	}
	evidenceRefs := append([]string(nil), request.EvidenceRefs...)
	if len(evidenceRefs) == 0 {
		evidenceRefs = []string{"evidence:" + request.OperationID}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET result_kind='completed',evidence_refs=? WHERE op_id=? AND attempt_epoch=?`, workflowJSON(evidenceRefs), request.OperationID, 1); err != nil {
		return nil, nil, wrapFailure(KindUnavailable, "workflow_action", "cannot prepare workflow evidence authority", true, "retry once the database is writable", err)
	}
	return step, evidenceRefs, nil
}

// workflowActionAssemblyInput is the normalized state the event assembly
// folds into the action's events.
type workflowActionAssemblyInput struct {
	ctx          context.Context
	tx           *sql.Tx
	entry        RegisteredDefinition
	request      WorkflowActionExecutionRequest
	currentStep  string
	step         *WorkflowStep
	payload      json.RawMessage
	evidenceRefs []string

	eventActor          string
	operatorRef         string
	actorRecordPrefix   *workflowActorRecordPrefix
	operatorNeedsRecord bool
	// registry is the authoritative definition source the dispatch verified
	// the entry through: nil selects the builtin registry.
	registry DefinitionRegistry
	// admission carries the fold's derived admission state, so the assembly's
	// payload checks read the folded conditions instead of re-deriving them.
	admission *WorkflowAdmissionState
}

type workflowActionEventAssembly struct {
	events       []Event
	attemptEpoch int64
}

// assembleWorkflowActionEventsTx resolves the execution mode and start epoch
// and folds the recorded actors, start or checkpoint envelope, and semantic
// events into the action's event list.
func assembleWorkflowActionEventsTx(ctx context.Context, tx *sql.Tx, in workflowActionAssemblyInput) (workflowActionEventAssembly, error) {
	var out workflowActionEventAssembly
	executionMode, ok := workflowActionExecutionMode(in.entry.Definition, in.request.ActionID)
	if !ok {
		return out, newFailure(KindInvariantViolation, "workflow_action", "workflow action execution mode is not declared", false, "repair the pinned workflow definition")
	}
	actor := in.eventActor
	events := []Event{}
	versionCursor := in.request.ExpectedVersion
	if in.actorRecordPrefix != nil {
		events = append(events, in.actorRecordPrefix.event(in.request.OperationID+":actor", in.request.WorkID, in.request.Now, versionCursor))
		versionCursor++
	}
	if in.operatorNeedsRecord {
		events = append(events, workflowTypedEvent(in.request.OperationID+":operator", WorkflowActorRecorded, in.request.WorkID, in.operatorRef, in.request.Now, versionCursor, map[string]any{"actor_ref": in.operatorRef, "principal_ref": in.request.OperatorActor.PrincipalRef, "client_ref": in.request.OperatorActor.ClientRef, "agent_ref": in.request.OperatorActor.AgentRef, "session_ref": in.request.OperatorActor.SessionRef, "actor_class": string(ActorOperator)}))
		versionCursor++
	}
	var workflowActionEpoch int64
	var epochErr error
	if builtinActionPolicies[in.request.ActionID].EventShape == ActionEventCheckpoint && executionMode != ActionFenced {
		// CD-0198 D3: the step's first admitted qualifying checkpoint
		// derives the step start in the same operation, before the
		// epoch-dependent checkpoint fold. A refused action never reaches
		// this assembly, so a derivation is atomic with its action, and the
		// started event's fold opens the same fence and epoch a callable
		// start opened.
		derivedEpoch, derivedStart, deriveErr := workflowDerivedCheckpointStartTx(ctx, tx, in.entry.Definition, in.request, in.currentStep, actor, versionCursor)
		if deriveErr != nil {
			return out, deriveErr
		}
		if derivedStart != nil {
			events = append(events, *derivedStart)
			workflowActionEpoch = derivedEpoch
		} else {
			workflowActionEpoch, epochErr = workflowCheckpointAttemptEpoch(ctx, tx, in.entry.Definition, in.step, in.request.WorkID, in.currentStep)
		}
	} else {
		workflowActionEpoch, epochErr = workflowActionStartEpochForDispatch(ctx, tx, in.request.WorkID, in.currentStep, executionMode == ActionFenced)
	}
	if epochErr != nil {
		return out, epochErr
	}
	out.attemptEpoch = workflowActionEpoch
	if executionMode == ActionFenced {
		resultVersion := versionCursor + 1
		startPayload, _ := json.Marshal(map[string]any{
			"work_id": in.request.WorkID, "expected_version": versionCursor, "resulting_version": resultVersion,
			"step_id": in.currentStep, "action_id": in.request.ActionID, "attempt_epoch": workflowActionEpoch,
			"accepted_inputs_digest": in.request.AcceptedInputsDigest, "idempotency_identity": in.request.IdempotencyIdentity, "actor_ref": actor,
		})
		events = append(events, Event{EventID: in.request.OperationID + ":started", Kind: WorkflowActionStarted, SubjectType: SubjectWorkItem, SubjectID: in.request.WorkID, Actor: actor, OccurredAt: in.request.Now, PayloadVersion: 1, Payload: startPayload})
	}
	switch {
	case builtinActionPolicies[in.request.ActionID].EventShape == ActionEventCheckpoint:
		resultVersion := versionCursor + int64(len(events)-int(versionCursor-in.request.ExpectedVersion)) + 1
		checkpointPayload, _ := json.Marshal(map[string]any{"action_id": in.request.ActionID, "fields": in.payload})
		checkpoint, _ := json.Marshal(map[string]any{
			"work_id": in.request.WorkID, "expected_version": versionCursor + int64(len(events)-int(versionCursor-in.request.ExpectedVersion)), "resulting_version": resultVersion,
			"step_id": in.currentStep, "step_kind": string(in.step.Kind), "attempt_epoch": workflowActionEpoch,
			"checkpoint_payload": json.RawMessage(checkpointPayload), "resume_cursor": "", "actor_ref": actor,
			"request_id": in.request.RequestID, "checkpoint_id": in.request.OperationID + ":checkpoint",
			"accepted_inputs_digest": in.request.AcceptedInputsDigest, "idempotency_identity": in.request.IdempotencyIdentity,
		})
		events = append(events, Event{EventID: in.request.OperationID + ":checkpoint", Kind: WorkflowActionCheckpointed, SubjectType: SubjectWorkItem, SubjectID: in.request.WorkID, Actor: actor, OccurredAt: in.request.Now, PayloadVersion: 1, Payload: checkpoint})
	case in.request.ActionID != "complete":
		semantic, semanticErr := workflowSemanticActionEvents(ctx, tx, in.entry.Definition, in.request, in.currentStep, actor, in.payload, versionCursor+int64(len(events)-int(versionCursor-in.request.ExpectedVersion)))
		if semanticErr != nil {
			return out, semanticErr
		}
		events = append(events, semantic...)
	}
	out.events = events
	return out, nil
}

// workflowActionOmitsGenericCompletion names the actions whose typed event is
// the durable action boundary: they append no generic completion, so the
// completed-action fold never moves the step on them, whatever execution mode
// a pinned definition declares (CD-0112 D1).
func workflowActionOmitsGenericCompletion(actionID string) bool {
	return actionID == "checkpoint_context" || actionID == "cross_context_boundary" || actionID == "supersede_contract" || actionID == "record_work_context"
}

// workflowActionAdvancesStep reports whether a completed action moves the
// instance to the next step: the fold advances on an advance-mode generic
// completion and on nothing else.
func workflowActionAdvancesStep(definition WorkflowDefinition, actionID string) bool {
	if workflowActionOmitsGenericCompletion(actionID) {
		return false
	}
	mode, ok := workflowActionExecutionMode(definition, actionID)
	return ok && mode == ActionAdvance
}

// appendGenericWorkflowCompletion appends the generic WorkflowActionCompleted
// event. Continuity's typed event is the durable action boundary; appending a
// generic completion after it would make the checkpoint immediately stale, so
// the continuity actions are excluded.
//
// For dispatch_worker the second return is the canonical lane-packet digest
// (sha256:hex of canonicalJSON(worker_packet)). Empty for every other verb,
// because every other action carries no worker packet; the caller must not
// surface the digest on the response result unless the string is non-empty.
// CD-0067 D2/D6: the digest is the value the worker-dispatch evidence
// boundary quotes on its signed assertion, so the core returns it to the
// adapter rather than letting the adapter compute it.
func appendGenericWorkflowCompletion(in workflowActionAssemblyInput, attemptEpoch int64, events []Event) ([]Event, string, error) {
	if workflowActionOmitsGenericCompletion(in.request.ActionID) {
		return events, "", nil
	}
	fields, fieldsErr := workflowActionObject(in.payload)
	if fieldsErr != nil {
		return events, "", fieldsErr
	}
	completionValues := map[string]any{
		"step_id": in.currentStep, "action_id": in.request.ActionID, "attempt_epoch": attemptEpoch, "result_evidence_refs": in.evidenceRefs,
		"changed_refs": []string{in.request.WorkID}, "actor_ref": in.eventActor,
	}
	if in.request.ActionID == "record_worker_job" {
		var err error
		events, err = appendWorkflowMemberCompletion(in, fields, events, completionValues)
		if err != nil {
			return events, "", err
		}
	}
	if in.request.ActionID == "record_verdict" {
		// The batched form declares its entry count on the completion, so the
		// fold bounds the operation's result evidence at the schema-bounded
		// batch union instead of the single-form bound. The single form stays
		// silent and keeps its bound.
		if _, batchPresent := fields["verdicts"]; batchPresent {
			entries, entriesErr := normalizeWorkflowVerdictEntries(fields)
			if entriesErr != nil {
				return events, "", entriesErr
			}
			completionValues["verdict_entry_count"] = len(entries)
		}
	}
	if in.request.ActionID == "record_delivery" && workflowDefinitionRequiresDeliveryPayload(in.entry.Definition) {
		artifact := workflowFieldStringDefault(fields, "delivery_artifact", "")
		state := workflowFieldStringDefault(fields, "delivery_state", "")
		if artifact == "" || state == "" {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "record_delivery requires delivery_artifact and delivery_state", false, "supply the asserted delivery artifact and state")
		}
		completionValues["delivery_artifact"] = artifact
		completionValues["delivery_state"] = state
	}
	if in.request.ActionID == "accept_worker_result" {
		if err := bindWorkflowAcceptedResultCompletion(in, fields, completionValues); err != nil {
			return events, "", err
		}
	}
	var workerPacketDigest string
	if in.request.ActionID == "accept_worker_result" || in.request.ActionID == "accept_worker_evidence" || in.request.ActionID == "record_worker_failure" || in.request.ActionID == "reject_worker_result" {
		completionValues["attempt_epoch"] = workflowFieldInt(fields, "attempt_epoch", 0)
		completionValues["worker_attempt_id"] = workflowFieldStringDefault(fields, "attempt_id", "")
	}
	if in.request.ActionID == "dispatch_worker" {
		var err error
		workerPacketDigest, err = bindWorkflowDispatchPacketCompletion(in, fields, completionValues)
		if err != nil {
			return events, "", err
		}
	}
	if in.request.ActionID == "reject_worker_result" || in.request.ActionID == "request_correction" {
		if err := bindWorkflowCorrectionCompletion(in, fields, completionValues); err != nil {
			return events, "", err
		}
	}
	resultVersion := in.request.ExpectedVersion + int64(len(events)) + 1
	events = append(events, workflowTypedEvent(in.request.OperationID+":completed", WorkflowActionCompleted, in.request.WorkID, in.eventActor, in.request.Now, resultVersion-1, completionValues))
	return events, workerPacketDigest, nil
}

func bindWorkflowAcceptedResultCompletion(in workflowActionAssemblyInput, fields map[string]json.RawMessage, completionValues map[string]any) error {
	// CD-0205: an acceptance records the worker-job disposition it
	// satisfies. The binding is derived here from the accepted attempt's
	// worker.dispatched event — the caller never asserts it — so the
	// correction window closes on a recorded disposition under the exact
	// dispatched job, and an unrelated accepted job cannot close another
	// job's window by name.
	if err := validateReviewAcceptance(in.ctx, in.tx, in.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""), in.eventActor); err != nil {
		return err
	}
	acceptedJob, jobErr := workflowDispatchedJobForAttempt(in.ctx, in.tx, in.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
	if jobErr != nil {
		return jobErr
	}
	if acceptedJob != nil {
		completionValues["worker_job"] = map[string]any{"job_id": acceptedJob.JobID, "revision": acceptedJob.Revision, "digest": acceptedJob.Digest}
	}
	// CD-0227 D2/D3: the acceptance holds the observed delivered
	// paths to the job's path scope and its evidenced caller repairs
	// (CD-0242 D6 keeps scope and ownership checks).
	if repairErr := admitWorkerDeliveredPathsTx(in.ctx, in.tx, in.request.WorkID, in.entry.Definition, acceptedJob, workflowFieldStringDefault(fields, "attempt_id", "")); repairErr != nil {
		return repairErr
	}
	// CD-0198 D4: the combined accept asserts the same delivery fields on the
	// same completion event a record_delivery appends, so delivery readers
	// identify the assertion by its asserted fields rather than by action_id.
	// The guard already required the fields; this refuses closed if the
	// route ever runs without them. Local acceptance of a job-bound attempt
	// carries no delivery assertion and holds the step (CD-0205).
	if workflowAcceptDeliveryAdmissionActive(in.entry.Definition, in.currentStep) {
		artifact := workflowFieldStringDefault(fields, "delivery_artifact", "")
		state := workflowFieldStringDefault(fields, "delivery_state", "")
		localJobAccept := artifact == "" && state == "" && acceptedJob != nil && workflowWorkerJobsActive(in.entry.Definition)
		if (artifact == "" || state == "") && !localJobAccept {
			return newFailure(KindInvalidPayload, "workflow_action", "the combined accept requires delivery_artifact and delivery_state", false, "supply the asserted delivery artifact and state")
		}
		if artifact != "" && state != "" {
			completionValues["delivery_artifact"] = artifact
			completionValues["delivery_state"] = state
		}
	}
	// Main's local acceptance records the disposition without advancing;
	// only combined acceptance and delivery leaves refinement.
	if !workflowAcceptAdvancesStep(in.entry.Definition, in.currentStep, fields) {
		completionValues["review_advance_held"] = true
	}
	return nil
}

func bindWorkflowDispatchPacketCompletion(in workflowActionAssemblyInput, fields map[string]json.RawMessage, completionValues map[string]any) (string, error) {
	// CD-0059 D1/D5: the dispatch_worker completion carries the authorized
	// attempt_id into the durable record so the worker-dispatch evidence
	// boundary can prove the window exists and has not been consumed. The
	// start already records the attempt_epoch; the completion binds the
	// attempt identity. CD-0067 D2 additionally binds the canonical
	// digest of the lane packet so the durable record names what the
	// window was opened for, not only which attempt it was opened
	// against. CD-0067 D6: the digest returned here is the value the
	// adapter quotes on its signed dispatch assertion — the adapter
	// does not compute the digest itself, so the assertion's evidence
	// is bound to the exact bytes the core digested and authorized.
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	completionValues["worker_attempt_id"] = attemptID
	packetRaw := workflowFieldRaw(fields, "worker_packet")
	// Preflight enforces worker_packet is present and decodes to one
	// strict JSON object, so an empty raw here means a defensive
	// fallback; refuse closed with the same typed failure rather
	// than record a half-bound attempt.
	if len(packetRaw) == 0 {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is absent from the action payload", false, "supply the lane packet bound to this work item and attempt")
	}
	var packetFields map[string]json.RawMessage
	if err := json.Unmarshal(packetRaw, &packetFields); err != nil {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is not a JSON object", false, "supply the lane packet bound to this work item and attempt")
	}
	packetWorkID, workIDOK := workflowFieldString(packetFields, "work_id")
	packetAttemptID, attemptIDOK := workflowFieldString(packetFields, "attempt_id")
	if !workIDOK || !attemptIDOK {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is missing work_id or attempt_id", false, "supply the lane packet bound to this work item and attempt")
	}
	if packetWorkID != in.request.WorkID {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet.work_id does not match the action's work_id", false, "supply the lane packet bound to this work item and attempt")
	}
	if packetAttemptID != attemptID {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet.attempt_id does not match fields.attempt_id", false, "supply the lane packet bound to this work item and attempt")
	}
	// The lane-step dispatch join is composed onto every
	// step kind some lane may dispatch, so the step admitting the action
	// does not by itself admit the lane. Resolve the packet's lane
	// identity and refuse when that lane's capability class is not
	// dispatchable at the current step's kind.
	packetLaneID, laneIDOK := workflowFieldString(packetFields, "lane_id")
	packetLaneDigest, laneDigestOK := workflowFieldString(packetFields, "lane_digest")
	if !laneIDOK || !laneDigestOK {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is missing a complete lane identity", false, "supply the lane packet bound to this work item and attempt")
	}
	lane, laneErr := LookupLane(packetLaneID, packetLaneDigest)
	if laneErr != nil {
		return "", laneErr
	}
	if in.step != nil && !LaneStepDispatchAllowed(lane.CapabilityClass, in.step.Kind) {
		// The inverse read of the join turns the refusal into a read:
		// the caller sees the classes this step kind admits and can
		// pick one instead of guessing.
		admitted := strings.Join(LaneStepDispatchClasses(in.step.Kind), ", ")
		return "", newFailure(KindUnauthorizedDispatch, "workflow_action", "lane capability class "+lane.CapabilityClass+" is not dispatchable at a "+string(in.step.Kind)+" step; the step admits capability classes "+admitted, false, "dispatch the lane at a step kind the lane-step dispatch join admits")
	}
	if in.tx != nil {
		if err := bindWorkerDispatchCompletionTx(in, lane, packetRaw, completionValues); err != nil {
			return "", err
		}
	}
	// The registry lane identity rides the completion so the fold can
	// bind the attempt durably in flight from the record alone, without
	// re-reading the packet bytes.
	completionValues["worker_lane_id"] = lane.ID
	completionValues["worker_lane_digest"] = lane.Digest
	completionValues["worker_capability_class"] = lane.CapabilityClass
	canonical, err := canonicalJSON(packetRaw)
	if err != nil {
		return "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet does not decode as canonical JSON", false, "supply the lane packet bound to this work item and attempt")
	}
	sum := sha256.Sum256(canonical)
	workerPacketDigest := "sha256:" + hex.EncodeToString(sum[:])
	completionValues["worker_packet_digest"] = workerPacketDigest
	// The typed outcome predicate ids are extracted from the same packet
	// bytes this digest covers, so the durable record carries the
	// discharge obligations the fold enforces on the completion. The
	// payload schema preflight already admitted the packet shape; this
	// read re-checks the identity rules the obligations rest on.
	predicateIDs, predicateErr := workerPacketPredicateIDs(packetRaw)
	if predicateErr != nil {
		return "", predicateErr
	}
	// The list is recorded unconditionally, empty when the packet
	// declared no typed predicates, so the fold can refuse a dispatch
	// record that carries no list instead of skipping the discharge
	// requirement: absence is a malformed record, never an empty one.
	// A nil extraction marshals as null, which the fold reads as
	// absence, so it normalizes to the empty list here.
	if predicateIDs == nil {
		predicateIDs = []string{}
	}
	completionValues["worker_packet_predicate_ids"] = predicateIDs
	if in.request.SessionWorktreeIdentity != "" {
		completionValues["worker_worktree_identity"] = in.request.SessionWorktreeIdentity
	}
	return workerPacketDigest, nil
}

func bindWorkflowCorrectionCompletion(in workflowActionAssemblyInput, fields map[string]json.RawMessage, completionValues map[string]any) error {
	completionValues["correction_diagnosis"] = workflowFieldStringDefault(fields, "diagnosis", "")
	completionValues["correction_strategy"] = workflowFieldStringDefault(fields, "strategy", "")
	completionValues["correction_predicate_ids"] = workflowFieldStrings(fields, "predicate_ids")
	completionValues["correction_evidence_refs"] = workflowFieldStrings(fields, "evidence_refs")
	// the optional coordinator attribution derives here, inside
	// the authenticated action transaction: the request carries the
	// direct producer observation alone and the store derives the core
	// identity from the cited combined run or the exact action attempt.
	block, blockErr := deriveCoordinatorCorrectionAttributionTx(in.ctx, in.tx, in.request.WorkID, in.request.ActionID, fields)
	if blockErr != nil {
		return blockErr
	}
	if block != nil {
		completionValues["failure_attribution"] = block
	}
	// The derived open finding set serializes onto the correction
	// record of both surfaces. A rejection carries it directly; a
	// correction request carries it beside the predicate list the legacy
	// comparison reads, and the folded convergence comparison prefers it
	// whenever the request recorded one.
	if _, present := fields["open_finding_ids"]; present {
		completionValues["correction_open_finding_ids"] = workflowFieldStrings(fields, "open_finding_ids")
	}
	return nil
}

func bindWorkerDispatchCompletionTx(in workflowActionAssemblyInput, lane LaneDefinition, packetRaw json.RawMessage, completionValues map[string]any) error {
	state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(in.ctx, in.tx, in.request.WorkID, in.entry.Definition, in.currentStep, "worker_dispatch", in.registry)
	if err != nil {
		return err
	}
	if err := bindWorkflowReviewRequest(in.ctx, in.tx, &state, in.request.WorkID, "dispatch_worker", in.request.Payload, in.request.Actor); err != nil {
		return err
	}
	if decision := workflowAdmit(in.entry.Definition, state, "dispatch_worker"); !decision.Admitted {
		return decision.Failure
	}
	if err := validateWorkerPacketBinding(in.ctx, in.tx, in.request.WorkID, in.request.ExpectedVersion, lane, packetRaw); err != nil {
		return err
	}
	if err := validateWorkerPacketCorrection(in.ctx, in.tx, in.request.WorkID, in.currentStep, packetRaw); err != nil {
		return err
	}
	// The packet consumes the current work-context view and correction;
	// admission compares the pinned bytes against current state.
	admittedContext, contextErr := admittedWorkerPacketWorkContext(in.ctx, in.tx, in.request.WorkID, packetRaw)
	if contextErr != nil {
		return contextErr
	}
	if admittedContext != nil && admittedContext.SubjectCommit != "" {
		completionValues["worker_subject_commit"] = admittedContext.SubjectCommit
	}
	// Every authorization records the admitted view's source-event frontier,
	// or explicit zero when no view is present. Terminal reports bind to it.
	dispatchFrontier := int64(0)
	if admittedContext != nil {
		dispatchFrontier = admittedContext.SourceEventFrontier
	}
	completionValues["worker_context_frontier"] = dispatchFrontier
	// The packet carries the latest context checkpoint, so directions
	// recorded before the spawn reach the dispatched worker.
	if err := validateWorkerPacketCheckpoint(in.ctx, in.tx, in.request.WorkID, packetRaw); err != nil {
		return err
	}
	// The law context, design, proposal, and work text are typed members
	// held to current records, never adapter-authored prose.
	if err := validateWorkerPacketRecords(in.ctx, in.tx, in.request.WorkID, packetRaw); err != nil {
		return err
	}
	// CD-0205: dispatch, report, and acceptance bind the selected job revision.
	job, repairReview, jobErr := validateWorkerPacketJob(in.ctx, in.tx, in.entry.Definition, in.request.WorkID, lane, packetRaw)
	if jobErr != nil {
		return jobErr
	}
	// Selected-job readiness keeps its specific refusal. No new authorization
	// may commit while an earlier own worker can still affect this assignment.
	assignment := workerRestartAssignment{stepID: in.currentStep, laneID: lane.ID}
	if job != nil {
		assignment.jobID = job.JobID
	}
	if err := guardWorkerDispatchRestart(in.ctx, in.tx, in.request.WorkID, in.eventActor, assignment, in.request.SessionWorktreeIdentity); err != nil {
		return err
	}
	// A confirming review binds its exact repairing revision and accepted
	// result. This records the review assignment, not another launch authority.
	if repairReview != nil {
		acceptanceSeq, found, acceptanceErr := workerChecksRepairConfirmingAcceptanceSeqTx(in.ctx, in.tx, in.request.WorkID, *repairReview)
		if acceptanceErr != nil {
			return acceptanceErr
		}
		if !found || acceptanceSeq == 0 {
			return newFailure(KindInvariantViolation, "workflow_action", "the confirming review dispatch binds no recorded repairing acceptance", false, "accept the repairing result before dispatching its confirming review")
		}
		completionValues["checks_repair_review"] = map[string]any{"worker_job": *repairReview, "repairing_acceptance_seq": acceptanceSeq}
	}
	if job != nil {
		completionValues["worker_job"] = map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest}
		// The packet validation held the packet's caller exclusions equal to
		// the core's computed set, so the completion retains them from the
		// same bytes for the caller-scope boundary (CD-0227 D1).
		if exclusions := workerPacketCallerExclusions(packetRaw); len(exclusions) > 0 {
			completionValues["worker_caller_exclusions"] = exclusions
		}
	}
	// Revalidate the sealed pre-transaction preparation with SQL only,
	// including the accepted implementation subject for a review dispatch.
	if err := admitWorkerDispatchSubjectFreshness(in.ctx, in.tx, in.request, in.entry.Definition, lane, admittedContext); err != nil {
		return err
	}
	if err := bindRetainedReviewCompletion(in.ctx, lane, job, completionValues); err != nil {
		return err
	}
	if err := freezeReviewDispatchPreparationsTx(in.ctx, in.tx, in.request.WorkID, lane, job, repairReview, completionValues); err != nil {
		return err
	}
	if err := validateWorkerReviewInput(in.ctx, in.tx, in.request.WorkID, lane, in.entry.Definition, packetRaw); err != nil {
		return err
	}
	// The whole context answers to one aggregate bound before dispatch
	// effects, including a retry derived from a combined rejection.
	if err := refuseOverBoundDispatchContext(packetRaw); err != nil {
		return err
	}
	// Every required reading resolved before dispatch. Revalidate sealed
	// witnesses against source registrations with SQL only.
	if err := admitWorkerDispatchReadingResolutionTx(in.ctx, in.tx, in.request.WorkID, admittedContext); err != nil {
		return err
	}
	return nil
}

func workflowDefinitionRequiresDeliveryPayload(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_delivery" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "delivery_artifact" {
				return true
			}
		}
	}
	return false
}

// Completion carries receipt references in its operation record. It neither
// creates receipts nor reclassifies them under a missing required evidence kind.
func extendCompletionEvidenceAuthorityTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest) error {
	refs := append([]string(nil), request.EvidenceRefs...)
	contractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, request.WorkID, "complete_workflow")
	if contractErr == nil {
		verdicts, verdictErr := latestWorkflowVerdicts(ctx, tx, request.WorkID, contractVersion)
		if verdictErr != nil {
			return verdictErr
		}
		for _, verdict := range verdicts {
			for _, ref := range verdict.EvaluationEvidence {
				if !slices.Contains(refs, ref) {
					refs = append(refs, ref)
				}
			}
		}
	} else if contractErr != sql.ErrNoRows {
		return contractErr
	}
	if len(refs) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET evidence_refs=? WHERE op_id=? AND attempt_epoch=?`, workflowJSON(refs), request.OperationID, 1); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot extend completion evidence authority", true, "retry once the database is writable", err)
	}
	return nil
}

// applyCompleteWorkflowActionTx completes the workflow in this transaction:
// the ordered completion gate runs and workflow.completed is appended here.
// The caller's write scope guards the whole action region.
func applyCompleteWorkflowActionTx(ctx context.Context, tx *sql.Tx, scope *writeScope, registry DefinitionRegistry, entry RegisteredDefinition, request WorkflowActionExecutionRequest, actor string, payload json.RawMessage, prefixEvents []Event) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	if scope == nil {
		return result, newFailure(KindInvalidOperation, "complete_workflow", "write scope is required", false, "open the write scope with beginWrite")
	}
	// The actor-recording prefix events fold before the completion gate opens
	// its own level, so the action holds one guard level across the whole
	// completion and the gate's enter and close stay balanced on top of it.
	if err := scope.enter(ctx); err != nil {
		return result, err
	}
	defer func() { _ = scope.close(ctx) }()
	startReceipt := len(scope.eventIDs)
	// The completion fold's requireActor refuses an event actor whose tuple
	// is not recorded, and the actor-recording events the guard minted are
	// the only writer that can land them (#909). Append and fold them first,
	// consuming one expected version each, so the completion event that
	// follows sequences on top of a recorded actor.
	for _, event := range prefixEvents {
		seq, err := appendEvent(ctx, tx, event, true)
		if err != nil {
			return result, err
		}
		event.Seq = seq
		scope.eventIDs = append(scope.eventIDs, event.EventID)
		if err := applyEffect(ctx, tx, event, scope); err != nil {
			return result, err
		}
		if err := advanceWorkEffectSeqTx(ctx, tx, event); err != nil {
			return result, err
		}
	}
	request.ExpectedVersion += int64(len(prefixEvents))
	if err := extendCompletionEvidenceAuthorityTx(ctx, tx, request); err != nil {
		return result, err
	}
	completion, completionErr := workflowCompletionEventForDefinition(ctx, tx, registry, entry.Definition, request, actor, payload)
	if completionErr != nil {
		return result, completionErr
	}
	if err := CompleteWorkflowTxWithRegistry(ctx, tx, registry, completion, scope); err != nil {
		return result, err
	}
	result.EventIDs = append([]string{}, scope.eventIDs[startReceipt:]...)
	result.ChangedRefs = []string{request.WorkID}
	result.OperationID = request.OperationID
	_ = tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, request.WorkID).Scan(&result.ResultingVersion)
	if result.ResultingVersion == 0 {
		result.ResultingVersion = request.ExpectedVersion + 1
	}
	result.Result, _ = json.Marshal(map[string]any{"changed_refs": []any{map[string]any{"entity_kind": "work_item", "id": request.WorkID, "version": result.ResultingVersion}}, "next_valid_intents": []any{}, "operation_id": request.OperationID})
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET result_kind='completed',result_payload=?,changed_refs=?,completed_at=? WHERE op_id=? AND attempt_epoch=?`, string(result.Result), workflowJSON([]string{fmt.Sprintf(`{"entity_kind":"work_item","id":%q,"version":%d}`, request.WorkID, result.ResultingVersion)}), request.Now.UTC().Format(time.RFC3339Nano), request.OperationID, 1); err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_action", "cannot complete durable workflow operation", true, "retry once the database is writable", err)
	}
	return result, nil
}
