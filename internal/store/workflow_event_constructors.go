package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The per-ActionID event constructors behind workflowSemanticActionEvents.
// Each holds one action's own validation and event assembly; the dispatch in
// workflow_dispatch.go keeps the case labels so the declared EventShape stays
// structurally pinned to this surface (workflow_event_shape_test.go). The
// design validation and correction assembly they call live in
// workflow_design.go.

// workflowContractProductGuard enforces the Product-truth boundary the
// initial approval and ordinary contract updates share: the architecture
// binding must parse, a Product-changing contract requires one, and a
// non-Product-changing contract carries none. A non-Product-changing contract
// may name law_modifies only when every named law is derived;
// the tier-scoped check runs against the same transaction the caller already holds.
// role names the contract side in the refusal messages; completeRecovery is
// the Product-changing refusal's recovery hint. The parsed binding returns
// for the caller's payload assembly.
func workflowContractProductGuard(ctx context.Context, tx *sql.Tx, workID string, fields map[string]json.RawMessage, lawModifies []string, productChanging bool, role, completeRecovery string) (*WorkflowArchitectureBinding, error) {
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
		return nil, newFailure(KindInvalidPayload, "workflow_action", "non-Product-changing "+role+" cannot carry architecture_binding", false, "omit architecture_binding")
	}
	if err := validateDerivedLawModification(ctx, tx, workID, lawModifies); err != nil {
		return nil, err
	}
	return binding, nil
}

func workflowApproveContractEvents(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if err := ensureInitialWorkflowContractApproval(ctx, tx, request.WorkID, fields); err != nil {
		return nil, err
	}
	contract, err := workflowContractContent(ctx, tx, planType, request.WorkID, fields, request.LawModifies)
	if err != nil {
		return nil, err
	}
	approvalEvent := workflowTypedEvent(eventID, WorkflowContractApproved, request.WorkID, actor, request.Now, expected, contract)
	approvalEvent.PayloadVersion = workflowContractPayloadVersion
	return []Event{approvalEvent}, nil
}

func workflowContractContent(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID string, fields map[string]json.RawMessage, requestedLawModifies []string) (map[string]any, error) {
	contractVersion := workflowContractVersionField(fields, 1)
	spec := workflowFieldStrings(fields, "spec_mandate")
	if spec == nil {
		spec = []string{}
	}
	lawModifies := workflowFieldStrings(fields, "law_modifies")
	if len(lawModifies) == 0 && len(requestedLawModifies) != 0 {
		lawModifies = append([]string(nil), requestedLawModifies...)
	}
	subjects := workflowFieldStrings(fields, "legislative_subjects")
	if subjects == nil {
		subjects = []string{}
	}
	contract := map[string]any{"contract_version": contractVersion, "spec_mandate": spec, "law_modifies": lawModifies, "legislative_subjects": subjects, "consequence_class": string(ActionInternalSQLite)}
	return workflowBoundContractContent(ctx, tx, planType, workID, fields, contract, spec, lawModifies)
}

func workflowBoundContractContent(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID string, fields map[string]json.RawMessage, contract map[string]any, spec, lawModifies []string) (map[string]any, error) {
	if err := validateLawModificationSubset(spec, lawModifies); err != nil {
		return nil, err
	}
	productChanging := planType != WorkflowStepPlanResearch
	binding, bindingErr := workflowContractProductGuard(ctx, tx, workID, fields, lawModifies, productChanging, "contract", "supply the complete architecture binding")
	if bindingErr != nil {
		return nil, bindingErr
	}
	if productChanging {
		contract["architecture_binding"] = binding
	}
	// law_modifies stays on non-Product-changing contracts that passed the
	// tier-scoped guard: the derived revision lines are the visible record
	// the operator approves. An empty law_modifies is
	// dropped only for non-Product-changing contracts, so a contract that
	// touches no law keeps its old shape; the Product-changing fold requires
	// the composed field to be present even when empty.
	if len(lawModifies) == 0 && !productChanging {
		delete(contract, "law_modifies")
	}
	contract["law_boundary_version"] = 1
	return contract, nil
}

