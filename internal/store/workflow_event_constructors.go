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
// may name law_modifies only when every named law is derived (CD-0222 D5);
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

func workflowContextCheckpointEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, stepID, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	var workflowRef string
	var attemptEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT workflow_type FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&workflowRef); err != nil {
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
		"workflow_ref": workflowRef, "actor_ref": actor, "request_id": request.RequestID,
	})}, nil
}

func workflowContextBoundaryEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	boundaryKind := workflowFieldStringDefault(fields, "boundary_kind", "summary")
	mode := workflowFieldStringDefault(fields, "mode", "summary")
	if boundaryKind == "restart" || mode == "restart" || fields["restart"] != nil {
		return nil, newFailure(KindUnavailable, "workflow_action", "restart dispatch is not implemented and fails closed pending Concord issue #120", false, "contact_operator")
	}
	if boundaryKind != "summary" || mode != "summary" || workflowFieldStringDefault(fields, "summary", "") == "" {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "context boundary currently accepts boundary_kind=summary only", false, "use boundary_kind=summary with a completed durable checkpoint")
	}
	var workflowRef string
	var attemptEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT workflow_type FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&workflowRef); err != nil {
		return nil, workflowProjectionError(err, "cannot read workflow identity for context boundary")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, request.WorkID).Scan(&attemptEpoch); err != nil {
		return nil, workflowProjectionError(err, "cannot read workflow attempt epoch")
	}
	checkpointID := workflowFieldStringDefault(fields, "checkpoint_id", "")
	if checkpointID == "" {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "summary boundary requires checkpoint_id", false, "reference the latest durable context checkpoint")
	}
	return []Event{workflowTypedEvent(eventID, WorkflowContextBoundaryCrossed, request.WorkID, actor, request.Now, expected, map[string]any{
		"boundary_id": request.OperationID + ":context-boundary", "boundary_sequence": workflowFieldInt(fields, "boundary_sequence", 0), "boundary_kind": "summary", "checkpoint_id": checkpointID, "checkpoint_sequence": workflowFieldInt(fields, "checkpoint_sequence", 0), "summary": workflowFieldStringDefault(fields, "summary", ""), "workflow_ref": workflowRef, "attempt_epoch": attemptEpoch, "actor_ref": actor, "request_id": request.RequestID,
	})}, nil
}

