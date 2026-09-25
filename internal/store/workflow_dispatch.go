package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	// EvidenceKinds carries the per-entry evidence kind for each locator the
	// caller submitted, in submission order. The bind-family event
	// constructors read it so one locator submitted under two kinds binds two
	// events, one per kind; an empty entry takes the payload's evidence_kind
	// default. It stays aligned to the submitted array, not to the merged set
	// the dispatch writes back into EvidenceRefs.
	EvidenceKinds   []string
	Actor           WorkflowActor
	SessionWorktree string
	// SessionWorktreeIdentity is computed by the core after the session
	// worktree matches its active claim. It binds later worker evidence to that
	// claim without placing a machine path in a public event.
	SessionWorktreeIdentity string
	// OperatorActor and OperatorApprovalRef are populated only after the signed
	// approval for an operator-authorized action has been verified and consumed.
	// It is never decoded from workflow action payload.
	OperatorActor       *WorkflowActor
	OperatorApprovalRef string
	// EscalatedRetryApproved is set only by the approval-gated mutation
	// boundary, in the same transaction where it consumed the operator
	// approval bound to this escalated correction. A boundary callback error
	// rolls the transaction back, so the dispatch fold sees the flag only
	// behind a consumed approval. It is never decoded from request input, and
	// every other caller of the fold keeps the escalated wall closed.
	EscalatedRetryApproved bool
	AcceptedInputsDigest   string
	IdempotencyIdentity    string
	OperationID            string
	PrincipalRef           string
	Tool                   string
	IdempotencyKey         string
	RequestID              string
	AcceptedScope          string
	LawModifies            []string
	ContractDigest         string
	// Approval binding is copied from the authenticated mutation boundary into
	// a recovery event. The fold compares these values with the consumed
	// approval record instead of trusting the approval reference alone.
	ApprovalOperationDigest string
	ApprovalScopeJSON       string
	ApprovalVersionsJSON    string
	ApprovalConsequence     string
	Now                     time.Time
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
	if actionID == "record_verdict" {
		var currentStep, state string
		if err := s.db.QueryRowContext(ctx, `SELECT current_step,instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep, &state); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow lifecycle", true, "retry once the workflow projection is readable", err)
		}
		lateVerdictRecovery, recoveryErr := workflowLateVerdictRecoveryAvailable(ctx, s.db, workID, entry.Definition, currentStep)
		if recoveryErr != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, recoveryErr
		}
		if state != "completed" && state != "cancelled" && state != "superseded" && lateVerdictRecovery {
			return entry, currentActionDefinition("record_verdict", true), nil
		}
	}
	if actionID == "supersede_contract" {
		var currentStep, state string
		if err := s.db.QueryRowContext(ctx, `SELECT current_step,instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep, &state); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow lifecycle", true, "retry once the workflow projection is readable", err)
		}
		if state == "cancelled" || state == "superseded" {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "contract recovery is unavailable for terminal work", false, "start a successor workflow")
		}
		// A completed workflow instance whose work item is still nonterminal
		// is the state the complete-step correction route recovers: completion
		// under a contract the operator has disproved. Terminality follows the
		// work item lifecycle, not the instance state alone.
		var lifecycle string
		if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot read the work item lifecycle", true, "retry once the work item is readable", err)
		}
		if lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded" {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "contract recovery is unavailable for terminal work", false, "start a successor workflow")
		}
		if workflowCompletedInstanceSupersedeOffShape(state, entry.Definition, currentStep) {
			// A completed instance off the supported pinned complete-step
			// shape keeps every recovery route closed; only the complete-step
			// admission reopens it.
			return RegisteredDefinition{}, WorkflowActionDefinition{}, workflowCompletedInstanceOffShapeFailure("workflow_action")
		}
		var activeCount int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeCount); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect active workflow contract", true, "retry once the workflow projection is readable", err)
		}
		if activeCount == 0 {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvariantViolation, "workflow_action", "contract recovery requires an active workflow contract", false, "rebuild the workflow contract projection")
		}
		if workflowCompleteStepCorrectionStep(entry.Definition, currentStep) {
			// The pinned complete step exposes one admission: the shared
			// complete-step state gate. Duplicate and stale-law recovery stay
			// on their declared earlier steps.
			available, gateErr := workflowCompleteStepCorrectionAvailable(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action")
			if gateErr != nil {
				return RegisteredDefinition{}, WorkflowActionDefinition{}, gateErr
			}
			if !available {
				return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
			}
			return entry, workflowContractRecoveryActionDefinition(), nil
		}
		if activeCount > 1 {
			return entry, workflowContractRecoveryActionDefinition(), nil
		}
		if err := checkWorkflowLawRevisionStalenessReadTx(ctx, s.db, workID); err != nil {
			var failure *Failure
			if failureAs(err, &failure) && failure.Kind == KindStaleLawRevision {
				return entry, workflowContractRecoveryActionDefinition(), nil
			}
			if !failureAs(err, &failure) || failure.Kind != KindDomainOverlap {
				return RegisteredDefinition{}, WorkflowActionDefinition{}, err
			}
		}
		correction, correctionErr := workflowContractCorrectionAvailable(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action")
		if correctionErr != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, correctionErr
		}
		if correction {
			return entry, workflowContractRecoveryActionDefinition(), nil
		}
		return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
	}
	if actionID == "record_worker_failure" && !containsString(entry.Definition.AvailableActions, actionID) {
		var currentStep string
		if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow step", true, "retry once the workflow projection is readable", err)
		}
		available, recoveryErr := workflowWorkerFailureRecoveryAvailable(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action")
		if recoveryErr != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, recoveryErr
		}
		if available {
			return entry, workerFailureRecoveryActionDefinition(), nil
		}
	}
	if actionID == "reject_worker_result" {
		var currentStep string
		if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow step", true, "retry once the workflow projection is readable", err)
		}
		if !stepDeclaresAction(entry.Definition, currentStep, "dispatch_worker") {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "worker result rejection is unavailable outside a worker-dispatch step", false, "reread_entities")
		}
		available, correctionErr := workflowRejectedWorkerResultAvailable(ctx, s.db, workID, currentStep, "workflow_action")
		if correctionErr != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, correctionErr
		}
		if available {
			return entry, workflowCorrectionActionDefinition(), nil
		}
		return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "worker result rejection is unavailable without a completed result", false, "accept or reject the completed worker result")
	}
	if actionID == "request_correction" {
		var currentStep string
		if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow step", true, "retry once the workflow projection is readable", err)
		}
		available, correctionErr := workflowCorrectionRequestAvailable(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action")
		if correctionErr != nil {
			return RegisteredDefinition{}, WorkflowActionDefinition{}, correctionErr
		}
		if available {
			return entry, workflowCorrectionRequestActionDefinition(), nil
		}
		return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindInvalidOperation, "workflow_action", "correction request is unavailable without a current non-ok verification verdict", false, "reread the current work pin")
	}
	for _, action := range entry.Definition.ActionDefinitions {
		if action.ID == actionID {
			return entry, action, nil
		}
	}
	return RegisteredDefinition{}, WorkflowActionDefinition{}, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared by the pinned definition", false, "reread_entities")
}