// workflowRecordDiagnosisEvents builds the typed diagnosis event.
// The action records all three required sections in one act: the
// reproduction record, the alignment search, and the root-cause record. The
// outcome enum and the outcome/related_ids legal input combinations are
// declared on the record_diagnosis registry payload and enforced at action
// validation by validateWorkflowPayloadCrossFieldRules before any event is
// built, so this
// constructor does not restate them. Each related id names a real work
// item, refused here at the boundary rather than at the fold's foreign key.
func workflowRecordDiagnosisEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	relatedIDs := workflowFieldStrings(fields, "related_ids")
	for _, relatedID := range relatedIDs {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, relatedID).Scan(&exists)
		if err == sql.ErrNoRows {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_diagnosis related id "+workflowRefExcerpt(relatedID)+" does not name a work item", false, "supply a real related work item id")
		} else if err != nil {
			return nil, workflowProjectionError(err, "cannot read the diagnosis related work item")
		}
	}
	values := map[string]any{
		"reproduction": workflowFieldStringDefault(fields, "reproduction", ""),
		"searched":     workflowFieldStringDefault(fields, "searched", ""),
		"outcome":      workflowFieldStringDefault(fields, "outcome", ""),
		"root_cause":   workflowFieldStringDefault(fields, "root_cause", ""),
	}
	if len(relatedIDs) != 0 {
		values["related_ids"] = relatedIDs
	}
	return []Event{workflowTypedEvent(eventID, WorkflowDiagnosisRecorded, request.WorkID, actor, request.Now, expected, values)}, nil
}

// workflowEvidenceBindingEvents binds the evidence the call submitted, one
// WorkflowEvidenceBound event per distinct (subject, evidence kind) pair. The
// merged submissions of one call — the evidence array, the declared payload
// fields, and the generated fallback — must name one immutable subject: ok is
// a claim about what the call durably bound, and a call naming several
// subjects could bind at most the first and silently drop the rest, so the
// store refuses and the caller splits the submission into one bind per
// subject. One locator submitted under two kinds binds two events, one per
// kind. Every candidate subject satisfies the same reference rule the
// declared payload fields already enforce.
func workflowEvidenceBindingEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	producerID := workflowFieldStringDefault(fields, "producer_id", request.PrincipalRef)
	producerRunRef := workflowFieldStringDefault(fields, "producer_run_ref", request.OperationID)
	producerWatermark := workflowFieldStringDefault(fields, "producer_watermark", request.RequestID)
	if request.ActionID == "bind_evidence" {
		producerWatermark = workflowFieldStringDefault(fields, "producer_watermark", "")
		if !ValidReference(producerWatermark) {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "bind_evidence requires a producer watermark", false, "supply the producer watermark")
		}
	}
	subjects := dedupeWorkflowRefs(request.EvidenceRefs)
	if len(subjects) == 0 {
		subjects = []string{"evidence:" + request.OperationID}
	}
	for _, subject := range subjects {
		if !ValidReference(subject) {
			return nil, newFailure(KindInvalidPayload, "workflow_action", fmt.Sprintf("%s immutable subject %s must satisfy the reference rule: 2 to 128 bytes with no whitespace", request.ActionID, workflowRefExcerpt(subject)), false, "supply a whitespace-free reference no longer than 128 bytes")
		}
	}
	if len(subjects) > 1 {
		named := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			named = append(named, workflowRefExcerpt(subject))
		}
		return nil, newFailure(KindInvalidPayload, "workflow_action", fmt.Sprintf("%s binds one immutable subject per call; the call names %d distinct subjects (%s); split the submission into one bind per subject", request.ActionID, len(subjects), strings.Join(named, ", ")), false, "split the submission into one bind per subject")
	}
	subject := subjects[0]
	kinds, err := workflowEvidenceBindingKinds(request.ActionID, request.EvidenceKinds, fields, subject)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(kinds))
	for i, kind := range kinds {
		payload := map[string]any{"evidence_kind": kind, "immutable_subject_ref": subject, "producer_id": producerID, "producer_run_ref": producerRunRef, "producer_watermark": producerWatermark, "observed_at": request.Now.UTC().Format(time.RFC3339Nano)}
		events = append(events, workflowTypedEvent(eventID+":evidence:"+fmt.Sprint(i), WorkflowEvidenceBound, request.WorkID, actor, request.Now, expected+int64(i), payload))
	}
	return events, nil
}