func workflowApproveContractEvents(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if err := ensureInitialWorkflowContractApproval(ctx, tx, request.WorkID, fields); err != nil {
		return nil, err
	}
	if err := requireResearchForPendingQuestions(ctx, tx, request.WorkID); err != nil {
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

// workflowDefaultRequiredEvidence carries the retired families' default
// evidence duties onto the picked type: change inherits implementation's
// verification, review and artifact duties; fix inherits break-fix's
// verification and artifact; research keeps its artifact duty; removal
// delivers a change and inherits change's duties. An explicit empty list in
// the payload stays explicit.
func workflowDefaultRequiredEvidence(planType WorkflowStepPlanType) []string {
	switch planType {
	case WorkflowStepPlanFix:
		return []string{string(EvidenceVerification), string(EvidenceArtifact)}
	case WorkflowStepPlanResearch:
		return []string{string(EvidenceArtifact)}
	case WorkflowStepPlanChange, WorkflowStepPlanRemoval:
		return []string{string(EvidenceVerification), string(EvidenceReview), string(EvidenceArtifact)}
	}
	return []string{}
}

func workflowContractContent(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID string, fields map[string]json.RawMessage, requestedLawModifies []string) (map[string]any, error) {
	var outcomePredicates []workflowContractPredicatePayload
	if err := json.Unmarshal(fields["outcome_predicates"], &outcomePredicates); err != nil || len(outcomePredicates) == 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "contract requires explicit outcome predicates", false, "supply the approved end-state predicates")
	}
	if route, ok := workflowFieldString(fields, "route_convention"); ok && route != "workflow_action" {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "route convention is not declared by the workflow action boundary", false, "use a declared route convention")
	}
	declaredRoutes := workflowFieldStrings(fields, "route_conventions")
	proposedRoutes := workflowFieldStrings(fields, "proposed_route_conventions")
	for _, proposed := range proposedRoutes {
		if !slices.Contains(declaredRoutes, proposed) {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "proposed route convention is not declared by the contract", false, "use a declared route convention")
		}
	}
	for _, required := range workflowFieldStrings(fields, "required_route_conventions") {
		if !slices.Contains(declaredRoutes, required) {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "required route convention is not declared by the contract", false, "declare every required route convention")
		}
	}
	contractVersion := workflowFieldInt(fields, "contract_version", 1)
	premise, _ := workflowFieldString(fields, "premise")
	required := workflowFieldStrings(fields, "required_evidence")
	if required == nil {
		required = workflowDefaultRequiredEvidence(planType)
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
	if len(lawModifies) == 0 && len(requestedLawModifies) != 0 {
		lawModifies = append([]string(nil), requestedLawModifies...)
	}
	rigor := workflowFieldStringDefault(fields, "rigor_class", "prototype_internal")
	contract := map[string]any{"contract_version": contractVersion, "premise": premise, "outcome_predicates": outcomePredicates, "required_evidence": required, "route_conventions": routes, "spec_mandate": spec, "law_modifies": lawModifies, "rigor_class": rigor, "consequence_class": string(ActionInternalSQLite)}
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
	// the operator approves (CD-0222 D5 as amended). An empty law_modifies is
	// dropped only for non-Product-changing contracts, so a contract that
	// touches no law keeps its old shape; the Product-changing fold requires
	// the composed field to be present even when empty.
	if len(lawModifies) == 0 && !productChanging {
		delete(contract, "law_modifies")
	}
	var decisionDomains []string
	if raw, present := fields["decision_domains"]; !present || json.Unmarshal(raw, &decisionDomains) != nil || decisionDomains == nil {
		return nil, newFailure(KindInvalidPayload, "workflow_contract", "approval requires the submitted decision-domain fact", false, "declare the contract's decision domains explicitly")
	}
	if err := validateWorkflowAuthoredDecisionDomains(decisionDomains); err != nil {
		return nil, err
	}
	contract["decision_domains"] = decisionDomains
	contract["law_boundary_version"] = 1
	return contract, nil
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
// with resolve_overlap. The outcome enum and the
// outcome/related_ids legal input combinations are declared on the
// record_alignment registry payload and enforced by
// validateWorkflowActionPayload before any event is built, so this
// constructor does not restate them. Each related id names a real work item,
// refused here at the boundary rather than at the fold's foreign key.
func workflowRecordAlignmentEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	relatedIDs := workflowFieldStrings(fields, "related_ids")
	for _, relatedID := range relatedIDs {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, relatedID).Scan(&exists)
		if err == sql.ErrNoRows {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_alignment related id "+workflowRefExcerpt(relatedID)+" does not name a work item", false, "supply a real related work item id")
		} else if err != nil {
			return nil, workflowProjectionError(err, "cannot read the alignment related work item")
		}
	}
	values := map[string]any{"searched": workflowFieldStringDefault(fields, "searched", ""), "outcome": workflowFieldStringDefault(fields, "outcome", "")}
	if len(relatedIDs) != 0 {
		values["related_ids"] = relatedIDs
	}
	return []Event{workflowTypedEvent(eventID, WorkflowBacklogAlignmentRecorded, request.WorkID, actor, request.Now, expected, values)}, nil
}

// workflowRecordDiagnosisEvents builds the typed CD-0198 D2 diagnosis event.
// The action records all three required sections in one act: the
// reproduction record, the alignment search, and the root-cause record. The
// outcome enum and the outcome/related_ids legal input combinations are
// declared on the record_diagnosis registry payload and enforced by
// validateWorkflowActionPayload before any event is built, so this
// constructor does not restate them. Each related id names a real work
// item, refused here at the boundary rather than at the fold's foreign key,
// under the same rule the alignment constructor applies.
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

