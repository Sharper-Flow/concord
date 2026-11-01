package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
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

	recoveryBind              bool
	correctionRequestRecovery bool
	correctionRequestMissing  string
	// admissionState and admissionDecision carry the fold's one admission
	// derivation: the tx-scoped loader's folded state and workflowAdmit's
	// decision over it. Guards consume them instead of re-deriving the
	// conditions per site.
	admissionState    *WorkflowAdmissionState
	admissionDecision *WorkflowAdmissionDecision
	actorRef          string
	eventActor        string
	actorRecordPrefix *workflowActorRecordPrefix
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
	"request_correction":     {guardPhaseRecovery, guardRequestCorrectionRecovery},
	"complete":               {guardPhaseBoundary, guardCompleteBoundary},
	"record_verdict":         {guardPhaseBoundary, guardRecordVerdictArtifactFreshness},
	"link_successor":         {guardPhasePostValidation, guardForwardLinkOnly},
	"cross_context_boundary": {guardPhaseClaim, guardNoRestartDispatch},
	"record_delivery":        {guardPhaseClaim, guardDeliveryAdmission},
}

// Historical oracle metadata does not require a new worker finding report.
// Explicit finding references must still name the exact retained open set.
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

func guardCorrectionRecovery(g *workflowActionGuardContext) error {
	if g.request.ActionID != "request_correction" {
		return nil
	}
	state, err := g.foldedAdmissionState()
	if err != nil {
		return err
	}
	if !state.CorrectionRequestRecovery {
		return workflowCorrectionRequestUnavailableFailure("workflow_action", state.CorrectionRequestMissing)
	}
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

// guardDeliveryAdmission consumes the shared admission decision.
func guardDeliveryAdmission(g *workflowActionGuardContext) error {
	decision, err := g.foldedAdmissionDecision()
	if err != nil {
		return err
	}
	if !decision.Admitted {
		return decision.Failure
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
	state, err := loadWorkflowAdmissionStateTxForRegistry(g.ctx, g.tx, g.request.WorkID, g.entry.Definition, g.currentStep, "workflow_action", g.registry)
	if err != nil {
		return nil, err
	}
	g.admissionState = &state
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
	if decision.Admitted || decision.OffStep || decision.AdvanceHeld {
		if err := validateWorkflowReviewRequest(g.ctx, g.tx, g.request.WorkID, g.request.ActionID, g.request.Payload); err != nil {
			return nil, err
		}
	}
	g.admissionDecision = &decision
	return &decision, nil
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

	eventActor        string
	actorRecordPrefix *workflowActorRecordPrefix
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
	return actionID == "checkpoint_context" || actionID == "cross_context_boundary" || actionID == "record_work_context"
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
func appendGenericWorkflowCompletion(in workflowActionAssemblyInput, attemptEpoch int64, events []Event) ([]Event, error) {
	if workflowActionOmitsGenericCompletion(in.request.ActionID) {
		return events, nil
	}
	fields, fieldsErr := workflowActionObject(in.payload)
	if fieldsErr != nil {
		return events, fieldsErr
	}
	completionValues := map[string]any{
		"step_id": in.currentStep, "action_id": in.request.ActionID, "attempt_epoch": attemptEpoch, "result_evidence_refs": in.evidenceRefs,
		"changed_refs": []string{in.request.WorkID}, "actor_ref": in.eventActor,
	}
	if in.request.ActionID == "record_review" {
		report, err := decodeRecordedReview(in.payload)
		if err != nil {
			return events, err
		}
		completionValues["review"] = report
	}
	if in.request.ActionID == "record_verdict" {
		// The batched form declares its entry count on the completion, so the
		// fold bounds the operation's result evidence at the schema-bounded
		// batch union instead of the single-form bound. The single form stays
		// silent and keeps its bound.
		if _, batchPresent := fields["verdicts"]; batchPresent {
			entries, entriesErr := normalizeWorkflowVerdictEntries(fields)
			if entriesErr != nil {
				return events, entriesErr
			}
			completionValues["verdict_entry_count"] = len(entries)
		}
	}
	if in.request.ActionID == "record_delivery" && workflowDefinitionRequiresDeliveryPayload(in.entry.Definition) {
		artifact := workflowFieldStringDefault(fields, "delivery_artifact", "")
		state := workflowFieldStringDefault(fields, "delivery_state", "")
		if artifact == "" || state == "" {
			return events, newFailure(KindInvalidPayload, "workflow_action", "record_delivery requires delivery_artifact and delivery_state", false, "supply the asserted delivery artifact and state")
		}
		completionValues["delivery_artifact"] = artifact
		completionValues["delivery_state"] = state
	}
	if in.request.ActionID == "request_correction" {
		bindWorkflowCorrectionCompletion(fields, completionValues)
	}
	resultVersion := in.request.ExpectedVersion + int64(len(events)) + 1
	events = append(events, workflowTypedEvent(in.request.OperationID+":completed", WorkflowActionCompleted, in.request.WorkID, in.eventActor, in.request.Now, resultVersion-1, completionValues))
	return events, nil
}

func bindWorkflowCorrectionCompletion(fields map[string]json.RawMessage, completionValues map[string]any) {
	completionValues["correction_diagnosis"] = workflowFieldStringDefault(fields, "diagnosis", "")
	completionValues["correction_strategy"] = workflowFieldStringDefault(fields, "strategy", "")
	completionValues["correction_predicate_ids"] = workflowFieldStrings(fields, "predicate_ids")
	completionValues["correction_evidence_refs"] = workflowFieldStrings(fields, "evidence_refs")
	// The derived open finding set serializes onto the correction
	// record of both surfaces. A rejection carries it directly; a
	// correction request carries it beside the predicate list the legacy
	// comparison reads, and the folded convergence comparison prefers it
	// whenever the request recorded one.
	if _, present := fields["open_finding_ids"]; present {
		completionValues["correction_open_finding_ids"] = workflowFieldStrings(fields, "open_finding_ids")
	}
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
func applyCompleteWorkflowActionTx(ctx context.Context, tx *sql.Tx, scope *writeScope, registry DefinitionRegistry, request WorkflowActionExecutionRequest, actor string, payload json.RawMessage, prefixEvents []Event) (WorkflowActionExecutionResult, error) {
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
	completion, completionErr := workflowCompletionEventForDefinition(ctx, tx, registry, request, actor, payload)
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