// ApplyWorkflowActionTx records one action's durable operation and event-folded
// result. The transaction's fold region owner supplies the scope; without one
// the action opens its own scope for this transaction. Every declared semantic
// action is translated to its closed event family here. The dispatcher is
// deliberately the only place where public action IDs acquire domain meaning.
func ApplyWorkflowActionTx(ctx context.Context, transaction *Transaction, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	tx, err := transactionSQL(transaction, "workflow_action")
	if err != nil {
		return WorkflowActionExecutionResult{}, err
	}
	scope := transaction.fold
	if scope == nil {
		scope = newFoldScope(tx)
	}
	return applyWorkflowActionRawTx(ctx, tx, scope, registry, request)
}

func applyWorkflowActionRawTx(ctx context.Context, tx *sql.Tx, scope *foldScope, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	if tx == nil {
		return result, newFailure(KindInvalidOperation, "workflow_action", "transaction is not open", false, "supply an active store transaction")
	}
	if scope == nil {
		return result, newFailure(KindInvalidOperation, "workflow_action", "fold scope is required", false, "open the fold scope with beginFold")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, request.WorkID)
	if err != nil {
		return result, err
	}
	var workerClaimedWorktree string
	if request.ActionID == "dispatch_worker" {
		claimed, claimErr := activeWorkerClaimedWorktree(ctx, tx, request.WorkID, request.SessionWorktree)
		if claimErr != nil {
			return result, claimErr
		}
		canonical, canonicalErr := canonicalWorkerWorktreePath(request.SessionWorktree)
		if canonicalErr != nil {
			return result, newFailure(KindUnauthorizedDispatch, "worker_dispatch", "host session worktree identity cannot be resolved", false, "refresh the host session boundary")
		}
		request.SessionWorktreeIdentity = workerWorktreeIdentity(canonical)
		workerClaimedWorktree = claimed
	}
	var currentStep, state, lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,instance_state,(SELECT lifecycle FROM work_items WHERE id=workflow_instances.work_id),(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &state, &lifecycle, &version); err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow state", true, "retry once the database is readable", err)
	}
	if request.ExpectedVersion != version {
		conflict, conflictErr := versionConflictForQuery(ctx, tx, SubjectWorkItem, request.WorkID, request.ExpectedVersion, version, true)
		if conflictErr != nil {
			return result, conflictErr
		}
		return result, conflict
	}
	if workflowCompletedInstanceActionImmutable(state, request.ActionID, lifecycle) {
		return result, newFailure(KindInvalidOperation, "workflow_action", "terminal workflow instance is immutable", false, "start a successor workflow")
	}
	guards := &workflowActionGuardContext{ctx: ctx, tx: tx, request: request, entry: entry, currentStep: currentStep, instanceState: state}
	if request.ActionID == "record_worker_failure" {
		guards.workerFailureRecovery, err = workflowWorkerFailureRecoveryAvailable(ctx, tx, request.WorkID, entry.Definition, currentStep, "workflow_action")
		if err != nil {
			return result, err
		}
	}
	if request.ActionID == "reject_worker_result" && stepDeclaresAction(entry.Definition, currentStep, "dispatch_worker") {
		guards.correctionRecovery, err = workflowRejectedWorkerResultAvailable(ctx, tx, request.WorkID, currentStep, "workflow_action")
		if err != nil {
			return result, err
		}
	}
	if request.ActionID == "request_correction" {
		guards.correctionRequestRecovery, err = workflowCorrectionRequestAvailable(ctx, tx, request.WorkID, entry.Definition, currentStep, "workflow_action")
		if err != nil {
			return result, err
		}
	}
	var retryCorrection *WorkflowCorrectionContext
	if request.ActionID == "dispatch_worker" {
		correction, correctionErr := workflowCorrectionContext(ctx, tx, request.WorkID, currentStep)
		if correctionErr != nil {
			return result, correctionErr
		}
		retryCorrection = correction
		if correction != nil && correction.Escalated && !request.EscalatedRetryApproved {
			return result, newFailure(KindApprovalRequired, "workflow_action", "worker correction reached the three-attempt limit", false, "escalate the failed or rejected result to the operator")
		}
		if err := validateFailedWorkerRetryIdentity(ctx, tx, request.WorkID, currentStep, request.Payload); err != nil {
			return result, err
		}
	}
	if request.ActionID == "request_correction" {
		if err := validateCorrectionRequestPayload(ctx, tx, request.WorkID, entry.Definition, currentStep, request.Payload, "workflow_action"); err != nil {
			return result, err
		}
	}
	if request.ActionID == "reject_worker_result" {
		if err := validateRejectCorrectionPinValues(request.Payload, request.EvidenceRefs); err != nil {
			return result, err
		}
	}
	if err := runWorkflowActionGuard(guards, guardPhaseRecovery); err != nil {
		return result, err
	} else if request.ActionID == "record_verdict" && !definitionStepAllows(entry.Definition, currentStep, request.ActionID) {
		if err := guardLateVerdictRecovery(guards); err != nil {
			return result, err
		}
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
	subject := "workflow_action"
	if request.ActionID == "complete" {
		subject = "complete_workflow"
	}
	if err := guardMandatedWorkflowLawBound(ctx, tx, request.WorkID, entry.Definition, currentStep, request.ActionID, subject); err != nil {
		return result, err
	}
	if err := runWorkflowActionGuard(guards, guardPhasePostValidation); err != nil {
		return result, err
	}
	if err := guardWorkflowActionStepMatch(request.Payload, currentStep); err != nil {
		return result, err
	}
	stepAllowed := guards.staleRecovery || guards.lateVerdictRecovery || guards.workerFailureRecovery || guards.correctionRecovery || guards.correctionRequestRecovery || definitionStepAllows(entry.Definition, currentStep, request.ActionID)
	if request.ActionID == "bind_evidence" {
		var recoveryErr error
		guards.recoveryBind, recoveryErr = guardRecoveryEvidenceBind(ctx, tx, request.WorkID, entry.Definition, currentStep, request.Payload, subject)
		if recoveryErr != nil {
			return result, recoveryErr
		}
		stepAllowed = stepAllowed || guards.recoveryBind
	}
	if !stepAllowed {
		return result, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared on the current step", false, "reread_entities")
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return result, err
	}
	guards.actorRef = actorRef
	guards.eventActor = actorRef
	if err := guardRecordedActorTuple(guards); err != nil {
		return result, err
	}
	if err := guardOperatorPremiseActor(guards); err != nil {
		return result, err
	}
	payload := guards.defaultedPayload()
	if err := normalizeWorkflowActionRequest(&request); err != nil {
		return result, err
	}
	evidenceRefs, defaultVerdictEvidence, err := workflowActionEvidenceRefs(request, payload)
	if err != nil {
		return result, err
	}
	if defaultVerdictEvidence {
		// The mint binds the work's real native-run capture, so the durable
		// operation must carry that reference as evidence authority too.
		nativeRunRef, nativeErr := defaultVerdictNativeRunRef(ctx, tx, request.WorkID, entry.Definition)
		if nativeErr != nil {
			return result, nativeErr
		}
		if nativeRunRef != "" && !contains(evidenceRefs, nativeRunRef) {
			evidenceRefs = append(evidenceRefs, nativeRunRef)
		}
	}
	request.EvidenceRefs = evidenceRefs
	guards.request = request
	step, evidenceRefs, err := claimDurableWorkflowOperationTx(ctx, tx, entry, request, currentStep)
	if err != nil {
		return result, err
	}
	if err := runWorkflowActionGuard(guards, guardPhaseClaim); err != nil {
		return result, err
	}
	if err := BindResearchRelianceTx(ctx, tx, request.WorkID, request.ResearchBindings, request.Now); err != nil {
		return result, err
	}
	assemblyInput := workflowActionAssemblyInput{
		ctx: ctx, tx: tx,
		entry: entry, request: request, currentStep: currentStep, step: step, payload: payload, evidenceRefs: evidenceRefs,
		actorRef: guards.actorRef, eventActor: guards.eventActor, operatorRef: guards.operatorRef,
		actorNeedsRecord: guards.actorNeedsRecord, operatorNeedsRecord: guards.operatorNeedsRecord,
		defaultVerdictEvidence: defaultVerdictEvidence, lateVerdictRecovery: guards.lateVerdictRecovery,
	}
	assembly, err := assembleWorkflowActionEventsTx(ctx, tx, assemblyInput)
	if err != nil {
		return result, err
	}
	if request.ActionID == "dispatch_worker" && retryCorrection != nil && assembly.attemptEpoch <= retryCorrection.FailedAttemptEpoch {
		return result, newFailure(KindStaleAttempt, "workflow_action", "worker retry must open a fresh step epoch", false, "mint a new fenced worker attempt")
	}
	if request.ActionID == "complete" {
		// The assembly's event list holds the actor- and operator-recording
		// events for a tuple first seen on this action (a host restart mints
		// a new session identity). Complete is the one action that does not
		// consume the assembly's events, so they travel as a prefix here;
		// dropping them left a restarted session unable to complete (#909).
		return applyCompleteWorkflowActionTx(ctx, tx, scope, registry, entry, request, currentStep, guards.eventActor, payload, assembly.events)
	}
	var workerPacketDigest string
	assembly.events, workerPacketDigest, err = appendGenericWorkflowCompletion(assemblyInput, assembly.attemptEpoch, assembly.events)
	if err != nil {
		return result, err
	}
	result.NativeRun = assembly.nativeRun

	operationResult, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: assembly.events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): request.ExpectedVersion}}, scope)
	if err != nil {
		return result, err
	}
	result.EventIDs = operationResult.EventIDs
	result.ChangedRefs = []string{request.WorkID}
	result.OperationID = request.OperationID
	resultVersion := request.ExpectedVersion + int64(len(operationResult.EventIDs))
	result.ResultingVersion = resultVersion
	changedRef := map[string]any{"entity_kind": "work_item", "id": request.WorkID, "version": resultVersion}
	resultMap := map[string]any{"changed_refs": []any{changedRef}, "next_valid_intents": []any{}, "operation_id": request.OperationID}
	// CD-0067 D6: the dispatch_worker response surfaces worker_packet_digest
	// on result only when the action was dispatch_worker. Every other verb
	// keeps the existing minimal envelope so unrelated callers see no
	// schema drift.
	if workerPacketDigest != "" {
		resultMap["worker_packet_digest"] = workerPacketDigest
	}
	// Issue #1322: the authorized dispatch names the durable claimed worktree
	// its authorization rested on, so the adapter can gate the calling tool
	// context against a store-owned answer that survives a host process
	// restart rather than against adapter memory alone.
	if workerClaimedWorktree != "" {
		resultMap["worker_worktree"] = workerClaimedWorktree
	}
	result.Result, _ = json.Marshal(resultMap)
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