func workflowRecordedReviewEvents(request WorkflowActionExecutionRequest, actor, eventID string, expected int64) ([]Event, error) {
	return []Event{workflowTypedEvent(eventID+":evidence", WorkflowEvidenceBound, request.WorkID, actor, request.Now, expected, map[string]any{
		"evidence_kind": string(EvidenceReview), "immutable_subject_ref": request.OperationID + ":completed",
		"producer_id": request.PrincipalRef, "producer_run_ref": request.OperationID, "producer_watermark": request.RequestID,
		"observed_at": request.Now.UTC().Format(time.RFC3339Nano),
	})}, nil
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
// declared payload fields already enforce, so an array-derived subject cannot
// mint evidence that no verdict or completion can name.
func workflowEvidenceBindingEvents(request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	producerID := workflowFieldStringDefault(fields, "producer_id", request.PrincipalRef)
	producerRunRef := workflowFieldStringDefault(fields, "producer_run_ref", request.OperationID)
	producerWatermark := workflowFieldStringDefault(fields, "producer_watermark", request.RequestID)
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
	var report *VerificationReport
	if raw, present := fields["verification_report"]; present {
		if request.ActionID != "bind_evidence" {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "verification reports require bind_evidence", false, "bind the report through bind_evidence")
		}
		var decoded VerificationReport
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "verification report is malformed", false, "supply the complete report")
		}
		if err := validateVerificationReport(subject, &decoded); err != nil {
			return nil, err
		}
		report = &decoded
		producerID, producerRunRef, producerWatermark = request.PrincipalRef, request.OperationID, request.RequestID
	}
	kinds, err := workflowEvidenceBindingKinds(request.ActionID, request.EvidenceKinds, fields, subject)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(kinds))
	for i, kind := range kinds {
		payload := map[string]any{"evidence_kind": kind, "immutable_subject_ref": subject, "producer_id": producerID, "producer_run_ref": producerRunRef, "producer_watermark": producerWatermark, "observed_at": request.Now.UTC().Format(time.RFC3339Nano)}
		if report != nil {
			if kind != "verification" {
				return nil, newFailure(KindInvalidPayload, "workflow_action", "verification report requires only verification evidence", false, "bind the report as verification evidence")
			}
			payload["verification_report"] = report
		}
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

// workflowVerdictBatchEntry is one criterion's captured receipt association.
type workflowVerdictBatchEntry struct {
	PredicateID              string
	VerdictKind              string
	EvaluationEvidence       []string
	IncomparableWithApproved bool
}

// normalizeWorkflowVerdictEntries decodes the bounded receipt batch. Neither
// an envelope evidence reference nor a caller verdict supplies missing proof.
func normalizeWorkflowVerdictEntries(fields map[string]json.RawMessage) ([]workflowVerdictBatchEntry, error) {
	batchRaw, batchPresent := fields["verdicts"]
	if !batchPresent {
		return nil, reviewFailure(KindInvalidPayload, "record_verdict requires a criterion-to-receipt batch", "supply verdicts entries containing predicate_id and receipt_refs")
	}
	var decoded []struct {
		PredicateID string   `json:"predicate_id"`
		ReceiptRefs []string `json:"receipt_refs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(batchRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict verdicts is not a decodable array of verdict entries", false, "supply one object per judged predicate, each naming predicate_id")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict verdicts carries trailing data after the entry array", false, "supply exactly one verdicts array")
	}
	entries := make([]workflowVerdictBatchEntry, 0, len(decoded))
	for _, item := range decoded {
		if item.PredicateID == "" || !ValidReference(item.PredicateID) {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict verdicts entry predicate_id must satisfy the reference rule: 2 to 128 bytes with no whitespace", false, "name an approved contract predicate in every entry")
		}
		if len(item.ReceiptRefs) < 1 || len(item.ReceiptRefs) > 32 {
			return nil, reviewFailure(KindInvalidPayload, "each criterion association requires 1 to 32 evidence references", "supply receipt_refs for recorded evidence")
		}
		entries = append(entries, workflowVerdictBatchEntry{PredicateID: item.PredicateID, VerdictKind: "ok", EvaluationEvidence: item.ReceiptRefs})
	}
	return entries, nil
}

func workflowRecordVerdictEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	associations, err := workflowVerdictReceiptAssociations(ctx, tx, request.WorkID, actor, fields)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(associations))
	for index, association := range associations {
		entryEventID := eventID
		if len(associations) != 1 {
			entryEventID = fmt.Sprintf("%s:verdict:%d", eventID, index)
		}
		events = append(events, workflowTypedEvent(entryEventID, WorkflowVerdictRecorded, request.WorkID, actor, request.Now, expected+int64(index), map[string]any{
			"contract_version": association.ContractVersion, "predicate_id": association.PredicateID, "verdict_kind": association.VerdictKind,
			"verdict_actor_ref": association.VerdictActorRef, "evaluation_evidence": association.EvaluationEvidence,
			"criterion_digest": association.CriterionDigest, "candidate": association.Candidate,
		}))
	}
	return events, nil
}

// workflowVerdictReceiptAssociations validates a whole batch against one read
// snapshot before either preflight admits it or event construction records it.
// The action boundary validates the authenticated actor tuple; a new actor's
// registration is still queued before its verdicts in the same transaction.
func workflowVerdictReceiptAssociations(ctx context.Context, tx queryer, workID, verdictActor string, fields map[string]json.RawMessage) ([]workflowVerdictRecordedPayload, error) {
	entries, entriesErr := normalizeWorkflowVerdictEntries(fields)
	if entriesErr != nil {
		return nil, entriesErr
	}
	if len(entries) == 0 {
		// Unreachable through payload validation, which the engine cross-
		// field declaration owns: a call carrying neither wire form is
		// refused there. The constructor still refuses to record zero
		// verdicts, so a bypassed caller cannot mint an empty judgment.
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict recorded no verdict entries", false, "supply predicate_id or the verdicts array")
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if seen[entry.PredicateID] {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict names predicate "+workflowRefExcerpt(entry.PredicateID)+" twice; a call judges each predicate once", false, "record one entry per judged predicate")
		}
		seen[entry.PredicateID] = true
	}
	// An omitted contract_version resolves the active contract exactly as
	// confirm_premise does, so a verdict recorded after any contract update
	// lands on the contract the confirmation reads instead of deadlocking
	// between versions.
	contractVersion := workflowFieldInt(fields, "contract_version", 0)
	if contractVersion == 0 {
		activeVersion, activeErr := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action")
		if activeErr != nil {
			if activeErr == sql.ErrNoRows {
				return nil, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract is missing", false, "reread_entities")
			}
			return nil, activeErr
		}
		contractVersion = activeVersion
	}
	// Every entry is validated before any event exists, so a malformed batch
	// refuses as a whole and the transaction never carries a partial write.
	// The membership rule mirrors the fold's own, including its shape for a
	// work with no contract rows at all.
	var contractCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractCount); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect workflow contract")
	}
	if contractCount != 0 {
		for _, entry := range entries {
			var approved int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, workID, contractVersion, entry.PredicateID).Scan(&approved); err != nil {
				return nil, workflowProjectionError(err, "cannot inspect approved workflow predicate")
			}
			if approved != 1 {
				return nil, newFailure(KindInvalidPayload, "workflow_action", "verdict predicate_id "+workflowRefExcerpt(entry.PredicateID)+" is not approved for contract version "+fmt.Sprint(contractVersion), false, "name an approved predicate of the call's contract version")
			}
		}
	}
	return associateWorkflowVerdictReceiptsTx(ctx, tx, workID, verdictActor, entries, contractVersion)
}

func associateWorkflowVerdictReceiptsTx(ctx context.Context, tx queryer, workID, verdictActor string, entries []workflowVerdictBatchEntry, contractVersion int64) ([]workflowVerdictRecordedPayload, error) {
	var associations []workflowVerdictRecordedPayload
	state, stateErr := loadWorkflowReviewState(ctx, tx, workID)
	if stateErr != nil {
		return nil, stateErr
	}
	research, err := workflowResearchCriterionFamily(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	if state.Candidate == nil && !research {
		return nil, reviewFailure(KindMissingEvidence, "criterion association requires a current integrated candidate", "accept the observed candidate before associating its proof")
	}
	dependencies, err := workflowReviewDependencies(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		evidence := entry.EvaluationEvidence
		if len(evidence) == 0 {
			return nil, newFailure(KindMissingEvidence, "workflow_action", "record_verdict requires durably bound evaluation evidence", false, "provide_evidence")
		}
		// The envelope bounds evaluation_evidence at 32 entries; the guard
		// keeps the allocation below it even for direct store callers.
		if len(evidence) > 32 {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_verdict evaluation_evidence exceeds 32 references", false, "supply a bounded evidence list")
		}
		for _, ref := range evidence {
			subject := ""
			if state.Candidate != nil {
				subject = state.Candidate.Commit
			}
			if err := workflowCriterionReceipt(ctx, tx, workID, entry.PredicateID, ref, subject); err != nil {
				return nil, err
			}
		}
		criterionDigest := ""
		for _, criterion := range dependencies.Criteria {
			if criterion.PredicateID == entry.PredicateID {
				criterionDigest = criterion.ContentDigest
			}
		}
		if criterionDigest == "" {
			return nil, reviewFailure(KindMissingEvidence, "criterion is not an accepted current criterion", "associate proof only with a criterion of the current approved contract")
		}
		associations = append(associations, workflowVerdictRecordedPayload{ContractVersion: contractVersion, PredicateID: entry.PredicateID, VerdictKind: "ok", VerdictActorRef: verdictActor, EvaluationEvidence: evidence, CriterionDigest: criterionDigest, Candidate: state.Candidate})
	}
	return associations, nil
}

func workflowConfirmPremiseEvents(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	contractVersion, err := workflowConfirmationAdmissionTx(ctx, tx, planType, request.WorkID, actor, fields)
	if err != nil {
		return nil, err
	}
	return []Event{workflowTypedEvent(eventID, WorkflowPremiseConfirmed, request.WorkID, actor, request.Now, expected, map[string]any{"contract_version": contractVersion, "confirming_actor_ref": actor, "confirmation_authority": "coordinator"})}, nil
}

func workflowConfirmationAdmissionTx(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID, actor string, fields map[string]json.RawMessage) (int64, error) {
	contractVersion := workflowFieldInt(fields, "contract_version", 0)
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

func validateWorkflowCoordinatorConfirmationTx(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID, actor string) error {
	var invokerClass string
	if tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, actor).Scan(&invokerClass) != nil || invokerClass != "agent" {
		return newFailure(KindInvalidOperation, "workflow_action", "an engineering premise confirmation records the authenticated coordinator actor", false, "confirm under an agent-class coordinator identity")
	}
	return workflowConfirmationDeliverablesTx(ctx, tx, planType, workID)
}

// workflowConfirmationDeliverablesTx holds the actor-independent confirmation
// admission facts: every approved predicate proved, every required evidence
// kind and mandate bound, and a current ready Review outside research. The
// write validates them here; reads (preflight, the work pin) ask the same
// question so no surface advertises a confirmation the fold refuses.
func workflowConfirmationDeliverablesTx(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, workID string) error {
	contract, err := workflowCompletionContractRows(ctx, tx, workID)
	if err != nil {
		return err
	}
	missing, err := workflowMissingCriterionAssociations(ctx, tx, workID)
	if err != nil {
		return err
	}
	if len(missing) != 0 {
		ids := slices.Sorted(maps.Keys(missing))
		return newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires proof for every approved predicate: missing "+strings.Join(ids, ", "), false, "record_verdict for each approved predicate")
	}
	var requirements []workflowEvidenceRequirement
	for _, kind := range contract.RequiredEvidence {
		requirements = append(requirements, workflowEvidenceRequirement{Kind: kind})
	}
	for _, ref := range contract.SpecMandate {
		requirements = append(requirements, workflowEvidenceRequirement{Reference: ref})
	}
	for _, obligation := range contract.VerificationObligations {
		requirements = append(requirements, workflowEvidenceRequirement{Kind: obligation.ObligationID, Reference: obligation.LawID})
	}
	outstanding, err := unboundWorkflowEvidenceRequirements(ctx, tx, workID, requirements, 0)
	if err != nil {
		return err
	}
	if len(outstanding) != 0 {
		return newFailure(KindMissingEvidence, "workflow_action", fmt.Sprintf("contract evidence requirements are not bound: kind %q reference %q", outstanding[0].Kind, outstanding[0].Reference), false, "bind required evidence")
	}
	if planType != WorkflowStepPlanResearch {
		review, err := loadWorkflowReviewState(ctx, tx, workID)
		if err != nil {
			return err
		}
		return workflowAdmitReview(review)
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
	return []Event{workflowTypedEvent(eventID, WorkflowSuccessorLinked, request.WorkID, actor, request.Now, expected, map[string]any{"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": successorKind, "definition_ref": string(successor.Type)})}, nil
}
