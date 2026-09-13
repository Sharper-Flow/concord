package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	ctx         context.Context
	tx          *sql.Tx
	request     WorkflowActionExecutionRequest
	entry       RegisteredDefinition
	currentStep string

	staleRecovery         bool
	lateVerdictRecovery   bool
	recoveryBind          bool
	workerFailureRecovery bool
	actorRef              string
	eventActor            string
	operatorRef           string
	actorNeedsRecord      bool
	operatorNeedsRecord   bool
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
	"complete":               {guardPhaseBoundary, guardCompleteBoundary},
	"link_successor":         {guardPhasePostValidation, guardForwardLinkOnly},
	"cross_context_boundary": {guardPhaseClaim, guardNoRestartDispatch},
	"record_delivery":        {guardPhaseClaim, guardDeliveryFollowsStart},
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

// guardMandatedWorkflowLawBound refuses actions that could strand a contract
// whose spec mandate still lacks its immutable evidence binding. An ordinary
// advance is refused only when it leaves a step that declares bind_evidence,
// because that step is the last place on the path where the mandate can be
// bound. Terminal acceptance actions are refused wherever they run. A late
// bind_evidence action remains available as the recovery route.
func guardMandatedWorkflowLawBound(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, actionID, subject string) error {
	if actionID == "supersede_contract" || actionID == "bind_evidence" {
		return nil
	}
	terminalGate := actionID == "record_verdict" || actionID == "confirm_premise" || actionID == "complete"
	if !terminalGate {
		mode, declared := workflowActionExecutionMode(definition, actionID)
		if !declared || mode != ActionAdvance || !stepDeclaresAction(definition, currentStep, "bind_evidence") {
			return nil
		}
	}
	mandate, err := workflowSpecMandate(ctx, q, workID, subject)
	if err != nil || len(mandate) == 0 {
		return err
	}
	bindingStep := workflowEvidenceBindingStep(definition, currentStep)
	if bindingStep == "" {
		return newFailure(KindInvariantViolation, subject, "workflow spec mandate has no bind_evidence step", false, "repair the pinned workflow definition")
	}
	for _, lawID := range mandate {
		bound, boundErr := workflowEvidenceReferenceBound(ctx, q, workID, lawID, subject)
		if boundErr != nil {
			return boundErr
		}
		if !bound {
			kind := KindMissingEvidence
			if actionID == "complete" {
				kind = KindInvariantViolation
			}
			return newFailure(kind, subject, fmt.Sprintf("spec mandate law %q is not bound", lawID), false, fmt.Sprintf("run bind_evidence on step %q before %s", bindingStep, actionID))
		}
	}
	return nil
}

