package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
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
	// ApprovedTakeover names the held attempt whose exact approval the
	// boundary consumed for this fenced start. It is never decoded from
	// request input.
	ApprovedTakeover     *WorkflowStartTakeover
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
	// Approval binding is copied from the authenticated mutation boundary into
	// a recovery event. Admission verified and consumed the approval row
	// against these exact values; the fold re-checks the recorded binding for
	// self-consistency and the operator actor from the log, so a replay
	// re-derives the admission without the mutable approval row.
	ApprovalOperationDigest string
	ApprovalScopeJSON       string
	ApprovalVersionsJSON    string
	ApprovalConsequence     string
	Now                     time.Time
	// ResearchBindings declares the pack revisions this action's work item
	// starts relying on (CD-0216). The engine binds each consumer and proves
	// freshness fail-closed inside this action's transaction; there is no
	// standalone binding operation, because reliance declared outside the
	// boundary that consumes it is unproven reliance.
	ResearchBindings []ResearchBindingDeclaration
	// ProjectTooling carries the tooling manifest resolved from the work
	// item's primary Project default ref before this action's transaction
	// opened (CD-0192). The refine-exit proof guard compares the bound
	// worktree-verify run's argv against it; nil reports that the Project
	// declares no tooling manifest on its default ref. It is never decoded
	// from request input.
	ProjectTooling *ProjectToolingManifest
}

type WorkflowActionExecutionResult struct {
	OperationID      string
	EventIDs         []string
	ChangedRefs      []string
	ResultingVersion int64
	Result           json.RawMessage
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
	active, err := outsideRepairActiveTx(ctx, s.db, workID)
	if err != nil {
		return RegisteredDefinition{}, WorkflowActionDefinition{}, err
	}
	if active {
		decision := workflowAdmit(entry.Definition, WorkflowAdmissionState{OutsideRepairActive: true}, actionID)
		return RegisteredDefinition{}, WorkflowActionDefinition{}, decision.Failure
	}
	action, found, err := workflowRouteActionDefinition(ctx, s, registry, entry, workID, actionID)
	if err != nil {
		return RegisteredDefinition{}, WorkflowActionDefinition{}, err
	}
	if found {
		return entry, action, nil
	}
	action, err = workflowDeclaredActionDefinition(entry.Definition, actionID)
	if err != nil {
		return RegisteredDefinition{}, WorkflowActionDefinition{}, err
	}
	return entry, action, nil
}