// workflowEvidenceBindingKinds is the producer's ordered, distinct kind inventory.
func workflowEvidenceBindingKinds(actionID string, evidenceKinds []string, fields map[string]json.RawMessage, subject string) ([]string, error) {
	declaredRef := workflowFieldStringDefault(fields, "immutable_subject_ref", workflowFieldStringDefault(fields, "evidence_ref", ""))
	defaultKind := workflowFieldStringDefault(fields, "evidence_kind", "verification")
	var kinds []string
	seenKinds := make(map[string]bool)
	addKind := func(kind string) error {
		if kind == "" || seenKinds[kind] {
			return nil
		}
		if !validEvidence(EvidenceKind(kind)) {
			return newFailure(KindInvalidPayload, "workflow_action", fmt.Sprintf("%s evidence kind %q is not a declared evidence kind", actionID, kind), false, "use verification, review, approval, commit, durable_note, native_run, or artifact")
		}
		seenKinds[kind] = true
		kinds = append(kinds, kind)
		return nil
	}
	for _, kind := range evidenceKinds {
		if err := addKind(kind); err != nil {
			return nil, err
		}
	}
	if declaredRef == subject {
		if err := addKind(defaultKind); err != nil {
			return nil, err
		}
	}
	if len(kinds) == 0 {
		if err := addKind(defaultKind); err != nil {
			return nil, err
		}
	}
	return kinds, nil
}

func workflowConfirmPremiseEvents(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	contractVersion, err := workflowConfirmationAdmissionTx(ctx, tx, planType, request.WorkID, actor, fields)
	if err != nil {
		return nil, err
	}
	return []Event{workflowTypedEvent(eventID, WorkflowPremiseConfirmed, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": contractVersion, "confirming_actor_ref": actor, "confirmation_authority": "coordinator"})}, nil
}

func workflowConfirmationAdmissionTx(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID, actor string, fields map[string]json.RawMessage) (int64, error) {
	contractVersion := workflowContractVersionField(fields, 0)
	activeVersion, err := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action")
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract is missing", false, "reread_entities")
		}
		return 0, err
	}
	if contractVersion == 0 {
		contractVersion = activeVersion
	}
	if contractVersion != activeVersion {
		return 0, newFailure(KindInvalidPayload, "workflow_action", fmt.Sprintf("premise confirmation names contract version %d, which supersession invalidated; the active version is %d", contractVersion, activeVersion), false, "confirm the active contract version")
	}
	if err := validateWorkflowCoordinatorConfirmationTx(ctx, tx, planType, workID, actor); err != nil {
		return 0, err
	}
	return contractVersion, nil
}

func validateWorkflowCoordinatorConfirmationTx(ctx context.Context, tx *sql.Tx, _ WorkflowStepPlanType, _, actor string) error {
	var invokerClass string
	if tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, actor).Scan(&invokerClass) != nil || invokerClass != "agent" {
		return newFailure(KindInvalidOperation, "workflow_action", "an engineering premise confirmation records the authenticated coordinator actor", false, "confirm under an agent-class coordinator identity")
	}
	return nil
}

func workflowLinkSuccessorEvents(ctx context.Context, tx *sql.Tx, _ WorkflowStepPlanType, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
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
	var successorKind, lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT kind,lifecycle FROM work_items WHERE id=?`, successorID).Scan(&successorKind, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return nil, newFailure(KindInvalidRelation, "workflow_action", "successor work item is not recorded", false, "create the successor before linking it")
		}
		return nil, workflowProjectionError(err, "cannot read successor work item")
	}
	if isTerminalLifecycle(lifecycle) {
		return nil, newFailure(KindInvalidRelation, "workflow_action", "successor work item is terminal", false, "link active successor work")
	}
	successor, err := readWorkflowPlanTx(ctx, tx, successorID)
	if err != nil {
		return nil, err
	}
	if successor == nil {
		return nil, newFailure(KindInvalidRelation, "workflow_action", "successor workflow type is not selected", false, "pick a successor workflow type")
	}
	return []Event{workflowTypedEvent(eventID, WorkflowSuccessorLinked, request.WorkID, actor, request.Now, expected, map[string]any{"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": successorKind})}, nil
}
