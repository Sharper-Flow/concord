package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	staleRecovery             bool
	lateVerdictRecovery       bool
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
	actorNeedsRecord        bool
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
	"record_alignment":       {guardPhasePostValidation, guardRecordAlignmentConsistency},
	"cross_context_boundary": {guardPhaseClaim, guardNoRestartDispatch},
	"record_delivery":        {guardPhaseClaim, guardDeliveryAdmission},
}

func guardRejectWorkerResultRecovery(g *workflowActionGuardContext) error {
	if !g.correctionRecovery {
		return newFailure(KindInvalidOperation, "workflow_action", "worker result rejection is unavailable without a completed result", false, "accept or reject the completed worker result")
	}
	return nil
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
	return validateCorrectionRequestPayload(g.ctx, g.tx, g.request.WorkID, g.request.Payload, "workflow_action", state.CorrectionRequestContext)
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
		return nil, 0, newFailure(KindInvariantViolation, subject, "workflow spec mandate is malformed", false, "rebuild projections from the event log")
	}
	return mandate, version, nil
}

// workflowEvidenceReferenceBound reports a durable evidence binding naming
// the reference inside the accepted history. afterSeq bounds that history:
// bindings at or before it do not count, and zero admits the whole history.
func workflowEvidenceReferenceBound(ctx context.Context, q queryer, workID, reference, subject string, afterSeq int64) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, afterSeq, reference).Scan(&count); err != nil {
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
	if !containsString(definition.AvailableActions, "record_worker_failure") {
		return workflowFailedWorkerAttempt(ctx, q, workID, currentStep, subject, excludeRecorded)
	}
	if stepDeclaresAction(definition, currentStep, "record_worker_failure") {
		return false, nil
	}
	if step := workflowStep(definition, currentStep); step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return false, nil
	}
	failedBinding, bindingErr := workflowCurrentFailedWorkerRetryBinding(ctx, q, definition, workID, currentStep)
	if bindingErr != nil {
		return false, bindingErr
	}
	attemptID, lifecycle, found, err := latestDispatchedAttemptAtStep(ctx, q, workID, currentStep, 0)
	if failedBinding != nil {
		attemptID, lifecycle, found = failedBinding.FailedAttemptID, "failed", true
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
	var recorded int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&recorded); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect recorded worker failures", true, "retry once the workflow event log is readable", err)
	}
	return recorded == 0, nil
}

// workflowFailedWorkerAttempt reports a failed worker attempt opened after
// the step's latest action start. The step-start bound is the failure
// recovery bound: a fresh fenced attempt at the step supersedes every earlier
// failure there.
func workflowFailedWorkerAttempt(ctx context.Context, q queryer, workID, currentStep, subject string, excludeRecorded bool) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	var startSeq sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(seq) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')=?`, string(SubjectWorkItem), workID, WorkflowActionStarted, currentStep).Scan(&startSeq); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect the current workflow action start", true, "retry once the workflow projection is readable", err)
	}
	if !startSeq.Valid {
		return false, nil
	}
	return workflowFailedWorkerAttemptSince(ctx, q, workID, startSeq.Int64, subject, excludeRecorded)
}