// workflowRouteActionDefinition resolves the actions whose definition depends
// on the folded state rather than the pinned step: the late verdict, contract
// supersession, worker failure recovery, and the correction routes. found is
// false when the action falls through to its pinned declaration.
func workflowRouteActionDefinition(ctx context.Context, s *Store, registry DefinitionRegistry, entry RegisteredDefinition, workID, actionID string) (WorkflowActionDefinition, bool, error) {
	switch actionID {
	case "record_verdict":
		// Discovery returns the same hold-mode declaration as ordinary
		// association. Folded admission, not discovery, proves availability.
		action, err := workflowDeclaredActionDefinition(entry.Definition, actionID)
		return action, err == nil, err
	case "supersede_contract":
		action, err := workflowSupersedeContractDiscovery(ctx, s, registry, entry, workID)
		return action, err == nil, err
	case "record_worker_failure":
		if slices.Contains(entry.Definition.AvailableActions, actionID) {
			return WorkflowActionDefinition{}, false, nil
		}
		currentStep, err := workflowDiscoveryCurrentStep(ctx, s, workID)
		if err != nil {
			return WorkflowActionDefinition{}, false, err
		}
		available, err := workflowWorkerFailureRecoveryAvailable(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action")
		if err != nil || !available {
			return WorkflowActionDefinition{}, false, err
		}
		return workerFailureRecoveryActionDefinition(), true, nil
	case "reject_worker_result":
		currentStep, err := workflowDiscoveryCurrentStep(ctx, s, workID)
		if err != nil {
			return WorkflowActionDefinition{}, false, err
		}
		if !stepDeclaresAction(entry.Definition, currentStep, "dispatch_worker") {
			return WorkflowActionDefinition{}, false, newFailure(KindInvalidOperation, "workflow_action", "worker result rejection is unavailable outside a worker-dispatch step", false, "reread_entities")
		}
		available, err := workflowRejectedWorkerResultAvailable(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action")
		if err != nil {
			return WorkflowActionDefinition{}, false, err
		}
		if !available {
			return WorkflowActionDefinition{}, false, newFailure(KindInvalidOperation, "workflow_action", "worker result rejection is unavailable without a completed result", false, "accept or reject the completed worker result")
		}
		return workflowCorrectionActionDefinitionFor(entry.Definition, "reject_worker_result"), true, nil
	case "request_correction":
		currentStep, err := workflowDiscoveryCurrentStep(ctx, s, workID)
		if err != nil {
			return WorkflowActionDefinition{}, false, err
		}
		available, missing, err := workflowCorrectionRequestAdmissionStateForRegistry(ctx, s.db, workID, entry.Definition, currentStep, "workflow_action", 0, registry)
		if err != nil {
			return WorkflowActionDefinition{}, false, err
		}
		if !available {
			return WorkflowActionDefinition{}, false, workflowCorrectionRequestUnavailableFailure("workflow_action", missing)
		}
		return workflowCorrectionActionDefinitionFor(entry.Definition, "request_correction"), true, nil
	}
	return WorkflowActionDefinition{}, false, nil
}

func workflowDiscoveryCurrentStep(ctx context.Context, s *Store, workID string) (string, error) {
	var currentStep string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow step", true, "retry once the workflow projection is readable", err)
	}
	return currentStep, nil
}

// workflowDeclaredActionDefinition returns the action as the pinned definition
// declares it.
func workflowDeclaredActionDefinition(definition WorkflowDefinition, actionID string) (WorkflowActionDefinition, error) {
	for _, action := range definition.ActionDefinitions {
		if action.ID != actionID {
			continue
		}
		// A consequence_v1 pin's initial contract approval resolves through
		// the selected decision-fact declaration; a typed selection refusal
		// propagates and every other declared action stays exactly as pinned.
		if actionID == "approve_contract" && workflowApprovalPolicyRequiresDecisionFact(definition) {
			return workflowDecisionFactInitialActionDefinition(definition)
		}
		return action, nil
	}
	return WorkflowActionDefinition{}, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared by the pinned definition", false, "reread_entities")
}

// workflowSupersedeContractDiscovery answers supersede_contract's discovery
// from the shared admission: one read transaction folds the instance history
// through loadWorkflowAdmissionStateTx, and workflowAdmitSupersede — the same
// pure decision the preflight, the guard, the work pin, and the dispatch fold
// consume — classifies the recovery. Discovery keeps no per-site predicate of
// its own: the shape refusal, the stale-law and duplicate classification, and
// the complete-step gate are the fold's answer, so no surface can refuse a
// recovery route another surface admits.
func workflowSupersedeContractDiscovery(ctx context.Context, s *Store, registry DefinitionRegistry, entry RegisteredDefinition, workID string) (WorkflowActionDefinition, error) {
	var action WorkflowActionDefinition
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return workflowSupersedeContractDiscoveryTx(ctx, q, registry, entry, workID, &action)
	}); err != nil {
		return WorkflowActionDefinition{}, err
	}
	return action, nil
}

func workflowSupersedeContractDiscoveryTx(ctx context.Context, tx queryer, registry DefinitionRegistry, entry RegisteredDefinition, workID string, action *WorkflowActionDefinition) error {
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "workflow_action", "workflow instance is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow step", true, "retry once the workflow projection is readable", err)
	}
	state, _, _, stateErr := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, entry.Definition, currentStep, "workflow_action", registry)
	if stateErr != nil {
		return stateErr
	}
	decision := workflowAdmit(entry.Definition, state, "supersede_contract")
	if decision.Admitted {
		*action = workflowDecisionFactRecoveryActionDefinition(entry.Definition)
		return nil
	}
	return decision.Failure
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
	scope := transaction.write
	if scope == nil {
		scope = newWriteScope(tx)
	}
	return applyWorkflowActionRawTx(ctx, tx, scope, registry, request)
}

