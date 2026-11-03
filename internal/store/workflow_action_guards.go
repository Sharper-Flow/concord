package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type workflowActionGuardContext struct {
	request WorkflowActionExecutionRequest
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
// the reference in the evidence history.
func workflowEvidenceReferenceBound(ctx context.Context, q queryer, workID, reference, subject string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, reference).Scan(&count); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect spec-mandate evidence", true, "retry once the workflow evidence projection is readable", err)
	}
	return count != 0, nil
}

func guardPlanMandate(ctx context.Context, q queryer, workID string, plan *WorkflowStepPlan, step string, action WorkflowActionDefinition) error {
	if action.ID == "bind_evidence" || action.ID == "complete" {
		return nil
	}
	terminal := action.ID == "record_review" || action.ID == "record_verdict" || action.ID == "confirm_premise"
	if !terminal && (action.ExecutionMode != ActionAdvance || !workflowPlanAllowsAction(plan, step, "bind_evidence")) {
		return nil
	}
	mandates, version, err := workflowSpecMandate(ctx, q, workID, "workflow_action")
	if err != nil {
		return err
	}
	for _, lawID := range mandates {
		bound, err := workflowEvidenceReferenceBound(ctx, q, workID, lawID, "workflow_action")
		if err != nil {
			return err
		}
		if bound {
			continue
		}
		added, err := workflowContractAddsLaw(ctx, q, workID, version, lawID)
		if err != nil {
			return err
		}
		if added && action.ID == "record_review" {
			continue
		}
		detail := fmt.Sprintf("spec mandate law %q is not bound", lawID)
		recovery := "run bind_evidence on a frontier step before " + action.ID
		if added {
			detail = fmt.Sprintf("spec mandate law %q is one of the contract's own law additions; bind it only when the branch adds that id", lawID)
			recovery += fmt.Sprintf(" only when the branch adds %q; otherwise use revise_intent with the complete contract", lawID)
		}
		return newFailure(KindMissingEvidence, "workflow_action", detail, false, recovery)
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

func recordWorkflowOperationTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, workflowType, workflowDigest, currentStep string) ([]string, error) {
	durableStepKind := "internal_sqlite"
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,result_kind) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'completed')`, request.OperationID, 1, request.WorkID, workflowType, workflowDigest, currentStep, durableStepKind, request.AcceptedInputsDigest, nullableWorkflowText(request.AcceptedScope), request.PrincipalRef, request.RequestID, request.Now.UTC().Format(time.RFC3339Nano), request.ContractDigest); err != nil {
		return nil, wrapFailure(KindIdempotencyConflict, "workflow_action", "atomic workflow operation identity is already recorded: "+err.Error(), false, "retry the same canonical request or use a new operation identity", err)
	}
	evidenceRefs := append([]string(nil), request.EvidenceRefs...)
	if len(evidenceRefs) == 0 {
		evidenceRefs = []string{"evidence:" + request.OperationID}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET result_kind='completed',evidence_refs=? WHERE op_id=? AND attempt_epoch=?`, workflowJSON(evidenceRefs), request.OperationID, 1); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot prepare workflow evidence authority", true, "retry once the database is writable", err)
	}
	return evidenceRefs, nil
}

func assemblePlanWorkflowActionEventsTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, action WorkflowActionDefinition, plan *WorkflowStepPlan, stepID, actor string, evidenceRefs []string, version int64) ([]Event, error) {
	if plan == nil {
		return nil, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow has no plan", false, "pick a workflow type")
	}
	policy := builtinActionPolicies[request.ActionID]
	starting := action.ExecutionMode == ActionFenced
	epoch, err := workflowActionStartEpochForDispatch(ctx, tx, request.WorkID, stepID, starting)
	if err != nil {
		return nil, err
	}
	var events []Event
	if starting {
		values := map[string]any{"step_id": stepID, "action_id": request.ActionID, "attempt_epoch": epoch, "accepted_inputs_digest": request.AcceptedInputsDigest, "idempotency_identity": request.IdempotencyIdentity, "actor_ref": actor}
		events = append(events, workflowTypedEvent(request.OperationID+":started", WorkflowActionStarted, request.WorkID, actor, request.Now, version, values))
		return events, nil
	}
	if policy.EventShape == ActionEventCheckpoint {
		_, _, found, err := latestWorkflowActionStart(ctx, tx, request.WorkID, stepID)
		if err != nil {
			return nil, err
		}
		if !found {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),0)+1 FROM workflow_checkpoints WHERE work_id=? AND step_id=?`, request.WorkID, stepID).Scan(&epoch); err != nil {
				return nil, workflowProjectionError(err, "cannot inspect prior workflow checkpoint epochs")
			}
		}
		checkpoint, err := json.Marshal(map[string]any{"action_id": request.ActionID, "fields": request.Payload})
		if err != nil {
			return nil, err
		}
		values := map[string]any{"step_id": stepID, "step_kind": "internal_sqlite", "attempt_epoch": epoch, "checkpoint_payload": json.RawMessage(checkpoint), "resume_cursor": "", "actor_ref": actor, "request_id": request.RequestID, "checkpoint_id": request.OperationID + ":checkpoint", "accepted_inputs_digest": request.AcceptedInputsDigest, "idempotency_identity": request.IdempotencyIdentity}
		return []Event{workflowTypedEvent(request.OperationID+":checkpoint", WorkflowActionCheckpointed, request.WorkID, actor, request.Now, version, values)}, nil
	}
	events, err = workflowSemanticActionEvents(ctx, tx, plan.Type, request, stepID, actor, request.Payload, version)
	if err != nil {
		return nil, err
	}
	if request.ActionID == "checkpoint_context" || request.ActionID == "cross_context_boundary" || request.ActionID == "record_work_context" {
		return events, nil
	}
	values := map[string]any{"step_id": stepID, "action_id": request.ActionID, "attempt_epoch": epoch, "result_evidence_refs": evidenceRefs, "changed_refs": []string{request.WorkID}, "actor_ref": actor}
	if request.ActionID == "record_review" {
		report, err := decodeRecordedReview(request.Payload)
		if err != nil {
			return nil, err
		}
		values["review"] = report
	}
	fields, err := workflowActionObject(request.Payload)
	if err != nil {
		return nil, err
	}
	if request.ActionID == "record_verdict" && fields["verdicts"] != nil {
		entries, err := normalizeWorkflowVerdictEntries(fields)
		if err != nil {
			return nil, err
		}
		values["verdict_entry_count"] = len(entries)
	}
	if request.ActionID == "record_delivery" {
		values["delivery_artifact"] = workflowFieldStringDefault(fields, "delivery_artifact", "")
		values["delivery_state"] = workflowFieldStringDefault(fields, "delivery_state", "")
	}
	events = append(events, workflowTypedEvent(request.OperationID+":completed", WorkflowActionCompleted, request.WorkID, actor, request.Now, version+int64(len(events)), values))
	return events, nil
}

// applyCompleteWorkflowActionTx completes the workflow in this transaction:
// the ordered completion gate runs and workflow.completed is appended here.
// The caller's write scope guards the whole action region.
func applyCompleteWorkflowActionTx(ctx context.Context, tx *sql.Tx, scope *writeScope, request WorkflowActionExecutionRequest, actor string, payload json.RawMessage, prefixEvents []Event) (WorkflowActionExecutionResult, error) {
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
	completion, completionErr := workflowCompletionEventForDefinition(ctx, tx, request, actor, payload)
	if completionErr != nil {
		return result, completionErr
	}
	if err := completion.validate(); err != nil {
		return result, err
	}
	if err := validateRegisteredEvent(completion); err != nil {
		return result, err
	}
	var completedPayload workflowCompletedPayload
	if err := decodeWorkflowPayload(completion, &completedPayload); err != nil {
		return result, err
	}
	if err := workflowBase(completion, completedPayload.WorkflowVersionFields); err != nil {
		return result, err
	}
	if err := completeWorkflowInScopeTx(ctx, tx, completion, scope, completedPayload); err != nil {
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