// workflowFailedWorkerAttemptSince reports a failed worker attempt whose
// authorization or dispatch evidence follows sinceSeq. Callers pass their
// attempt window's bound: contract correction passes the step's pass boundary, so a
// failure recorded in a previous pass cannot open correction in the pass that
// returned to the step (CD-0133 D1).
func workflowFailedWorkerAttemptSince(ctx context.Context, q queryer, workID string, sinceSeq int64, subject string, excludeRecorded bool) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND ((d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id) OR (d.kind=? AND json_extract(d.payload,'$.action_id')='dispatch_worker' AND json_extract(d.payload,'$.worker_attempt_id')=a.attempt_id)) WHERE a.work_id=? AND a.lifecycle_state='failed' AND d.seq>?`
	args := []any{string(SubjectWorkItem), WorkerDispatched, WorkflowActionCompleted, workID, sinceSeq}
	if excludeRecorded {
		query += ` AND NOT EXISTS (SELECT 1 FROM domain_events f WHERE f.subject_type=? AND f.subject_id=a.work_id AND f.kind=? AND json_extract(f.payload,'$.action_id')='record_worker_failure' AND json_extract(f.payload,'$.worker_attempt_id')=a.attempt_id)`
		args = append(args, string(SubjectWorkItem), WorkflowActionCompleted)
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
		return workflowCompleteStepCorrectionAvailable(ctx, q, workID, definition, currentStep, subject)
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
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM domain_events
		WHERE subject_type=? AND subject_id=? AND seq>=?
		AND (kind=? OR (kind IN (?,?) AND json_extract(payload,'$.action_id')='dispatch_worker'))
	)`, string(SubjectWorkItem), workID, boundary, WorkerDispatched, WorkflowActionStarted, WorkflowActionCompleted).Scan(&dispatched); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect worker dispatch authorization", true, "retry once the workflow event log is readable", err)
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
	if stepDeclaresAction(definition, currentStep, "bind_evidence") {
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

// guardRecoveryEvidenceBind admits one outstanding evidence requirement after
// the definition's evidence-binding step. It also guards the typed recovery
// action declared by definition version 6.
func guardRecoveryEvidenceBind(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string, payload json.RawMessage, subject string) (bool, error) {
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
	required, mandates, obligations, cutoff, inputsErr := workflowEvidenceRequirementInputs(ctx, q, workID)
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
	requirements, requirementsErr := outstandingWorkflowEvidenceRequirements(ctx, q, workID, required, mandates, definition, obligations, cutoff)
	if requirementsErr != nil {
		return false, requirementsErr
	}
	for _, requirement := range requirements {
		if requirement.Kind != "" && requirement.Kind != kind {
			continue
		}
		if requirement.Reference != "" && requirement.Reference != reference {
			continue
		}
		return true, nil
	}
	return false, newFailure(KindIllegalLifecycleTransition, subject, "recovery bind_evidence is only available for an outstanding contract evidence requirement", false, fmt.Sprintf("use bind_evidence on step %q before advancing", bindingStep))
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
	declaresRoute := containsString(workflowFieldStrings(fields, "route_conventions"), workflowCompleteStepCorrectionRoute)
	if declaresRoute && !atCompleteStep {
		return newFailure(KindInvalidPayload, "workflow_action", "route convention complete_step_correction is reserved for correction at the pinned complete step", false, "drop the reserved route convention")
	}
	if atCompleteStep && !declaresRoute {
		// The payload satisfies its own declaration; what refuses is the step
		// state, so the refusal is an operation refusal, not a payload one.
		return newFailure(KindInvalidOperation, "workflow_action", "complete-step correction requires the reserved route convention complete_step_correction", false, "declare complete_step_correction in the successor route conventions")
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
	if containsString(definition.StepGraph.TerminalSteps, currentStep) {
		return false
	}
	if workflowUnhealthyVerdictRouteTarget(definition, currentStep) != "" {
		return true
	}
	if step.Kind == WorkflowStepHumanCheckpoint {
		return containsString(step.Actions, "confirm_premise")
	}
	return step.Kind == WorkflowStepExternalEffect && containsString(step.Actions, "dispatch_worker")
}

// workflowCompleteStepCorrectionAvailable reports whether the pinned complete
// step of a break-fix or implementation workflow admits operator-approved
// contract correction. Every condition is state the action boundary, work pin,
// and preflight can read without the successor payload: the work item is
// nonterminal, at least one approved contract is active, no worker attempt is
// unsettled, and a same-work observation was recorded after the latest
// recorded verdict. Any other work kind or pinned shape refuses.
func workflowCompleteStepCorrectionAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
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
	unsettled, err := workflowUnsettledWorkerAttempt(ctx, q, workID, subject)
	if err != nil || unsettled {
		return false, err
	}
	return workflowContradictionObservationRecorded(ctx, q, workID, subject)
}

// workflowUnsettledWorkerAttempt reports a worker attempt that still holds
// the work: a dispatched attempt, a completed report without an accepted or
// rejected disposition, or a failed attempt without a recorded failure.
// Correction at the complete step never strands one.
func workflowUnsettledWorkerAttempt(ctx context.Context, q queryer, workID, subject string) (bool, error) {
	var unsettled int
	if err := q.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='dispatched') +
 (SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='completed' AND NOT EXISTS (
    SELECT 1 FROM domain_events f WHERE f.subject_type='work_item' AND f.subject_id=a.work_id
      AND f.kind=? AND json_extract(f.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result')
      AND json_extract(f.payload,'$.worker_attempt_id')=a.attempt_id)) +
 (SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.lifecycle_state='failed' AND NOT EXISTS (
    SELECT 1 FROM domain_events f WHERE f.subject_type='work_item' AND f.subject_id=a.work_id
      AND f.kind=? AND json_extract(f.payload,'$.action_id')='record_worker_failure'
      AND json_extract(f.payload,'$.worker_attempt_id')=a.attempt_id))`, workID, workID, WorkflowActionCompleted, workID, WorkflowActionCompleted).Scan(&unsettled); err != nil {
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
func workflowContradictionObservationRecorded(ctx context.Context, q queryer, workID, subject string) (bool, error) {
	var recorded int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events e
		WHERE e.subject_type='work_item' AND e.subject_id=? AND e.kind=?
		AND e.seq > COALESCE((SELECT MAX(v.seq) FROM domain_events v
			WHERE v.subject_type='work_item' AND v.subject_id=? AND v.kind=?), 0))`, workID, WorkObservationRecorded, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect work observations", true, "retry once the work observation projection is readable", err)
	}
	return recorded != 0, nil
}

func guardLateVerdictRecovery(g *workflowActionGuardContext) error {
	available, err := workflowLateVerdictRecoveryForActionPayload(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, g.defaultedPayload())
	if err != nil {
		return err
	}
	if !available {
		fields, fieldsErr := workflowActionObject(g.defaultedPayload())
		if fieldsErr != nil {
			return fieldsErr
		}
		_, singlePresent := workflowFieldString(fields, "predicate_id")
		_, batchPresent := fields["verdicts"]
		if !singlePresent && !batchPresent {
			return nil
		}
		return newFailure(KindInvalidOperation, "workflow_action", "late verdict recovery requires a missing, non-ok, or incomparable verdict for an active predicate", false, "record the verdict at its normal verification step or refresh the active contract")
	}
	g.lateVerdictRecovery = true
	return nil
}

// workflowLateVerdictRecoveryForActionPayload normalizes the call to its
// ordered entry list first, so both wire shapes answer to one rule: recovery
// admits a call only when every entry names a predicate whose latest verdict
// is missing, non-ok, or incomparable for the active contract (CD-0198 D1).
// A malformed shape is not recovery-eligible and reports false here; the
// typed shape refusal belongs to the verdict constructor's own validation,
// which runs before any event and after the consequential boundaries.
func workflowLateVerdictRecoveryForActionPayload(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string, payload json.RawMessage) (bool, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return false, err
	}
	entries, entriesErr := normalizeWorkflowVerdictEntries(fields)
	if entriesErr != nil {
		return false, nil
	}
	contractVersion, present := workflowFieldIntOK(fields, "contract_version")
	if !present {
		if _, batchPresent := fields["verdicts"]; batchPresent {
			// The batched route resolves an omitted contract_version to the
			// active contract through the caller's queryer, exactly as the
			// verdict constructor does at the verification step, so a batch
			// recorded after a supersession is judged on the contract its
			// entries land on. A projection with no contract row is not
			// recovery-eligible here; the constructor owns that refusal.
			active, activeErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
			if activeErr != nil {
				if activeErr == sql.ErrNoRows {
					return false, nil
				}
				return false, activeErr
			}
			contractVersion = active
		} else {
			contractVersion = 1
		}
	}
	for _, entry := range entries {
		qualifies, qualifiesErr := workflowLateVerdictRecoveryForPredicate(ctx, q, workID, definition, currentStep, entry.PredicateID, contractVersion)
		if qualifiesErr != nil || !qualifies {
			return false, qualifiesErr
		}
	}
	return true, nil
}

