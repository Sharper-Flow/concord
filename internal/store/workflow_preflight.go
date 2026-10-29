package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

type WorkflowDefinitionPin struct {
	Ref    string
	Digest string
}

type WorkflowActionPreflightRequest struct {
	WorkID                string
	ExpectedVersion       int64
	StepID                string
	ActionID              string
	SelectedChoice        string
	DecisionContextDigest string
	Payload               json.RawMessage
	Actor                 WorkflowActor
	// SessionWorktree is the host worktree boundary. It is checked only for
	// dispatch_worker, whose worker must run in the active claimed worktree.
	SessionWorktree string
}

func VerifyWorkflowDefinitionPin(registry DefinitionRegistry, pin WorkflowDefinitionPin) (RegisteredDefinition, error) {
	entry, err := resolveCurrentWorkflowDefinition(registry, pin.Ref)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	if pin.Digest != entry.Digest {
		return RegisteredDefinition{}, workflowPinFailure("requested workflow definition is not current")
	}
	return entry, nil
}

func resolveCurrentWorkflowDefinition(registry DefinitionRegistry, ref string) (RegisteredDefinition, error) {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	if !validWorkflowRef(ref) {
		return RegisteredDefinition{}, workflowPinFailure("workflow definition family is incomplete")
	}
	// No archived family registers a definition (CD-0242 D14). Its
	// nonterminal work refuses every public action and read by name until the
	// cutover migration resolves it (CD-0242 D13).
	if workflowFamilyArchived(ref) {
		return RegisteredDefinition{}, newFailure(KindInvariantViolation, "workflow_preflight",
			"workflow family "+ref+" is archived: no definition is registered, so new public workflow actions are refused", false,
			"resolve the work through the cutover migration, or start retained-family work")
	}
	entry, ok := registry.Current(ref)
	if !ok {
		return RegisteredDefinition{}, workflowPinFailure("current workflow definition is unavailable: " + ref)
	}
	computed, err := WorkflowDefinitionDigest(entry.Definition)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	if computed != entry.Digest {
		return RegisteredDefinition{}, workflowPinFailure("current workflow definition digest drifted")
	}
	return entry, nil
}

func workflowPinFailure(detail string) error {
	return newFailure(KindInvariantViolation, "workflow_preflight", detail, false, "reread_entities")
}

// workflowPreflightFailure re-subjects one folded admission refusal for the
// preflight surface: the kind, detail, and recovery action are the shared
// admission's answer, and only the reporting operation names the surface that
// refused.
func workflowPreflightFailure(failure *Failure) *Failure {
	resubjected := *failure
	resubjected.Op = "workflow_action_preflight"
	return &resubjected
}