func applyWorkflowActionRawTx(ctx context.Context, tx *sql.Tx, scope *writeScope, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	var result WorkflowActionExecutionResult
	if tx == nil {
		return result, newFailure(KindInvalidOperation, "workflow_action", "transaction is not open", false, "supply an active store transaction")
	}
	if scope == nil {
		return result, newFailure(KindInvalidOperation, "workflow_action", "write scope is required", false, "open the write scope with beginWrite")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	if err := scope.bindRegistry(tx, registry); err != nil {
		return result, err
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, request.WorkID)
	if err != nil {
		return result, err
	}
	currentStep, state, err := readWorkflowActionStateTx(ctx, tx, request.WorkID, request.ExpectedVersion)
	if err != nil {
		return result, err
	}
	guards := &workflowActionGuardContext{ctx: ctx, tx: tx, request: request, entry: entry, currentStep: currentStep, instanceState: state, registry: registry}
	if WorkflowActionRequiresSubjectFreshness(request.ActionID) {
		if err := validateWorkflowAcceptanceFreshness(ctx, tx, request.WorkID); err != nil {
			return result, err
		}
	}
	if err := prepareWorkflowExecutionAdmission(guards); err != nil {
		return result, err
	}
	decision := *guards.admissionDecision
	workerClaimedWorktree, retryCorrection, rejectRetry, err := prepareWorkflowActionIntents(guards, &request)
	if err != nil {
		return result, err
	}
	if err := runWorkflowActionAdmissionGuards(guards, request, decision); err != nil {
		return result, err
	}
	if err := guardWorkflowActionActor(guards, request); err != nil {
		return result, err
	}
	payload, err := prepareWorkflowActionEvidence(guards, &request)
	if err != nil {
		return result, err
	}
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
		eventActor: guards.eventActor, operatorRef: guards.operatorRef,
		actorRecordPrefix: guards.actorRecordPrefix, operatorNeedsRecord: guards.operatorNeedsRecord,
		admission: guards.admissionState,
		registry:  registry,
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
		// without them a restarted session cannot complete.
		return applyCompleteWorkflowActionTx(ctx, tx, scope, registry, entry, request, guards.eventActor, payload, assembly.events)
	}
	var workerPacketDigest string
	assembly.events, workerPacketDigest, err = appendGenericWorkflowCompletion(assemblyInput, assembly.attemptEpoch, assembly.events)
	if err != nil {
		return result, err
	}

	operationResult, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: assembly.events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): request.ExpectedVersion}}, scope)
	if err != nil {
		return result, err
	}
	if err := checkWorkContextDeclarationTx(ctx, tx, request.WorkID, assembly.events); err != nil {
		return result, err
	}
	result.EventIDs = operationResult.EventIDs
	result.ChangedRefs = []string{request.WorkID}
	result.OperationID = request.OperationID
	result.ResultingVersion = request.ExpectedVersion + int64(len(operationResult.EventIDs))
	// CD-0198 D5: the combined rejection records its retry dispatch in the
	// same transaction, through the one dispatch owner below. The recursion
	// re-folds the post-rejection state, so the wall, the correction
	// context, and every packet rule answer from the state the rejection
	// just committed; a refusal here fails the whole transaction and the
	// rejection commits no partial writes. The response serializes the
	// exact admitted retry dispatch — the derived packet bytes, the minted
	// attempt identity, the digest the fresh window opened for, and the
	// claimed worktree the authorization rested on — so the adapter's host
	// launch routes through the core's own record and builds no packet.
	var resultMapRetryRecord retryDispatchRecord
	if rejectRetry != nil {
		retryRecord, retryErr := appendRejectRetryDispatchTx(ctx, tx, scope, registry, request, entry, rejectRetry, &result)
		if retryErr != nil {
			return result, retryErr
		}
		resultMapRetryRecord = retryRecord
	}
	changedRef := map[string]any{"entity_kind": "work_item", "id": request.WorkID, "version": result.ResultingVersion}
	resultMap := workflowActionResultMap(changedRef, request.OperationID, workerPacketDigest, resultMapRetryRecord, workerClaimedWorktree)
	if err := appendWorkflowAssignmentGroupResult(resultMap, assembly.events); err != nil {
		return result, err
	}
	result.Result, _ = json.Marshal(resultMap)
	durableChangedRef, _ := json.Marshal(changedRef)
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations SET result_kind='completed',result_payload=?,changed_refs=?,completed_at=? WHERE op_id=? AND attempt_epoch=?`, string(result.Result), workflowJSON([]string{string(durableChangedRef)}), request.Now.UTC().Format(time.RFC3339Nano), request.OperationID, 1); err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_action", "cannot complete durable workflow operation", true, "retry once the database is writable", err)
	}
	return result, nil
}

// One admission derivation for the fold: the tx-scoped loader folds the
// instance history once. Only the payload-bound review, operator approval,
// and interactive closed-question walls defer to their owning guards.
func prepareWorkflowExecutionAdmission(g *workflowActionGuardContext) error {
	request := g.request
	admission, proofRuns, integrationRuns, err := loadWorkflowAdmissionStateTxForRegistry(g.ctx, g.tx, request.WorkID, g.entry.Definition, g.currentStep, "workflow_action", g.registry)
	if err != nil {
		return err
	}
	if err := bindWorkflowReviewRequest(g.ctx, g.tx, &admission, request.WorkID, request.ActionID, request.Payload, request.Actor); err != nil {
		return err
	}
	decision := workflowAdmit(g.entry.Definition, admission, request.ActionID)
	if !decision.Admitted && !decision.OffStep && !decision.AdvanceHeld && !decision.OperatorQuestionClosed {
		return workflowExecutionAdmissionFailure(decision, request.ProjectTooling)
	}
	g.admissionState = &admission
	g.admissionDecision = &decision
	g.deliveryProofRuns = proofRuns
	g.deliveryIntegrationRuns = integrationRuns
	g.workerFailureRecovery = admission.WorkerFailureRecovery
	g.correctionRecovery = admission.CorrectionRecovery
	g.correctionRequestRecovery = admission.CorrectionRequestRecovery
	g.correctionRequestMissing = admission.CorrectionRequestMissing
	return nil
}

func appendWorkflowAssignmentGroupResult(resultMap map[string]any, events []Event) error {
	for _, event := range events {
		if event.Kind == WorkflowActionCompleted {
			var completed workflowActionCompletedPayload
			if err := json.Unmarshal(event.Payload, &completed); err != nil {
				return err
			}
			if completed.AssignmentGroup != nil {
				resultMap["assignment_group"] = struct {
					GroupID string                `json:"group_id"`
					Members []workflowGroupMember `json:"members"`
				}{completed.AssignmentGroup.GroupID, completed.AssignmentGroup.Members}
			}
		}
	}
	return nil
}

// readWorkflowActionStateTx reads the instance's current step and state and
// refuses a stale expected work version.
func readWorkflowActionStateTx(ctx context.Context, tx *sql.Tx, workID string, expectedVersion int64) (string, string, error) {
	var currentStep, state, lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,instance_state,(SELECT lifecycle FROM work_items WHERE id=workflow_instances.work_id),(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep, &state, &lifecycle, &version); err != nil {
		return "", "", wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow state", true, "retry once the database is readable", err)
	}
	if expectedVersion != version {
		conflict, err := versionConflictForQuery(ctx, tx, workID, expectedVersion, version)
		if err != nil {
			return "", "", err
		}
		return "", "", conflict
	}
	return currentStep, state, nil
}

// appendRejectRetryDispatchTx records the combined rejection's retry dispatch
// into result and returns the admitted retry record.
func appendRejectRetryDispatchTx(ctx context.Context, tx *sql.Tx, scope *writeScope, registry DefinitionRegistry, request WorkflowActionExecutionRequest, entry RegisteredDefinition, rejectRetry *rejectRetryIntent, result *WorkflowActionExecutionResult) (retryDispatchRecord, error) {
	retryResult, retryRecord, err := applyRejectRetryDispatchTx(ctx, tx, scope, registry, request, entry, rejectRetry)
	if err != nil {
		return retryDispatchRecord{}, err
	}
	result.EventIDs = append(result.EventIDs, retryResult.EventIDs...)
	version, err := workflowInstanceStateVersionTx(ctx, tx, request.WorkID)
	if err != nil {
		return retryDispatchRecord{}, err
	}
	result.ResultingVersion = version
	return retryRecord, nil
}

// prepareWorkflowActionIntents resolves the request intents the action's
// later fences read: the claimed worker worktree and session identity for
// dispatch_worker with its retry correction context, and the parsed retry
// intent of a combined reject_worker_result.
func prepareWorkflowActionIntents(guards *workflowActionGuardContext, request *WorkflowActionExecutionRequest) (workerClaimedWorktree string, retryCorrection *WorkflowCorrectionContext, rejectRetry *rejectRetryIntent, err error) {
	if request.ActionID == "dispatch_worker" {
		claimed, claimErr := activeWorkerClaimedWorktree(guards.ctx, guards.tx, request.WorkID, request.SessionWorktree)
		if claimErr != nil {
			return "", nil, nil, claimErr
		}
		canonical, canonicalErr := canonicalWorkerWorktreePath(request.SessionWorktree)
		if canonicalErr != nil {
			return "", nil, nil, newFailure(KindUnauthorizedDispatch, "worker_dispatch", "host session worktree identity cannot be resolved", false, "refresh the host session boundary")
		}
		request.SessionWorktreeIdentity = workerWorktreeIdentity(canonical)
		workerClaimedWorktree = claimed
		guards.request = *request
	}
	if request.ActionID == "dispatch_worker" {
		// The completion fence reads the correction context the wall admission
		// folded: a retry must open a fresh attempt epoch past the failed one.
		correction, correctionErr := workflowCorrectionContext(guards.ctx, guards.tx, request.WorkID, guards.currentStep)
		if correctionErr != nil {
			return "", nil, nil, correctionErr
		}
		retryCorrection = correction
		if err := validateFailedWorkerRetryIdentity(guards.ctx, guards.tx, request.WorkID, guards.currentStep, request.Payload); err != nil {
			return "", nil, nil, err
		}
	}
	if request.ActionID == "reject_worker_result" {
		if err := validateRejectCorrectionPinValues(request.Payload, request.EvidenceRefs); err != nil {
			return "", nil, nil, err
		}
		var parseErr error
		rejectRetry, parseErr = parseRejectRetryIntent(guards.entry.Definition, request.Payload)
		if parseErr != nil {
			return "", nil, nil, parseErr
		}
		// A combined rejection with a retry prepared one dispatch proof
		// before this transaction opened. The pre-rejection state the proof
		// sampled must still stand at this outer entry.
		if rejectRetry != nil {
			if err := revalidateDispatchProofAtRejectEntry(guards.ctx, guards.tx, request.WorkID); err != nil {
				return "", nil, nil, err
			}
		}
	}
	return workerClaimedWorktree, retryCorrection, rejectRetry, nil
}

// runWorkflowActionAdmissionGuards runs the recovery, boundary, and
// post-validation guard phases around payload validation, then applies the
// folded step legality after the payload-bound recoveries refine it.
func runWorkflowActionAdmissionGuards(guards *workflowActionGuardContext, request WorkflowActionExecutionRequest, decision WorkflowAdmissionDecision) error {
	if err := runWorkflowActionGuard(guards, guardPhaseRecovery); err != nil {
		return err
	}
	if err := runWorkflowActionGuard(guards, guardPhaseBoundary); err != nil {
		return err
	}
	if guards.staleRecovery {
		if err := validateWorkflowContractRecoveryPayload(guards.entry.Definition, request.Payload); err != nil {
			return err
		}
	} else if err := validateWorkflowActionPayload(guards.entry.Definition, request.ActionID, workflowEnvelopeProjectedPayload(guards.entry.Definition, request.ActionID, request.Payload, request.SelectedChoice, request.DecisionContextDigest)); err != nil {
		return err
	}
	if err := runWorkflowActionGuard(guards, guardPhasePostValidation); err != nil {
		return err
	}
	if err := guardWorkflowActionStepMatch(request.Payload, guards.currentStep); err != nil {
		return err
	}
	// The folded admission owns step legality (workflowAdmissionStepAllows),
	// applied here — the engine's late position, after the payload and guard
	// checks whose specific refusals precede the generic off-step one. The
	// evidence-binding recovery first binds the outstanding requirement the
	// request's payload names.
	if request.ActionID == "bind_evidence" {
		var recoveryErr error
		guards.recoveryBind, recoveryErr = guardRecoveryEvidenceBind(guards.ctx, guards.tx, request.WorkID, guards.entry.Definition, guards.currentStep, request.Payload, "workflow_action", request.EvidenceKinds...)
		if recoveryErr != nil {
			return recoveryErr
		}
		if !guards.recoveryBind && !definitionStepAllows(guards.entry.Definition, guards.currentStep, request.ActionID) {
			return newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action is not declared on the current step", false, "reread_entities")
		}
	} else if decision.OffStep {
		return decision.Failure
	}
	return nil
}

// guardWorkflowActionActor resolves the request actor and runs the actor
// tuple, operator premise, and held-attempt takeover guards.
func guardWorkflowActionActor(guards *workflowActionGuardContext, request WorkflowActionExecutionRequest) error {
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return err
	}
	guards.actorRef = actorRef
	guards.eventActor = actorRef
	if err := guardRecordedActorTuple(guards); err != nil {
		return err
	}
	if err := guardWorkflowMemberAction(guards.ctx, guards.tx, request); err != nil {
		return err
	}
	if err := guardOperatorPremiseActor(guards); err != nil {
		return err
	}
	if action := workflowActionDefinitionByID(guards.entry.Definition, request.ActionID); WorkflowActionOpensHeldAttempt(action) {
		if err := guardStartTakeover(guards.ctx, guards.tx, action, request.WorkID, actorRef, workflowDispatchAssignment(request.Payload), request.SessionWorktreeIdentity, request.ApprovedTakeover); err != nil {
			return err
		}
	}
	return nil
}

// prepareWorkflowActionEvidence normalizes references without creating proof.
func prepareWorkflowActionEvidence(guards *workflowActionGuardContext, request *WorkflowActionExecutionRequest) (payload json.RawMessage, err error) {
	payload = guards.defaultedPayload()
	if err := normalizeWorkflowActionRequest(request); err != nil {
		return nil, err
	}
	evidenceRefs, err := workflowActionEvidenceRefs(*request, payload)
	if err != nil {
		return nil, err
	}
	request.EvidenceRefs = evidenceRefs
	guards.request = *request
	return payload, nil
}

// workflowActionResultMap builds the action's response envelope.
func workflowActionResultMap(changedRef map[string]any, operationID, workerPacketDigest string, retryRecord retryDispatchRecord, workerClaimedWorktree string) map[string]any {
	resultMap := map[string]any{"changed_refs": []any{changedRef}, "next_valid_intents": []any{}, "operation_id": operationID}
	// CD-0067 D6: the dispatch_worker response surfaces worker_packet_digest
	// on result only when the action was dispatch_worker. Every other verb
	// keeps the existing minimal envelope so unrelated callers see no
	// schema drift.
	if workerPacketDigest != "" {
		resultMap["worker_packet_digest"] = workerPacketDigest
	}
	if retryRecord.Digest != "" {
		resultMap["worker_packet_digest"] = retryRecord.Digest
	}
	if retryRecord.AttemptID != "" {
		resultMap["retry_attempt_id"] = retryRecord.AttemptID
		resultMap["retry_worker_packet"] = retryRecord.Packet
	}
	if retryRecord.Worktree != "" {
		resultMap["worker_worktree"] = retryRecord.Worktree
	}
	// Issue #1322: the authorized dispatch names the durable claimed worktree
	// its authorization rested on, so the adapter can gate the calling tool
	// context against a store-owned answer that survives a host process
	// restart rather than against adapter memory alone.
	if workerClaimedWorktree != "" {
		resultMap["worker_worktree"] = workerClaimedWorktree
	}
	return resultMap
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
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, successorID).Scan(&pin.Ref, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return "", "", newFailure(KindInvalidRelation, "workflow_action", "successor has no workflow instance, so its family is undetermined", false, "select a workflow definition for the successor before linking it")
		}
		return "", "", wrapFailure(KindUnavailable, "workflow_action", "cannot read successor workflow definition", true, "retry once the database is readable", err)
	}
	successor, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), pin)
	if err != nil {
		return "", "", err
	}
	if !slices.Contains(source.CompositionRules.AllowedSuccessorWorkKinds, successor.Definition.WorkKind) {
		return "", "", newFailure(KindInvalidRelation, "workflow_action", "successor family is not allowed by the source workflow composition", false, "use an allowed forward-linked successor family")
	}
	return successorKind, pin.Ref, nil
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
	case "complete", "accept_worker_result", "accept_worker_evidence", "bind_evidence", "record_verdict":
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
	case "accept_worker_result", "accept_worker_evidence":
		// The accepted attempt is the evidence the acceptance certifies. Its id
		// joins the operation's evidence refs so the evidence_bound fold finds
		// the acceptance as the binding's durable authority (#865); CD-0187's
		// checkpoint accept binds under the same rule.
		if attemptID := workflowFieldStringDefault(fields, "attempt_id", ""); attemptID != "" {
			refs = dedupeWorkflowRefs(append(refs, attemptID))
		}
		return refs, nil
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

// workflowDeclaresDocument checks the pinned payload against the required
// document members owned by the action schema, not the definition's identity.
func workflowDeclaresDocument(definition WorkflowDefinition, actionID string) (bool, error) {
	action, err := workflowDeclaredActionDefinition(definition, actionID)
	if err != nil {
		return false, err
	}
	for _, member := range builtinActionPolicies[actionID].Payload.Fields {
		if member.Required && !slices.ContainsFunc(action.Payload.Fields, func(field WorkflowPayloadField) bool {
			return field.Name == member.Name
		}) {
			return false, nil
		}
	}
	return true, nil
}

// workflowProposalRecordedEvents builds the typed proposal event. An omitted
// optional list stays absent, so it never reads back as an empty one.
func workflowProposalRecordedEvents(definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if declared, err := workflowDeclaresDocument(definition, "record_proposal"); err != nil || !declared {
		return nil, err
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
	if proposal.OutOfScope != nil {
		values["out_of_scope"] = proposal.OutOfScope
	}
	return []Event{workflowTypedEvent(eventID, WorkflowProposalRecorded, request.WorkID, actor, request.Now, expected, values)}, nil
}

// workflowSemanticActionEvents constructs typed events. An empty result uses
// the ordinary action_completed event.
func workflowSemanticActionEvents(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, stepID, actor string, raw json.RawMessage, expected int64) ([]Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return nil, err
	}
	eventID := request.OperationID + ":semantic"
	switch request.ActionID {
	case "checkpoint_context":
		return workflowContextCheckpointEvents(ctx, tx, request, stepID, actor, fields, eventID, expected)
	case "cross_context_boundary":
		return workflowContextBoundaryEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "record_work_context":
		return workflowRecordWorkContextEvents(ctx, tx, request, stepID, actor, fields, eventID, expected)
	case "record_design":
		return workflowDesignRecordedEvents(definition, request, actor, raw, eventID, expected)
	case "record_proposal":
		return workflowProposalRecordedEvents(definition, request, actor, raw, eventID, expected)
	case "approve_contract":
		return workflowApproveContractEvents(ctx, tx, definition, request, actor, fields, eventID, expected)
	case "revise_candidates":
		return workflowReviseCandidatesEvents(request, actor, fields, eventID, expected)
	case "record_alignment":
		return workflowRecordAlignmentEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "record_diagnosis":
		return workflowRecordDiagnosisEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "supersede_contract":
		return workflowSupersedeContractEvents(ctx, tx, definition, request, actor, raw, fields, eventID, expected)
	case "record_worker_job":
		if workflowMemberVariant(fields) {
			return nil, nil
		}
		return workflowRecordWorkerJobEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "accept_worker_result", "accept_worker_evidence":
		return workflowAcceptWorkerResultEvents(ctx, tx, request, actor, fields, eventID, expected)
	case "bind_evidence", "record_research", "record_report", "accept_decision", "approve_operation":
		return workflowEvidenceBindingEvents(request, actor, fields, eventID, expected)
	case "record_verdict":
		events, err := workflowRecordVerdictEvents(ctx, tx, request, actor, fields, eventID, expected)
		if err != nil {
			return nil, err
		}
		if err := guardLateCriterionAssociations(ctx, tx, request.WorkID, definition, stepID, fields); err != nil {
			return nil, err
		}
		return events, nil
	case "confirm_premise":
		return workflowConfirmPremiseEvents(ctx, tx, definition, request, actor, fields, eventID, expected)
	case "link_successor":
		return workflowLinkSuccessorEvents(ctx, tx, definition, request, actor, fields, eventID, expected)
	default:
		return nil, nil
	}
}

func workflowCompletionEventForDefinition(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage) (Event, error) {
	fields, err := workflowActionObject(raw)
	if err != nil {
		return Event{}, err
	}
	// Completion takes operator authority under the same recorded-delivery
	// condition as the verdict. Evidence and outcome gates still apply.
	// consequence_v1 rejects the operator claim outright: the authenticated
	// coordinator completes without substitution.
	if request.OperatorActor != nil {
		if definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
			return Event{}, newFailure(KindUnauthorized, "complete_workflow", "consequence_v1 takes no operator substitution for completion; the authorized coordinator completes under its own identity", false, "complete as the authenticated coordinator")
		}
		if err := requireOperatorVerdictExit(ctx, tx, request.WorkID); err != nil {
			return Event{}, err
		}
	}
	if workflowCompletionCommitDrifted(fields) {
		return Event{}, newFailure(KindMissingEvidence, "complete_workflow", "immutable evidence commit does not match the current commit", false, "rebind_evidence")
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
	impactVerdict := workflowCompletionImpactVerdict(fields)
	if impactVerdict != "breaking" && impactVerdict != "non-breaking" {
		return Event{}, newFailure(KindInvalidPayload, "complete_workflow", "completion requires impact_verdict breaking or non-breaking", false, "supply the delivered change impact verdict")
	}
	// The completion record is derived from the log the completion gate
	// verifies, never from caller fields or literals (#856): the recorded
	// verdict kinds, the count of bound evidence events, and the premise
	// confirmation the gate itself reads.
	contractData, _, contractErr := workflowCompletionContract(ctx, tx, registry, request.WorkID)
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
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, request.WorkID).Scan(&boundEvidence); err != nil {
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

// workflowCompletionCommitDrifted reports whether the completion's evidence
// commit differs from its current commit, at the top level or in the nested
// payload.
func workflowCompletionCommitDrifted(fields map[string]json.RawMessage) bool {
	if evidenceCommit, ok := workflowFieldString(fields, "evidence_commit"); ok {
		if currentCommit, currentOK := workflowFieldString(fields, "current_commit"); currentOK && evidenceCommit != currentCommit {
			return true
		}
	}
	payloadRaw := workflowFieldRaw(fields, "payload")
	if len(payloadRaw) == 0 {
		return false
	}
	var payloadFields map[string]json.RawMessage
	if json.Unmarshal(payloadRaw, &payloadFields) != nil {
		return false
	}
	evidenceCommit := workflowFieldStringDefault(payloadFields, "evidence_commit", "")
	return evidenceCommit != "" && evidenceCommit != workflowFieldStringDefault(payloadFields, "current_commit", evidenceCommit)
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
			outcomeKind := workflowFieldStringDefault(fields, "outcome_kind", "")
			outcome := workflowFieldRaw(fields, "outcome")
			if len(outcome) != 0 && string(outcome) != "null" {
				var predicate map[string]any
				if json.Unmarshal(outcome, &predicate) == nil {
					outcomeKind = workflowFieldStringDefaultMap(predicate, "kind", outcomeKind)
				}
			}
			outcomePayload := workflowFieldRaw(fields, "outcome_payload")
			if len(outcomePayload) == 0 {
				outcomePayload = defaultWorkflowOutcome(definition, fields)
			}
			outcomePredicates = []workflowContractPredicatePayload{{PredicateID: "predicate:primary", OutcomeKind: outcomeKind, OutcomePayload: outcomePayload}}
		}
	}
	return outcomePredicates, nil
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
	verdictStep := operatorVerdictStep(registered.Definition, currentStep)
	if verdictStep == "" {
		return newFailure(KindInvalidOperation, "workflow_action", "operator verdict identity requires a record_delivery or accept_worker_result exit or another definition-backed advancing exit", false, "request operator evaluation from the pinned verdict step")
	}
	advancingActions := advancingActionsBeforeStep(registered.Definition, verdictStep)
	if len(advancingActions) == 0 {
		return newFailure(KindInvalidOperation, "workflow_action", "operator verdict identity requires a record_delivery or accept_worker_result exit or another definition-backed advancing exit", false, "complete the pinned workflow step before requesting operator evaluation")
	}
	exits, err := readWorkflowExits(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, exit := range exits {
		if advancingActions[exit.stepID][exit.actionID] {
			return nil
		}
	}
	// A carried-forward instance keeps each completion's family. Resolve
	// that family's current graph when the instance graph cannot vouch for
	// an exit that preceded the verdict step.
	if carriedExitSatisfiedItsPinnedDefinition(verdictStep, exits) {
		return nil
	}
	return newFailure(KindInvalidOperation, "workflow_action", "operator verdict identity requires a record_delivery or accept_worker_result exit or another definition-backed advancing exit", false, "complete the pinned workflow step before requesting operator evaluation")
}

// operatorVerdictStep returns the step that records the verdict: the current
// step when it declares record_verdict, else the nearest such step reached
// backward over forward edges, or "" when none exists.
func operatorVerdictStep(definition WorkflowDefinition, currentStep string) string {
	if slices.Contains(workflowStep(definition, currentStep).Actions, "record_verdict") {
		return currentStep
	}
	visited := map[string]bool{currentStep: true}
	queue := []string{currentStep}
	for len(queue) != 0 {
		stepID := queue[0]
		queue = queue[1:]
		for _, edge := range definition.StepGraph.Edges {
			if edge.To != stepID || edge.Kind != WorkflowEdgeForward || visited[edge.From] {
				continue
			}
			visited[edge.From] = true
			step := workflowStep(definition, edge.From)
			if step != nil && slices.Contains(step.Actions, "record_verdict") {
				return step.ID
			}
			queue = append(queue, edge.From)
		}
	}
	return ""
}

// advancingActionsBeforeStep maps each step with a forward edge into stepID to
// the advancing actions it declares.
func advancingActionsBeforeStep(definition WorkflowDefinition, stepID string) map[string]map[string]bool {
	advancingActions := make(map[string]map[string]bool)
	for _, edge := range definition.StepGraph.Edges {
		if edge.To != stepID || edge.Kind != WorkflowEdgeForward {
			continue
		}
		step := workflowStep(definition, edge.From)
		if step == nil {
			continue
		}
		for _, actionID := range step.Actions {
			if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
				if advancingActions[edge.From] == nil {
					advancingActions[edge.From] = make(map[string]bool)
				}
				advancingActions[edge.From][actionID] = true
			}
		}
	}
	return advancingActions
}

// readWorkflowExits reads the latest completion per family/step/action class,
// newest first. Held and local completions also witness these admission classes.
func readWorkflowExits(ctx context.Context, tx *sql.Tx, workID string) ([]workflowExitRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT step_id,action_id,definition_ref,exit_seq FROM workflow_completed_exits WHERE work_id=? ORDER BY exit_seq DESC`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect the workflow exits", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var exits []workflowExitRecord
	for rows.Next() {
		var exit workflowExitRecord
		if err := rows.Scan(&exit.stepID, &exit.actionID, &exit.definitionRef, &exit.seq); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the workflow exits", true, "retry once the database is readable", err)
		}
		exits = append(exits, exit)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the workflow exits", true, "retry once the database is readable", err)
	}
	return exits, nil
}