func workflowSuccessorFamily(ctx context.Context, tx *sql.Tx, source WorkflowDefinition, successorID string) (string, string, error) {
	var successorKind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, successorID).Scan(&successorKind); err != nil {
		if err == sql.ErrNoRows {
			return "", "", newFailure(KindInvalidRelation, "workflow_action", "successor work item is not recorded", false, "create the typed successor before linking it")
		}
		return "", "", wrapFailure(KindUnavailable, "workflow_action", "cannot read successor work item", true, "retry once the database is readable", err)
	}
	var pin WorkflowDefinitionPin
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, successorID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return "", "", newFailure(KindInvalidRelation, "workflow_action", "successor has no workflow instance, so its family is undetermined", false, "select a workflow definition for the successor before linking it")
		}
		return "", "", wrapFailure(KindUnavailable, "workflow_action", "cannot read successor workflow definition", true, "retry once the database is readable", err)
	}
	successor, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), pin)
	if err != nil {
		return "", "", err
	}
	if !containsWorkKind(source.CompositionRules.AllowedSuccessorWorkKinds, successor.Definition.WorkKind) {
		return "", "", newFailure(KindInvalidRelation, "workflow_action", "successor family is not allowed by the source workflow composition", false, "use an allowed forward-linked successor family")
	}
	return successorKind, pin.Ref, nil
}