func VerifyWorkflowInstanceDefinition(ctx context.Context, s *Store, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
	if s == nil || s.db == nil {
		return RegisteredDefinition{}, newFailure(KindUnavailable, "workflow_preflight", "store is not open", false, "open the authority database")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	var pin WorkflowDefinitionPin
	if err := s.db.QueryRowContext(ctx, `SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, workflowPinFailure("workflow instance is not recorded")
		}
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_preflight", "cannot read workflow definition pin", true, "retry once the database is readable", err)
	}
	return resolveCurrentWorkflowDefinition(registry, pin.Ref)
}

func WorkflowActionAvailableWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, workID string) (bool, error) {
	if _, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, workID); err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindInvariantViolation {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// WorkflowActionPreflightWithRegistry answers whether the named workflow
// action would pass the owning boundary against current authority, without
// advancing the instance. The admission is the single implementation: the
// action resolution runs against the store (it takes the pool-backed reader),
// and the transaction preflight — the same function the owning boundary runs
// — folds and decides inside one read snapshot. Inspection does not acquire
// the write lock; the owning boundary revalidates before any effect.
func WorkflowActionPreflightWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_action_preflight", "store is not open", false, "open the authority database")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	if _, _, err := WorkflowActionDefinitionFor(ctx, s, registry, request.WorkID, request.ActionID); err != nil {
		return err
	}
	readTx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot begin workflow admission read snapshot", true, "retry once the database is readable", err)
	}
	defer func() { _ = readTx.Rollback() }()
	_, err = workflowActionPreflightTx(ctx, readTx, registry, request)
	return err
}

// AuthorizeWorkflowActionAtBoundaryTx is the owning-action transaction
// coordinator. The preflight, authorization, and the persisted action callback
// share one transaction and one write guard, so a callback error or mutation
// failure commits nothing.
func AuthorizeWorkflowActionAtBoundaryTx(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest, authorize func(*Transaction) error, mutate func(*Transaction) error) error {
	return authorizeWorkflowActionAtBoundaryCore(ctx, s, registry, request, nil, authorize, mutate)
}

// AuthorizeWorkflowActionAtBoundaryWithPreflightTx lets an owning dispatcher
// inspect durable idempotency before the version-sensitive preflight. This
// preserves exact replay when concurrent requests share one action key.
func AuthorizeWorkflowActionAtBoundaryWithPreflightTx(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest, beforePreflight func(*Transaction) (bool, error), authorize func(*Transaction) error, mutate func(*Transaction) error) error {
	if beforePreflight == nil {
		return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, registry, request, authorize, mutate)
	}
	return authorizeWorkflowActionAtBoundaryCore(ctx, s, registry, request, beforePreflight, authorize, mutate)
}

func authorizeWorkflowActionAtBoundaryCore(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest, beforePreflight func(*Transaction) (bool, error), authorize func(*Transaction) error, mutate func(*Transaction) error) (retErr error) {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_action_boundary", "store is not open", false, "open the authority database")
	}
	if mutate == nil {
		return newFailure(KindInvalidOperation, "workflow_action_boundary", "owning action mutation is required", false, "supply the persisted action operation")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot begin owning action", true, "retry once the database is writable", err)
	}
	transaction := &Transaction{tx: tx.Tx}
	defer tx.finish(&retErr)
	defer func() { transaction.tx = nil }()
	scope, err := beginWrite(ctx, tx.Tx)
	if err != nil {
		return err
	}
	transaction.write = scope
	defer func() { _ = scope.close(ctx) }()
	if beforePreflight != nil {
		handled, err := beforePreflight(transaction)
		if err != nil {
			return err
		}
		if handled {
			if err := scope.close(ctx); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot commit owning action replay", true, "retry once the database is writable", err)
			}
			return nil
		}
	}
	entry, err := workflowActionPreflightTx(ctx, tx.Tx, registry, request)
	if err != nil {
		return err
	}
	if workflowActionConsequence(entry.Definition, request.ActionID) != ActionInternalSQLite {
		// Project-session handoff admission (CD-0182 amendment): a
		// session whose receiving Project holds an unconsumed or stale
		// frontier executes no managed external effect until a session of
		// that Project consumes the addressed handoff under the active
		// contract. A work with no handoffs is unaffected.
		if err := RefuseUnconsumedProjectHandoffTx(ctx, tx.Tx, request.WorkID, request.Actor.SessionRef); err != nil {
			return err
		}
	}
	if authorize != nil {
		if err := authorize(transaction); err != nil {
			return err
		}
	}
	if err := mutate(transaction); err != nil {
		return err
	}
	if err := scope.close(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot commit owning action", true, "retry once the database is writable", err)
	}
	return nil
}

func workflowActionConsequence(definition WorkflowDefinition, actionID string) ActionConsequence {
	if actionID == "supersede_contract" || actionID == "record_verdict" || actionID == "record_worker_failure" || actionID == "reject_worker_result" || actionID == "request_correction" {
		return ActionInternalSQLite
	}
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			return action.Consequence
		}
	}
	return ""
}

func workflowActionPreflightTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionPreflightRequest) (RegisteredDefinition, error) {
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, request.WorkID)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	var currentStep, state, lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,instance_state,(SELECT lifecycle FROM work_items WHERE id=workflow_instances.work_id),(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &state, &lifecycle, &version); err != nil {
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot read workflow instance state", true, "retry once the database is readable", err)
	}
	if request.StepID != "" && request.StepID != currentStep {
		return RegisteredDefinition{}, workflowPinFailure("workflow action request does not match the current definition step")
	}
	if request.ExpectedVersion > 0 && request.ExpectedVersion != version {
		conflict, conflictErr := versionConflictForQuery(ctx, tx, request.WorkID, request.ExpectedVersion, version)
		if conflictErr != nil {
			return RegisteredDefinition{}, conflictErr
		}
		return RegisteredDefinition{}, conflict
	}
	// One admission derivation for the preflight: the tx-scoped loader folds
	// the instance history into the abstract admission state once, and the
	// pure workflowAdmit decides. The refusal applies here except the review
	// gate's own fresh-review refusal (its payload-bound ready-review
	// carve-out), the operator-approval walls, and the closed-question wall,
	// which the approval-gated mutation boundary and the selection chain own
	// before the fold refuses them. A staleness, impact, wall, or
	// step-legality refusal is never deferred. The recovery flags and the
	// supersede classification below read the same folded state instead of
	// re-deriving the conditions per site.
	admission, _, _, admissionErr := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, request.WorkID, entry.Definition, currentStep, "workflow_action_preflight", registry)
	if admissionErr != nil {
		return RegisteredDefinition{}, admissionErr
	}
	if err := bindWorkflowReviewRequest(ctx, tx, &admission, request.WorkID, request.ActionID, request.Payload, request.Actor); err != nil {
		return RegisteredDefinition{}, err
	}
	decision := workflowAdmit(entry.Definition, admission, request.ActionID)
	if !decision.Admitted && !decision.OffStep && !decision.AdvanceHeld && !decision.OperatorQuestionClosed {
		return RegisteredDefinition{}, workflowPreflightFailure(decision.Failure)
	}
	staleRecovery := decision.RecoveryRoute
	if request.ActionID == "supersede_contract" {
		if err := validateWorkflowCompleteCorrectionRoute(entry.Definition, currentStep, request.Payload); err != nil {
			return RegisteredDefinition{}, err
		}
	}
	if err := ValidateWorkflowActor(request.Actor); err != nil {
		return RegisteredDefinition{}, err
	}
	if _, err := WorkflowActorRef(request.Actor); err != nil {
		return RegisteredDefinition{}, err
	}
	if staleRecovery {
		if err := validateWorkflowContractRecoveryPayload(entry.Definition, request.Payload); err != nil {
			return RegisteredDefinition{}, err
		}
	} else if err := validateWorkflowActionEnvelopePayload(entry.Definition, request); err != nil {
		return RegisteredDefinition{}, err
	}
	// The folded admission owns step legality, applied here — the engine's
	// late position, after the payload and guard checks whose specific
	// refusals precede the generic off-step one. The payload-bound recoveries
	// refine it first: the evidence-binding recovery binds the outstanding
	// requirement the payload names.
	if err := preflightWorkflowActionStepTx(ctx, tx, entry.Definition, currentStep, request, decision); err != nil {
		return RegisteredDefinition{}, err
	}
	if err := preflightWorkflowActionGuardsTx(ctx, tx, entry.Definition, currentStep, request, admission); err != nil {
		return RegisteredDefinition{}, err
	}
	// The selection core consumes the folded admission's confirmation
	// answer: one classification, no second fact read.
	if err := validateWorkflowOperatorSelectionCoreTx(ctx, tx, entry, request, workflowConfirmationFold{authority: admission.ConfirmationAuthority, factFailure: admission.ConfirmationFactFailure}); err != nil {
		return RegisteredDefinition{}, err
	}
	// See the note in the non-transactional preflight: recording a new actor is
	// the action guard's job, and refusing here would prevent it.
	return entry, nil
}

func validateWorkflowCompleteCorrectionRoute(definition WorkflowDefinition, currentStep string, payload json.RawMessage) error {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	atCompleteStep := workflowCompleteStepCorrectionStep(definition, currentStep)
	declaresRoute := slices.Contains(workflowFieldStrings(fields, "route_conventions"), workflowCompleteStepCorrectionRoute)
	if declaresRoute && !atCompleteStep {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", "route convention complete_step_correction is reserved for correction at the pinned complete step", false, "drop the reserved route convention")
	}
	if atCompleteStep && !declaresRoute {
		// The payload satisfies its own declaration; what refuses is the
		// step state, so the refusal is an operation refusal, not a
		// payload one.
		return newFailure(KindInvalidPayload, "workflow_action_preflight", "complete-step correction requires complete_step_correction in fields.route_conventions", false, "declare complete_step_correction in the successor route conventions")
	}
	return nil
}

func preflightWorkflowActionStepTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, currentStep string, request WorkflowActionPreflightRequest, decision WorkflowAdmissionDecision) error {
	if request.ActionID == "bind_evidence" {
		// Preflight carries only payload evidence. Execution checks the full
		// envelope kind inventory through the same guard before writing.
		recoveryBind, recoveryErr := guardRecoveryEvidenceBind(ctx, tx, request.WorkID, definition, currentStep, request.Payload, "workflow_action_preflight")
		if recoveryErr != nil {
			return recoveryErr
		}
		if !recoveryBind && !definitionStepAllows(definition, currentStep, request.ActionID) {
			return newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", "workflow action is not declared on the current step", false, "reread_entities")
		}
	} else if decision.OffStep {
		return workflowPreflightFailure(decision.Failure)
	}
	return nil
}

func preflightWorkflowActionGuardsTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, currentStep string, request WorkflowActionPreflightRequest, admission WorkflowAdmissionState) error {
	switch request.ActionID {
	case "dispatch_worker":
		return validateWorkerDispatchWorktree(ctx, tx, request.WorkID, request.SessionWorktree)
	case "record_verdict":
		return preflightWorkflowVerdictTx(ctx, tx, definition, currentStep, request)
	case "request_correction":
		// The payload check binds against the folded state's one correction
		// request derivation; it never re-enters the loader.
		if admission.CorrectionRequestContext == nil {
			return workflowCorrectionRequestUnavailableFailure("workflow_action_preflight", admission.CorrectionRequestMissing)
		}
		return validateCorrectionRequestPayload(ctx, tx, request.WorkID, request.Payload, "workflow_action_preflight", admission.CorrectionRequestContext)
	case "accept_worker_evidence", "accept_worker_result":
		return preflightWorkflowWorkerAcceptanceTx(ctx, tx, definition, currentStep, request)
	}
	return nil
}

func preflightWorkflowVerdictTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, currentStep string, request WorkflowActionPreflightRequest) error {
	fields, err := workflowActionObject(request.Payload)
	if err != nil {
		return err
	}
	actor, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return err
	}
	if _, err := workflowVerdictReceiptAssociations(ctx, tx, request.WorkID, actor, fields); err != nil {
		return err
	}
	return guardLateCriterionAssociations(ctx, tx, request.WorkID, definition, currentStep, fields)
}

func preflightWorkflowWorkerAcceptanceTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, currentStep string, request WorkflowActionPreflightRequest) error {
	fields, fieldsErr := workflowActionObject(request.Payload)
	if fieldsErr != nil {
		return fieldsErr
	}
	if request.ActionID == "accept_worker_evidence" && !definitionStepAllows(definition, currentStep, request.ActionID) {
		if err := validateRefineReviewEvidenceDisposition(ctx, tx, request.WorkID, definition, currentStep, fields, "workflow_action_preflight", 0); err != nil {
			return err
		}
	}
	attempt := workflowFieldStringDefault(fields, "attempt_id", "")
	var capability string
	if err := tx.QueryRowContext(ctx, `SELECT capability_class FROM worker_attempts WHERE work_id=? AND attempt_id=?`, request.WorkID, attempt).Scan(&capability); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "workflow_action_preflight", "worker attempt does not exist for this work item", false, "dispatch the worker attempt before recording its result")
		}
		return wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot read the requested worker capability", true, "retry once the database is readable", err)
	}
	if capability == "review" {
		if err := workflowAcceptanceSubjectCurrent(ctx, tx, request.WorkID, []string{attempt}, nil); err != nil {
			return err
		}
	}
	return nil
}

// workflowEnvelopeProjectedPayload merges the envelope-carried values of
// declared envelope fields into the payload map the preflight and the fold
// validate. The generated envelope schema projects the declared envelope
// fields to the envelope outer level, so the same declaration is read here: a
// supplied envelope value is validated against the declared type, and the
// fields object carries only what the envelope does not project. Absent
// values are left to the operator-selection guard, which owns presence
// enforcement at the agent boundary; the fold consumes the operator approval
// instead. The merged copy is validation input only; the recorded payload
// stays what the caller supplied.
func workflowEnvelopeProjectedPayload(definition WorkflowDefinition, actionID string, payload json.RawMessage, selectedChoice, decisionContextDigest string) json.RawMessage {
	var payloadDefinition WorkflowPayloadDefinition
	found := false
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			payloadDefinition = action.Payload
			found = true
			break
		}
	}
	if !found {
		return payload
	}
	envelopeValues := map[string]string{"selected_choice": selectedChoice, "decision_context_digest": decisionContextDigest}
	projected := map[string]string{}
	for _, field := range payloadDefinition.Fields {
		if !field.Envelope {
			continue
		}
		if value, ok := envelopeValues[field.Name]; ok && value != "" {
			projected[field.Name] = value
		}
	}
	if len(projected) == 0 {
		return payload
	}
	fields := map[string]json.RawMessage{}
	if len(payload) != 0 {
		// A payload that is not one JSON object is reported by
		// validateWorkflowActionPayload with its own message; validate it
		// unmerged so the refusal matches the unprojected shape.
		if err := json.Unmarshal(payload, &fields); err != nil {
			return payload
		}
	}
	for name, value := range projected {
		encoded, err := json.Marshal(value)
		if err != nil {
			return payload
		}
		fields[name] = encoded
	}
	// encoding/json sorts map keys, so the merged validation payload is
	// deterministic for a given input.
	merged, err := json.Marshal(fields)
	if err != nil {
		return payload
	}
	return merged
}

// workflowLivePayloadSchemaDocument resolves declared schema references
// against the generated public authoring document.
func workflowLivePayloadSchemaDocument() map[string]any {
	return payloadschema.Document()
}

// validateWorkflowActionEnvelopePayload validates the declared action payload
// the way the envelope carries it, projecting the envelope-level
// operator-decision values into the validation map when the declaration names
// them.
func validateWorkflowActionEnvelopePayload(definition WorkflowDefinition, request WorkflowActionPreflightRequest) error {
	payload := workflowEnvelopeProjectedPayload(definition, request.ActionID, request.Payload, request.SelectedChoice, request.DecisionContextDigest)
	return validateWorkflowActionPayload(definition, request.ActionID, payload)
}

func validateWorkflowActionPayload(definition WorkflowDefinition, actionID string, payload json.RawMessage) error {
	document := workflowLivePayloadSchemaDocument()
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := decodeWorkflowActionPayloadStrict(payload, &fields); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", "workflow action payload is not one strict JSON object", false, "supply the registered action payload")
	}
	if err := validateEvidenceBindingReferences(actionID, fields); err != nil {
		return err
	}
	payloadDefinition, found, err := workflowActionPayloadDefinition(definition, actionID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	definitionFields := payloadDefinition.Fields
	if !payloadDefinition.Closed && len(definitionFields) == 0 {
		return nil
	}
	allowed := make(map[string]WorkflowPayloadField, len(definitionFields))
	for _, field := range definitionFields {
		allowed[field.Name] = field
	}
	if err := validateWorkflowPayloadStructure(actionID, definitionFields, allowed, fields); err != nil {
		return err
	}
	// The structure is complete, so the per-field value and schema checks
	// walk the supplied names in sorted order and keep stop-at-first
	// behavior: a type error on one field is a per-field value fact outside
	// the structural refusal.
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		field := allowed[name]
		if !validateWorkflowPayloadValue(field, fields[name]) {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q has the wrong registered type or bounds for rule %s", name, workflowPayloadFieldRule(field)), false, "supply the declared action field type and bounds")
		}
		if err := validateWorkflowPayloadSchema(document, field, fields[name]); err != nil {
			return err
		}
	}
	if err := validateWorkflowPayloadCrossFieldRules(actionID, fields); err != nil {
		return err
	}
	if actionID == "approve_contract" && workflowApprovalPolicyRequiresDecisionFact(definition) {
		_, err := workflowSubmittedDecisionDomains(definition, "approval", fields)
		return err
	}
	// The proposal document rule is cross-field, so it runs only for a pinned
	// definition that declares the document. A definition pinned before the
	// typed payload keeps its empty call.
	if _, typed := allowed["problem"]; typed && actionID == "record_proposal" {
		_, err := decodeWorkflowProposalContent(payload)
		return err
	}
	return nil
}

func workflowActionPayloadDefinition(definition WorkflowDefinition, actionID string) (WorkflowPayloadDefinition, bool, error) {
	var payloadDefinition WorkflowPayloadDefinition
	found := false
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			payloadDefinition = action.Payload
			found = true
			break
		}
	}
	// A consequence_v1 pin validates its found initial contract approval
	// against the selected decision-fact declaration; the typed refusal
	// propagates.
	if found && actionID == "approve_contract" && workflowApprovalPolicyRequiresDecisionFact(definition) {
		selected, err := workflowDecisionFactInitialActionDefinition(definition)
		if err != nil {
			return WorkflowPayloadDefinition{}, false, err
		}
		payloadDefinition = selected.Payload
	}
	if !found {
		if actionID == "record_worker_failure" {
			payloadDefinition = workerFailureRecoveryActionDefinition().Payload
			found = true
		}
		if actionID == "reject_worker_result" || actionID == "request_correction" {
			payloadDefinition = workflowCorrectionActionDefinitionFor(definition, actionID).Payload
			found = true
		}
	}
	return payloadDefinition, found, nil
}

func validateWorkflowPayloadStructure(actionID string, definitionFields []WorkflowPayloadField, allowed map[string]WorkflowPayloadField, fields map[string]json.RawMessage) error {
	// The structural scan runs before any per-field value check and refuses
	// once for the whole object: missing required fields in declared order,
	// then undeclared fields sorted by name, each segment keeping the
	// per-field message. A payload missing N fields therefore costs one
	// retry, not N, and the sorted undeclared walk reports the same fields
	// in the same order on every run. Envelope fields are exempt from the
	// required scan: the envelope carries them at the outer level, the
	// generated envelope schema requires them there, and the
	// operator-selection guard owns their presence at the agent boundary.
	missing := make([]string, 0)
	exempt := workflowAlternativeForbiddenFields(actionID, fields)
	for _, field := range definitionFields {
		if field.Required && !field.Envelope && !exempt[field.Name] {
			if _, ok := fields[field.Name]; !ok {
				missing = append(missing, field.Name)
			}
		}
	}
	undeclared := make([]string, 0)
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if _, ok := allowed[name]; !ok {
			undeclared = append(undeclared, name)
		}
	}
	if len(missing) > 0 || len(undeclared) > 0 {
		segments := make([]string, 0)
		for _, name := range missing {
			segments = append(segments, fmt.Sprintf("workflow action payload field %q is required for action %q", name, actionID))
		}
		for _, name := range undeclared {
			segments = append(segments, fmt.Sprintf("workflow action payload field %q is not declared for action %q", name, actionID))
		}
		remedy := "supply every required registered action field"
		if len(undeclared) > 0 {
			if len(missing) > 0 {
				remedy = "supply every required registered action field and use only fields declared by the pinned definition"
			} else {
				remedy = "use only fields declared by the pinned definition"
			}
		}
		return newFailure(KindInvalidPayload, "workflow_action_preflight", strings.Join(segments, "; "), false, remedy)
	}
	return nil
}

// workflowAlternativeForbiddenFields names the fields a fully supplied
// alternative group forbids. A required field is not required beside a group
// that forbids it: the published branch for that group omits it, and the
// cross-field rule refuses it there.
func workflowAlternativeForbiddenFields(actionID string, fields map[string]json.RawMessage) map[string]bool {
	forbidden := map[string]bool{}
	for _, group := range workflowActionCrossField(actionID).Alternatives {
		if slices.ContainsFunc(group.Fields, func(name string) bool { _, ok := fields[name]; return !ok }) {
			continue
		}
		for _, name := range group.Forbids {
			forbidden[name] = true
		}
	}
	return forbidden
}

// validateWorkflowPayloadCrossFieldRules enforces the legal input
// combinations and alternative field groups the action's ENGINE registry
// declaration names — the same single declaration (resolved through
// workflowActionCrossField) the generated published variants build their
// closed branches from, so a payload any published variant refuses, core
// admission refuses too, and vice versa (CON-412). The rules are
// version-invariant engine law: they bind whatever action version the
// pinned definition carries, exactly as the released engine guard table
// always did.
func validateWorkflowPayloadCrossFieldRules(actionID string, fields map[string]json.RawMessage) error {
	declaration := workflowActionCrossField(actionID)
	if combination, field, requires, violated := workflowPayloadCombinationViolation(declaration, fields); violated {
		remedy := combination.Remedy
		if remedy == "" {
			remedy = "supply the fields the declared " + combination.Field + " value requires"
		}
		if requires {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s %s %s requires %s", actionID, combination.Field, combination.Value, field), false, remedy)
		}
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s %s %s cannot carry %s", actionID, combination.Field, combination.Value, field), false, remedy)
	}
	if declaration.Alternatives != nil {
		complete, partial := workflowPayloadAlternativeGroups(declaration.Alternatives, fields)
		if complete == 1 && !partial {
			if err := workflowAlternativeForbids(actionID, declaration.Alternatives, fields); err != nil {
				return err
			}
		} else {
			groups := make([]string, 0, len(declaration.Alternatives))
			for _, group := range declaration.Alternatives {
				groups = append(groups, strings.Join(group.Fields, "+"))
			}
			switch {
			case complete == 0 && !partial:
				return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s requires one of the declared field groups: %s", actionID, strings.Join(groups, "; ")), false, "supply exactly one declared field group")
			case complete >= 2:
				return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s must supply exactly one declared field group, not several of: %s", actionID, strings.Join(groups, "; ")), false, "supply exactly one declared field group")
			default:
				// Zero or one complete group plus a partially supplied other
				// group. Counting only complete groups admitted these payloads
				// while the published closed branches refused them; the closed
				// branch semantics is the declaring owner's (CON-412).
				return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s must supply exactly one declared field group: a field of another group is present without its complete group (%s)", actionID, strings.Join(groups, "; ")), false, "complete the started field group or remove its fields")
			}
		}
	}
	if match, fieldKind, objectKind, violated := workflowPayloadKindMatchViolation(declaration, fields); violated {
		return workflowPayloadKindMatchRefusal(actionID, match, fieldKind, objectKind)
	}
	return nil
}

// workflowAlternativeForbids enforces the one complete group's Forbids: a
// field the declaration forbids beside that group cannot ride at the top
// level even though no group claims it (record_verdict's entry-level fields
// beside the batch, CD-0198 D1). The refusal names the field and the group
// it cannot ride beside, from the same declaration the published variant
// builds its closed branch from (CON-412).
func workflowAlternativeForbids(actionID string, alternatives []WorkflowPayloadAlternative, fields map[string]json.RawMessage) error {
	for _, group := range alternatives {
		present := 0
		for _, name := range group.Fields {
			if _, ok := fields[name]; ok {
				present++
			}
		}
		if present != len(group.Fields) {
			continue
		}
		for _, name := range group.Forbids {
			if _, ok := fields[name]; ok {
				remedy := group.Remedy
				if remedy == "" {
					remedy = "drop " + name + " or supply the other declared field group"
				}
				return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s field %s cannot ride beside the %s group", actionID, name, strings.Join(group.Fields, "+")), false, remedy)
			}
		}
		return nil
	}
	return nil
}

// workflowPayloadCombinationViolation evaluates the supplied fields against
// the declaration's legal input combinations and reports the first broken
// rule: which combination, the requires/forbids field that broke it, and
// whether the broken side is a required (absent) or forbidden (present)
// field. Callers render their own typed refusal; this function and the
// declaration are the rule's only owners (CON-412).
func workflowPayloadCombinationViolation(declaration WorkflowActionCrossField, fields map[string]json.RawMessage) (combination WorkflowPayloadCombination, field string, requires bool, violated bool) {
	for _, candidate := range declaration.Combinations {
		value, ok := workflowFieldString(fields, candidate.Field)
		if !ok || value != candidate.Value {
			continue
		}
		for _, name := range candidate.Requires {
			if _, present := fields[name]; !present {
				return candidate, name, true, true
			}
		}
		for _, name := range candidate.Forbids {
			if _, present := fields[name]; present {
				return candidate, name, false, true
			}
		}
	}
	return WorkflowPayloadCombination{}, "", false, false
}

// workflowPayloadAlternativeGroups reports how many of the declaration's
// alternative field groups the payload supplies completely, and whether any
// group is partially engaged (a member present without its whole group).
// The published closed branches accept exactly one complete group with no
// field of any other group present, so a partially supplied second group is
// a violation, not a neutral payload (CON-412).
func workflowPayloadAlternativeGroups(alternatives []WorkflowPayloadAlternative, fields map[string]json.RawMessage) (complete int, partial bool) {
	for _, group := range alternatives {
		present := 0
		for _, name := range group.Fields {
			if _, ok := fields[name]; ok {
				present++
			}
		}
		if present == len(group.Fields) {
			complete++
		} else if present > 0 {
			partial = true
		}
	}
	return complete, partial
}

// workflowPayloadKindMatchViolation evaluates the declaration's nested
// discriminator equalities against the supplied fields and reports the first
// broken match: which match, the scalar value, and the object's
// discriminator value. The caller renders the field-named typed refusal;
// this function and the declaration are the rule's only owners (CON-412).
func workflowPayloadKindMatchViolation(declaration WorkflowActionCrossField, fields map[string]json.RawMessage) (match WorkflowPayloadKindMatch, fieldKind, objectKind string, violated bool) {
	for _, candidate := range declaration.KindMatches {
		scalar, ok := workflowFieldString(fields, candidate.Field)
		if !ok {
			continue
		}
		raw, present := fields[candidate.Object]
		if !present {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			continue
		}
		kindRaw, carries := object[candidate.Discriminator]
		if !carries {
			continue
		}
		if json.Unmarshal(kindRaw, &objectKind) != nil || objectKind == "" {
			continue
		}
		if scalar != objectKind {
			return candidate, scalar, objectKind, true
		}
	}
	return WorkflowPayloadKindMatch{}, "", "", false
}

// workflowPayloadKindMatchRefusal renders the field-named refusal for a
// broken nested discriminator equality, naming both fields and both values.
func workflowPayloadKindMatchRefusal(actionID string, match WorkflowPayloadKindMatch, fieldKind, objectKind string) error {
	remedy := match.Remedy
	if remedy == "" {
		remedy = "supply the " + match.Object + " whose " + match.Discriminator + " equals " + match.Field
	}
	return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s field %q is %q but field %q carries %s %q", actionID, match.Field, fieldKind, match.Object, match.Discriminator, objectKind), false, remedy)
}

func validateEvidenceBindingReferences(actionID string, fields map[string]json.RawMessage) error {
	if actionID != "bind_evidence" {
		return nil
	}
	for _, name := range []string{"evidence_ref", "immutable_subject_ref"} {
		raw, present := fields[name]
		if !present {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || !ValidReference(value) {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q must satisfy the reference rule: 2 to 128 bytes with no Unicode White_Space", name), false, "supply a bounded reference with no Unicode White_Space")
		}
	}
	return nil
}

// validateWorkflowPayloadSchema enforces the structure a payload field already
// declares by name. The registry carries schema_ref for its object and array
// fields, and the agent boundary resolves the same schema, so one declaration
// has one enforcement authority and every refusal is one a published contract
// states.
//
// The document argument is the public authoring document. A declared name
// that the document does not carry fails closed. A declaration pointing at a schema
// nobody generated enforces nothing, and silently passing it would admit a
// payload no contract checked.
func validateWorkflowPayloadSchema(document map[string]any, field WorkflowPayloadField, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	// item_ref names the contract for each element. For a string list it
	// selects a built-in item kind, which validWorkflowPayloadListItem already
	// applied; for an array it names a generated schema.
	if field.ValueType == PayloadArray && field.ItemRef != "" {
		return validateWorkflowPayloadItems(document, field, raw)
	}
	if field.SchemaRef == "" {
		return nil
	}
	if !payloadschema.HasIn(document, field.SchemaRef) {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q declares schema %q, which is not generated", field.Name, field.SchemaRef), false, "regenerate the payload contracts or correct the declared schema reference")
	}
	// An array field frozen before item_ref could name a generated schema
	// describing one element rather than the whole value. Those definitions
	// cannot be edited, so the element reading is preserved for them: it is the
	// only reading under which they ever accepted a payload. A definition
	// written now says which it means by choosing item_ref or schema_ref.
	if field.ValueType == PayloadArray && !payloadschema.DescribesArrayIn(document, field.SchemaRef) {
		return validateWorkflowPayloadItems(document, WorkflowPayloadField{Name: field.Name, ValueType: PayloadArray, ItemRef: field.SchemaRef}, raw)
	}
	if err := payloadschema.ValidateInDocument(document, field.SchemaRef, raw); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q does not satisfy its declared schema %q: %v", field.Name, field.SchemaRef, err), false, "supply a value the declared field schema accepts")
	}
	return nil
}

// validateWorkflowPayloadItems checks every element of an array field against
// the schema its item reference names.
func validateWorkflowPayloadItems(document map[string]any, field WorkflowPayloadField, raw json.RawMessage) error {
	if !payloadschema.HasIn(document, field.ItemRef) {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q declares item schema %q, which is not generated", field.Name, field.ItemRef), false, "regenerate the payload contracts or correct the declared item schema reference")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q is not one JSON array", field.Name), false, "supply the declared action field type and bounds")
	}
	for index, item := range items {
		if err := payloadschema.ValidateInDocument(document, field.ItemRef, item); err != nil {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q item %d does not satisfy its declared schema %q: %v", field.Name, index, field.ItemRef, err), false, "supply items the declared item schema accepts")
		}
	}
	return nil
}

func workflowPayloadFieldRule(field WorkflowPayloadField) string {
	parts := []string{string(field.ValueType)}
	if field.MinLength != nil {
		parts = append(parts, fmt.Sprintf("min_length=%d", *field.MinLength))
	}
	if field.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("max_length=%d", *field.MaxLength))
	}
	if field.MinItems != nil {
		parts = append(parts, fmt.Sprintf("min_items=%d", *field.MinItems))
	}
	if field.MaxItems != nil {
		parts = append(parts, fmt.Sprintf("max_items=%d", *field.MaxItems))
	}
	if field.Minimum != nil {
		parts = append(parts, fmt.Sprintf("minimum=%d", *field.Minimum))
	}
	if field.Maximum != nil {
		parts = append(parts, fmt.Sprintf("maximum=%d", *field.Maximum))
	}
	if len(field.Enum) != 0 {
		parts = append(parts, "enum="+strings.Join(field.Enum, ","))
	}
	if field.SchemaRef != "" {
		parts = append(parts, "schema_ref="+field.SchemaRef)
	}
	if field.ItemRef != "" {
		parts = append(parts, "item_ref="+field.ItemRef)
	}
	if field.NonBlank {
		parts = append(parts, "non_blank=true")
	}
	if len(field.Forbidden) != 0 {
		parts = append(parts, "forbidden_values="+strings.Join(field.Forbidden, ","))
	}
	return strings.Join(parts, ",")
}

// validateWorkflowPayloadValue enforces the declared value type and bounds
// against the generated public authoring document.
func validateWorkflowPayloadValue(field WorkflowPayloadField, raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return false
	}
	switch field.ValueType {
	case PayloadString, PayloadRef, PayloadDigest:
		return validateWorkflowPayloadText(field, value)
	case PayloadStringList:
		return validateWorkflowPayloadStringList(field, value)
	case PayloadInteger:
		return validateWorkflowPayloadInteger(field, value)
	case PayloadBoolean:
		_, ok := value.(bool)
		return ok
	case PayloadObject:
		// CD-0067 D3: an object field must decode to exactly one strict JSON
		// object. UseNumber above preserves numeric literals, so the
		// decoded form is map[string]any (with json.Number values); any
		// other JSON type — array, scalar, null — fails closed. Structural
		// bounds beyond object-ness live in the contract schema, which
		// the generator embeds; this gate only enforces the registry's
		// declaration that the value is one object.
		_, ok := value.(map[string]any)
		return ok
	case PayloadArray:
		values, ok := value.([]any)
		if !ok {
			return false
		}
		return (field.MinItems == nil || int64(len(values)) >= *field.MinItems) && (field.MaxItems == nil || int64(len(values)) <= *field.MaxItems)
	default:
		return false
	}
}

func validateWorkflowPayloadText(field WorkflowPayloadField, value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	if field.ValueType == PayloadRef && !ValidReference(text) {
		return false
	}
	if field.ValueType == PayloadDigest && !workflowDigestPattern.MatchString(text) {
		return false
	}
	if field.NonBlank && strings.TrimSpace(text) == "" || slices.Contains(field.Forbidden, text) {
		return false
	}
	if len(field.Enum) != 0 && !slices.Contains(field.Enum, text) {
		return false
	}
	// String bounds count UTF-8 bytes, not runes: every enforcing
	// workflowString guard counts bytes, so the declared limits carry
	// the enforcing owner's unit and a schema-bound payload cannot be
	// refused by a deeper guard for length alone. JSON Schema
	// minLength/maxLength count code points — that published
	// representation limit is recorded beside the corpus tests.
	if field.MinLength != nil && int64(len(text)) < *field.MinLength || field.MaxLength != nil && int64(len(text)) > *field.MaxLength {
		return false
	}
	return true
}

func validateWorkflowPayloadStringList(field WorkflowPayloadField, value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	if field.MinItems != nil && int64(len(values)) < *field.MinItems || field.MaxItems != nil && int64(len(values)) > *field.MaxItems {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, item := range values {
		text, ok := item.(string)
		if !ok || !validWorkflowPayloadListItem(field.ItemRef, text) || len(field.Enum) != 0 && !slices.Contains(field.Enum, text) {
			return false
		}
		if _, exists := seen[text]; exists {
			return false
		}
		seen[text] = struct{}{}
	}
	return true
}

func validateWorkflowPayloadInteger(field WorkflowPayloadField, value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	integer, err := number.Int64()
	if err != nil || integer < -2147483648 || integer > 2147483647 {
		return false
	}
	if field.Minimum != nil && integer < *field.Minimum || field.Maximum != nil && integer > *field.Maximum {
		return false
	}
	return true
}

func validWorkflowPayloadListItem(itemRef, value string) bool {
	switch itemRef {
	case "law_id":
		runes := []rune(value)
		return len(runes) >= 2 && len(runes) <= 256 && !unicode.IsSpace(runes[0]) && !unicode.IsSpace(runes[len(runes)-1])
	case "proposal_affected_text":
		return validWorkflowProseItem(value, 256)
	case "proposal_text":
		return validWorkflowProseItem(value, 512)
	case "", "reference":
		return ValidReference(value)
	default:
		document := payloadschema.Document()
		if !payloadschema.HasIn(document, itemRef) {
			return false
		}
		raw, err := json.Marshal(value)
		return err == nil && payloadschema.ValidateInDocument(document, itemRef, raw) == nil
	}
}

func validWorkflowProseItem(value string, upper int) bool {
	return len([]rune(value)) >= 1 && len([]rune(value)) <= upper && strings.TrimSpace(value) != ""
}

func definitionStepAllows(definition WorkflowDefinition, stepID, actionID string) bool {
	if stepID == "" || actionID == "" {
		return false
	}
	for _, step := range definition.StepGraph.Steps {
		if step.ID == stepID {
			return slices.Contains(step.Actions, actionID)
		}
	}
	return false
}

func verifyWorkflowDefinitionPinTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	var pin WorkflowDefinitionPin
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Digest); err != nil {
		return RegisteredDefinition{}, wrapFailure(KindInvariantViolation, "workflow_preflight", "workflow definition pin is unavailable", false, "reread_entities", err)
	}
	return resolveCurrentWorkflowDefinition(registry, pin.Ref)
}

func preflightWorkflowClaimTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, req ClaimRequest) error {
	if err := refuseRemovedWorkTx(ctx, tx, req.WorkID, "workflow_preflight"); err != nil {
		return err
	}
	var workflowExists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, req.WorkID).Scan(&workflowExists); err != nil {
		return wrapFailure(KindUnavailable, "workflow_preflight", "cannot inspect workflow claim identity", true, "retry once the workflow projection is readable", err)
	}
	if workflowExists == 0 {
		return nil
	}
	entry, err := verifyWorkflowDefinitionPinTx(ctx, tx, registry, req.WorkID)
	if err != nil {
		return err
	}
	if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, req.WorkID); err != nil {
		return err
	}
	// Every registered item workflow definition lives under the workflow.*
	// namespace (validWorkflowRef). A claim naming such a ref claims a step of
	// the item's own workflow, so its identity and step must match the stored
	// pin. A claim naming anything else (the compaction publish's
	// concord.pm6.compaction) is an auxiliary operation layered over the item:
	// the item's pin already verified above, and the auxiliary claim is not a
	// step of that workflow, so the identity and current-step equalities do
	// not apply to it.
	if !validWorkflowRef(req.WorkflowTypeRef) {
		return nil
	}
	if entry.Definition.Ref != req.WorkflowTypeRef || entry.Digest != req.WorkflowTypeDigest {
		return workflowPinFailure("workflow claim identity does not match the stored definition pin")
	}
	if entry.Definition.StepGraph.StartStep == "" {
		return workflowPinFailure(fmt.Sprintf("workflow %q has no start step", req.WorkflowTypeRef))
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, req.WorkID).Scan(&currentStep); err != nil {
		return workflowPinFailure("workflow instance is unavailable")
	}
	if req.StepID != currentStep {
		return workflowPinFailure("workflow claim does not match the current definition step")
	}
	return nil
}

func preflightWorkflowOperationTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, opID string) error {
	var workID, workflowRef, workflowDigest, stepID string
	err := tx.QueryRowContext(ctx, `SELECT work_id,workflow_type_ref,workflow_type_digest,step_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&workID, &workflowRef, &workflowDigest, &stepID)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "workflow_preflight", "workflow operation is not recorded", false, "claim the workflow operation before completing it")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_preflight", "cannot read workflow operation identity", true, "retry once the database is readable", err)
	}
	if err := refuseRemovedWorkTx(ctx, tx, workID, "workflow_preflight"); err != nil {
		return err
	}
	if !strings.HasPrefix(workflowRef, "workflow.") {
		return nil
	}
	entry, err := verifyWorkflowDefinitionPinTx(ctx, tx, registry, workID)
	if err != nil {
		return err
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		return workflowPinFailure("workflow instance is unavailable")
	}
	if currentStep != stepID || entry.Digest != workflowDigest || entry.Definition.Ref != workflowRef {
		return workflowPinFailure("workflow operation does not match its pinned definition step")
	}
	return nil
}

func preflightWorkflowOperation(ctx context.Context, s *Store, opID string) error {
	var workID, workflowRef, workflowDigest string
	if err := s.db.QueryRowContext(ctx, `SELECT work_id,workflow_type_ref,workflow_type_digest FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&workID, &workflowRef, &workflowDigest); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "workflow_preflight", "workflow operation is not recorded", false, "claim the workflow operation before resuming it")
		}
		return wrapFailure(KindUnavailable, "workflow_preflight", "cannot read workflow operation identity", true, "retry once the database is readable", err)
	}
	if err := refuseRemovedWorkTx(ctx, s.db, workID, "workflow_preflight"); err != nil {
		return err
	}
	if !strings.HasPrefix(workflowRef, "workflow.") {
		return nil
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return err
	}
	if entry.Definition.Ref != workflowRef || entry.Digest != workflowDigest {
		return workflowPinFailure("workflow resume identity does not match the stored definition pin")
	}
	return nil
}