func guardCompleteBoundary(g *workflowActionGuardContext) error {
	return workflowCompletionBoundaryPreflight(g.request.Payload)
}

// guardRecordAlignmentConsistency refuses an alignment payload whose outcome
// contradicts its related_ids list (CD-0156 D3): a related_found outcome with
// no ids records a claim about nothing, and a none_found outcome with ids
// records a found set the search did not return. The declared payload bounds
// cannot express the cross-field rule, so this guard owns it.
func guardRecordAlignmentConsistency(g *workflowActionGuardContext) error {
	fields, fieldErr := workflowActionObject(g.defaultedPayload())
	if fieldErr != nil {
		return fieldErr
	}
	relatedIDs := workflowFieldStrings(fields, "related_ids")
	switch workflowFieldStringDefault(fields, "outcome", "") {
	case "related_found":
		if len(relatedIDs) == 0 {
			return newFailure(KindInvalidPayload, "workflow_action", "record_alignment outcome related_found requires related_ids", false, "name the related work items the search found")
		}
	case "none_found":
		if len(relatedIDs) != 0 {
			return newFailure(KindInvalidPayload, "workflow_action", "record_alignment outcome none_found cannot carry related_ids", false, "drop related_ids or record outcome related_found")
		}
	}
	return nil
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
// workflow actor record, or marks the actor for recording when no row exists.
// It applies to every action rather than to one action ID, so the dispatcher
// calls it directly instead of through the per-action table, as it does for
// guardOperatorPremiseActor. Any action the current step declares is some
// session's possible first action, and the folds that call requireActor refuse
// until the row exists (issue #740).
func guardRecordedActorTuple(g *workflowActionGuardContext) error {
	var recordedPrincipal, recordedClient, recordedAgent, recordedSession string
	recordErr := g.tx.QueryRowContext(g.ctx, `SELECT principal_ref,client_ref,agent_ref,session_ref FROM workflow_actors WHERE actor_ref=?`, g.actorRef).Scan(&recordedPrincipal, &recordedClient, &recordedAgent, &recordedSession)
	switch {
	case recordErr == sql.ErrNoRows:
		g.actorNeedsRecord = true
		return nil
	case recordErr != nil:
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow actor", true, "retry once the workflow actor authority is readable", recordErr)
	}
	if recordedPrincipal != g.request.Actor.PrincipalRef || recordedClient != g.request.Actor.ClientRef || recordedAgent != g.request.Actor.AgentRef || recordedSession != g.request.Actor.SessionRef {
		return newFailure(KindInvariantViolation, "workflow_action", "recorded workflow actor tuple does not match the authenticated actor", false, "reread workflow actor authority")
	}
	return nil
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
	if g.request.OperatorActor == nil {
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
			return newFailure(KindApprovalRequired, "workflow_action", "correction request requires the verified operator approval identity", false, "request_approval")
		}
		if state.ContractCorrectionAvailable {
			return newFailure(KindApprovalRequired, "workflow_action", "contract correction requires the verified operator approval identity", false, "request_approval")
		}
		return nil
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

// guardDeliveryAdmission consumes the shared decision, then binds the proof
// to the request's external tool declaration and the ready-review identity.
func guardDeliveryAdmission(g *workflowActionGuardContext) error {
	state, err := g.foldedAdmissionState()
	if err != nil {
		return err
	}
	decision, err := g.foldedAdmissionDecision()
	if err != nil {
		return err
	}
	if !decision.Admitted && !workflowAdmissionDefersToReviewGate(*decision, g.request.ActionID) {
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
		if delivery := workflowAdmitDeliveryForAccept(*state, WorkflowAdmissionDecision{}, accepting); delivery.Failure != nil {
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
	return guardPostRejectionReviewGate(g)
}

// workflowAcceptDeliveryAdmissionActive reports whether the pinned definition
// and step admit the combined accept-and-delivery route (CD-0198 D4):
// workflow.implementation v21+ and workflow.break_fix v19+ at their
// refinement step. Earlier definition versions keep the behavior and digest
// they shipped with, and the CD-0166 delivery gate stays record_delivery-only.
func workflowAcceptDeliveryAdmissionActive(definition WorkflowDefinition, currentStep string) bool {
	if currentStep == "" || workflowRefinementStepID(definition) != currentStep {
		return false
	}
	switch definition.Ref {
	case "workflow.implementation":
		return definition.Version >= 21
	case "workflow.break_fix":
		return definition.Version >= 19
	default:
		return false
	}
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
// accept carries its delivery fields and runs guardDeliveryAdmission, the
// unchanged function record_delivery runs; everywhere else the delivery
// fields refuse and the accept keeps the existing post-rejection review
// gate. A plain accept at the admitting step refuses with the
// delivery-admission remedy: the refine step exits only through an admitted
// delivery assertion, so the CD-0192 refine-exit proof can no longer be
// crossed by an advancing accept that carries no artifact.
func guardAcceptWorkerResultDeliveryRoute(g *workflowActionGuardContext) error {
	fields, fieldsErr := workflowActionObject(g.defaultedPayload())
	if fieldsErr != nil {
		return fieldsErr
	}
	artifact := workflowFieldStringDefault(fields, "delivery_artifact", "")
	state := workflowFieldStringDefault(fields, "delivery_state", "")
	if workflowAcceptDeliveryAdmissionActive(g.entry.Definition, g.currentStep) {
		// The accepted disposition of a ready non-settling review is the one
		// non-delivery accept the delivery-admitting refinement step takes:
		// the review refused the result, so its acceptance binds the findings
		// and leaves the debt outstanding, carries no delivery assertion, and
		// rides the review gate's identity binding (CD-0201 D3). The gate
		// owns both halves — it refuses the identity-satisfying accept that
		// does carry the assertion, and admits the one that does not.
		if workflowAcceptBindsReadyUnsettledReview(g.admissionState, fields) {
			return guardPostRejectionReviewGate(g)
		}
		// On a job-capable pin a plain accept of a job-bound attempt is local
		// acceptance of that one worker job: the fold holds the step, so the
		// accept carries no exit and keeps the post-rejection review gate
		// (CD-0205). An attempt dispatched without a job binding has no local
		// disposition to record, and the refine step still exits only through
		// an admitted delivery assertion.
		if artifact == "" && state == "" && workflowWorkerJobsActive(g.entry.Definition) {
			job, jobErr := workflowDispatchedJobForAttempt(g.ctx, g.tx, g.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
			if jobErr != nil {
				return jobErr
			}
			if job != nil {
				return guardPostRejectionReviewGate(g)
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
		return newFailure(KindInvalidOperation, "workflow_action",
			"accept_worker_result carries delivery fields only at the delivery-admitting refinement step of workflow.implementation v21+ and workflow.break_fix v19+",
			false, "drop the delivery fields here, or assert the delivery through record_delivery")
	}
	return guardPostRejectionReviewGate(g)
}

// workflowRefineProofGateActive reports whether the refine-exit proof guard
// governs record_delivery on this step. The guard is active only for
// workflow.implementation v18+ and workflow.break_fix v16+ at their
// refinement step; earlier definition versions keep the behavior and digest
// they shipped with.
func workflowRefineProofGateActive(definition WorkflowDefinition, currentStep string) bool {
	if currentStep == "" || workflowRefinementStepID(definition) != currentStep {
		return false
	}
	switch definition.Ref {
	case "workflow.implementation":
		return definition.Version >= 18
	case "workflow.break_fix":
		return definition.Version >= 16
	default:
		return false
	}
}

// workflowVerificationBindingsAfter lists the immutable subject refs bound as
// verification evidence after afterSeq, the current refine epoch's start, in
// binding order.
func workflowVerificationBindingsAfter(ctx context.Context, q queryer, workID string, afterSeq int64, subject string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.immutable_subject_ref') FROM domain_events
		WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>?
		AND json_extract(payload,'$.evidence_kind')='verification'
		ORDER BY seq`, workID, WorkflowEvidenceBound, afterSeq)
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
		if !contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot enumerate the refine epoch's evidence bindings", true, "retry once the workflow evidence projection is readable", err)
	}
	return refs, nil
}

// loadWorkflowVerifyRun folds a bound verify operation and its lease. Command
// declaration matching remains outside the database fold. The lease's Project
// rides with the command so the integration facet can validate reference
// scope without a second fold.
func loadWorkflowVerifyRun(ctx context.Context, q queryer, workID, reference string, startedAt time.Time, subject string) ([]string, string, string, error) {
	var resultPayload string
	err := q.QueryRowContext(ctx, `SELECT result_payload FROM durable_operations
		WHERE op_id=? AND work_id=? AND workflow_type_ref='worktree.verify' AND result_kind='completed'`, reference, workID).Scan(&resultPayload)
	if err == sql.ErrNoRows {
		return nil, "", "bound evidence " + reference + " names no completed worktree.verify durable operation", nil
	}
	if err != nil {
		return nil, "", "", wrapFailure(KindUnavailable, subject, "cannot read the bound worktree verify operation", true, "retry once the durable operation projection is readable", err)
	}
	var result struct {
		LeaseID             string `json:"lease_id"`
		TrackedFilesChanged bool   `json:"tracked_files_changed"`
	}
	if err := json.Unmarshal([]byte(resultPayload), &result); err != nil || result.LeaseID == "" {
		return nil, "", "", newFailure(KindInvariantViolation, subject, "the bound worktree verify operation's result is malformed", false, "rebuild projections from the event log")
	}
	var outcome string
	var exitCode sql.NullInt64
	var acquiredAt string
	var commandJSON string
	var projectID string
	err = q.QueryRowContext(ctx, `SELECT outcome,exit_code,acquired_at,command_json,project_id FROM worktree_verify_leases WHERE lease_id=? AND work_id=?`, result.LeaseID, workID).Scan(&outcome, &exitCode, &acquiredAt, &commandJSON, &projectID)
	if err == sql.ErrNoRows {
		return nil, "", "the bound worktree.verify run names no verify lease for this work item", nil
	}
	if err != nil {
		return nil, "", "", wrapFailure(KindUnavailable, subject, "cannot read the bound verify lease", true, "retry once the verify lease record is readable", err)
	}
	if outcome != "completed" {
		return nil, "", "the bound verify lease recorded outcome " + outcome, nil
	}
	if !exitCode.Valid || exitCode.Int64 != 0 {
		return nil, "", "the bound verify lease did not record exit code 0", nil
	}
	if result.TrackedFilesChanged {
		return nil, "", "the bound verify run changed tracked files", nil
	}
	acquired, parseErr := time.Parse(time.RFC3339Nano, acquiredAt)
	if parseErr != nil {
		return nil, "", "", newFailure(KindInvariantViolation, subject, "the bound verify lease records an unreadable acquire time", false, "rebuild projections from the event log")
	}
	if !acquired.After(startedAt) {
		// A replayed lease keeps its original acquired_at, so a lease held in
		// an earlier refine epoch cannot prove this one.
		return nil, "", "the bound verify run was acquired at or before the current refine start", nil
	}
	var command []string
	if err := json.Unmarshal([]byte(commandJSON), &command); err != nil {
		return nil, "", "", newFailure(KindInvariantViolation, subject, "the bound verify lease records an unreadable command", false, "rebuild projections from the event log")
	}
	return command, projectID, "", nil
}

// workflowActionOccurredAt reads one workflow event's occurred_at, the wall
// clock the refine-exit proof bounds the verify lease against.
func workflowActionOccurredAt(ctx context.Context, q queryer, workID string, seq int64) (time.Time, error) {
	var occurred string
	if err := q.QueryRowContext(ctx, `SELECT occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND seq=?`, workID, seq).Scan(&occurred); err != nil {
		return time.Time{}, wrapFailure(KindUnavailable, "workflow_action", "cannot read the refine step's start time", true, "retry once the workflow projection is readable", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		return time.Time{}, newFailure(KindInvariantViolation, "workflow_action", "the refine step's start event records an unreadable time", false, "rebuild projections from the event log")
	}
	return startedAt, nil
}

// guardPostRejectionReviewGate refuses an advance toward delivery whose
// refinement history carries a rejected result no fresh accepted review has
// covered. The admission is the shared derivation: the tx-scoped loader folds
// the refinement history into the abstract admission state once, and the pure
// workflowAdmit decides, so this guard, the work pin intents, and the
// delivery-gate correction binding answer identically for the same state.
// Accepting the ready settling review is itself the fresh review, so the
// guard admits the accept whose attempt identity the decision names; any
// other accept, and either delivery exit, waits for one. The refusal leaves
// the evidence-bearing corrective return as the only route off a parked,
// unreviewed gate.
func guardPostRejectionReviewGate(g *workflowActionGuardContext) error {
	// Review debt is same-step attempt recovery and keeps its existing
	// owner: the refinement shape the step-shape switch below names. No
	// cross-step recovery choice is made here; the correction the gate
	// admits reads its target from the route table.
	switch g.request.ActionID {
	case "accept_worker_result":
		if !stepDeclaresAction(g.entry.Definition, g.currentStep, "start_refine") {
			return nil
		}
	case "record_delivery":
		if !workflowPostRejectionReviewStep(g.entry.Definition, g.currentStep) {
			return nil
		}
	default:
		return nil
	}
	// The dispatch fold already folded and decided this request's admission;
	// the guard consumes that derivation. A caller without the fold's state
	// (no other production caller exists) falls back to its own fold.
	decision, err := g.foldedAdmissionDecision()
	if err != nil {
		return err
	}
	// An accept that names a completed review a newer completed review has
	// superseded is stale: only the newest review's result can bind, whatever
	// the folded state names — the ready review included, because a delayed
	// pre-frontier report can complete after the ready one (CD-0206 D3). The
	// check runs before the admitted shortcut because the fresh-review
	// refusal below binds the ready review only while review debt stands.
	if g.request.ActionID == "accept_worker_result" {
		fields, fieldsErr := workflowActionObject(g.request.Payload)
		if fieldsErr != nil {
			return fieldsErr
		}
		if attemptID := workflowFieldStringDefault(fields, "attempt_id", ""); attemptID != "" {
			stale, staleErr := workflowStaleReviewAcceptTx(g.ctx, g.tx, g.request.WorkID, attemptID, "workflow_action")
			if staleErr != nil {
				return staleErr
			}
			if stale {
				return workflowAdmitFreshReviewRefusal(*decision).Failure
			}
		}
	}
	if decision.Admitted {
		return nil
	}
	// A refusal whose cause is not the review gate's own — staleness, an
	// impact notice, the wall — applies here unchanged; the ready-review
	// carve-out below is the acceptance route of the fresh-review refusal
	// alone, so no unrelated cause can ride it.
	if !decision.FreshReviewRequired {
		return decision.Failure
	}
	// The attempt identity and the delivery assertion stay guard checks: the
	// request's attempt_id must name the ready review the decision folded, so
	// an accept of a different attempt cannot ride its admission, and the
	// ready review's acceptance carries the delivery assertion only when its
	// verdict settles — a no_ship review binds its findings and leaves the
	// debt outstanding, so the advance waits for a settling review (CD-0201
	// D3).
	if g.request.ActionID == "accept_worker_result" && decision.ReadyReviewAttemptID != "" {
		fields, fieldsErr := workflowActionObject(g.request.Payload)
		if fieldsErr != nil {
			return fieldsErr
		}
		if workflowFieldStringDefault(fields, "attempt_id", "") == decision.ReadyReviewAttemptID {
			if !decision.ReadyReviewSettles && workflowAcceptCarriesDeliveryAssertion(fields) {
				return decision.Failure
			}
			return nil
		}
	}
	return decision.Failure
}

// foldedAdmissionState returns the fold's admission state, folding it on the
// guard's transaction when the caller did not carry one. The dispatch fold
// always folds before the guard phases run; the fallback keeps a directly
// constructed guard context on the same single derivation.
func (g *workflowActionGuardContext) foldedAdmissionState() (*WorkflowAdmissionState, error) {
	if g.admissionState != nil {
		return g.admissionState, nil
	}
	state, proofRuns, integrationRuns, err := loadWorkflowAdmissionStateTx(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, "workflow_action")
	if err != nil {
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

// workflowAcceptBindsReadyUnsettledReview reports whether one accept payload
// names the folded ready review whose verdict does not settle the
// post-rejection review debt: the accepted non-delivery disposition the claim
// guard admits and the delivery-fields backstop exempts (CD-0201 D3). A ready
// attempt folds only under outstanding debt at a review step, so the identity
// match alone names the guard-approved shape.
func workflowAcceptBindsReadyUnsettledReview(state *WorkflowAdmissionState, fields map[string]json.RawMessage) bool {
	if state == nil || state.ReadyReviewAttemptID == "" || state.ReadyReviewSettles {
		return false
	}
	return workflowFieldStringDefault(fields, "attempt_id", "") == state.ReadyReviewAttemptID
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, request.OperationID, 1, request.WorkID, entry.Definition.Ref, entry.Definition.Version, currentStep, durableStepKind, request.AcceptedInputsDigest, nullableWorkflowText(request.AcceptedScope), request.PrincipalRef, request.RequestID, request.Now.UTC().Format(time.RFC3339Nano), request.ContractDigest); err != nil {
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

	actorRef               string
	eventActor             string
	operatorRef            string
	actorNeedsRecord       bool
	operatorNeedsRecord    bool
	defaultVerdictEvidence bool
	lateVerdictRecovery    bool
	envelopeEvidenceRefs   []string
	// admission carries the fold's derived admission state, so the assembly's
	// payload checks read the folded conditions instead of re-deriving them.
	admission *WorkflowAdmissionState
}

type workflowActionEventAssembly struct {
	events       []Event
	nativeRun    *NativeRunReport
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
	var workflowActionEpoch int64
	var epochErr error
	if builtinActionPolicies[in.request.ActionID].EventShape == ActionEventCheckpoint && executionMode != ActionFenced {
		workflowActionEpoch, epochErr = workflowCheckpointAttemptEpoch(ctx, tx, in.entry.Definition, in.step, in.request.WorkID, in.currentStep, 0)
	} else {
		workflowActionEpoch, epochErr = workflowActionStartEpochForDispatch(ctx, tx, in.request.WorkID, in.currentStep, executionMode == ActionFenced)
	}
	if epochErr != nil {
		return out, epochErr
	}
	out.attemptEpoch = workflowActionEpoch
	actor := in.eventActor
	events := []Event{}
	versionCursor := in.request.ExpectedVersion
	if in.actorNeedsRecord {
		events = append(events, workflowTypedEvent(in.request.OperationID+":actor", WorkflowActorRecorded, in.request.WorkID, in.actorRef, in.request.Now, versionCursor, map[string]any{"actor_ref": in.actorRef, "principal_ref": in.request.Actor.PrincipalRef, "client_ref": in.request.Actor.ClientRef, "agent_ref": in.request.Actor.AgentRef, "session_ref": in.request.Actor.SessionRef, "actor_class": string(in.request.Actor.ActorClass)}))
		versionCursor++
	}
	if in.operatorNeedsRecord {
		events = append(events, workflowTypedEvent(in.request.OperationID+":operator", WorkflowActorRecorded, in.request.WorkID, in.operatorRef, in.request.Now, versionCursor, map[string]any{"actor_ref": in.operatorRef, "principal_ref": in.request.OperatorActor.PrincipalRef, "client_ref": in.request.OperatorActor.ClientRef, "agent_ref": in.request.OperatorActor.AgentRef, "session_ref": in.request.OperatorActor.SessionRef, "actor_class": string(ActorOperator)}))
		versionCursor++
	}
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
		semantic, semanticErr := workflowSemanticActionEvents(ctx, tx, in.entry.Definition, in.request, in.currentStep, actor, in.payload, versionCursor+int64(len(events)-int(versionCursor-in.request.ExpectedVersion)), in.defaultVerdictEvidence, in.envelopeEvidenceRefs)
		if semanticErr != nil {
			return out, semanticErr
		}
		if len(semantic) != 0 {
			events = append(events, semantic...)
			out.nativeRun = nativeRunFromSemanticEvents(semantic)
		}
	default:
		staleness, present, stalenessErr := workflowStalenessObservationEvent(in.request.OperationID+":staleness", in.request.WorkID, actor, in.request.AcceptedInputsDigest, in.payload, in.request.Now)
		if stalenessErr != nil {
			return out, stalenessErr
		}
		if present {
			recorded, recordedErr := workflowStalenessObservationRecordedTx(ctx, tx, staleness)
			if recordedErr != nil {
				return out, recordedErr
			}
			if !recorded {
				events = append(events, staleness)
			}
		}
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
	if workflowActionOmitsGenericCompletion(in.request.ActionID) || in.lateVerdictRecovery {
		return events, "", nil
	}
	resultVersion := in.request.ExpectedVersion + int64(len(events)) + 1
	fields, fieldsErr := workflowActionObject(in.payload)
	if fieldsErr != nil {
		return events, "", fieldsErr
	}
	completionValues := map[string]any{
		"step_id": in.currentStep, "action_id": in.request.ActionID, "attempt_epoch": attemptEpoch, "result_evidence_refs": in.evidenceRefs,
		"changed_refs": []string{in.request.WorkID}, "actor_ref": in.eventActor,
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
	// CD-0205: an acceptance records the worker-job disposition it
	// satisfies. The binding is derived here from the accepted attempt's
	// worker.dispatched event — the caller never asserts it — so the
	// correction window closes on a recorded disposition under the exact
	// dispatched job, and an unrelated accepted job cannot close another
	// job's window by name.
	var acceptedJob *WorkerJobBinding
	if in.request.ActionID == "accept_worker_result" {
		job, jobErr := workflowDispatchedJobForAttempt(in.ctx, in.tx, in.request.WorkID, workflowFieldStringDefault(fields, "attempt_id", ""))
		if jobErr != nil {
			return events, "", jobErr
		}
		acceptedJob = job
		if job != nil {
			completionValues["worker_job"] = map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest}
		}
	}
	// CD-0198 D4: the combined accept asserts the same delivery fields on the
	// same completion event a record_delivery appends, so delivery readers
	// identify the assertion by its asserted fields rather than by action_id.
	// The guard already required the fields; this refuses closed if the
	// route ever runs without them. Two non-delivery shapes are exempt, each
	// admitted by the guard: the accepted disposition of a ready non-settling
	// review, whose refused result carries no delivery assertion (CD-0201
	// D3), and on a job-capable pin the local acceptance of a job-bound
	// attempt, which the fold holds at the step (CD-0205).
	if in.request.ActionID == "accept_worker_result" && workflowAcceptDeliveryAdmissionActive(in.entry.Definition, in.currentStep) {
		artifact := workflowFieldStringDefault(fields, "delivery_artifact", "")
		state := workflowFieldStringDefault(fields, "delivery_state", "")
		localJobAccept := artifact == "" && state == "" && acceptedJob != nil && workflowWorkerJobsActive(in.entry.Definition)
		if (artifact == "" || state == "") && !workflowAcceptBindsReadyUnsettledReview(in.admission, fields) && !localJobAccept {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "the combined accept requires delivery_artifact and delivery_state", false, "supply the asserted delivery artifact and state")
		}
		if artifact != "" && state != "" {
			completionValues["delivery_artifact"] = artifact
			completionValues["delivery_state"] = state
		}
	}
	// CD-0201 D3: the accept guard's admission decision rides the completion
	// it authors. The accepted non-delivery disposition of a ready
	// non-settling review holds the step's advance — the debt waits for a
	// settling review — so the guard records the hold from the folded
	// admission state, and the fold honors the recorded field only.
	if in.request.ActionID == "accept_worker_result" && workflowAcceptBindsReadyUnsettledReview(in.admission, fields) {
		completionValues["review_advance_held"] = true
	}
	var workerPacketDigest string
	if in.request.ActionID == "accept_worker_result" || in.request.ActionID == "accept_worker_evidence" || in.request.ActionID == "record_worker_failure" || in.request.ActionID == "reject_worker_result" {
		completionValues["attempt_epoch"] = workflowFieldInt(fields, "attempt_epoch", 0)
		completionValues["worker_attempt_id"] = workflowFieldStringDefault(fields, "attempt_id", "")
	}
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
	if in.request.ActionID == "dispatch_worker" {
		attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
		completionValues["worker_attempt_id"] = attemptID
		packetRaw := workflowFieldRaw(fields, "worker_packet")
		// Preflight enforces worker_packet is present and decodes to one
		// strict JSON object, so an empty raw here means a defensive
		// fallback; refuse closed with the same typed failure rather
		// than record a half-bound attempt.
		if len(packetRaw) == 0 {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is absent from the action payload", false, "supply the lane packet bound to this work item and attempt")
		}
		var packetFields map[string]json.RawMessage
		if err := json.Unmarshal(packetRaw, &packetFields); err != nil {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is not a JSON object", false, "supply the lane packet bound to this work item and attempt")
		}
		packetWorkID, workIDOK := workflowFieldString(packetFields, "work_id")
		packetAttemptID, attemptIDOK := workflowFieldString(packetFields, "attempt_id")
		if !workIDOK || !attemptIDOK {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is missing work_id or attempt_id", false, "supply the lane packet bound to this work item and attempt")
		}
		if packetWorkID != in.request.WorkID {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet.work_id does not match the action's work_id", false, "supply the lane packet bound to this work item and attempt")
		}
		if packetAttemptID != attemptID {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet.attempt_id does not match fields.attempt_id", false, "supply the lane packet bound to this work item and attempt")
		}
		// The lane-step dispatch join (#892): the pair is composed onto every
		// step kind some lane may dispatch, so the step admitting the action
		// does not by itself admit the lane. Resolve the packet's lane
		// identity and refuse when that lane's capability class is not
		// dispatchable at the current step's kind.
		packetLaneID, laneIDOK := workflowFieldString(packetFields, "lane_id")
		packetLaneDigest, laneDigestOK := workflowFieldString(packetFields, "lane_digest")
		packetLaneVersion := workflowFieldInt(packetFields, "lane_version", 0)
		if !laneIDOK || !laneDigestOK || packetLaneVersion < 1 {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is missing a complete lane identity", false, "supply the lane packet bound to this work item and attempt")
		}
		lane, laneErr := LookupLane(packetLaneID, packetLaneVersion, packetLaneDigest)
		if laneErr != nil {
			return events, "", laneErr
		}
		if in.step != nil && !LaneStepDispatchAllowed(lane.CapabilityClass, in.step.Kind) {
			// The inverse read of the join turns the refusal into a read:
			// the caller sees the classes this step kind admits and can
			// pick one instead of guessing.
			admitted := strings.Join(LaneStepDispatchClasses(in.step.Kind), ", ")
			return events, "", newFailure(KindUnauthorizedDispatch, "workflow_action", "lane capability class "+lane.CapabilityClass+" is not dispatchable at a "+string(in.step.Kind)+" step; the step admits capability classes "+admitted, false, "dispatch the lane at a step kind the lane-step dispatch join admits")
		}
		if in.tx != nil {
			if err := validateWorkerPacketBinding(in.ctx, in.tx, in.request.WorkID, in.request.ExpectedVersion, lane, packetRaw); err != nil {
				return events, "", err
			}
			if err := validateWorkerPacketCorrection(in.ctx, in.tx, in.request.WorkID, in.currentStep, packetRaw); err != nil {
				return events, "", err
			}
			// CD-0205: the completion records the selected worker-job
			// revision the packet binds, so worker.dispatched, the report,
			// and the acceptance can each be held to that exact revision.
			job, jobErr := validateWorkerPacketJob(in.ctx, in.tx, in.entry.Definition, in.request.WorkID, lane, packetRaw)
			if jobErr != nil {
				return events, "", jobErr
			}
			if job != nil {
				completionValues["worker_job"] = map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest}
			}
		}
		// The registry lane identity rides the completion so the fold can
		// bind the attempt durably in flight from the record alone, without
		// re-reading the packet bytes.
		completionValues["worker_lane_id"] = lane.ID
		completionValues["worker_lane_version"] = lane.Version
		completionValues["worker_lane_digest"] = lane.Digest
		completionValues["worker_capability_class"] = lane.CapabilityClass
		canonical, err := canonicalJSON(packetRaw)
		if err != nil {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet does not decode as canonical JSON", false, "supply the lane packet bound to this work item and attempt")
		}
		sum := sha256.Sum256(canonical)
		workerPacketDigest = "sha256:" + hex.EncodeToString(sum[:])
		completionValues["worker_packet_digest"] = workerPacketDigest
		// The typed outcome predicate ids are extracted from the same packet
		// bytes this digest covers, so the durable record carries the
		// discharge obligations the fold enforces on the completion. The
		// payload schema preflight already admitted the packet shape; this
		// read re-checks the identity rules the obligations rest on.
		predicateIDs, predicateErr := workerPacketPredicateIDs(packetRaw)
		if predicateErr != nil {
			return events, "", predicateErr
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
	}
	if in.request.ActionID == "reject_worker_result" || in.request.ActionID == "request_correction" {
		completionValues["correction_diagnosis"] = workflowFieldStringDefault(fields, "diagnosis", "")
		completionValues["correction_strategy"] = workflowFieldStringDefault(fields, "strategy", "")
		completionValues["correction_predicate_ids"] = workflowFieldStrings(fields, "predicate_ids")
		completionValues["correction_evidence_refs"] = workflowFieldStrings(fields, "evidence_refs")
	}
	events = append(events, workflowTypedEvent(in.request.OperationID+":completed", WorkflowActionCompleted, in.request.WorkID, in.eventActor, in.request.Now, resultVersion-1, completionValues))
	return events, workerPacketDigest, nil
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

func lateBindWorkflowEvidenceTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, payload json.RawMessage, definition WorkflowDefinition) ([]Event, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return nil, err
	}
	refs := append([]string(nil), request.EvidenceRefs...)
	contractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, request.WorkID, "complete_workflow")
	if contractErr == nil {
		verdicts, verdictErr := latestWorkflowVerdicts(ctx, tx, request.WorkID, contractVersion)
		if verdictErr != nil {
			return nil, verdictErr
		}
		for _, verdict := range verdicts {
			for _, ref := range verdict.EvaluationEvidence {
				if !contains(refs, ref) {
					refs = append(refs, ref)
				}
			}
		}
	} else if contractErr != sql.ErrNoRows {
		return nil, contractErr
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET evidence_refs=? WHERE op_id=? AND attempt_epoch=?`, workflowJSON(refs), request.OperationID, 1); err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot extend completion evidence authority", true, "retry once the database is writable", err)
	}
	// An explicit envelope kind stays a single-kind override; an absent one
	// derives the contract's required kinds, so outstanding verdict refs
	// late-bind under the same kinds the born-bind mints (issue #842).
	var kinds []string
	if explicitKind := workflowFieldStringDefault(fields, "evidence_kind", ""); explicitKind != "" {
		kinds = []string{explicitKind}
	} else {
		var kindErr error
		kinds, kindErr = bornBoundEvidenceKinds(ctx, tx, request.WorkID, definition)
		if kindErr != nil {
			return nil, kindErr
		}
	}
	events := make([]Event, 0, len(refs))
	for refIndex, ref := range refs {
		var bound int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, SubjectWorkItem, request.WorkID, WorkflowEvidenceBound, ref).Scan(&bound); err != nil {
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect completion evidence", true, "retry once the database is readable", err)
		}
		if bound != 0 {
			continue
		}
		for _, kind := range kinds {
			events = append(events, workflowTypedEvent(request.OperationID+":evidence:"+kind+":"+fmt.Sprint(refIndex), WorkflowEvidenceBound, request.WorkID, actor, request.Now, request.ExpectedVersion+int64(len(events)), map[string]any{
				"evidence_kind": kind, "immutable_subject_ref": ref, "producer_id": request.PrincipalRef,
				"producer_run_ref": request.OperationID, "producer_watermark": request.RequestID,
				"observed_at": request.Now.UTC().Format(time.RFC3339Nano),
			}))
		}
	}
	for _, event := range events {
		if _, err := appendEvent(ctx, tx, event, true); err != nil {
			return nil, err
		}
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			return nil, err
		}
	}
	return events, nil
}

// nativeRunFromSemanticEvents lifts the native-run report the semantic events
// carry into the action result. The last report wins, matching the order the
// events were appended in.
func nativeRunFromSemanticEvents(semantic []Event) *NativeRunReport {
	var run *NativeRunReport
	for _, semanticEvent := range semantic {
		if semanticEvent.Kind != WorkflowNativeRunRecorded {
			continue
		}
		var report nativeRunPayload
		if decodePayload(semanticEvent, &report) == nil {
			run = &NativeRunReport{RunID: report.RunID, Phase: report.Phase, Status: report.Status, EventID: semanticEvent.EventID, ReportingAuthorityRef: report.ReportingAuthorityRef, ActorRef: report.ActorRef, NativeSubjectRef: report.NativeSubjectRef, SubjectDigest: report.SubjectDigest, EvidenceRef: report.EvidenceRef, EvidenceDigest: report.EvidenceDigest, AssertedAt: report.AssertedAt, RecordedAt: semanticEvent.OccurredAt.UTC().Format(time.RFC3339Nano), Unverified: true}
		}
	}
	return run
}

// applyCompleteWorkflowActionTx completes the workflow in this transaction:
// the ordered completion gate runs and workflow.completed is appended here.
// The caller's fold scope guards the whole action region.
func applyCompleteWorkflowActionTx(ctx context.Context, tx *sql.Tx, scope *foldScope, registry DefinitionRegistry, entry RegisteredDefinition, request WorkflowActionExecutionRequest, actor string, payload json.RawMessage, prefixEvents []Event) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	if scope == nil {
		return result, newFailure(KindInvalidOperation, "complete_workflow", "fold scope is required", false, "open the fold scope with beginFold")
	}
	// The actor-recording prefix events fold before the completion gate opens
	// its own level, so the action holds one guard level across the whole
	// completion and the gate's enter and close stay balanced on top of it.
	if err := scope.enter(ctx); err != nil {
		return result, err
	}
	defer func() { _ = scope.close(ctx) }()
	startSeq, err := operationEventSequence(ctx, tx)
	if err != nil {
		return result, err
	}
	// The completion fold's requireActor refuses an event actor whose tuple
	// is not recorded, and the actor-recording events the guard minted are
	// the only writer that can land them (#909). Append and fold them first,
	// consuming one expected version each, so the completion event that
	// follows sequences on top of a recorded actor.
	for _, event := range prefixEvents {
		if _, err := appendEvent(ctx, tx, event, true); err != nil {
			return result, err
		}
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			return result, err
		}
	}
	request.ExpectedVersion += int64(len(prefixEvents))
	bindingEvents, bindingErr := lateBindWorkflowEvidenceTx(ctx, tx, request, actor, payload, entry.Definition)
	if bindingErr != nil {
		return result, bindingErr
	}
	request.ExpectedVersion += int64(len(bindingEvents))
	completion, completionErr := workflowCompletionEvent(ctx, tx, request, actor, payload)
	if completionErr != nil {
		return result, completionErr
	}
	if err := CompleteWorkflowTxWithRegistry(ctx, tx, registry, completion, scope); err != nil {
		return result, err
	}
	result.EventIDs, err = operationEventIDsSince(ctx, tx, startSeq)
	if err != nil {
		return result, err
	}
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