func workflowSpecMandate(ctx context.Context, q queryer, workID, subject string) ([]string, error) {
	var mandateJSON string
	if err := q.QueryRowContext(ctx, `SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&mandateJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the workflow spec mandate", true, "retry once the workflow contract is readable", err)
	}
	var mandate []string
	if err := json.Unmarshal([]byte(mandateJSON), &mandate); err != nil {
		return nil, newFailure(KindInvariantViolation, subject, "workflow spec mandate is malformed", false, "rebuild projections from the event log")
	}
	return mandate, nil
}

func workflowEvidenceReferenceBound(ctx context.Context, q queryer, workID, reference, subject string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, reference).Scan(&count); err != nil {
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

// workflowWorkerFailureRecovery reports whether a failed worker attempt is
// still held by the current step. The recovery action is not added to the
// pinned definition, so this query preserves the definition digest.
func workflowWorkerFailureRecovery(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeRecorded bool) (bool, error) {
	if containsString(definition.AvailableActions, "record_worker_failure") || currentStep == "" {
		return false, nil
	}
	return workflowFailedWorkerAttempt(ctx, q, workID, currentStep, subject, excludeRecorded)
}

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
	query := `SELECT EXISTS(SELECT 1 FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND a.lifecycle_state='failed' AND d.seq>?)`
	args := []any{string(SubjectWorkItem), WorkerDispatched, workID, startSeq.Int64}
	if excludeRecorded {
		query = `SELECT EXISTS(SELECT 1 FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND a.lifecycle_state='failed' AND d.seq>? AND NOT EXISTS (SELECT 1 FROM domain_events f WHERE f.subject_type=? AND f.subject_id=a.work_id AND f.kind=? AND json_extract(f.payload,'$.action_id')='record_worker_failure' AND json_extract(f.payload,'$.worker_attempt_id')=a.attempt_id))`
		args = append(args, string(SubjectWorkItem), WorkflowActionCompleted)
	}
	var available int
	if err := q.QueryRowContext(ctx, query, args...).Scan(&available); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect failed worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	return available != 0, nil
}

// workflowContractCorrectionAvailable reports whether operator-approved
// contract correction is open on the current step. A human checkpoint carries
// the route unconditionally. A worker-dispatch step carries it only once the
// honest lane failure is in the durable record, so the audit trail holds the
// failure the correction answers before it holds the correction (CD-0133 D1).
func workflowContractCorrectionAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	if !workflowContractCorrectionCheckpoint(definition, currentStep) {
		return false, nil
	}
	step := workflowStep(definition, currentStep)
	if step.Kind == WorkflowStepHumanCheckpoint {
		return true, nil
	}
	failed, err := workflowFailedWorkerAttempt(ctx, q, workID, currentStep, subject, false)
	if err != nil || !failed {
		return false, err
	}
	unrecorded, err := workflowFailedWorkerAttempt(ctx, q, workID, currentStep, subject, true)
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
	for _, step := range definition.StepGraph.Steps {
		if step.ID == currentStep {
			return ""
		}
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
	requirements, requirementsErr := workflowEvidenceRecoveryRequirements(ctx, q, workID, definition, currentStep)
	if requirementsErr != nil {
		return false, requirementsErr
	}
	if len(requirements) == 0 {
		return false, nil
	}
	bindingStep := workflowEvidenceRecoveryBindingStep(definition, currentStep)
	fields, err := workflowActionObject(payload)
	if err != nil {
		return false, err
	}
	reference := workflowFieldStringDefault(fields, "evidence_ref", "")
	reference = workflowFieldStringDefault(fields, "immutable_subject_ref", reference)
	kind := workflowFieldStringDefault(fields, "evidence_kind", "verification")
	if reference != "" {
		exactBound, exactErr := workflowEvidenceTupleBound(ctx, q, workID, kind, reference)
		if exactErr != nil {
			return false, exactErr
		}
		if exactBound {
			return false, newFailure(KindIllegalLifecycleTransition, subject, "recovery bind_evidence cannot reopen an already bound evidence tuple", false, fmt.Sprintf("use bind_evidence on step %q before advancing", bindingStep))
		}
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

func workflowEvidenceRecoveryRequirements(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string) ([]workflowEvidenceRequirement, error) {
	bindingStep := workflowEvidenceRecoveryBindingStep(definition, currentStep)
	if bindingStep == "" || !workflowStepFollows(definition, bindingStep, currentStep) || containsString(definition.StepGraph.TerminalSteps, currentStep) {
		return nil, nil
	}
	return outstandingWorkflowEvidenceRequirementsForWork(ctx, q, workID, definition)
}

func workflowEvidenceRecoveryAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string) (bool, error) {
	requirements, err := workflowEvidenceRecoveryRequirements(ctx, q, workID, definition, currentStep)
	if err != nil {
		return false, err
	}
	return len(requirements) != 0, nil
}

// guardSupersedeContractRecovery admits contract recovery only for a workflow
// contract whose law revision is stale or domain-overlapped, and records that
// recovery for the later validation stages.
func guardSupersedeContractRecovery(g *workflowActionGuardContext) error {
	if err := checkWorkflowLawRevisionStalenessTx(g.ctx, g.tx, g.request.WorkID); err != nil {
		var failure *Failure
		if !failureAs(err, &failure) || (failure.Kind != KindStaleLawRevision && failure.Kind != KindDomainOverlap) {
			return err
		}
		g.staleRecovery = true
		return nil
	}
	correction, correctionErr := workflowContractCorrectionAvailable(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, "workflow_action")
	if correctionErr != nil {
		return correctionErr
	}
	if correction {
		g.staleRecovery = true
		return nil
	}
	return newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
}

func workflowContractCorrectionCheckpoint(definition WorkflowDefinition, currentStep string) bool {
	step := workflowStep(definition, currentStep)
	if step == nil || containsString(definition.StepGraph.TerminalSteps, currentStep) {
		return false
	}
	if step.Kind == WorkflowStepHumanCheckpoint {
		return containsString(step.Actions, "confirm_premise")
	}
	return step.Kind == WorkflowStepExternalEffect && containsString(step.Actions, "dispatch_worker")
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
		if _, present := workflowFieldString(fields, "predicate_id"); !present {
			return nil
		}
		return newFailure(KindInvalidOperation, "workflow_action", "late verdict recovery requires a missing, non-ok, or incomparable verdict for an active predicate", false, "record the verdict at its normal verification step or refresh the active contract")
	}
	g.lateVerdictRecovery = true
	return nil
}

func workflowLateVerdictRecoveryForActionPayload(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string, payload json.RawMessage) (bool, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return false, err
	}
	predicateID, present := workflowFieldString(fields, "predicate_id")
	if !present || predicateID == "" {
		return false, nil
	}
	contractVersion, present := workflowFieldIntOK(fields, "contract_version")
	if !present {
		contractVersion = 1
	}
	return workflowLateVerdictRecoveryForPredicate(ctx, q, workID, definition, currentStep, predicateID, contractVersion)
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

// The operator identity belongs only to approval-gated evaluation and premise
// confirmation. A worker cannot acquire this authority through its report.
func guardOperatorPremiseActor(g *workflowActionGuardContext) error {
	if g.request.OperatorActor == nil {
		if g.request.ActionID != "supersede_contract" {
			return nil
		}
		correction, correctionErr := workflowContractCorrectionAvailable(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, "workflow_action")
		if correctionErr != nil {
			return correctionErr
		}
		if correction {
			return newFailure(KindApprovalRequired, "workflow_action", "contract correction requires the verified operator approval identity", false, "request_approval")
		}
		return nil
	}
	if (g.request.ActionID != "confirm_premise" && g.request.ActionID != "record_verdict" && g.request.ActionID != "complete" && g.request.ActionID != "supersede_contract") || g.request.OperatorActor.ActorClass != ActorOperator {
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

// guardDeliveryFollowsStart admits record_delivery only on a step whose fenced
// start action has run in the current attempt. Delivery states that the step's
// own work finished, so a step that never started has nothing to deliver. The
// fold refuses delivery after a worker dispatch in the current attempt.
func guardDeliveryFollowsStart(g *workflowActionGuardContext) error {
	_, _, found, err := latestWorkflowActionStart(g.ctx, g.tx, g.request.WorkID, g.currentStep)
	if err != nil {
		return err
	}
	if !found {
		return newFailure(KindInvalidOperation, "workflow_action", "record_delivery requires the step's fenced start action in this attempt", false, "start the step, do its work, then record delivery")
	}
	return nil
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
	workflowActionEpoch, epochErr := workflowActionStartEpochForDispatch(ctx, tx, in.request.WorkID, in.currentStep, executionMode == ActionFenced)
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
	if builtinActionPolicies[in.request.ActionID].EventShape == ActionEventCheckpoint {
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
	} else if in.request.ActionID != "complete" {
		semantic, semanticErr := workflowSemanticActionEvents(ctx, tx, in.entry.Definition, in.request, in.currentStep, actor, in.payload, versionCursor+int64(len(events)-int(versionCursor-in.request.ExpectedVersion)), in.defaultVerdictEvidence)
		if semanticErr != nil {
			return out, semanticErr
		}
		if len(semantic) != 0 {
			events = append(events, semantic...)
			out.nativeRun = nativeRunFromSemanticEvents(semantic)
		}
	} else {
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
	if in.request.ActionID == "checkpoint_context" || in.request.ActionID == "cross_context_boundary" || in.request.ActionID == "supersede_contract" || in.lateVerdictRecovery {
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
	var workerPacketDigest string
	if in.request.ActionID == "accept_worker_result" || in.request.ActionID == "record_worker_failure" {
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
			return events, "", newFailure(KindUnauthorizedDispatch, "workflow_action", "lane capability class "+lane.CapabilityClass+" is not dispatchable at a "+string(in.step.Kind)+" step", false, "dispatch the lane at a step kind the lane-step dispatch join admits")
		}
		canonical, err := canonicalJSON(packetRaw)
		if err != nil {
			return events, "", newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet does not decode as canonical JSON", false, "supply the lane packet bound to this work item and attempt")
		}
		sum := sha256.Sum256(canonical)
		workerPacketDigest = "sha256:" + hex.EncodeToString(sum[:])
		completionValues["worker_packet_digest"] = workerPacketDigest
		if in.request.SessionWorktreeIdentity != "" {
			completionValues["worker_worktree_identity"] = in.request.SessionWorktreeIdentity
		}
	}
	events = append(events, workflowTypedEvent(in.request.OperationID+":completed", WorkflowActionCompleted, in.request.WorkID, in.eventActor, in.request.Now, resultVersion-1, completionValues))
	return events, workerPacketDigest, nil
}

func lateBindWorkflowEvidenceTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, payload json.RawMessage, definition WorkflowDefinition) ([]Event, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return nil, err
	}
	refs := append([]string(nil), request.EvidenceRefs...)
	var contractVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, request.WorkID).Scan(&contractVersion); err == nil {
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
	} else if err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot read active workflow contract", true, "retry once the database is readable", err)
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
func applyCompleteWorkflowActionTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, entry RegisteredDefinition, request WorkflowActionExecutionRequest, currentStep, actor string, payload json.RawMessage, prefixEvents []Event) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
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
	completion, completionErr := workflowCompletionEvent(ctx, tx, request, entry.Definition, currentStep, actor, payload)
	if completionErr != nil {
		return result, completionErr
	}
	if err := CompleteWorkflowTxWithRegistry(ctx, tx, registry, completion); err != nil {
		return result, err
	}
	result.EventIDs = make([]string, 0, len(prefixEvents)+len(bindingEvents)+1)
	for _, prefix := range prefixEvents {
		result.EventIDs = append(result.EventIDs, prefix.EventID)
	}
	for _, binding := range bindingEvents {
		result.EventIDs = append(result.EventIDs, binding.EventID)
	}
	result.EventIDs = append(result.EventIDs, completion.EventID)
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
