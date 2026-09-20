package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// The per-ActionID event constructors behind workflowSemanticActionEvents.
// Each holds one action's own validation and event assembly; the dispatch in
// workflow_dispatch.go keeps the case labels so the declared EventShape stays
// structurally pinned to this surface (workflow_event_shape_test.go). The
// design validation and correction assembly they call live in
// workflow_design.go.

// workflowContractProductGuard enforces the Product-truth boundary the
// approve_contract and supersede_contract arms share: the architecture
// binding must parse, a Product-changing contract requires one, and a
// non-Product-changing contract carries none and modifies no Product law.
// role names the contract side in the refusal messages; completeRecovery is
// the Product-changing refusal's recovery hint. The parsed binding returns
// for the caller's payload assembly.
func workflowContractProductGuard(fields map[string]json.RawMessage, lawModifies []string, productChanging bool, role, completeRecovery string) (*WorkflowArchitectureBinding, error) {
	bindingRaw, bindingPresent := fields["architecture_binding"]
	binding, err := parseWorkflowArchitectureBinding(bindingRaw)
	if err != nil {
		return nil, err
	}
	if productChanging {
		if !bindingPresent || binding == nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "Product-changing "+role+" requires architecture_binding", false, completeRecovery)
		}
		return binding, nil
	}
	if bindingPresent && string(bindingRaw) != "null" {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing "+role+" cannot carry architecture_binding", false, "select a registered Product-changing workflow")
	}
	if len(lawModifies) != 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing "+role+" cannot modify Product law", false, "leave law_modifies empty")
	}
	return binding, nil
}

func workflowContextCheckpointEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, stepID, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
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
}

func workflowContextBoundaryEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
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
}

func workflowDesignRecordEvents(definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if definition.Version < 6 {
		return nil, nil
	}
	return workflowDesignRecordedEvents(request, actor, raw, eventID, expected)
}

func workflowApproveContractEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if err := ensureInitialWorkflowContractApproval(ctx, tx, request.WorkID, fields); err != nil {
		return nil, err
	}
	if err := requireResearchForPendingQuestions(ctx, tx, request.WorkID); err != nil {
		return nil, err
	}
	outcomePredicates, err := workflowContractOutcomePredicates(definition, fields)
	if err != nil {
		return nil, err
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
	contractVersion := workflowFieldInt(fields, "contract_version", 1)
	premise, _ := workflowFieldString(fields, "premise")
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
	contract := map[string]any{"contract_version": contractVersion, "premise": premise, "outcome_predicates": outcomePredicates, "outcome_kind": outcomeKind, "outcome_payload": outcome, "required_evidence": required, "route_conventions": routes, "spec_mandate": spec, "law_modifies": lawModifies, "rigor_class": rigor, "consequence_class": string(ActionInternalSQLite)}
	productChanging := definition.ChangesProductTruth != nil && *definition.ChangesProductTruth
	binding, bindingErr := workflowContractProductGuard(fields, lawModifies, productChanging, "approval", "supply the complete architecture binding")
	if bindingErr != nil {
		return nil, bindingErr
	}
	if productChanging {
		contract["architecture_binding"] = binding
	}
	if !productChanging {
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
	approvalEvent := workflowTypedEvent(eventID, WorkflowContractApproved, request.WorkID, actor, request.Now, expected, contract)
	approvalEvent.PayloadVersion = 4
	return []Event{approvalEvent}, nil
}

func workflowReviseCandidatesEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	added := workflowFieldStrings(fields, "added")
	if len(added) == 0 {
		added = workflowFieldStrings(fields, "candidate_ids")
	}
	removed := workflowFieldStrings(fields, "removed")
	if len(added) == 0 && len(removed) == 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "candidate revision requires an addition or removal", false, "supply disjoint candidate IDs")
	}
	return []Event{workflowTypedEvent(eventID, WorkflowCandidateSetRevised, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": workflowFieldInt(fields, "contract_version", 1), "candidate_kind": workflowFieldStringDefault(fields, "candidate_kind", "work_item"), "candidate_ref": workflowFieldStringDefault(fields, "candidate_ref", request.WorkID), "added": added, "removed": removed})}, nil
}

// workflowRecordAlignmentEvents builds the typed CD-0156 alignment event. The
// action records the candidate set only: it creates no relation, so the
// related ids travel as recorded search output, and relation creation stays
// with concord_work_relate.link and resolve_overlap. Each related id names a
// real work item, refused here at the boundary rather than at the fold's
// foreign key, matching the declare_impact target rule (issue #823).
func workflowRecordAlignmentEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	outcome, ok := workflowFieldString(fields, "outcome")
	if !ok || (outcome != "related_found" && outcome != "none_found") {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_alignment requires outcome related_found or none_found", false, "record the closed search outcome")
	}
	relatedIDs := workflowFieldStrings(fields, "related_ids")
	if outcome == "related_found" && len(relatedIDs) == 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_alignment outcome related_found requires related_ids", false, "name the related work items the search found")
	}
	if outcome == "none_found" && len(relatedIDs) != 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_alignment outcome none_found cannot carry related_ids", false, "drop related_ids or record outcome related_found")
	}
	for _, relatedID := range relatedIDs {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, relatedID).Scan(&exists)
		if err == sql.ErrNoRows {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_alignment related id "+workflowRefExcerpt(relatedID)+" does not name a work item", false, "supply a real related work item id")
		} else if err != nil {
			return nil, workflowProjectionError(err, "cannot read the alignment related work item")
		}
	}
	values := map[string]any{"searched": workflowFieldStringDefault(fields, "searched", ""), "outcome": outcome}
	if len(relatedIDs) != 0 {
		values["related_ids"] = relatedIDs
	}
	return []Event{workflowTypedEvent(eventID, WorkflowBacklogAlignmentRecorded, request.WorkID, actor, request.Now, expected, values)}, nil
}

func workflowSupersedeContractEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if err := validateWorkflowContractRecoveryPayload(raw); err != nil {
		return nil, err
	}
	predecessors, previous, predecessorErr := resolveWorkflowContractPredecessors(ctx, tx, request.WorkID, fields)
	if predecessorErr != nil {
		return nil, predecessorErr
	}
	next := workflowFieldInt(fields, "contract_version", 0)
	if next != maxInt64(predecessors)+1 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "successor contract version must immediately follow the active contract", false, "supply the next contract version")
	}
	audit := workflowFieldStrings(fields, "audit_evidence")
	if len(audit) == 0 {
		audit = []string{"audit:" + request.OperationID}
	}
	lawModifies := workflowFieldStrings(fields, "law_modifies")
	specMandate := workflowFieldStrings(fields, "spec_mandate")
	productChanging := definition.ChangesProductTruth != nil && *definition.ChangesProductTruth
	binding, bindingErr := workflowContractProductGuard(fields, lawModifies, productChanging, "successor", "supply the complete successor architecture binding")
	if bindingErr != nil {
		return nil, bindingErr
	}
	selfRepairRaw, selfRepairPresent := fields["self_repair"]
	selfRepair, selfRepairErr := parseWorkflowSelfRepair(selfRepairRaw)
	if selfRepairErr != nil {
		return nil, selfRepairErr
	}
	if selfRepairPresent && selfRepair != nil && !productChanging {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing successor cannot carry self_repair", false, "classify only Product-changing Concord defect repair")
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
	premise, _ := workflowFieldString(fields, "premise")
	successor := map[string]any{
		"contract_version": next, "premise": premise, "outcome_predicates": workflowFieldRaw(fields, "outcome_predicates"), "outcome_kind": workflowFieldStringDefault(fields, "outcome_kind", ""),
		"outcome_payload": workflowFieldRaw(fields, "outcome_payload"), "required_evidence": workflowFieldStrings(fields, "required_evidence"),
		"route_conventions": workflowFieldStrings(fields, "route_conventions"), "spec_mandate": specMandate, "law_modifies": lawModifies,
		"law_revisions": revisions, "law_boundary_version": 1, "rigor_class": workflowFieldStringDefault(fields, "rigor_class", ""), "consequence_class": string(ActionInternalSQLite),
	}
	if productChanging {
		successor["architecture_binding"] = binding
	} else {
		delete(successor, "law_modifies")
	}
	if selfRepairPresent && selfRepair != nil {
		successor["self_repair"] = selfRepair
	}
	events := []Event{workflowTypedEvent(eventID, WorkflowContractSuperseded, request.WorkID, actor, request.Now, expected, map[string]any{"previous_contract_version": previous, "predecessor_contract_versions": predecessors, "new_contract_version": next, "supersede_reason": workflowFieldStringDefault(fields, "supersede_reason", "contract revision"), "audit_evidence": audit, "approval_ref": request.OperatorApprovalRef, "approval_operation_digest": request.ApprovalOperationDigest, "approval_scope_json": request.ApprovalScopeJSON, "approval_versions_json": request.ApprovalVersionsJSON, "approval_consequence": request.ApprovalConsequence, "successor_contract": successor})}
	return appendWorkflowDesignCorrection(ctx, tx, definition, request, actor, fields["design_record"], eventID, expected, events)
}

func workflowAcceptWorkerResultEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	// Acceptance binds the attempt it certifies (#865). The evidence kind
	// is the lane's capability class, so a verify lane's report is
	// verification evidence and the completion gate's requirement is met
	// by the lane that verified. The verdict on the next step cites the
	// attempt id.
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	// The fold owns every refusal about the attempt itself: missing,
	// foreign, incomplete, or without readback. This arm reads only the
	// capability class and lets an absent row fall through to the fold.
	var capability string
	if err := tx.QueryRowContext(ctx, `SELECT capability_class FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&capability); err != nil && err != sql.ErrNoRows {
		return nil, workflowProjectionError(err, "cannot read the accepted worker attempt")
	}
	return []Event{workflowTypedEvent(eventID+":evidence", WorkflowEvidenceBound, request.WorkID, actor, request.Now, expected, map[string]any{
		"evidence_kind": workerAttemptEvidenceKind(capability), "immutable_subject_ref": attemptID,
		"producer_id": request.PrincipalRef, "producer_run_ref": request.OperationID, "producer_watermark": request.RequestID,
		"observed_at": request.Now.UTC().Format(time.RFC3339Nano),
	})}, nil
}

func workflowEvidenceBindingEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	evidenceRef := "evidence:" + request.OperationID
	if len(request.EvidenceRefs) != 0 {
		evidenceRef = request.EvidenceRefs[0]
	}
	// fields.evidence_ref is the declared route for naming the immutable
	// subject; the top-level evidence array remains the alternative.
	// Without this the declared key is accepted and silently discarded.
	if declared, ok := workflowFieldString(fields, "evidence_ref"); ok && declared != "" {
		evidenceRef = declared
	}
	return []Event{workflowTypedEvent(eventID, WorkflowEvidenceBound, request.WorkID, actor, request.Now, expected, map[string]any{"evidence_kind": workflowFieldStringDefault(fields, "evidence_kind", "verification"), "immutable_subject_ref": workflowFieldStringDefault(fields, "immutable_subject_ref", evidenceRef), "producer_id": workflowFieldStringDefault(fields, "producer_id", request.PrincipalRef), "producer_run_ref": workflowFieldStringDefault(fields, "producer_run_ref", request.OperationID), "producer_watermark": workflowFieldStringDefault(fields, "producer_watermark", request.RequestID), "observed_at": request.Now.UTC().Format(time.RFC3339Nano)})}, nil
}

func workflowRecordVerdictEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64, defaultVerdictEvidence bool) ([]Event, error) {
	verdictActor, actorErr := workflowAuthenticatedActorField(fields, "verdict_actor_ref", actor)
	if actorErr != nil {
		return nil, actorErr
	}
	if request.OperatorActor != nil {
		// Operator approval supplies evaluator authority without relabeling
		// the coordinator or the worker that performed the delivery.
		if err := requireOperatorVerdictExit(ctx, tx, request.WorkID); err != nil {
			return nil, err
		}
		operatorRef, refErr := WorkflowActorRef(*request.OperatorActor)
		if refErr != nil {
			return nil, refErr
		}
		verdictActor = operatorRef
	} else {
		if err := workflowStepExecutorRefusal(ctx, tx, request.WorkID, verdictActor, "workflow_action"); err != nil {
			return nil, err
		}
	}
	evidence := workflowFieldStrings(fields, "evaluation_evidence")
	if len(evidence) == 0 {
		evidence = append([]string(nil), request.EvidenceRefs...)
	}
	if len(evidence) == 0 {
		return nil, newFailure(KindMissingEvidence, "workflow_action", "record_verdict requires durably bound evaluation evidence", false, "provide_evidence")
	}
	if !defaultVerdictEvidence {
		if err := verifyVerdictEvidence(ctx, tx, request.WorkID, evidence); err != nil {
			return nil, err
		}
	}
	predicateID, predicatePresent := workflowFieldString(fields, "predicate_id")
	if !predicatePresent || predicateID == "" {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict requires predicate_id", false, "name an approved contract predicate")
	}
	// The envelope bounds evaluation_evidence at 32 entries; the guard
	// keeps the allocation below it even for direct store callers.
	if len(evidence) > 32 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict evaluation_evidence exceeds 32 references", false, "supply a bounded evidence list")
	}
	var mintedEvents []Event
	if defaultVerdictEvidence {
		var mintErr error
		var effective []string
		mintedEvents, effective, mintErr = bornBoundEvidenceEvents(ctx, tx, request.WorkID, definition, request, actor, eventID, evidence, expected)
		if mintErr != nil {
			return nil, mintErr
		}
		evidence = effective
	}
	// An omitted contract_version resolves the active contract exactly as
	// confirm_premise does, so a verdict recorded after any supersession
	// lands on the contract the confirmation reads instead of deadlocking
	// between versions.
	contractVersion := workflowFieldInt(fields, "contract_version", 0)
	if contractVersion == 0 {
		activeVersion, activeErr := activeWorkflowContractVersion(ctx, tx, request.WorkID, "workflow_action")
		if activeErr != nil {
			if activeErr == sql.ErrNoRows {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract is missing", false, "reread_entities")
			}
			return nil, activeErr
		}
		contractVersion = activeVersion
	}
	events := make([]Event, 0, len(mintedEvents)+1)
	events = append(events, mintedEvents...)
	verdictExpected := expected + int64(len(events))
	events = append(events, workflowTypedEvent(eventID, WorkflowVerdictRecorded, request.WorkID, actor, request.Now, verdictExpected, map[string]any{"contract_version": contractVersion, "predicate_id": predicateID, "verdict_kind": workflowFieldStringDefault(fields, "verdict_kind", "ok"), "verdict_actor_ref": verdictActor, "evaluation_evidence": evidence, "incomparable_with_approved": workflowFieldBool(fields, "incomparable_with_approved")}))
	return events, nil
}

func workflowConfirmPremiseEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if request.OperatorActor == nil || request.OperatorActor.ActorClass != ActorOperator {
		return nil, newFailure(KindApprovalRequired, "workflow_action", "premise confirmation requires the verified operator approval identity", false, "request_approval")
	}
	operatorRef, actorErr := WorkflowActorRef(*request.OperatorActor)
	if actorErr != nil {
		return nil, actorErr
	}
	contractVersion := workflowFieldInt(fields, "contract_version", 0)
	if contractVersion == 0 {
		activeVersion, err := activeWorkflowContractVersion(ctx, tx, request.WorkID, "workflow_action")
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract is missing", false, "reread_entities")
			}
			return nil, err
		}
		contractVersion = activeVersion
	}
	// The acceptance step's deliverables are prerequisites of completion:
	// the recorded verdict and every contract-required evidence kind.
	// Confirming the premise advances the step, and once past acceptance
	// no declared action can bind them — so the confirmation refuses here
	// instead of letting the workflow walk itself into an uncompletable
	// state. The verdict check mirrors foldWorkflowCompleted's actor
	// comparison requirement.
	if err := requireAcceptanceDeliverables(ctx, tx, request.WorkID); err != nil {
		return nil, err
	}
	return []Event{workflowTypedEvent(eventID, WorkflowPremiseConfirmed, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": contractVersion, "confirming_actor_ref": operatorRef})}, nil
}

func workflowLinkSuccessorEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
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
	successorKind, definitionRef, successorErr := workflowSuccessorFamily(ctx, tx, definition, successorID)
	if successorErr != nil {
		return nil, successorErr
	}
	return []Event{workflowTypedEvent(eventID, WorkflowSuccessorLinked, request.WorkID, actor, request.Now, expected, map[string]any{"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": successorKind, "definition_ref": definitionRef})}, nil
}

func workflowDeclareImpactEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	// The impact edge carries a foreign key to work_items, so a target
	// the caller never named cannot default into existence: a phantom
	// "<work>-target" id only moves the failure to fold time. The target
	// is required and must name a real work item, refused here at the
	// boundary with a typed payload error (issue #823).
	targetID, targetOK := workflowFieldString(fields, "target_work_id")
	if !targetOK || targetID == "" {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "declare_impact requires target_work_id naming the impacted work item", false, "supply a real target work item id")
	}
	var targetExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, targetID).Scan(&targetExists); err == sql.ErrNoRows {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "declare_impact target_work_id does not name a work item", false, "supply a real target work item id")
	} else if err != nil {
		return nil, workflowProjectionError(err, "cannot read the impact target")
	}
	return []Event{workflowTypedEvent(eventID, WorkflowImpactDeclared, request.WorkID, actor, request.Now, expected, map[string]any{"edge_id": workflowFieldStringDefault(fields, "edge_id", "edge:"+request.OperationID), "edge_kind": workflowFieldStringDefault(fields, "edge_kind", "modifies"), "edge_class": workflowFieldStringDefault(fields, "edge_class", "hard"), "target_work_id": targetID, "target_kind": "work_item", "severity": workflowFieldStringDefault(fields, "severity", "non-breaking")})}, nil
}

func workflowAddConditionEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	values := map[string]any{"condition_id": workflowFieldStringDefault(fields, "condition_id", "condition:"+request.OperationID), "await_type": workflowFieldStringDefault(fields, "await_type", "timer"), "await_ref": workflowFieldStringDefault(fields, "await_ref", "await:"+request.OperationID), "resolution_authority": workflowFieldStringDefault(fields, "resolution_authority", "durable_operation:"+request.OperationID)}
	// Issue #87: a step delegating completion to an external actor may
	// declare how long the wait is expected to take; exceeding it reads
	// as overdue. Omitted or zero means no declared expectation.
	if bound, ok := workflowFieldIntOK(fields, "expected_within_seconds"); ok {
		values["expected_within_seconds"] = bound
	}
	return []Event{workflowTypedEvent(eventID, WorkflowConditionAdded, request.WorkID, actor, request.Now, expected, values)}, nil
}

func workflowResolveConditionEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	return []Event{workflowTypedEvent(eventID, WorkflowConditionResolved, request.WorkID, actor, request.Now, expected, map[string]any{"condition_id": workflowFieldStringDefault(fields, "condition_id", "condition:"+request.OperationID), "resolution_evidence": workflowFieldStringsDefault(fields, "resolution_evidence", []string{"evidence:" + request.OperationID}), "resolved_by_event": workflowFieldStringDefault(fields, "resolved_by_event", eventID)})}, nil
}

func workflowCancelConditionEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	return []Event{workflowTypedEvent(eventID, WorkflowConditionCancelled, request.WorkID, actor, request.Now, expected, map[string]any{"condition_id": workflowFieldStringDefault(fields, "condition_id", "condition:"+request.OperationID), "cancellation_authority": workflowFieldStringDefault(fields, "cancellation_authority", actor), "cancellation_evidence": workflowFieldStringsDefault(fields, "cancellation_evidence", []string{"evidence:" + request.OperationID}), "cancelled_by_event": workflowFieldStringDefault(fields, "cancelled_by_event", eventID)})}, nil
}
