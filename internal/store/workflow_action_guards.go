package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// workflowActionGuardPhase names the point in the dispatcher's sequence where
// a guard runs. The phases are separate because the guards are not
// independent: stale recovery selects payload validation and lifecycle
// legality, the actor guards produce state the event assembly reads, and the
// claim guard runs after the durable operation is claimed.
type workflowActionGuardPhase int

const (
	guardPhaseRecovery workflowActionGuardPhase = iota
	guardPhaseBoundary
	guardPhasePostValidation
	guardPhaseActor
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

	staleRecovery       bool
	actorRef            string
	eventActor          string
	operatorRef         string
	actorNeedsRecord    bool
	operatorNeedsRecord bool
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
	"record_verdict":         {guardPhaseActor, guardRecordedActorTuple},
	"cross_context_boundary": {guardPhaseClaim, guardNoRestartDispatch},
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
	return newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
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

// guardOperatorPremiseActor applies to every action, not only premise
// confirmation: an operator actor is valid nowhere else, so the action check
// lives inside the guard rather than in the phase table. A nil operator actor
// is the common case and passes.
func guardOperatorPremiseActor(g *workflowActionGuardContext) error {
	if g.request.OperatorActor == nil {
		return nil
	}
	if g.request.ActionID != "confirm_premise" || g.request.OperatorActor.ActorClass != ActorOperator {
		return newFailure(KindUnauthorized, "workflow_action", "operator actor is only valid for signed premise confirmation", false, "use the verified approval identity")
	}
	ref, err := WorkflowActorRef(*g.request.OperatorActor)
	if err != nil {
		return err
	}
	if ref == g.actorRef {
		return newFailure(KindUnauthorized, "workflow_action", "operator actor cannot relabel the invoking agent", false, "approve from an independent operator identity")
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

	actorRef            string
	eventActor          string
	operatorRef         string
	actorNeedsRecord    bool
	operatorNeedsRecord bool
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
		resultVersion++
	}
	if executionMode == ActionCheckpoint {
		resultVersion := versionCursor + int64(len(events)-int(versionCursor-in.request.ExpectedVersion)) + 1
		checkpointPayload, _ := json.Marshal(map[string]any{"action_id": in.request.ActionID, "fields": json.RawMessage(in.payload)})
		checkpoint, _ := json.Marshal(map[string]any{
			"work_id": in.request.WorkID, "expected_version": versionCursor + int64(len(events)-int(versionCursor-in.request.ExpectedVersion)), "resulting_version": resultVersion,
			"step_id": in.currentStep, "step_kind": string(in.step.Kind), "attempt_epoch": workflowActionEpoch,
			"checkpoint_payload": json.RawMessage(checkpointPayload), "resume_cursor": "", "actor_ref": actor,
			"request_id": in.request.RequestID, "checkpoint_id": in.request.OperationID + ":checkpoint",
			"accepted_inputs_digest": in.request.AcceptedInputsDigest, "idempotency_identity": in.request.IdempotencyIdentity,
		})
		events = append(events, Event{EventID: in.request.OperationID + ":checkpoint", Kind: WorkflowActionCheckpointed, SubjectType: SubjectWorkItem, SubjectID: in.request.WorkID, Actor: actor, OccurredAt: in.request.Now, PayloadVersion: 1, Payload: checkpoint})
	} else if in.request.ActionID != "complete" {
		semantic, semanticErr := workflowSemanticActionEvents(ctx, tx, in.entry.Definition, in.request, in.currentStep, actor, in.payload, versionCursor+int64(len(events)-int(versionCursor-in.request.ExpectedVersion)))
		if semanticErr != nil {
			return out, semanticErr
		}
		if len(semantic) != 0 {
			events = append(events, semantic...)
			out.nativeRun = nativeRunFromSemanticEvents(semantic)
		}
	}
	out.events = events
	return out, nil
}

// appendGenericWorkflowCompletion appends the generic WorkflowActionCompleted
// event. Continuity's typed event is the durable action boundary; appending a
// generic completion after it would make the checkpoint immediately stale, so
// the continuity actions are excluded.
func appendGenericWorkflowCompletion(in workflowActionAssemblyInput, attemptEpoch int64, events []Event) ([]Event, error) {
	if in.request.ActionID == "checkpoint_context" || in.request.ActionID == "cross_context_boundary" || in.request.ActionID == "supersede_contract" {
		return events, nil
	}
	resultVersion := in.request.ExpectedVersion + int64(len(events)) + 1
	fields, fieldsErr := workflowActionObject(in.payload)
	if fieldsErr != nil {
		return events, fieldsErr
	}
	completionValues := map[string]any{
		"step_id": in.currentStep, "action_id": in.request.ActionID, "attempt_epoch": attemptEpoch, "result_evidence_refs": in.evidenceRefs,
		"changed_refs": []string{in.request.WorkID}, "actor_ref": in.eventActor,
	}
	if in.request.ActionID == "accept_worker_result" {
		completionValues["attempt_epoch"] = workflowFieldInt(fields, "attempt_epoch", 0)
		completionValues["worker_attempt_id"] = workflowFieldStringDefault(fields, "attempt_id", "")
	}
	// CD-0059 D1/D5: the dispatch_worker completion carries the authorized
	// attempt_id into the durable record so the worker-dispatch evidence
	// boundary can prove the window exists and has not been consumed. The
	// start already records the attempt_epoch; the completion binds the
	// attempt identity.
	if in.request.ActionID == "dispatch_worker" {
		completionValues["worker_attempt_id"] = workflowFieldStringDefault(fields, "attempt_id", "")
	}
	events = append(events, workflowTypedEvent(in.request.OperationID+":completed", WorkflowActionCompleted, in.request.WorkID, in.eventActor, in.request.Now, resultVersion-1, completionValues))
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
func applyCompleteWorkflowActionTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, entry RegisteredDefinition, request WorkflowActionExecutionRequest, currentStep, actor string, payload json.RawMessage) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	completion, completionErr := workflowCompletionEvent(ctx, tx, request, entry.Definition, currentStep, actor, payload)
	if completionErr != nil {
		return result, completionErr
	}
	if err := CompleteWorkflowTxWithRegistry(ctx, tx, registry, completion); err != nil {
		return result, err
	}
	result.EventIDs = []string{completion.EventID}
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