// workflowActionEvidenceRefs merges the operation's evidence locators with the
// refs an action derives from its own payload, and returns that merge as a set.
// An evidence array may legally name one locator twice, once per evidence kind
// — a pull request is both the commit and the review evidence — and the stored
// result_evidence_refs records each subject once. Every derived ref below is
// appended through a contains guard, so normalizing the request's own refs here
// makes the whole merged list a set.
func workflowActionEvidenceRefs(request WorkflowActionExecutionRequest, payload json.RawMessage) ([]string, bool, error) {
	request.EvidenceRefs = dedupeWorkflowRefs(request.EvidenceRefs)
	if request.ActionID == "complete" {
		fields, err := workflowActionObject(payload)
		if err != nil {
			return nil, false, err
		}
		refs := append([]string(nil), request.EvidenceRefs...)
		if len(refs) == 0 {
			refs = dedupeWorkflowRefs(workflowFieldStrings(fields, "evidence_refs"))
		}
		return refs, false, nil
	}
	if request.ActionID == "accept_worker_result" {
		// The accepted attempt is the evidence the acceptance certifies. Its id
		// joins the operation's evidence refs so the evidence_bound fold finds
		// the acceptance as the binding's durable authority (#865).
		fields, err := workflowActionObject(payload)
		if err != nil {
			return nil, false, err
		}
		refs := append([]string(nil), request.EvidenceRefs...)
		if attemptID := workflowFieldStringDefault(fields, "attempt_id", ""); attemptID != "" && !contains(refs, attemptID) {
			refs = append(refs, attemptID)
		}
		return refs, false, nil
	}
	if request.ActionID == "bind_evidence" {
		fields, err := workflowActionObject(payload)
		if err != nil {
			return nil, false, err
		}
		refs := append([]string(nil), request.EvidenceRefs...)
		// fields.evidence_ref is the declared route for naming the immutable
		// subject. An explicit immutable_subject_ref overrides that fallback.
		reference := workflowFieldStringDefault(fields, "evidence_ref", "")
		reference = workflowFieldStringDefault(fields, "immutable_subject_ref", reference)
		if reference != "" && !contains(refs, reference) {
			refs = append(refs, reference)
		}
		return refs, false, nil
	}
	if request.ActionID != "record_verdict" {
		return append([]string(nil), request.EvidenceRefs...), false, nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return nil, false, err
	}
	if raw, present := fields["evaluation_evidence"]; present {
		refs := workflowFieldStrings(fields, "evaluation_evidence")
		if len(refs) == 0 {
			return nil, false, newFailure(KindInvalidPayload, "workflow_action", "evaluation_evidence must contain at least one evidence reference", false, "supply the bound evaluation evidence")
		}
		if string(raw) == "null" {
			return nil, false, newFailure(KindInvalidPayload, "workflow_action", "evaluation_evidence must be an array", false, "supply the bound evaluation evidence")
		}
		allRefs := append([]string(nil), request.EvidenceRefs...)
		for _, ref := range refs {
			if !contains(allRefs, ref) {
				allRefs = append(allRefs, ref)
			}
		}
		return allRefs, false, nil
	}
	if len(request.EvidenceRefs) != 0 {
		return append([]string(nil), request.EvidenceRefs...), false, nil
	}
	return []string{"evidence:" + request.OperationID}, true, nil
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

// workflowSemanticActionEvents constructs only typed, foldable events. Empty
// return means the action uses the ordinary action_completed event.
// defaultVerdictEvidence marks a record_verdict whose evaluation evidence the
// caller minted (issue #816): its refs are bound in this same event list.
// bornBoundEvidenceKinds returns the evidence kinds a defaulted verdict's
// minted evidence carries: the approved contract's required kinds in declared
// order, then the definition's required kinds not already named. Completion
// clause 1 demands every kind in that union, so the mint binds each
// evaluation ref under each kind and a defaulted verdict satisfies the clause
// by construction. A contract and definition that name no kind keep the
// review default the fold historically minted.
func bornBoundEvidenceKinds(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition) ([]string, error) {
	var required string
	contractVersion, activeErr := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action")
	if activeErr != nil {
		return nil, activeErr
	}
	if err := tx.QueryRowContext(ctx, `SELECT required_evidence FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&required); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the approved workflow contract", true, "retry once the database is readable", err)
	}
	var kinds []string
	if err := json.Unmarshal([]byte(required), &kinds); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract evidence kinds are malformed", false, "reread_entities")
	}
	for _, kind := range definition.RequiredEvidenceKinds {
		if !contains(kinds, string(kind)) {
			kinds = append(kinds, string(kind))
		}
	}
	if len(kinds) == 0 {
		kinds = []string{"review"}
	}
	return kinds, nil
}

// verifiedNativeRunRef returns the observation the work already captured for
// a native run, once that capture is verified. The native_run consumption
// gate resolves evidence against these two projections, so a minted
// reference can never satisfy it and the real capture is the only ref that
// can. Absence is reported as a missing capture, not as an empty string.
// defaultVerdictNativeRunRef names the capture a defaulted verdict must bind
// under the native_run kind, or the empty string when the approved contract
// requires no native run. It is the one authority for that rule: the durable
// operation's evidence authority and the minted bindings both consult it, so
// the reference they record cannot diverge.
func defaultVerdictNativeRunRef(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition) (string, error) {
	kinds, err := bornBoundEvidenceKinds(ctx, tx, workID, definition)
	if err != nil {
		return "", err
	}
	if !contains(kinds, "native_run") {
		return "", nil
	}
	return verifiedNativeRunRef(ctx, tx, workID)
}

func verifiedNativeRunRef(ctx context.Context, tx *sql.Tx, workID string) (string, error) {
	var ref string
	err := tx.QueryRowContext(ctx, `SELECT observation_id FROM workflow_native_runs WHERE work_id=? AND observation_id IS NOT NULL AND verification_state=? ORDER BY recorded_at DESC, run_id LIMIT 1`, workID, string(VerificationVerified)).Scan(&ref)
	if err == nil {
		return ref, nil
	}
	if err != sql.ErrNoRows {
		return "", wrapFailure(KindUnavailable, "workflow_action", "cannot read the work's native-run captures", true, "retry once the projection is readable", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT observation_id FROM external_observations WHERE work_id=? AND subject_kind='native_run' AND verification_state=? ORDER BY captured_at DESC, observation_id LIMIT 1`, workID, string(VerificationVerified)).Scan(&ref)
	if err == sql.ErrNoRows {
		return "", newFailure(KindMissingEvidence, "workflow_action", "the approved contract requires native_run evidence and this work holds no verified native-run capture: record the run as an external observation and verify it, then name that observation in evaluation_evidence", false, "provide_evidence")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_action", "cannot read the work's native-run captures", true, "retry once the projection is readable", err)
	}
	return ref, nil
}

// bornBoundEvidenceEvents mints the evidence rows a defaulted verdict is
// born bound under: one row per (evaluation ref, required kind), each
// consuming one expected sequence number starting at expected. The
// native_run kind is the exception. Its consumption gate demands a captured
// and verified record, so the mint binds the work's real capture under that
// kind rather than a reference it invented. The effective evaluation
// evidence is returned, because a contract requiring only native_run carries
// the capture alone and the minted reference would otherwise stay unbound.
func bornBoundEvidenceEvents(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor, eventID string, evidence []string, expected int64) ([]Event, []string, error) {
	kinds, err := bornBoundEvidenceKinds(ctx, tx, workID, definition)
	if err != nil {
		return nil, nil, err
	}
	mintedKinds := make([]string, 0, len(kinds))
	requiresNativeRun := false
	for _, kind := range kinds {
		if kind == "native_run" {
			requiresNativeRun = true
			continue
		}
		mintedKinds = append(mintedKinds, kind)
	}
	nativeRunRef := ""
	if requiresNativeRun {
		if nativeRunRef, err = verifiedNativeRunRef(ctx, tx, workID); err != nil {
			return nil, nil, err
		}
	}
	events := make([]Event, 0, len(evidence))
	effective := make([]string, 0, len(evidence)+1)
	appendBinding := func(kind, ref string) {
		events = append(events, workflowTypedEvent(eventID+":evidence:"+fmt.Sprint(len(events)), WorkflowEvidenceBound, workID, actor, request.Now, expected+int64(len(events)), map[string]any{
			"evidence_kind": kind, "immutable_subject_ref": ref, "producer_id": request.PrincipalRef,
			"producer_run_ref": request.OperationID, "producer_watermark": request.RequestID,
			"observed_at": request.Now.UTC().Format(time.RFC3339Nano),
		}))
	}
	for _, ref := range evidence {
		for _, kind := range mintedKinds {
			appendBinding(kind, ref)
		}
		if len(mintedKinds) != 0 {
			effective = append(effective, ref)
		}
	}
	if requiresNativeRun {
		appendBinding("native_run", nativeRunRef)
		if !contains(effective, nativeRunRef) {
			effective = append(effective, nativeRunRef)
		}
	}
	return events, effective, nil
}

// workflowProposalRecordedEvents builds the typed proposal event for a
// definition that declares the proposal document. A definition pinned before
// the document keeps its generic completion and emits no typed event. An
// omitted optional list stays absent, so it never reads back as an empty one.
func workflowProposalRecordedEvents(definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if definition.Version < 7 {
		return nil, nil
	}
	proposal, err := decodeWorkflowProposalContent(raw)
	if err != nil {
		return nil, err
	}
	values := map[string]any{"problem": proposal.Problem, "affected": proposal.Affected, "stakes": proposal.Stakes, "user_outcomes": proposal.UserOutcomes}
	if proposal.Constraints != nil {
		values["constraints"] = proposal.Constraints
	}
	if proposal.OpenQuestions != nil {
		values["open_questions"] = proposal.OpenQuestions
	}
	return []Event{workflowTypedEvent(eventID, WorkflowProposalRecorded, request.WorkID, actor, request.Now, expected, values)}, nil
}

func workflowSemanticActionEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, stepID, actor string, raw json.RawMessage, expected int64, defaultVerdictEvidence bool) ([]Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return nil, err
	}
	eventID := request.OperationID + ":semantic"
	switch request.ActionID {
	case "start_run", "record_health", "rollback_run", "cleanup_run":
		return workflowNativeRunPhaseEvents(request, fields, eventID, expected)
	case "checkpoint_context":
		return workflowContextCheckpointEvents(ctx, tx, request, stepID, actor, fields, eventID, expected)
	case "cross_context_boundary":
		return workflowContextBoundaryEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "record_design":
		return workflowDesignRecordEvents(definition, request, actor, raw, eventID, expected)
	case "record_proposal":
		return workflowProposalRecordedEvents(definition, request, actor, raw, eventID, expected)
	case "approve_contract":
		return workflowApproveContractEvents(ctx, tx, definition, request, actor, fields, eventID, expected)
	case "revise_candidates":
		return workflowReviseCandidatesEvents(request, actor, fields, eventID, expected)
	case "record_alignment":
		return workflowRecordAlignmentEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "supersede_contract":
		return workflowSupersedeContractEvents(ctx, tx, definition, request, actor, raw, fields, eventID, expected)
	case "accept_worker_result":
		return workflowAcceptWorkerResultEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "bind_evidence", "record_research", "record_report", "accept_decision", "approve_operation":
		return workflowEvidenceBindingEvents(request, actor, fields, eventID, expected)
	case "record_verdict":
		return workflowRecordVerdictEvents(ctx, tx, definition, request, actor, fields, eventID, expected, defaultVerdictEvidence)
	case "confirm_premise":
		return workflowConfirmPremiseEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "link_successor":
		return workflowLinkSuccessorEvents(ctx, tx, definition, request, actor, fields, eventID, expected)
	case "declare_impact":
		return workflowDeclareImpactEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "add_condition":
		return workflowAddConditionEvents(request, actor, fields, eventID, expected)
	case "resolve_condition":
		return workflowResolveConditionEvents(request, actor, fields, eventID, expected)
	case "cancel_condition":
		return workflowCancelConditionEvents(request, actor, fields, eventID, expected)
	default:
		return nil, nil
	}
}

