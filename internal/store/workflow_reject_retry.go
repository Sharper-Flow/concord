package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// rejectRetryIntent is the closed retry dispatch intent a rejected result can
// carry (CD-0198 D5): the lane to re-dispatch. Every other member of the
// combined operation is derived, so the caller authors no attempt identity
// and no packet bytes.
type rejectRetryIntent struct {
	LaneID string
}

// parseRejectRetryIntent reads the optional retry member off a
// reject_worker_result payload. The member is closed: unknown fields and a
// missing lane_id refuse before any write. On a pinned definition that does
// not derive the workflow failure record the member refuses too — the
// combined route is new-pin behavior, and released pins keep the payload
// they shipped with.
func parseRejectRetryIntent(definition WorkflowDefinition, payload json.RawMessage) (*rejectRetryIntent, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return nil, err
	}
	raw, present := fields["retry"]
	if !present || string(raw) == "null" {
		return nil, nil
	}
	if !workflowDerivedWorkerFailureActive(definition) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result retry dispatch is not declared by the pinned workflow definition", false, "reject the result without the retry payload, or pin a definition that derives the worker failure record")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var intent struct {
		LaneID string `json:"lane_id"`
	}
	if err := decoder.Decode(&intent); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result retry is not one closed retry object with a lane_id", false, "carry retry:{lane_id} only")
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result retry is not one closed retry object with a lane_id", false, "carry retry:{lane_id} only")
	}
	if !validLaneID(intent.LaneID) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result retry lane_id is not a registered lane identity", false, "name a registered lane id")
	}
	return &rejectRetryIntent{LaneID: intent.LaneID}, nil
}