// workflowExitRecord names one current completion class and its latest witness.
type workflowExitRecord struct {
	seq           int64
	stepID        string
	actionID      string
	definitionRef string
}

// carriedExitSatisfiedItsPinnedDefinition accepts a recorded exit that
// advanced out of a step directly preceding the verdict step under the
// current definition of the family selected when it completed. An empty
// family means no definition had been selected and contributes no fallback.
func carriedExitSatisfiedItsPinnedDefinition(verdictStep string, exits []workflowExitRecord) bool {
	byFamily := make(map[string]map[string]bool)
	for _, exit := range exits {
		if exit.definitionRef == "" {
			continue
		}
		advancing, resolved := byFamily[exit.definitionRef]
		if !resolved {
			advancing = nil
			if entry, ok := BuiltinWorkflowRegistry().Current(exit.definitionRef); ok {
				advancing = workflowVerdictPredecessorAdvancingExits(entry.Definition, verdictStep)
			}
			byFamily[exit.definitionRef] = advancing
		}
		if advancing[exit.stepID+"\x00"+exit.actionID] {
			return true
		}
	}
	return false
}

// workflowVerdictPredecessorAdvancingExits returns the advancing exits out of
// the verdict step's direct forward predecessors under one definition, keyed
// as stepID plus action ID. A definition that does not declare the verdict
// step as a record_verdict step contributes nothing.
func workflowVerdictPredecessorAdvancingExits(definition WorkflowDefinition, verdictStep string) map[string]bool {
	verdict := workflowStep(definition, verdictStep)
	if verdict == nil || !slices.Contains(verdict.Actions, "record_verdict") {
		return nil
	}
	advancing := map[string]bool{}
	for _, edge := range definition.StepGraph.Edges {
		if edge.To != verdictStep || edge.Kind != WorkflowEdgeForward {
			continue
		}
		if step := workflowStep(definition, edge.From); step != nil {
			for _, actionID := range step.Actions {
				if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
					advancing[edge.From+"\x00"+actionID] = true
				}
			}
		}
	}
	return advancing
}