// workflowNativeRunPhaseEvents stays in this file: the native-run status
// vocabulary guard pins NativeRunStatusAllowed(phase, status) to the
// dispatch surface (scripts/check-native-run-statuses.py).
func workflowNativeRunPhaseEvents(request WorkflowActionExecutionRequest, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
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
	if !NativeRunStatusAllowed(phase, status) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", status+" is not a "+phase+" status", false, "use the closed status vocabulary for this phase")
	}
	nativeEvent, err := buildNativeRunEvent(eventID+":native-run", request.WorkID, request.Actor, request.Now, expected, phase, runID, subjectRef, status, evidenceRef, evidenceDigest, assertedAt)
	if err != nil {
		return nil, err
	}
	return []Event{nativeEvent}, nil
}

func workflowCompletionEvent(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, definition WorkflowDefinition, stepID, actor string, raw json.RawMessage) (Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return Event{}, err
	}
	// Completion takes operator authority under the same recorded-delivery
	// condition as the verdict. Evidence and outcome gates still apply.
	if request.OperatorActor != nil {
		if err := requireOperatorVerdictExit(ctx, tx, request.WorkID); err != nil {
			return Event{}, err
		}
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
	// The completion record is derived from the log the completion gate
	// verifies, never from caller fields or literals (#856): the recorded
	// verdict kinds, the count of bound evidence events, and the premise
	// confirmation the gate itself reads.
	contractData, _, contractErr := workflowCompletionContract(ctx, tx, BuiltinWorkflowRegistry(), request.WorkID)
	if contractErr != nil {
		return Event{}, contractErr
	}
	verdicts, verdictsErr := latestWorkflowVerdicts(ctx, tx, request.WorkID, contractData.Version)
	if verdictsErr != nil {
		return Event{}, verdictsErr
	}
	finalVerdictKind := "ok"
	for _, verdict := range verdicts {
		if verdict.VerdictKind != "ok" {
			finalVerdictKind = verdict.VerdictKind
			break
		}
	}
	var boundEvidence int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=?`, request.WorkID, WorkflowEvidenceBound).Scan(&boundEvidence); err != nil {
		return Event{}, wrapFailure(KindUnavailable, "complete_workflow", "cannot count the bound workflow evidence", true, "retry once the database is readable", err)
	}
	premiseConfirmed := false
	var premiseRow int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?`, request.WorkID, contractData.Version).Scan(&premiseRow); err == nil {
		premiseConfirmed = true
	}
	payload := map[string]any{"terminal_state": "completed", "final_verdict_kind": finalVerdictKind, "verdict_actor_ref": verdictActor, "premise_confirmed": premiseConfirmed, "evidence_count": boundEvidence, "changed_refs_digest": WorkflowChangedRefsDigest([]string{request.WorkID}), "impact_verdict": impactVerdict}
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
		return newFailure(KindInvariantViolation, "workflow_action", "pending workflow questions are malformed", false, "rebuild the workflow context projection")
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