// resolveLaneByID resolves the one current registry definition a lane id
// names. The generated lane registry carries exactly one current revision
// per id; a drift there is a registry corruption the resolution refuses.
func resolveLaneByID(laneID string) (LaneDefinition, error) {
	var found []LaneDefinition
	for _, candidate := range BuiltinLaneDefinitions() {
		if candidate.ID == laneID {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return LaneDefinition{}, newFailure(KindLaneDefinitionNotRegistered, "workflow_action", "reject_worker_result retry names a lane that is not registered", false, "name a registered lane id")
	default:
		return LaneDefinition{}, newFailure(KindLaneDefinitionNotRegistered, "workflow_action", "reject_worker_result retry names a lane whose registry carries more than one current revision", false, "repair the generated lane registry")
	}
}

// retryDispatchRecord is what the combined rejection response serializes
// about the retry dispatch the core committed: the exact admitted packet
// bytes, the minted fresh attempt identity, the digest the fresh dispatch
// window was opened for, and the durable claimed worktree the recursive
// dispatch recorded. The adapter launches through these values alone; it
// builds no packet and re-authorizes nothing.
type retryDispatchRecord struct {
	AttemptID string
	Packet    json.RawMessage
	Digest    string
	Worktree  string
}

// applyRejectRetryDispatchTx derives and applies the retry dispatch a
// combined rejection carries, in the rejection's own transaction and through
// the one dispatch owner: the same admission, guards, and packet rules every
// dispatch_worker call answers to. The caller supplies only the lane; the
// core mints the fresh fenced attempt identity, derives the canonical lane
// packet from the recorded state the rejection just committed, and binds the
// same job revision the rejected attempt was dispatched under. A refusal —
// including the CD-0173 escalation wall the post-rejection state arms —
// fails the whole transaction, so the rejection commits no partial writes
// and the rejection-only escape stays available.
func applyRejectRetryDispatchTx(ctx context.Context, tx *sql.Tx, scope *writeScope, registry DefinitionRegistry, request WorkflowActionExecutionRequest, entry RegisteredDefinition, intent *rejectRetryIntent) (WorkflowActionExecutionResult, retryDispatchRecord, error) {
	var result WorkflowActionExecutionResult
	var record retryDispatchRecord
	fields, err := workflowActionObject(request.Payload)
	if err != nil {
		return result, record, err
	}
	rejectedAttempt := workflowFieldStringDefault(fields, "attempt_id", "")
	lane, err := resolveLaneByID(intent.LaneID)
	if err != nil {
		return result, record, err
	}
	retryAttemptID := request.OperationID + workflowDerivedRetryAttemptIDSuffix
	// The derived identities answer to the published bounds before any
	// write: the attempt id, the retry idempotency key, and the retry
	// request id each carry the suffix, and the idempotency key's bound is
	// a structural CHECK on the durable key tables. Appending past a bound
	// would either fail the key insert as a raw constraint error or record
	// an identity the published contract never admits, so the combined
	// refusal is typed here, before this transaction writes anything.
	if len(retryAttemptID) > 128 || len(request.IdempotencyKey)+len(workflowDerivedRetryAttemptIDSuffix) > maxRetryDerivedKeyLength || len(request.RequestID)+len(workflowDerivedRetryAttemptIDSuffix) > maxRetryDerivedKeyLength {
		return result, record, newFailure(KindInvalidPayload, "workflow_action", "the derived retry identity exceeds the bounded attempt, idempotency-key, or request-id length", false, "retry the rejection with a shorter operation identity, or reject without the retry payload")
	}
	currentStep, version, lifecycle, err := workflowInstanceStateTx(ctx, tx, request.WorkID)
	if err != nil {
		return result, record, err
	}
	if lifecycle != "" && lifecycle == "completed" {
		return result, record, newFailure(KindInvalidOperation, "workflow_action", "a completed workflow instance carries no retry dispatch", false, "start a successor workflow")
	}
	packetRaw, err := deriveRetryWorkerPacketTx(ctx, tx, entry.Definition, request.WorkID, currentStep, lane, retryAttemptID, version, rejectedAttempt)
	if err != nil {
		return result, record, err
	}
	retryPayload, err := json.Marshal(map[string]any{"attempt_id": retryAttemptID, "worker_packet": packetRaw})
	if err != nil {
		return result, record, workflowProjectionError(err, "cannot encode the retry dispatch payload")
	}
	// The derived retry reuses the one proof prepared before the outer
	// rejection transaction. A changed work-context view or claim cannot
	// borrow that proof; the whole combined operation refuses rather than
	// splitting the rejection from the retry.
	if err := revalidateDispatchProofForRetryTx(ctx, tx, request.WorkID); err != nil {
		return result, record, err
	}
	sum := sha256.Sum256(retryPayload)
	retryRequest := request
	retryRequest.ActionID = "dispatch_worker"
	retryRequest.Payload = retryPayload
	retryRequest.ExpectedVersion = version
	retryRequest.OperationID = retryAttemptID
	retryRequest.IdempotencyIdentity = retryAttemptID
	retryRequest.IdempotencyKey = request.IdempotencyKey + ":retry"
	retryRequest.RequestID = request.RequestID + ":retry"
	retryRequest.AcceptedInputsDigest = "sha256:" + hex.EncodeToString(sum[:])
	retryResult, err := applyWorkflowActionRawTx(withWorkerDispatchRetryRecursion(ctx), tx, scope, registry, retryRequest)
	if err != nil {
		return result, record, err
	}
	record = retryDispatchRecord{AttemptID: retryAttemptID, Packet: packetRaw}
	if digest, ok := workflowResultStringField(retryResult.Result, "worker_packet_digest"); ok {
		record.Digest = digest
	}
	if worktree, ok := workflowResultStringField(retryResult.Result, "worker_worktree"); ok {
		record.Worktree = worktree
	}
	return retryResult, record, nil
}

// workflowDerivedRetryAttemptIDSuffix names the fresh attempt a combined
// rejection mints. The suffix keys the attempt to the rejection operation,
// so the identity is unique by construction and a retried combined call
// cannot mint a second one.
const workflowDerivedRetryAttemptIDSuffix = ":retry"

// maxRetryDerivedKeyLength is the published 128-byte bound the retry
// idempotency key and retry request id answer to. The suffix must fit under
// it beside the caller's key; a key with no room for the suffix refuses
// whole instead of recording an identity the contract never admits.
const maxRetryDerivedKeyLength = 128

// workflowInstanceStateTx reads the instance's current step, the work
// version, and the work lifecycle in the caller's transaction.
func workflowInstanceStateTx(ctx context.Context, tx *sql.Tx, workID string) (string, int64, string, error) {
	var currentStep, lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,(SELECT version FROM work_items WHERE id=wi.work_id),(SELECT lifecycle FROM work_items WHERE id=wi.work_id) FROM workflow_instances wi WHERE wi.work_id=?`, workID).Scan(&currentStep, &version, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return "", 0, "", newFailure(KindProjectionNotFound, "workflow_action", "workflow instance is not recorded", false, "reread_entities")
		}
		return "", 0, "", wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow state", true, "retry once the database is readable", err)
	}
	return currentStep, version, lifecycle, nil
}

// workerPacketSchemaIdentity resolves the packet schema version and the
// report protocol from the generated worker-packet contract, so the derived
// packet reads the current published format instead of restating it.
func workerPacketSchemaIdentity() (string, string, error) {
	doc := payloadschema.Document()
	defs, _ := doc["$defs"].(map[string]any)
	def, _ := defs["worker_packet"].(map[string]any)
	if def == nil {
		return "", "", newFailure(KindInvariantViolation, "workflow_action", "the generated payload contracts carry no current worker packet definition", false, "regenerate the payload contracts")
	}
	properties, _ := def["properties"].(map[string]any)
	schemaVersion, err := payloadSchemaLastEnumString(properties, "schema_version")
	if err != nil {
		return "", "", err
	}
	inputs, _ := properties["inputs"].(map[string]any)
	inputProperties, _ := inputs["properties"].(map[string]any)
	if inputProperties == nil {
		return "", "", newFailure(KindInvariantViolation, "workflow_action", "the generated worker packet contract carries no inputs object", false, "regenerate the payload contracts")
	}
	reportProtocol, err := payloadSchemaConstString(inputProperties, "report_protocol")
	if err != nil {
		return "", "", err
	}
	return schemaVersion, reportProtocol, nil
}

// payloadSchemaLastEnumString reads the last enum entry of one named string
// property off a generated schema object. The packet schema version is the
// last entry the generated contract declares.
func payloadSchemaLastEnumString(properties map[string]any, name string) (string, error) {
	refuse := func() error {
		return newFailure(KindInvariantViolation, "workflow_action", "the generated payload contract carries no readable "+name, false, "regenerate the payload contracts")
	}
	property, _ := properties[name].(map[string]any)
	entries, _ := property["enum"].([]any)
	if len(entries) == 0 {
		return "", refuse()
	}
	value, _ := entries[len(entries)-1].(string)
	if value == "" {
		return "", refuse()
	}
	return value, nil
}

// payloadSchemaConstString reads the const of one named string property off a
// generated schema object.
func payloadSchemaConstString(properties map[string]any, name string) (string, error) {
	refuse := func() error {
		return newFailure(KindInvariantViolation, "workflow_action", "the generated payload contract carries no readable "+name, false, "regenerate the payload contracts")
	}
	property, _ := properties[name].(map[string]any)
	value, _ := property["const"].(string)
	if value == "" {
		return "", refuse()
	}
	return value, nil
}

// deriveRetryWorkerPacketTx builds the canonical lane packet a retry dispatch
// binds, from the recorded state and the same readers the dispatch packet
// owner validates against. Every member is derived, never authored: the
// premise or recorded question is the task, the binding carries the admitted
// work and contract versions and the lane's assigned result, the correction
// carries the rejection the combined call just recorded, the worker job
// carries the same revision the rejected attempt was dispatched under, and
// the recorded records, work context, checkpoint, and typed outcome
// predicates ride verbatim from their owners. The dispatch path revalidates
// every member against the same owners, so a divergence refuses closed
// instead of dispatching wrong bytes.
func deriveRetryWorkerPacketTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, workID, currentStep string, lane LaneDefinition, attemptID string, admittedVersion int64, rejectedAttempt string) (json.RawMessage, error) {
	schemaVersion, reportProtocol, err := workerPacketSchemaIdentity()
	if err != nil {
		return nil, err
	}
	task, objectiveSource, contractVersionValue, err := retryPacketObjectiveTx(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	assigned, ok := WorkerScopeAssignedResult(lane.ID)
	if !ok {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "the retry lane carries no worker-scope assignment", false, "retry on a lane the worker-scope contract bounds")
	}
	binding := map[string]any{"objective_source": objectiveSource, "work_version": admittedVersion, "assigned_result": assigned}
	if contractVersionValue == nil {
		binding["contract_version"] = nil
	} else {
		binding["contract_version"] = contractVersionValue
	}
	inputs := map[string]any{"task": task, "binding": binding, "report_protocol": reportProtocol}
	reviewInput, err := currentWorkerReviewInput(ctx, tx, workID, lane, definition)
	if err != nil {
		return nil, err
	}
	if reviewInput != nil {
		inputs["review_input"] = reviewInput
	}
	if err := retryPacketWorkerJobTx(ctx, tx, definition, workID, currentStep, lane, rejectedAttempt, inputs); err != nil {
		return nil, err
	}
	if err := retryPacketRecordsTx(ctx, tx, workID, inputs); err != nil {
		return nil, err
	}
	if err := retryPacketCorrectionTx(ctx, tx, workID, currentStep, attemptID, inputs); err != nil {
		return nil, err
	}
	if err := retryPacketWorkContextTx(ctx, tx, workID, inputs); err != nil {
		return nil, err
	}
	if err := retryPacketCheckpointTx(ctx, tx, workID, inputs); err != nil {
		return nil, err
	}
	if err := retryPacketOutcomePredicatesTx(ctx, tx, workID, inputs); err != nil {
		return nil, err
	}
	packet := map[string]any{
		"schema_version": schemaVersion, "attempt_id": attemptID,
		"lane_id": lane.ID, "lane_digest": lane.Digest,
		"work_id": workID, "step_id": currentStep, "inputs": inputs,
	}
	return json.Marshal(packet)
}

// retryPacketObjectiveTx resolves the packet's task and binding authority
// exactly as the packet binding owner validates them: the active contract's
// premise when one stands, the recorded question when none does.
func retryPacketObjectiveTx(ctx context.Context, tx *sql.Tx, workID string) (string, string, any, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action")
	if err != nil && err != sql.ErrNoRows {
		return "", "", nil, err
	}
	if err == sql.ErrNoRows {
		question, err := readRecordedWorkQuestion(ctx, tx, workID)
		if err != nil {
			return "", "", nil, err
		}
		return question, "work_question", nil, nil
	}
	var premise string
	if err := tx.QueryRowContext(ctx, `SELECT premise FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, contractVersion).Scan(&premise); err != nil {
		return "", "", nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the active workflow contract premise", true, "retry once the workflow contract projection is readable", err)
	}
	return premise, "contract_premise", contractVersion, nil
}

