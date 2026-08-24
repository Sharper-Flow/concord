package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// WorkflowActionExecutionRequest is the domain portion of one already
// authenticated workflow_action. The caller-owned transaction is the same
// transaction used by the action-boundary coordinator.
type WorkflowActionExecutionRequest struct {
	WorkID                string
	ExpectedVersion       int64
	ActionID              string
	SelectedChoice        string
	DecisionContextDigest string
	Payload               json.RawMessage
	EvidenceRefs          []string
	Actor                 WorkflowActor
	// OperatorActor is populated only after the signed approval for
	// confirm_premise has been verified and consumed by the agent boundary.
	// It is never decoded from workflow action payload.
	OperatorActor        *WorkflowActor
	AcceptedInputsDigest string
	IdempotencyIdentity  string
	OperationID          string
	PrincipalRef         string
	Tool                 string
	IdempotencyKey       string
	RequestID            string
	AcceptedScope        string
	LawModifies          []string
	ContractDigest       string
	Now                  time.Time
	// ResearchBindings declares the pack revisions this action's work item
	// starts relying on (CD-0025). The engine binds each consumer and proves
	// freshness fail-closed inside this action's transaction; there is no
	// standalone binding operation, because reliance declared outside the
	// boundary that consumes it is unproven reliance.
	ResearchBindings []ResearchBindingDeclaration
}

type WorkflowActionExecutionResult struct {
	OperationID      string
	EventIDs         []string
	ChangedRefs      []string
	ResultingVersion int64
	Result           json.RawMessage
	// NativeRun is the attributed report this action recorded, if any. A
	// failure-classified report makes the logical operation partial (CD-0039
	// D7/D8): the native steps are durable facts, the approved change did not
	// complete successfully, and ok is reserved for successful predicates.
	NativeRun *NativeRunReport
}

// WorkflowActionDefinitionFor returns the registered action policy after the
// instance pin has been verified. It is intentionally read-only; authorization
// and mutation remain owned by AuthorizeWorkflowActionAtBoundaryTx.
func WorkflowActionDefinitionFor(ctx context.Context, s *Store, registry DefinitionRegistry, workID, actionID string) (RegisteredDefinition, WorkflowActionDefinition, error) {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, workID)
	if err != nil {
		return RegisteredDefinition{}, WorkflowActionDefinition{}, err
	}
	if actionID == "supersede_contract" {
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow lifecycle", true, "retry once the workflow projection is readable", err)
		}
		if state == "completed" || state == "cancelled" || state == "superseded" {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "contract recovery is unavailable for terminal work", false, "start a successor workflow")
		}
		var activeCount int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect active workflow contract", true, "retry once the workflow projection is readable", err)
		}
		if activeCount != 1 {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvariantViolation, "workflow_action", "contract recovery requires exactly one active workflow contract", false, "rebuild the workflow contract projection")
		}
		if err := checkWorkflowLawRevisionStalenessDB(ctx, s.db, workID); err != nil {
			var failure *Failure
			if failureAs(err, &failure) && failure.Kind == KindStaleLawRevision {
				return entry, workflowContractRecoveryActionDefinition(), nil
			}
			return RegisteredDefinition{}, WorkflowActionDefinition{}, err
		}
		return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
	}
	for _, action := range entry.Definition.ActionDefinitions {
		if action.ID == actionID {
			return entry, action, nil
		}
	}
	return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared by the pinned definition", false, "reread_entities")
}

// ApplyWorkflowActionTx records one action's durable operation and event-folded
// result. The caller must already own fold_guard; no alternate mutation path is
// exposed. Every declared semantic action is translated to its closed event
// family here. The dispatcher is deliberately the only place where public
// action IDs acquire domain meaning.
func ApplyWorkflowActionTx(ctx context.Context, transaction *Transaction, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	tx, err := transactionSQL(transaction, "workflow_action")
	if err != nil {
		return WorkflowActionExecutionResult{}, err
	}
	return applyWorkflowActionRawTx(ctx, tx, registry, request)
}

func applyWorkflowActionRawTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	if tx == nil {
		return result, newFailure(KindInvalidOperation, "workflow_action", "transaction is not open", false, "supply an active store transaction")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, request.WorkID)
	if err != nil {
		return result, err
	}
	var currentStep, state string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,instance_state,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &state, &version); err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow state", true, "retry once the database is readable", err)
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	if request.ExpectedVersion != version {
		return result, versionConflict(SubjectWorkItem, request.WorkID, request.ExpectedVersion, version, true)
	}
	if state == "completed" || state == "cancelled" || state == "superseded" {
		return result, newFailure(KindInvalidOperation, "workflow_action", "terminal workflow instance is immutable", false, "start a successor workflow")
	}
	guards := &workflowActionGuardContext{ctx: ctx, tx: tx, request: request, entry: entry, currentStep: currentStep}
	if err := runWorkflowActionGuard(guards, guardPhaseRecovery); err != nil {
		return result, err
	} else if !guards.staleRecovery && !workflowExecutionAllowsStaleRecovery(request.ActionID, request.Payload) {
		if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, request.WorkID); err != nil {
			return result, err
		}
	}
	if err := runWorkflowActionGuard(guards, guardPhaseBoundary); err != nil {
		return result, err
	}
	if guards.staleRecovery {
		if err := validateWorkflowContractRecoveryPayload(request.Payload); err != nil {
			return result, err
		}
	} else if err := validateWorkflowActionPayload(entry.Definition, request.ActionID, request.Payload); err != nil {
		return result, err
	}
	if err := runWorkflowActionGuard(guards, guardPhasePostValidation); err != nil {
		return result, err
	}
	if err := guardWorkflowActionStepMatch(request.Payload, currentStep); err != nil {
		return result, err
	}
	if !guards.staleRecovery && !definitionStepAllows(entry.Definition, currentStep, request.ActionID) {
		return result, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared on the current step", false, "reread_entities")
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return result, err
	}
	guards.actorRef = actorRef
	guards.eventActor = actorRef
	if err := runWorkflowActionGuard(guards, guardPhaseActor); err != nil {
		return result, err
	}
	if err := guardOperatorPremiseActor(guards); err != nil {
		return result, err
	}
	if err := normalizeWorkflowActionRequest(&request); err != nil {
		return result, err
	}
	guards.request = request
	step, evidenceRefs, err := claimDurableWorkflowOperationTx(ctx, tx, entry, request, currentStep)
	if err != nil {
		return result, err
	}
	payload := guards.defaultedPayload()
	if err := runWorkflowActionGuard(guards, guardPhaseClaim); err != nil {
		return result, err
	}
	assemblyInput := workflowActionAssemblyInput{
		entry: entry, request: request, currentStep: currentStep, step: step, payload: payload, evidenceRefs: evidenceRefs,
		actorRef: guards.actorRef, eventActor: guards.eventActor, operatorRef: guards.operatorRef,
		actorNeedsRecord: guards.actorNeedsRecord, operatorNeedsRecord: guards.operatorNeedsRecord,
	}
	assembly, err := assembleWorkflowActionEventsTx(ctx, tx, assemblyInput)
	if err != nil {
		return result, err
	}
	if request.ActionID == "complete" {
		return applyCompleteWorkflowActionTx(ctx, tx, registry, entry, request, currentStep, guards.eventActor, payload)
	}
	assembly.events, err = appendGenericWorkflowCompletion(assemblyInput, assembly.attemptEpoch, assembly.events)
	if err != nil {
		return result, err
	}
	result.NativeRun = assembly.nativeRun

	if err := BindResearchRelianceTx(ctx, tx, request.WorkID, request.ResearchBindings, request.Now); err != nil {
		return result, err
	}
	operationResult, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: assembly.events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): request.ExpectedVersion}})
	if err != nil {
		return result, err
	}
	result.EventIDs = operationResult.EventIDs
	result.ChangedRefs = []string{request.WorkID}
	result.OperationID = request.OperationID
	resultVersion := request.ExpectedVersion + int64(len(assembly.events))
	result.ResultingVersion = resultVersion
	changedRef := map[string]any{"entity_kind": "work_item", "id": request.WorkID, "version": resultVersion}
	result.Result, _ = json.Marshal(map[string]any{"changed_refs": []any{changedRef}, "next_valid_intents": []any{}, "operation_id": request.OperationID})
	durableChangedRef, _ := json.Marshal(changedRef)
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET result_kind='completed',result_payload=?,changed_refs=?,completed_at=? WHERE op_id=? AND attempt_epoch=?`, string(result.Result), workflowJSON([]string{string(durableChangedRef)}), request.Now.UTC().Format(time.RFC3339Nano), request.OperationID, 1); err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_action", "cannot complete durable workflow operation", true, "retry once the database is writable", err)
	}
	return result, nil
}

func workflowCompletionBoundaryPreflight(raw json.RawMessage) error {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return err
	}
	if evidenceCommit, ok := workflowFieldString(fields, "evidence_commit"); ok {
		if currentCommit, currentOK := workflowFieldString(fields, "current_commit"); currentOK && evidenceCommit != currentCommit {
			return newFailure(KindMissingEvidence, "complete_workflow", "immutable evidence commit does not match the current commit", false, "rebind_evidence")
		}
	}
	if nestedRaw := workflowFieldRaw(fields, "payload"); len(nestedRaw) != 0 {
		var nested map[string]json.RawMessage
		if json.Unmarshal(nestedRaw, &nested) == nil {
			if evidenceCommit := workflowFieldStringDefault(nested, "evidence_commit", ""); evidenceCommit != "" && evidenceCommit != workflowFieldStringDefault(nested, "current_commit", evidenceCommit) {
				return newFailure(KindMissingEvidence, "complete_workflow", "immutable evidence commit does not match the current commit", false, "rebind_evidence")
			}
			staleness := map[string]json.RawMessage{}
			if stalenessRaw := nested["staleness"]; len(stalenessRaw) != 0 {
				_ = json.Unmarshal(stalenessRaw, &staleness)
			}
			if workflowFieldBool(staleness, "drifted") && workflowFieldStringDefault(staleness, "severity", "") == "block" {
				return newFailure(KindStaleRequiresReview, "complete_workflow", "blocking staleness drift requires review", false, "refresh_context")
			}
		}
	}
	return nil
}

// workflowSemanticActionEvents constructs only typed, foldable events. Empty
// return means the action uses the ordinary action_completed event.
func workflowSemanticActionEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, stepID, actor string, raw json.RawMessage, expected int64) ([]Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return nil, err
	}
	eventID := request.OperationID + ":semantic"
	switch request.ActionID {
	case "start_run", "record_health", "rollback_run", "cleanup_run":
		// CD-0039 D5/D6: the native-run actions carry typed phase payloads.
		// The action ID fixes the phase; callers never choose it.
		phaseByAction := map[string]string{"start_run": "start", "record_health": "health", "rollback_run": "rollback", "cleanup_run": "cleanup"}
		phase := phaseByAction[request.ActionID]
		runID, runOK := workflowFieldString(fields, "run_id")
		subjectRef, subjectOK := workflowFieldString(fields, "native_subject_ref")
		status, statusOK := workflowFieldString(fields, "status")
		evidenceRef, evidenceOK := workflowFieldString(fields, "evidence_ref")
		evidenceDigest, digestOK := workflowFieldString(fields, "evidence_digest")
		assertedAt := workflowFieldStringDefault(fields, "asserted_at", request.Now.Format(time.RFC3339Nano))
		missing := []string{}
		for name, ok := range map[string]bool{"run_id": runOK, "native_subject_ref": subjectOK, "status": statusOK, "evidence_ref": evidenceOK, "evidence_digest": digestOK} {
			if !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 || runID == "" || subjectRef == "" || status == "" || evidenceRef == "" || evidenceDigest == "" {
			return nil, newFailure(KindInvalidPayload, "workflow_action", request.ActionID+" requires typed native-run fields: run_id, native_subject_ref, status, evidence_ref, evidence_digest", false, "supply the native authority's attributed report fields")
		}
		if !nativeRunStatusVocab[phase][status] {
			return nil, newFailure(KindInvalidPayload, "workflow_action", status+" is not a "+phase+" status", false, "use the closed status vocabulary for this phase")
		}
		nativeEvent, err := buildNativeRunEvent(eventID+":native-run", request.WorkID, request.Actor, request.Now, expected, phase, runID, subjectRef, status, evidenceRef, evidenceDigest, assertedAt)
		if err != nil {
			return nil, err
		}
		return []Event{nativeEvent}, nil
	case "checkpoint_context":
		var workflowRef, workflowDigestValue string
		var workflowDefinitionVersion, attemptEpoch int64
		if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&workflowRef, &workflowDefinitionVersion, &workflowDigestValue); err != nil {
			return nil, workflowProjectionError(err, "cannot read workflow identity for context checkpoint")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, request.WorkID).Scan(&attemptEpoch); err != nil {
			return nil, workflowProjectionError(err, "cannot read workflow attempt epoch")
		}
		if attemptEpoch <= 0 {
			attemptEpoch = 1
		}
		checkpointID := workflowFieldStringDefault(fields, "checkpoint_id", request.OperationID+":context-checkpoint")
		return []Event{workflowTypedEvent(eventID, WorkflowContextCheckpointed, request.WorkID, actor, request.Now, expected, map[string]any{
			"checkpoint_id": checkpointID, "checkpoint_sequence": workflowFieldInt(fields, "checkpoint_sequence", 0), "step_id": stepID, "attempt_epoch": attemptEpoch,
			"active_unit": workflowFieldStringDefault(fields, "active_unit", ""), "hypothesis": workflowFieldStringDefault(fields, "hypothesis", ""), "diagnosis": workflowFieldStringDefault(fields, "diagnosis", ""), "strategy": workflowFieldStringDefault(fields, "strategy", ""),
			"touched_refs": workflowFieldStrings(fields, "touched_refs"), "evidence_refs": workflowFieldStrings(fields, "evidence_refs"), "pending_questions": workflowFieldStrings(fields, "pending_questions"), "pending_decisions": workflowFieldStrings(fields, "pending_decisions"),
			"workflow_ref": workflowRef, "workflow_definition_version": workflowDefinitionVersion, "workflow_definition_digest": workflowDigestValue, "actor_ref": actor, "request_id": request.RequestID,
		})}, nil
	case "cross_context_boundary":
		boundaryKind := workflowFieldStringDefault(fields, "boundary_kind", "summary")
		mode := workflowFieldStringDefault(fields, "mode", "summary")
		if boundaryKind == "restart" || mode == "restart" || fields["restart"] != nil {
			return nil, newFailure(KindUnavailable, "workflow_action", "restart dispatch is not implemented and fails closed pending Concord issue #120", false, "contact_operator")
		}
		if boundaryKind != "summary" || mode != "summary" || workflowFieldStringDefault(fields, "summary", "") == "" {
			return nil, newFailure(KindInvalidOperation, "workflow_action", "context boundary currently accepts summary only", false, "use boundary_kind=summary with a completed durable checkpoint")
		}
		var workflowRef, workflowDigestValue string
		var workflowDefinitionVersion, attemptEpoch int64
		if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&workflowRef, &workflowDefinitionVersion, &workflowDigestValue); err != nil {
			return nil, workflowProjectionError(err, "cannot read workflow identity for context boundary")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, request.WorkID).Scan(&attemptEpoch); err != nil {
			return nil, workflowProjectionError(err, "cannot read workflow attempt epoch")
		}
		checkpointID := workflowFieldStringDefault(fields, "checkpoint_id", "")
		if checkpointID == "" {
			return nil, newFailure(KindInvalidOperation, "workflow_action", "summary boundary requires checkpoint_id", false, "reference the latest durable context checkpoint")
		}
		return []Event{workflowTypedEvent(eventID, WorkflowContextBoundaryCrossed, request.WorkID, actor, request.Now, expected, map[string]any{
			"boundary_id": request.OperationID + ":context-boundary", "boundary_sequence": workflowFieldInt(fields, "boundary_sequence", 0), "boundary_kind": "summary", "checkpoint_id": checkpointID, "checkpoint_sequence": workflowFieldInt(fields, "checkpoint_sequence", 0), "summary": workflowFieldStringDefault(fields, "summary", ""), "workflow_ref": workflowRef, "workflow_definition_version": workflowDefinitionVersion, "workflow_definition_digest": workflowDigestValue, "attempt_epoch": attemptEpoch, "actor_ref": actor, "request_id": request.RequestID,
		})}, nil
	case "approve_contract":
		if rawOutcome, present := fields["outcome"]; present && string(rawOutcome) == "null" {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "planning requires an explicit outcome predicate", false, "supply the approved end-state predicate")
		}
		if rawPayload := workflowFieldRaw(fields, "payload"); len(rawPayload) != 0 {
			var payloadFields map[string]json.RawMessage
			if json.Unmarshal(rawPayload, &payloadFields) == nil {
				if nestedOutcome, present := payloadFields["outcome"]; present && string(nestedOutcome) == "null" {
					return nil, newFailure(KindInvariantViolation, "workflow_action", "planning requires an explicit outcome predicate", false, "supply the approved end-state predicate")
				}
			}
		}
		if route, ok := workflowFieldString(fields, "route_convention"); ok && route != "workflow_action" {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "route convention is not declared by the workflow action boundary", false, "use a declared route convention")
		}
		declaredRoutes := workflowFieldStrings(fields, "route_conventions")
		proposedRoutes := workflowFieldStrings(fields, "proposed_route_conventions")
		for _, proposed := range proposedRoutes {
			if !contains(declaredRoutes, proposed) {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "proposed route convention is not declared by the contract", false, "use a declared route convention")
			}
		}
		for _, required := range workflowFieldStrings(fields, "required_route_conventions") {
			if !contains(declaredRoutes, required) {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "required route convention is not declared by the contract", false, "declare every required route convention")
			}
		}
		outcomeKindForVacuity := workflowFieldStringDefault(fields, "outcome_kind", "")
		if outcome := workflowFieldRaw(fields, "outcome"); len(outcome) != 0 && string(outcome) != "null" {
			var predicate map[string]any
			if json.Unmarshal(outcome, &predicate) == nil {
				outcomeKindForVacuity = workflowFieldStringDefaultMap(predicate, "kind", outcomeKindForVacuity)
			}
		}
		if payload := workflowFieldRaw(fields, "payload"); len(payload) != 0 {
			var groundTruth map[string]any
			if json.Unmarshal(payload, &groundTruth) == nil && strings.HasSuffix(workflowFieldStringDefaultMap(groundTruth, "ground_truth", ""), "-present") && outcomeKindForVacuity == "exists" {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "approved end-state is already satisfied", false, "supply a non-vacuous required end state")
			}
		}
		contractVersion := workflowFieldInt(fields, "contract_version", 1)
		premise := workflowFieldStringDefault(fields, "premise", "workflow premise")
		outcomeKind := workflowFieldStringDefault(fields, "outcome_kind", string(definition.OutcomeSchema.DefaultKind))
		outcome := workflowFieldRaw(fields, "outcome_payload")
		if len(outcome) == 0 {
			outcome = defaultWorkflowOutcome(definition, fields)
		}
		required := workflowFieldStrings(fields, "required_evidence")
		if len(required) == 0 {
			for _, kind := range definition.RequiredEvidenceKinds {
				required = append(required, string(kind))
			}
		}
		routes := workflowFieldStrings(fields, "route_conventions")
		if routes == nil {
			routes = []string{}
		}
		spec := workflowFieldStrings(fields, "spec_mandate")
		if spec == nil {
			spec = []string{}
		}
		lawModifies := workflowFieldStrings(fields, "law_modifies")
		if len(lawModifies) == 0 && len(request.LawModifies) != 0 {
			lawModifies = append([]string(nil), request.LawModifies...)
		}
		rigor := workflowFieldStringDefault(fields, "rigor_class", "prototype_internal")
		contract := map[string]any{"contract_version": contractVersion, "premise": premise, "outcome_kind": outcomeKind, "outcome_payload": json.RawMessage(outcome), "required_evidence": required, "route_conventions": routes, "spec_mandate": spec, "law_modifies": lawModifies, "rigor_class": rigor, "consequence_class": string(ActionInternalSQLite)}
		productChanging := definition.ChangesProductTruth != nil && *definition.ChangesProductTruth
		bindingRaw, bindingPresent := fields["architecture_binding"]
		binding, bindingErr := parseWorkflowArchitectureBinding(bindingRaw)
		if bindingErr != nil {
			return nil, bindingErr
		}
		if productChanging {
			if !bindingPresent || binding == nil {
				return nil, newFailure(KindInvalidPayload, "workflow_action", "Product-changing approval requires architecture_binding", false, "supply the complete architecture binding")
			}
			contract["architecture_binding"] = binding
		} else if bindingPresent && string(bindingRaw) != "null" {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing approval cannot carry architecture_binding", false, "select a registered Product-changing workflow")
		}
		if !productChanging && len(lawModifies) != 0 {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing approval cannot modify Product law", false, "leave law_modifies empty")
		}
		if !productChanging && definition.Version >= 4 {
			delete(contract, "law_modifies")
		}
		revisionMandate := spec
		var revisionErr error
		if productChanging {
			revisionMandate, revisionErr = architectureBindingRevisionMandate(spec, binding.LawAdditions)
		}
		if revisionErr != nil {
			return nil, revisionErr
		}
		revisions, revisionErr := deriveWorkflowLawRevisionsTx(ctx, tx, request.WorkID, revisionMandate, lawModifies)
		if revisionErr != nil {
			return nil, revisionErr
		}
		contract["law_revisions"] = revisions
		contract["law_boundary_version"] = 1
		return []Event{workflowTypedEvent(eventID, WorkflowContractApproved, request.WorkID, actor, request.Now, expected, contract)}, nil
	case "revise_candidates":
		added := workflowFieldStrings(fields, "added")
		if len(added) == 0 {
			added = workflowFieldStrings(fields, "candidate_ids")
		}
		removed := workflowFieldStrings(fields, "removed")
		if len(added) == 0 && len(removed) == 0 {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "candidate revision requires an addition or removal", false, "supply disjoint candidate IDs")
		}
		return []Event{workflowTypedEvent(eventID, WorkflowCandidateSetRevised, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": workflowFieldInt(fields, "contract_version", 1), "candidate_kind": workflowFieldStringDefault(fields, "candidate_kind", "work_item"), "candidate_ref": workflowFieldStringDefault(fields, "candidate_ref", request.WorkID), "added": added, "removed": removed})}, nil
	case "supersede_contract":
		if err := validateWorkflowContractRecoveryPayload(raw); err != nil {
			return nil, err
		}
		var activeCount, previous int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, request.WorkID).Scan(&activeCount); err != nil {
			return nil, workflowProjectionError(err, "cannot inspect active workflow contract")
		}
		if activeCount != 1 {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "stale-law recovery requires exactly one active workflow contract", false, "rebuild the workflow contract projection")
		}
		if err := tx.QueryRowContext(ctx, `SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, request.WorkID).Scan(&previous); err != nil {
			return nil, workflowProjectionError(err, "cannot read active workflow contract")
		}
		next := workflowFieldInt(fields, "contract_version", 0)
		if next != previous+1 {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "successor contract version must immediately follow the active contract", false, "supply the next contract version")
		}
		audit := workflowFieldStrings(fields, "audit_evidence")
		if len(audit) == 0 {
			audit = []string{"audit:" + request.OperationID}
		}
		lawModifies := workflowFieldStrings(fields, "law_modifies")
		specMandate := workflowFieldStrings(fields, "spec_mandate")
		productChanging := definition.ChangesProductTruth != nil && *definition.ChangesProductTruth
		bindingRaw, bindingPresent := fields["architecture_binding"]
		binding, bindingErr := parseWorkflowArchitectureBinding(bindingRaw)
		if bindingErr != nil {
			return nil, bindingErr
		}
		if productChanging {
			if !bindingPresent || binding == nil {
				return nil, newFailure(KindInvalidPayload, "workflow_action", "Product-changing successor requires architecture_binding", false, "supply the complete successor architecture binding")
			}
		} else if bindingPresent && string(bindingRaw) != "null" {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing successor cannot carry architecture_binding", false, "select a registered Product-changing workflow")
		}
		if !productChanging && len(lawModifies) != 0 {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing successor cannot modify Product law", false, "leave law_modifies empty")
		}
		revisionMandate := specMandate
		var revisionErr error
		if productChanging {
			revisionMandate, revisionErr = architectureBindingRevisionMandate(specMandate, binding.LawAdditions)
			if revisionErr != nil {
				return nil, revisionErr
			}
		}
		revisions, revisionErr := deriveWorkflowLawRevisionsTx(ctx, tx, request.WorkID, revisionMandate, lawModifies)
		if revisionErr != nil {
			return nil, revisionErr
		}
		successor := map[string]any{
			"contract_version": next, "premise": workflowFieldStringDefault(fields, "premise", ""), "outcome_kind": workflowFieldStringDefault(fields, "outcome_kind", ""),
			"outcome_payload": workflowFieldRaw(fields, "outcome_payload"), "required_evidence": workflowFieldStrings(fields, "required_evidence"),
			"route_conventions": workflowFieldStrings(fields, "route_conventions"), "spec_mandate": specMandate, "law_modifies": lawModifies,
			"law_revisions": revisions, "law_boundary_version": 1, "rigor_class": workflowFieldStringDefault(fields, "rigor_class", ""), "consequence_class": string(ActionInternalSQLite),
		}
		if productChanging {
			successor["architecture_binding"] = binding
		} else if definition.Version >= 4 {
			delete(successor, "law_modifies")
		}
		return []Event{workflowTypedEvent(eventID, WorkflowContractSuperseded, request.WorkID, actor, request.Now, expected, map[string]any{"previous_contract_version": previous, "new_contract_version": next, "supersede_reason": workflowFieldStringDefault(fields, "supersede_reason", "contract revision"), "audit_evidence": audit, "successor_contract": successor})}, nil
	case "bind_evidence", "record_research", "record_report", "accept_decision", "approve_operation":
		evidenceRef := "evidence:" + request.OperationID
		if len(request.EvidenceRefs) != 0 {
			evidenceRef = request.EvidenceRefs[0]
		}
		return []Event{workflowTypedEvent(eventID, WorkflowEvidenceBound, request.WorkID, actor, request.Now, expected, map[string]any{"evidence_kind": workflowFieldStringDefault(fields, "evidence_kind", "verification"), "immutable_subject_ref": workflowFieldStringDefault(fields, "immutable_subject_ref", evidenceRef), "producer_id": workflowFieldStringDefault(fields, "producer_id", request.PrincipalRef), "producer_run_ref": workflowFieldStringDefault(fields, "producer_run_ref", request.OperationID), "producer_watermark": workflowFieldStringDefault(fields, "producer_watermark", request.RequestID), "observed_at": request.Now.UTC().Format(time.RFC3339Nano)})}, nil
	case "record_verdict":
		verdictActor, actorErr := workflowAuthenticatedActorField(fields, "verdict_actor_ref", actor)
		if actorErr != nil {
			return nil, actorErr
		}
		var executingActor string
		if err := tx.QueryRowContext(ctx, `SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&executingActor); err == nil && executingActor != "" && executingActor == verdictActor {
			return nil, newFailure(KindUnauthorized, "workflow_action", "executing actor cannot evaluate its own delivery", false, "contact_operator")
		}
		evidence := workflowFieldStrings(fields, "evaluation_evidence")
		if len(evidence) == 0 {
			evidence = []string{"evidence:" + request.OperationID}
		}
		return []Event{workflowTypedEvent(eventID, WorkflowVerdictRecorded, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": workflowFieldInt(fields, "contract_version", 1), "predicate_id": workflowFieldStringDefault(fields, "predicate_id", "predicate:"+request.OperationID), "verdict_kind": workflowFieldStringDefault(fields, "verdict_kind", "ok"), "verdict_actor_ref": verdictActor, "evaluation_evidence": evidence, "incomparable_with_approved": workflowFieldBool(fields, "incomparable_with_approved")})}, nil
	case "confirm_premise":
		if request.OperatorActor == nil || request.OperatorActor.ActorClass != ActorOperator {
			return nil, newFailure(KindApprovalRequired, "workflow_action", "premise confirmation requires the verified operator approval identity", false, "request_approval")
		}
		operatorRef, actorErr := WorkflowActorRef(*request.OperatorActor)
		if actorErr != nil {
			return nil, actorErr
		}
		contractVersion := workflowFieldInt(fields, "contract_version", 0)
		if contractVersion == 0 {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(contract_version),1) FROM workflow_contracts WHERE work_id=?`, request.WorkID).Scan(&contractVersion); err != nil {
				return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the approved workflow contract", true, "retry once the workflow contract is readable", err)
			}
		}
		return []Event{workflowTypedEvent(eventID, WorkflowPremiseConfirmed, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": contractVersion, "confirming_actor_ref": operatorRef})}, nil
	case "link_successor":
		relation := workflowFieldStringDefault(fields, "relation", "forward_link")
		relationData := map[string]json.RawMessage{}
		if rawRelation := workflowFieldRaw(fields, "relation_data"); len(rawRelation) != 0 {
			if err := json.Unmarshal(rawRelation, &relationData); err != nil {
				return nil, newFailure(KindInvalidRelation, "workflow_action", "relation_data is not a JSON object", false, "supply the typed forward-link relation")
			}
		}
		if len(relationData) != 0 && workflowFieldStringDefault(relationData, "kind", "") == "forward_link" {
			relation = "forward_link"
		}
		if relation == "nested" || relation != "forward_link" {
			return nil, newFailure(KindInvalidRelation, "workflow_action", "nested or non-forward workflow composition is forbidden", false, "use relation=forward_link")
		}
		successorID := workflowFieldStringDefault(fields, "successor_work_id", "")
		if successorID == "" {
			return nil, newFailure(KindInvalidRelation, "workflow_action", "successor_work_id is required for a forward link", false, "supply the typed successor work item")
		}
		var successorKind, definitionRef string
		if err := tx.QueryRowContext(ctx, `SELECT w.kind,COALESCE(i.definition_ref,'') FROM work_items w LEFT JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, successorID).Scan(&successorKind, &definitionRef); err != nil {
			if err == sql.ErrNoRows {
				return nil, newFailure(KindInvalidRelation, "workflow_action", "successor work item is not recorded", false, "create the typed successor before linking it")
			}
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read successor work item", true, "retry once the database is readable", err)
		}
		var sourceDefinitionRef string
		if err := tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&sourceDefinitionRef); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read source workflow definition", true, "retry once the database is readable", err)
		}
		source, sourceErr := BuiltinWorkflowDefinitionForRef(sourceDefinitionRef)
		if sourceErr != nil {
			return nil, sourceErr
		}
		if !containsWorkKind(source.Definition.CompositionRules.AllowedSuccessorWorkKinds, WorkKind(successorKind)) {
			return nil, newFailure(KindInvalidRelation, "workflow_action", "successor family is not allowed by the source workflow composition", false, "use an allowed forward-linked successor family")
		}
		return []Event{workflowTypedEvent(eventID, WorkflowSuccessorLinked, request.WorkID, actor, request.Now, expected, map[string]any{"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": successorKind, "definition_ref": definitionRef})}, nil
	case "declare_impact":
		return []Event{workflowTypedEvent(eventID, WorkflowImpactDeclared, request.WorkID, actor, request.Now, expected, map[string]any{"edge_id": workflowFieldStringDefault(fields, "edge_id", "edge:"+request.OperationID), "edge_kind": workflowFieldStringDefault(fields, "edge_kind", "modifies"), "edge_class": workflowFieldStringDefault(fields, "edge_class", "hard"), "target_work_id": workflowFieldStringDefault(fields, "target_work_id", request.WorkID+"-target"), "target_kind": "work_item", "severity": workflowFieldStringDefault(fields, "severity", "non-breaking")})}, nil
	case "add_condition":
		values := map[string]any{"condition_id": workflowFieldStringDefault(fields, "condition_id", "condition:"+request.OperationID), "await_type": workflowFieldStringDefault(fields, "await_type", "timer"), "await_ref": workflowFieldStringDefault(fields, "await_ref", "await:"+request.OperationID), "resolution_authority": workflowFieldStringDefault(fields, "resolution_authority", "durable_operation:"+request.OperationID)}
		// Issue #87: a step delegating completion to an external actor may
		// declare how long the wait is expected to take; exceeding it reads
		// as overdue. Omitted or zero means no declared expectation.
		if bound, ok := workflowFieldIntOK(fields, "expected_within_seconds"); ok {
			values["expected_within_seconds"] = bound
		}
		return []Event{workflowTypedEvent(eventID, WorkflowConditionAdded, request.WorkID, actor, request.Now, expected, values)}, nil
	case "resolve_condition":
		return []Event{workflowTypedEvent(eventID, WorkflowConditionResolved, request.WorkID, actor, request.Now, expected, map[string]any{"condition_id": workflowFieldStringDefault(fields, "condition_id", "condition:"+request.OperationID), "resolution_evidence": workflowFieldStringsDefault(fields, "resolution_evidence", []string{"evidence:" + request.OperationID}), "resolved_by_event": workflowFieldStringDefault(fields, "resolved_by_event", eventID)})}, nil
	case "cancel_condition":
		return []Event{workflowTypedEvent(eventID, WorkflowConditionCancelled, request.WorkID, actor, request.Now, expected, map[string]any{"condition_id": workflowFieldStringDefault(fields, "condition_id", "condition:"+request.OperationID), "cancellation_authority": workflowFieldStringDefault(fields, "cancellation_authority", actor), "cancellation_evidence": workflowFieldStringsDefault(fields, "cancellation_evidence", []string{"evidence:" + request.OperationID}), "cancelled_by_event": workflowFieldStringDefault(fields, "cancelled_by_event", eventID)})}, nil
	default:
		return nil, nil
	}
}

func workflowCompletionEvent(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, definition WorkflowDefinition, stepID, actor string, raw json.RawMessage) (Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return Event{}, err
	}
	if evidenceCommit, ok := workflowFieldString(fields, "evidence_commit"); ok {
		if currentCommit, currentOK := workflowFieldString(fields, "current_commit"); currentOK && evidenceCommit != currentCommit {
			return Event{}, newFailure(KindMissingEvidence, "complete_workflow", "immutable evidence commit does not match the current commit", false, "rebind_evidence")
		}
	}
	if payloadRaw := workflowFieldRaw(fields, "payload"); len(payloadRaw) != 0 {
		var payloadFields map[string]json.RawMessage
		if json.Unmarshal(payloadRaw, &payloadFields) == nil {
			if evidenceCommit := workflowFieldStringDefault(payloadFields, "evidence_commit", ""); evidenceCommit != "" && evidenceCommit != workflowFieldStringDefault(payloadFields, "current_commit", evidenceCommit) {
				return Event{}, newFailure(KindMissingEvidence, "complete_workflow", "immutable evidence commit does not match the current commit", false, "rebind_evidence")
			}
			severity := workflowFieldStringDefault(payloadFields, "staleness_severity", "")
			drifted := workflowFieldBool(payloadFields, "staleness_drifted")
			if nested, present := payloadFields["staleness"]; present {
				var staleness map[string]json.RawMessage
				if json.Unmarshal(nested, &staleness) == nil {
					severity = workflowFieldStringDefault(staleness, "severity", severity)
					drifted = workflowFieldBool(staleness, "drifted")
				}
			}
			if drifted && severity == "block" {
				return Event{}, newFailure(KindStaleRequiresReview, "complete_workflow", "blocking staleness drift requires review", false, "refresh_context")
			}
		}
	}
	verdictActor := actor
	if verdict, verdictErr := latestWorkflowVerdict(ctx, tx, request.WorkID); verdictErr != nil {
		return Event{}, verdictErr
	} else if verdict != nil {
		verdictActor = verdict.VerdictActorRef
	}
	verdictActor, err = workflowAuthenticatedActorField(fields, "verdict_actor_ref", verdictActor)
	if err != nil {
		return Event{}, err
	}
	impactVerdict := workflowFieldStringDefault(fields, "impact_verdict", "")
	if impactVerdict == "" {
		if payloadFields, ok := fields["payload"]; ok {
			var nested map[string]json.RawMessage
			if json.Unmarshal(payloadFields, &nested) == nil {
				impactVerdict = workflowFieldStringDefault(nested, "impact_verdict", "")
			}
		}
	}
	if impactVerdict != "breaking" && impactVerdict != "non-breaking" {
		return Event{}, newFailure(KindInvalidPayload, "complete_workflow", "completion requires impact_verdict breaking or non-breaking", false, "supply the delivered change impact verdict")
	}
	payload := map[string]any{"terminal_state": "completed", "final_verdict_kind": workflowFieldStringDefault(fields, "final_verdict_kind", "ok"), "verdict_actor_ref": verdictActor, "premise_confirmed": workflowFieldBool(fields, "premise_confirmed"), "evidence_count": int64(0), "changed_refs_digest": WorkflowChangedRefsDigest([]string{request.WorkID}), "impact_verdict": impactVerdict}
	if payloadFields, ok := fields["payload"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(payloadFields, &nested) == nil {
			if severity := workflowFieldStringDefault(nested, "staleness_severity", ""); severity == "warning" && workflowFieldBool(nested, "staleness_drifted") {
				payload["warnings"] = []string{"rule:workflow-staleness"}
			}
			if stalenessRaw := nested["staleness"]; len(stalenessRaw) != 0 {
				var staleness map[string]json.RawMessage
				if json.Unmarshal(stalenessRaw, &staleness) == nil && workflowFieldStringDefault(staleness, "severity", "") == "warning" && workflowFieldBool(staleness, "drifted") {
					payload["warnings"] = []string{"rule:workflow-staleness"}
				}
			}
		}
	}
	return workflowTypedEvent(request.OperationID+":completed", WorkflowCompleted, request.WorkID, actor, request.Now, request.ExpectedVersion, payload), nil
}

func workflowTypedEvent(id, kind, workID, actor string, now time.Time, expected int64, values map[string]any) Event {
	values["work_id"] = workID
	values["expected_version"] = expected
	values["resulting_version"] = expected + 1
	raw, _ := json.Marshal(values)
	payloadVersion := 1
	if registration, ok := registeredEventKind(kind); ok {
		payloadVersion = registration.CurrentVersion
	}
	return Event{EventID: id, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: now.UTC(), PayloadVersion: payloadVersion, Payload: raw}
}

func workflowActionObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "workflow action payload must be a JSON object", false, "supply the registered action payload")
	}
	return fields, nil
}

func workflowFieldRaw(fields map[string]json.RawMessage, name string) json.RawMessage {
	return fields[name]
}
func workflowFieldString(fields map[string]json.RawMessage, name string) (string, bool) {
	var value string
	ok := fields[name] != nil && json.Unmarshal(fields[name], &value) == nil
	return value, ok
}

func workflowAuthenticatedActorField(fields map[string]json.RawMessage, name, actor string) (string, error) {
	if raw := fields[name]; len(raw) != 0 {
		value, ok := workflowFieldString(fields, name)
		if !ok || strings.TrimSpace(value) != actor {
			return "", newFailure(KindUnauthorized, "workflow_action", name+" must match the authenticated invocation actor", false, "use the authenticated workflow actor")
		}
	}
	return actor, nil
}
func workflowFieldStringDefault(fields map[string]json.RawMessage, name, fallback string) string {
	if value, ok := workflowFieldString(fields, name); ok && value != "" {
		return value
	}
	return fallback
}
func workflowFieldInt(fields map[string]json.RawMessage, name string, fallback int64) int64 {
	var value int64
	if fields[name] != nil && json.Unmarshal(fields[name], &value) == nil && value > 0 {
		return value
	}
	return fallback
}
func workflowFieldIntOK(fields map[string]json.RawMessage, name string) (int64, bool) {
	var value int64
	if fields[name] != nil && json.Unmarshal(fields[name], &value) == nil && value > 0 {
		return value, true
	}
	return 0, false
}
func workflowFieldBool(fields map[string]json.RawMessage, name string) bool {
	var value bool
	_ = json.Unmarshal(fields[name], &value)
	return value
}
func workflowFieldStrings(fields map[string]json.RawMessage, name string) []string {
	var values []string
	if fields[name] != nil {
		_ = json.Unmarshal(fields[name], &values)
	}
	return values
}
func workflowFieldStringsDefault(fields map[string]json.RawMessage, name string, fallback []string) []string {
	if values := workflowFieldStrings(fields, name); len(values) != 0 {
		return values
	}
	return fallback
}

func workflowFieldStringDefaultMap(fields map[string]any, name, fallback string) string {
	if value, ok := fields[name].(string); ok && value != "" {
		return value
	}
	return fallback
}
func defaultWorkflowOutcome(definition WorkflowDefinition, fields map[string]json.RawMessage) json.RawMessage {
	if raw := workflowFieldRaw(fields, "outcome"); len(raw) != 0 && string(raw) != "null" {
		return raw
	}
	if definition.OutcomeSchema.DefaultKind == PredicateOutcome {
		return json.RawMessage(`{"kind":"outcome","allowed":["completed"]}`)
	}
	return json.RawMessage(`{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_result":"pass"}`)
}

func workflowStep(definition WorkflowDefinition, id string) *WorkflowStep {
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].ID == id {
			return &definition.StepGraph.Steps[i]
		}
	}
	return nil
}

func nullableWorkflowText(value string) any {
	if value == "" {
		return "{}"
	}
	return value
}