func defaultWorkflowOutcome(definition WorkflowDefinition, fields map[string]json.RawMessage) json.RawMessage {
	if raw := workflowFieldRaw(fields, "outcome"); len(raw) != 0 && string(raw) != "null" {
		return raw
	}
	if definition.OutcomeSchema.DefaultKind == PredicateOutcome {
		return json.RawMessage(`{"kind":"outcome","allowed":["completed"]}`)
	}
	return json.RawMessage(`{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_result":"pass"}`)
}

func workflowContractOutcomePredicates(definition WorkflowDefinition, fields map[string]json.RawMessage) ([]workflowContractPredicatePayload, error) {
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
	var actionGroundTruth map[string]any
	if payload := workflowFieldRaw(fields, "payload"); len(payload) != 0 {
		_ = json.Unmarshal(payload, &actionGroundTruth)
	}
	var outcomePredicates []workflowContractPredicatePayload
	if rawPredicates := workflowFieldRaw(fields, "outcome_predicates"); len(rawPredicates) != 0 {
		_ = json.Unmarshal(rawPredicates, &outcomePredicates)
	} else {
		_, hasLegacyOutcomeKind := fields["outcome_kind"]
		_, hasLegacyOutcomePayload := fields["outcome_payload"]
		_, hasLegacyOutcome := fields["outcome"]
		if !hasLegacyOutcomeKind && !hasLegacyOutcomePayload && !hasLegacyOutcome {
			outcomePredicates = []workflowContractPredicatePayload{}
		} else {
			outcomeKindForVacuity := workflowFieldStringDefault(fields, "outcome_kind", "")
			outcome := workflowFieldRaw(fields, "outcome")
			if len(outcome) != 0 && string(outcome) != "null" {
				var predicate map[string]any
				if json.Unmarshal(outcome, &predicate) == nil {
					outcomeKindForVacuity = workflowFieldStringDefaultMap(predicate, "kind", outcomeKindForVacuity)
				}
			}
			outcomePayload := workflowFieldRaw(fields, "outcome_payload")
			if len(outcomePayload) == 0 {
				outcomePayload = defaultWorkflowOutcome(definition, fields)
			}
			outcomePredicates = []workflowContractPredicatePayload{{PredicateID: "predicate:primary", OutcomeKind: outcomeKindForVacuity, OutcomePayload: outcomePayload}}
		}
	}
	for _, predicate := range outcomePredicates {
		var groundTruth map[string]any
		groundTruthValue := ""
		if json.Unmarshal(predicate.OutcomePayload, &groundTruth) == nil {
			groundTruthValue = workflowFieldStringDefaultMap(groundTruth, "ground_truth", "")
		}
		if groundTruthValue == "" {
			groundTruthValue = workflowFieldStringDefaultMap(actionGroundTruth, "ground_truth", "")
		}
		if workflowOutcomePredicateVacuous(predicate.OutcomeKind, groundTruthValue) {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "approved end-state is already satisfied", false, "supply a non-vacuous required end state")
		}
	}
	return outcomePredicates, nil
}