// retryPacketWorkerJobTx binds the same worker-job revision the rejected
// attempt was dispatched under, projected from the recorded revision. A
// definition or lane that carries no job binds no member, exactly as the
// packet job owner validates.
func retryPacketWorkerJobTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, workID, currentStep string, lane LaneDefinition, rejectedAttempt string, inputs map[string]any) error {
	if !workflowWorkerJobsActive(definition) || !stepDeclaresAction(definition, currentStep, "record_worker_job") || !workerJobCapabilityClass(lane.CapabilityClass) {
		return nil
	}
	found, binding, err := dispatchCompletionJobForAttempt(ctx, tx, workID, rejectedAttempt)
	if err != nil {
		return err
	}
	if !found || binding == nil {
		return newFailure(KindInvalidPayload, "workflow_action", "the rejected attempt carries no worker-job binding to retry", false, "record the bounded job with record_worker_job, then reject with the retry payload")
	}
	if err := requireWorkerJobRevisionReadyTx(ctx, tx, workID, *binding); err != nil {
		return err
	}
	views, err := readWorkerJobRevisions(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, view := range views {
		if view.Binding != *binding {
			continue
		}
		job := packetJobFromView(view)
		// A size-capable implementation retry carries the same computed
		// caller exclusions first dispatch would, so the packet binds the
		// work's other corrections identically.
		if lane.CapabilityClass == "implementation" && workflowCallerScopeActive(definition) {
			exclusions, exclusionsErr := workerJobCallerExclusionsTx(ctx, tx, workID, view.Binding.JobID)
			if exclusionsErr != nil {
				return exclusionsErr
			}
			job.CallerExclusions = exclusions
		}
		inputs["worker_job"] = job
		return nil
	}
	return newFailure(KindInvalidPayload, "workflow_action", "the rejected attempt's worker-job binding names no recorded revision", false, "record the bounded job with record_worker_job before retrying it")
}

// retryPacketRecordsTx rides the recorded law context, design record,
// proposal, and work text from the readers the packet records owner
// validates against.
func retryPacketRecordsTx(ctx context.Context, tx *sql.Tx, workID string, inputs map[string]any) error {
	if lawContext, err := readActiveWorkflowLawContext(ctx, tx, workID); err != nil {
		return err
	} else if lawContext != nil {
		inputs["law_context"] = lawContext
	}
	if design, err := readCurrentWorkflowDesign(ctx, tx, workID); err != nil {
		return err
	} else if design != nil {
		inputs["design_record"] = design
	}
	if proposal, err := readLatestWorkerPacketProposal(ctx, tx, workID); err != nil {
		return err
	} else if proposal != nil {
		inputs["proposal_record"] = proposal
	}
	if work, err := readWorkerPacketWorkRecord(ctx, tx, workID); err != nil {
		return err
	} else if work != nil {
		inputs["work_record"] = work
	}
	return nil
}

// retryPacketCorrectionTx rides the correction context the rejection just
// committed, derived by the one correction owner and consumed the way the
// packet correction owner validates.
func retryPacketCorrectionTx(ctx context.Context, tx *sql.Tx, workID, currentStep, attemptID string, inputs map[string]any) error {
	correction, err := workflowCorrectionContextForDispatch(ctx, tx, workID, currentStep, attemptID)
	if err != nil {
		return err
	}
	if correction != nil {
		inputs["correction"] = correction
	}
	return nil
}

// retryPacketWorkContextTx rides the current work-context view when one
// stands.
func retryPacketWorkContextTx(ctx context.Context, tx *sql.Tx, workID string, inputs map[string]any) error {
	view, err := readWorkContextView(ctx, tx, workID)
	if err != nil {
		return err
	}
	if view != nil {
		inputs["work_context"] = view
	}
	return nil
}

// retryPacketCheckpointTx rides the latest context checkpoint when one
// stands, from the reader the packet checkpoint owner validates against.
func retryPacketCheckpointTx(ctx context.Context, tx *sql.Tx, workID string, inputs map[string]any) error {
	checkpoint, err := readLatestContextCheckpointTx(ctx, tx, workID, "workflow_action")
	if err != nil {
		return err
	}
	if checkpoint != nil {
		inputs["checkpoint"] = checkpoint
	}
	return nil
}

// retryPacketOutcomePredicatesTx rides the pinned contract's typed outcome
// predicates, decoded into the closed predicate objects the packet schema
// and the dispatch obligation fold both read.
func retryPacketOutcomePredicatesTx(ctx context.Context, tx *sql.Tx, workID string, inputs map[string]any) error {
	contractVersion, err := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action")
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		return nil
	}
	predicates, err := readWorkflowContractPredicates(ctx, tx, workID, contractVersion)
	if err != nil {
		return err
	}
	if len(predicates) == 0 {
		return nil
	}
	decoded := make([]map[string]any, 0, len(predicates))
	for _, predicate := range predicates {
		var payload json.RawMessage
		if json.Unmarshal([]byte(predicate.OutcomePayload), &payload) != nil {
			return newFailure(KindInvariantViolation, "workflow_action", "the pinned contract's outcome predicate payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		decoded = append(decoded, map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": payload})
	}
	inputs["outcome_predicates"] = decoded
	return nil
}

// workflowResultStringField reads one string member off a marshaled action
// result envelope, so the combined response can name what the retry dispatch
// itself recorded — the packet digest the fresh window opened for and the
// claimed worktree its authorization rested on.
func workflowResultStringField(raw json.RawMessage, name string) (string, bool) {
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return "", false
	}
	value, ok := decoded[name].(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

// workflowInstanceStateVersionTx reads the work item's projected version in
// the caller's transaction.
func workflowInstanceStateVersionTx(ctx context.Context, tx *sql.Tx, workID string) (int64, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_action", "cannot read the work version", true, "retry once the work item is readable", err)
	}
	return version, nil
}

// RejectRetryMemberPresent reports whether a reject_worker_result payload
// carries the retry dispatch member, so the mutation boundary prepares the
// one dispatch proof before its transaction opens. It performs no
// validation: the closed retry parsing stays the transaction's own.
func RejectRetryMemberPresent(payload json.RawMessage) bool {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return false
	}
	raw, present := fields["retry"]
	return present && string(raw) != "null"
}
