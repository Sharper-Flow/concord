package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// WorkflowActionExecutionRequest is the domain portion of one already
// authenticated workflow_action. The caller-owned transaction is the same
// transaction used by the action-boundary coordinator.
type WorkflowActionExecutionRequest struct {
	WorkID          string
	ExpectedVersion int64
	ActionID        string
	Payload         json.RawMessage
	EvidenceRefs    []string
	// EvidenceKinds carries the per-entry evidence kind for each locator the
	// caller submitted, in submission order. The bind-family event
	// constructors read it so one locator submitted under two kinds binds two
	// events, one per kind; an empty entry takes the payload's evidence_kind
	// default. It stays aligned to the submitted array, not to the merged set
	// the dispatch writes back into EvidenceRefs.
	EvidenceKinds []string
	Actor         WorkflowActor
	// ExpectedTakeover pins the boundary read of the held epoch and holder.
	// It is never decoded from request input.
	ExpectedTakeover     *WorkflowStartTakeover
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
	// starts relying on (CD-0216). The engine binds each consumer and proves
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
}

// ApplyWorkflowActionTx records one action's durable operation and event-folded
// result. The transaction's fold region owner supplies the scope; without one
// the action opens its own scope for this transaction. Every declared semantic
// action is translated to its closed event family here. The dispatcher is
// deliberately the only place where public action IDs acquire domain meaning.
func ApplyWorkflowActionTx(ctx context.Context, transaction *Transaction, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	tx, err := transactionSQL(transaction, "workflow_action")
	if err != nil {
		return WorkflowActionExecutionResult{}, err
	}
	scope := transaction.write
	if scope == nil {
		scope = newWriteScope(tx)
	}
	return applyWorkflowActionRawTx(ctx, tx, scope, request)
}

func applyWorkflowActionRawTx(ctx context.Context, tx *sql.Tx, scope *writeScope, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	if tx == nil {
		return WorkflowActionExecutionResult{}, newFailure(KindInvalidOperation, "workflow_action", "transaction is not open", false, "supply an active store transaction")
	}
	if scope == nil {
		return WorkflowActionExecutionResult{}, newFailure(KindInvalidOperation, "workflow_action", "write scope is required", false, "open the write scope with beginWrite")
	}
	return applyWorkflowPlanActionTx(ctx, tx, scope, request)
}

func finishWorkflowActionTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, operationResult ApplyOperationResult) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	result.EventIDs = operationResult.EventIDs
	result.ChangedRefs = []string{request.WorkID}
	result.OperationID = request.OperationID
	result.ResultingVersion = request.ExpectedVersion + int64(len(operationResult.EventIDs))
	changedRef := map[string]any{"entity_kind": "work_item", "id": request.WorkID, "version": result.ResultingVersion}
	resultMap := map[string]any{"changed_refs": []any{changedRef}, "next_valid_intents": []any{}, "operation_id": request.OperationID}
	result.Result, _ = json.Marshal(resultMap)
	durableChangedRef, _ := json.Marshal(changedRef)
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET result_kind='completed',result_payload=?,changed_refs=?,completed_at=? WHERE op_id=? AND attempt_epoch=?`, string(result.Result), workflowJSON([]string{string(durableChangedRef)}), request.Now.UTC().Format(time.RFC3339Nano), request.OperationID, 1); err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_action", "cannot complete durable workflow operation", true, "retry once the database is writable", err)
	}
	return result, nil
}

func validateWorkflowPlanActionPayload(actionID string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	action, err := catalogAction(actionID)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := decodeWorkflowActionPayloadStrict(raw, &fields); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "workflow action payload is not one strict JSON object", false, "supply the registered action payload")
	}
	if err := validateEvidenceBindingReferences(actionID, fields); err != nil {
		return nil, err
	}
	definitionFields := action.Payload.Fields
	allowed := make(map[string]WorkflowPayloadField, len(definitionFields))
	for _, field := range definitionFields {
		allowed[field.Name] = field
	}
	if err := validateWorkflowPayloadStructure(actionID, definitionFields, allowed, fields); err != nil {
		return nil, err
	}
	document := workflowLivePayloadSchemaDocument()
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		field := allowed[name]
		if !validateWorkflowPayloadValue(field, fields[name]) {
			return nil, newFailure(KindInvalidPayload, "workflow_action", fmt.Sprintf("workflow action payload field %q has the wrong registered type or bounds", name), false, "supply the declared action field type and bounds")
		}
		if err := validateWorkflowPayloadSchema(document, field, fields[name]); err != nil {
			return nil, err
		}
	}
	if err := validateWorkflowPayloadCrossFieldRules(actionID, fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func applyWorkflowPlanActionTx(ctx context.Context, tx *sql.Tx, scope *writeScope, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	action, plan, currentStep, err := workflowPlanActionAdmissionTx(ctx, tx, request.WorkID, request.ExpectedVersion, request.ActionID, request.Payload)
	if err != nil {
		return result, err
	}
	if err := validateWorkflowReviewRequest(ctx, tx, request.WorkID, request.ActionID, request.Payload); err != nil {
		return result, err
	}
	if err := normalizeWorkflowActionRequest(&request); err != nil {
		return result, err
	}
	actor, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return result, err
	}
	if request.PrincipalRef != request.Actor.PrincipalRef {
		return result, newFailure(KindUnauthorized, "workflow_action", "operation principal must match the authenticated actor", false, "use the authenticated principal")
	}
	prefix, err := workflowActorRecordPrefixTx(ctx, tx, request.Actor)
	if err != nil {
		return result, err
	}
	if WorkflowActionOpensHeldAttempt(action) {
		if err := guardStartTakeover(ctx, tx, action, request.WorkID, actor, request.ExpectedTakeover); err != nil {
			return result, err
		}
	}
	if request.ActionID == "link_successor" {
		if err := guardForwardLinkOnly(&workflowActionGuardContext{request: request}); err != nil {
			return result, err
		}
	}
	if request.ActionID == "cross_context_boundary" {
		if err := guardNoRestartDispatch(&workflowActionGuardContext{request: request}); err != nil {
			return result, err
		}
	}
	request.EvidenceRefs, err = workflowActionEvidenceRefs(request, request.Payload)
	if err != nil {
		return result, err
	}
	typeName := ""
	if plan != nil {
		typeName = string(plan.Type)
	}
	evidenceRefs, err := recordWorkflowOperationTx(ctx, tx, request, typeName, "", currentStep)
	if err != nil {
		return result, err
	}
	if err := BindResearchRelianceTx(ctx, tx, request.WorkID, request.ResearchBindings, request.Now); err != nil {
		return result, err
	}
	version := request.ExpectedVersion
	var events []Event
	if prefix != nil {
		events = append(events, prefix.event(request.OperationID+":actor", request.WorkID, request.Now, version))
		version++
	}
	if request.ActionID == "complete" {
		return applyCompleteWorkflowActionTx(ctx, tx, scope, request, actor, request.Payload, events)
	}
	planEvents, err := workflowPlanActionEventsTx(ctx, tx, request, action, plan, currentStep, actor, evidenceRefs, version)
	if err != nil {
		return result, err
	}
	events = append(events, planEvents...)
	operationResult, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): request.ExpectedVersion}}, scope)
	if err != nil {
		return result, err
	}
	if err := checkWorkContextDeclarationTx(ctx, tx, request.WorkID, events); err != nil {
		return result, err
	}
	return finishWorkflowActionTx(ctx, tx, request, operationResult)
}

func workflowPlanActionEventsTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, action WorkflowActionDefinition, plan *WorkflowStepPlan, currentStep, actor string, evidenceRefs []string, version int64) ([]Event, error) {
	planKinds := map[string]string{"record_intake": WorkflowIntakeRecorded, "record_clarify": WorkflowClarifyRecorded, "pick_type": WorkflowTypePicked, "insert_step": WorkflowStepInserted, "complete_step": WorkflowStepCompleted}
	if kind, ok := planKinds[request.ActionID]; ok {
		fields, err := workflowActionObject(request.Payload)
		if err != nil {
			return nil, err
		}
		values := make(map[string]any, len(fields)+1)
		for name, value := range fields {
			values[name] = value
		}
		if request.ActionID == "complete_step" {
			values["actor"] = actor
		}
		return []Event{workflowTypedEvent(request.OperationID+":semantic", kind, request.WorkID, actor, request.Now, version, values)}, nil
	}
	return assemblePlanWorkflowActionEventsTx(ctx, tx, request, action, plan, currentStep, actor, evidenceRefs, version)
}

// readWorkflowActionStateTx reads the instance's current step and state and
// refuses a stale expected work version.
func readWorkflowActionStateTx(ctx context.Context, tx *sql.Tx, workID string, expectedVersion int64) (string, string, error) {
	var currentStep, state, lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT instance_state,(SELECT lifecycle FROM work_items WHERE id=workflow_instances.work_id),(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, workID).Scan(&state, &lifecycle, &version); err != nil {
		return "", "", wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow state", true, "retry once the database is readable", err)
	}
	if expectedVersion != version {
		conflict, err := versionConflictForQuery(ctx, tx, workID, expectedVersion, version)
		if err != nil {
			return "", "", err
		}
		return "", "", conflict
	}
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil {
		return "", "", err
	}
	currentStep = "intake"
	if plan != nil {
		currentStep = ""
		frontier := WorkflowStepPlanFrontier(*plan)
		if len(frontier) > 0 {
			currentStep = frontier[0].ID
		}
	}
	return currentStep, state, nil
}

// workflowActionEvidenceRefs merges the operation's evidence locators with the
// refs an action derives from its own payload, and returns that merge as a set.
// An evidence array may legally name one locator twice, once per evidence kind
// — a pull request is both the commit and the review evidence — and the stored
// result_evidence_refs records each subject once. Every derived ref below
// merges through dedupeWorkflowRefs, so normalizing the request's own refs here
// makes the whole merged list a set.
func workflowActionEvidenceRefs(request WorkflowActionExecutionRequest, payload json.RawMessage) ([]string, error) {
	request.EvidenceRefs = dedupeWorkflowRefs(request.EvidenceRefs)
	refs := append([]string(nil), request.EvidenceRefs...)
	switch request.ActionID {
	case "complete", "record_review", "bind_evidence", "record_verdict":
	default:
		return refs, nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return nil, err
	}
	switch request.ActionID {
	case "complete":
		if len(refs) == 0 {
			refs = dedupeWorkflowRefs(workflowFieldStrings(fields, "evidence_refs"))
		}
		return refs, nil
	case "record_review":
		return dedupeWorkflowRefs(append(refs, request.OperationID+":completed")), nil
	case "bind_evidence":
		// fields.evidence_ref is the declared route for naming the immutable
		// subject. An explicit immutable_subject_ref overrides that fallback.
		reference := workflowFieldStringDefault(fields, "evidence_ref", "")
		reference = workflowFieldStringDefault(fields, "immutable_subject_ref", reference)
		if reference != "" {
			refs = dedupeWorkflowRefs(append(refs, reference))
		}
		return refs, nil
	}
	return workflowVerdictEvidenceRefs(fields)
}

// workflowVerdictEvidenceRefs retains only the receipts explicitly associated
// with criteria. An envelope locator cannot substitute for a receipt.
func workflowVerdictEvidenceRefs(fields map[string]json.RawMessage) ([]string, error) {
	entries, err := normalizeWorkflowVerdictEntries(fields)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, entry := range entries {
		refs = append(refs, entry.EvaluationEvidence...)
	}
	return dedupeWorkflowRefs(refs), nil
}

// dedupeWorkflowRefs keeps the first occurrence of each reference and preserves
// order, so the merged evidence list stays a set without reordering the refs a
// caller supplied.
func dedupeWorkflowRefs(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// workflowSemanticActionEvents constructs typed events. An empty result uses
// the ordinary action_completed event.
func workflowSemanticActionEvents(ctx context.Context, tx *sql.Tx, planType WorkflowStepPlanType, request WorkflowActionExecutionRequest, stepID, actor string, raw json.RawMessage, expected int64) ([]Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return nil, err
	}
	eventID := request.OperationID + ":semantic"
	switch request.ActionID {
	case "record_plan":
		return workflowPlanDocumentEvents(request, actor, raw, eventID, expected, WorkflowPlanRecorded), nil
	case "record_removal_scope":
		return workflowPlanDocumentEvents(request, actor, raw, eventID, expected, WorkflowRemovalScopeRecorded), nil
	case "checkpoint_context":
		return workflowContextCheckpointEvents(ctx, tx, request, stepID, actor, fields, eventID, expected)
	case "cross_context_boundary":
		return workflowContextBoundaryEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "record_work_context":
		return workflowRecordWorkContextEvents(ctx, tx, request, stepID, actor, fields, eventID, expected)
	case "approve_contract":
		return workflowApproveContractEvents(ctx, tx, planType, request, actor, fields, eventID, expected)
	case "revise_candidates":
		return workflowReviseCandidatesEvents(request, actor, fields, eventID, expected)
	case "record_diagnosis":
		return workflowRecordDiagnosisEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "record_review":
		return workflowRecordedReviewEvents(request, actor, eventID, expected)
	case "bind_evidence", "record_research", "record_report", "accept_decision", "approve_operation":
		return workflowEvidenceBindingEvents(request, actor, fields, eventID, expected)
	case "record_verdict":
		events, err := workflowRecordVerdictEvents(ctx, tx, request, actor, fields, eventID, expected)
		if err != nil {
			return nil, err
		}
		if err := guardLateCriterionAssociations(ctx, tx, request.WorkID, planType, stepID, fields); err != nil {
			return nil, err
		}
		return events, nil
	case "confirm_premise":
		return workflowConfirmPremiseEvents(ctx, tx, planType, request, actor, fields, eventID, expected)
	case "link_successor":
		return workflowLinkSuccessorEvents(ctx, tx, planType, request, actor, fields, eventID, expected)
	default:
		return nil, nil
	}
}

func workflowCompletionEventForDefinition(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage) (Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return Event{}, err
	}
	verdictActor := actor
	verdictActor, err = workflowAuthenticatedActorField(fields, "verdict_actor_ref", verdictActor)
	if err != nil {
		return Event{}, err
	}
	impactVerdict := workflowCompletionImpactVerdict(fields)
	if impactVerdict != "breaking" && impactVerdict != "non-breaking" {
		return Event{}, newFailure(KindInvalidPayload, "complete_workflow", "completion requires impact_verdict breaking or non-breaking", false, "supply the delivered change impact verdict")
	}
	// Completion reports recorded facts without re-evaluating acceptance.
	contractVersion, contractErr := completionActiveContractVersion(ctx, tx, request.WorkID)
	if contractErr != nil {
		return Event{}, contractErr
	}
	var boundEvidence int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, request.WorkID).Scan(&boundEvidence); err != nil {
		return Event{}, wrapFailure(KindUnavailable, "complete_workflow", "cannot count the bound workflow evidence", true, "retry once the database is readable", err)
	}
	premiseConfirmed := false
	var premiseRow int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?`, request.WorkID, contractVersion).Scan(&premiseRow); err == nil {
		premiseConfirmed = true
	}
	payload := map[string]any{"terminal_state": "completed", "verdict_actor_ref": verdictActor, "premise_confirmed": premiseConfirmed, "evidence_count": boundEvidence, "changed_refs_digest": WorkflowChangedRefsDigest([]string{request.WorkID}), "impact_verdict": impactVerdict}
	return workflowTypedEvent(request.OperationID+":completed", WorkflowCompleted, request.WorkID, actor, request.Now, request.ExpectedVersion, payload), nil
}