func workflowOutcomePredicateVacuous(kind, groundTruth string) bool {
	return (kind == "exists" && strings.HasSuffix(groundTruth, "-present")) || (kind == "absent" && strings.HasSuffix(groundTruth, "-absent"))
}

func workflowStep(definition WorkflowDefinition, id string) *WorkflowStep {
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].ID == id {
			return &definition.StepGraph.Steps[i]
		}
	}
	return nil
}

// workflowStepIsDeliveryGate names the CD-0166 delivery-gate step shape: the
// obligation action plus the two continuity holds, with the evidence-bearing
// corrective return between them on the four-action shape, so the single
// forward edge out of the step cannot be crossed without a recorded delivery.
// Read paths, guards, and folds must agree on this one predicate, and both
// shapes admit the same parked-gate recovery.
func workflowStepIsDeliveryGate(step *WorkflowStep) bool {
	if step == nil || len(step.Actions) < 3 || len(step.Actions) > 4 || step.Actions[0] != "record_delivery" {
		return false
	}
	if len(step.Actions) == 4 && step.Actions[1] != "request_correction" {
		return false
	}
	return step.Actions[len(step.Actions)-2] == "checkpoint_context" && step.Actions[len(step.Actions)-1] == "cross_context_boundary"
}

func nullableWorkflowText(value string) any {
	if value == "" {
		return "{}"
	}
	return value
}