// workflowCompletionImpactVerdict reads impact_verdict from the fields, falling
// back to the nested payload.
func workflowCompletionImpactVerdict(fields map[string]json.RawMessage) string {
	if impactVerdict := workflowFieldStringDefault(fields, "impact_verdict", ""); impactVerdict != "" {
		return impactVerdict
	}
	payloadFields, ok := fields["payload"]
	if !ok {
		return ""
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(payloadFields, &nested) != nil {
		return ""
	}
	return workflowFieldStringDefault(nested, "impact_verdict", "")
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
func workflowFieldStrings(fields map[string]json.RawMessage, name string) []string {
	var values []string
	if fields[name] != nil {
		_ = json.Unmarshal(fields[name], &values)
	}
	return values
}

// requireResearchForPendingQuestions prevents contract approval from closing
// a plan while its latest durable context checkpoint still has open questions.
func requireResearchForPendingQuestions(ctx context.Context, q queryer, workID string) error {
	var questionsJSON string
	err := q.QueryRowContext(ctx, `SELECT pending_questions FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence DESC LIMIT 1`, workID).Scan(&questionsJSON)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot inspect pending workflow questions", true, "retry once the workflow context is readable", err)
	}
	var questions []string
	if err := json.Unmarshal([]byte(questionsJSON), &questions); err != nil {
		return newFailure(KindInvariantViolation, "workflow_action", "pending workflow questions are malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if len(questions) == 0 {
		return nil
	}
	var bound int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM active_research_consumers WHERE consumer_work_id=?)`, workID).Scan(&bound); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot inspect research bound to pending workflow questions", true, "retry once the research projection is readable", err)
	}
	if bound != 0 {
		return nil
	}
	return newFailure(KindMissingEvidence, "workflow_action", "contract approval requires a research pack revision for recorded pending questions", false, "add research_bindings to the approving action to bind a research pack revision to the work item before approving the contract")
}

func nullableWorkflowText(value string) any {
	if value == "" {
		return "{}"
	}
	return value
}