// workerAttemptEvidenceKind maps a lane's capability class onto the closed
// evidence kind its accepted report binds as.
func workerAttemptEvidenceKind(capability string) string {
	switch capability {
	case "verification":
		return "verification"
	case "review":
		return "review"
	default:
		return "artifact"
	}
}

// Operator evaluation requires an advancing action from the pinned workflow's
// preceding step, not merely a completed worker attempt.
func requireOperatorVerdictExit(ctx context.Context, tx *sql.Tx, workID string) error {
	registered, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return err
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "workflow_action", "workflow instance does not exist", false, "initialize the workflow before requesting operator evaluation")
		}
		return wrapFailure(KindUnavailable, "workflow_action", "cannot inspect the current workflow step", true, "retry once the database is readable", err)
	}
	if workflowStep(registered.Definition, currentStep) == nil {
		return newFailure(KindInvariantViolation, "workflow_action", "current workflow step is outside the pinned definition", false, "reread the workflow definition pin")
	}
	verdictStep := currentStep
	if !containsString(workflowStep(registered.Definition, verdictStep).Actions, "record_verdict") {
		visited := map[string]bool{currentStep: true}
		queue := []string{currentStep}
		verdictStep = ""
		for len(queue) != 0 && verdictStep == "" {
			stepID := queue[0]
			queue = queue[1:]
			for _, edge := range registered.Definition.StepGraph.Edges {
				if edge.To != stepID || edge.Kind != WorkflowEdgeForward || visited[edge.From] {
					continue
				}
				visited[edge.From] = true
				step := workflowStep(registered.Definition, edge.From)
				if step != nil && containsString(step.Actions, "record_verdict") {
					verdictStep = step.ID
					break
				}
				queue = append(queue, edge.From)
			}
		}
		if verdictStep == "" {
			return newFailure(KindInvalidOperation, "workflow_action", "operator verdict identity requires a record_delivery or accept_worker_result exit or another definition-backed advancing exit", false, "request operator evaluation from the pinned verdict step")
		}
	}
	preceding := make(map[string]bool)
	for _, edge := range registered.Definition.StepGraph.Edges {
		if edge.To == verdictStep && edge.Kind == WorkflowEdgeForward {
			preceding[edge.From] = true
		}
	}
	advancingActions := make(map[string]map[string]bool)
	for stepID := range preceding {
		step := workflowStep(registered.Definition, stepID)
		if step == nil {
			continue
		}
		for _, actionID := range step.Actions {
			mode, ok := workflowActionExecutionMode(registered.Definition, actionID)
			if ok && mode == ActionAdvance {
				if advancingActions[stepID] == nil {
					advancingActions[stepID] = make(map[string]bool)
				}
				advancingActions[stepID][actionID] = true
			}
		}
	}
	if len(advancingActions) == 0 {
		return newFailure(KindInvalidOperation, "workflow_action", "operator verdict identity requires a record_delivery or accept_worker_result exit or another definition-backed advancing exit", false, "complete the pinned workflow step before requesting operator evaluation")
	}
	rows, err := tx.QueryContext(ctx, `SELECT json_extract(payload,'$.step_id'),json_extract(payload,'$.action_id') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq DESC`, SubjectWorkItem, workID, WorkflowActionCompleted)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot inspect the workflow exits", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stepID, actionID string
		if err := rows.Scan(&stepID, &actionID); err != nil {
			return wrapFailure(KindUnavailable, "workflow_action", "cannot scan the workflow exits", true, "retry once the database is readable", err)
		}
		if advancingActions[stepID][actionID] {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read the workflow exits", true, "retry once the database is readable", err)
	}
	return newFailure(KindInvalidOperation, "workflow_action", "operator verdict identity requires a record_delivery or accept_worker_result exit or another definition-backed advancing exit", false, "complete the pinned workflow step before requesting operator evaluation")
}
