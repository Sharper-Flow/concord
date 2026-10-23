package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// CD-0017 §D4 draws the worker authority boundary in both directions. A worker
// run is the bounded execution attempt of one workflow step — the position
// workflow.action_started and workflow.action_checkpointed already model — so a
// worker legitimately runs its own step. But workers never record a step
// transition, a verdict, or completion, and never spawn nested workflow
// authority: durable workflow authority stays with the owning Concord workflow.
// The prohibition binds authority, not labor. These tests prove both halves of
// the boundary hold in the store, not just in the prose.

// A dispatched worker attempt is durable evidence on its own surface: a lane,
// routing policy, resolved model, and lifecycle. The worker fold writes only
// worker_attempts. The owning workflow fold may read that projection while
// validating accept_worker_result, but only the distinct workflow owner can
// append the step-advancing workflow.action_completed event.
func TestWorkerAuthorityBoundaryHoldsInBothDirections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "authority-work")
	seedWorkflowLaw(t, s)

	// Prepare the workflow owner and start the external step before dispatching a
	// real worker attempt. The dispatch then belongs to this action-start window.
	lane := BuiltinLaneDefinitions()[0]
	dispatch := Event{
		EventID: "dispatch-1", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "authority-work",
		Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
			AttemptID: "dispatch-1", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
			CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane),
			PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
		}),
	}
	// The same actor identity is an agent-class workflow actor on the work
	// item, with the implementation definition selected — the position a worker
	// holds when it executes one step.
	workerRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/worker", "session/worker-1")
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("authority-actor", WorkflowActorRecorded, "authority-work", map[string]any{"work_id": "authority-work", "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/worker", "session_ref": "session/worker-1", "actor_class": "agent"}),
		workflowEvent("authority-definition", WorkflowDefinitionSelected, "authority-work", map[string]any{"work_id": "authority-work", "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("authority-proposal", "authority-work", workerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("authority-discovery", "authority-work", workerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("authority-design", "authority-work", workerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("authority-planning", "authority-work", workerRef, 7, "planning", "approve_contract"),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-work"): 2}}); err != nil {
		t.Fatal(err)
	}

	// The allowed half of D4: the worker IS the bounded execution attempt of
	// the external-effect step, so starting it and checkpointing within it
	// succeed. Neither advances the step.
	start := workflowEventWithActor("authority-start", WorkflowActionStarted, "authority-work", workerRef, map[string]any{"work_id": "authority-work", "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "authority:start", "actor_ref": workerRef, "execution_model": preferredModelForLane(lane)})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-work"): 8}}); err != nil {
		t.Fatalf("the worker could not start the step it owns: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("dispatching the worker attempt: %v", err)
	}
	if got := countRows(t, s, "worker_attempts"); got != 1 {
		t.Fatalf("dispatched attempts = %d, want 1", got)
	}
	if got := currentStep(t, s, "authority-work"); got != "execution" {
		t.Fatalf("starting a step must not advance it: current_step=%q", got)
	}

	// A worker cannot complete the workflow. The completion gate's
	// actor-distinctness and evaluator clauses refuse it.
	t.Run("cannot complete the workflow", func(t *testing.T) {
		completion := workflowEventWithActor("authority-completion", WorkflowCompleted, "authority-work", workerRef, map[string]any{"work_id": "authority-work", "expected_version": 9, "resulting_version": 10, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": workerRef, "premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("c", 64)})
		err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-work"): 9}})
		if err == nil {
			t.Fatal("a worker recorded workflow completion")
		}
	})

	// The allowed half left the workflow where it put it: running the worker's
	// step, with no completion recorded.
	if got := instanceState(t, s, "authority-work"); got != "running" {
		t.Fatalf("rejected completion changed instance state: %q", got)
	}
	var completed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, "authority-work", WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("a refused workflow completion was recorded: %d", completed)
	}
}

func TestWorkerCannotAdvanceItsStepWithAnUndeclaredCompletionAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, workerRef := seedDispatchedWorkerAtExecution(t, "authority-step-transition")
	completed := workflowEventWithActor("authority-step-transition-completed", WorkflowActionCompleted, "authority-step-transition", workerRef, map[string]any{
		"work_id": "authority-step-transition", "expected_version": 11, "resulting_version": 12,
		"step_id": "execution", "action_id": "record_proposal", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": workerRef,
	})
	err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-step-transition"): 11}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("worker step transition failure = %v, want %s", err, KindIllegalLifecycleTransition)
	}
	if got := currentStep(t, s, "authority-step-transition"); got != "execution" {
		t.Fatalf("rejected worker completion advanced step to %q", got)
	}
	if got := readWorkVersion(t, s, "authority-step-transition"); got != 11 {
		t.Fatalf("rejected worker completion advanced version to %d", got)
	}
}

// Production topology: the orchestrator starts the external-effect step,
// dispatches a lane through the signed worker boundary, and then accepts the
// lane's result. CD-0017 D4 makes the dispatched lane the executing actor of
// the step, recorded as a workflow actor at dispatch, so the owner's
// accept_worker_result satisfies CD-0013 D5 against the actor that actually
// executed.
func TestAcceptWorkerResultSucceedsWhenLaneIsExecutingActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "authority-lane-executing"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + workID
	laneActor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord", AgentRef: "agent/lane:" + lane.ID, SessionRef: "session/" + attemptID, ActorClass: ActorAgent}
	laneRef, err := WorkflowActorRef(laneActor)
	if err != nil {
		t.Fatal(err)
	}
	setup := workflowExecutionSetupEvents(t, "lane", workID, ownerRef, owner, laneRef, laneActor, lane)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", ownerRef)
	dispatch := Event{EventID: "lane-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{
		"attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
	})}
	completed := Event{EventID: "lane-completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID)})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch, completed}}); err != nil {
		t.Fatalf("recording the dispatched lane attempt: %v", err)
	}
	var executing string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&executing); err != nil {
		t.Fatal(err)
	}
	if executing != laneRef {
		t.Fatalf("dispatch must pin the lane as executing actor: got %q, want %q", executing, laneRef)
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 12, ActionID: "accept_worker_result", Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "lane:accept", OperationID: "lane:accept",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "lane:accept", RequestID: "request:lane:accept", ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("owner accepting the lane result was refused: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("ordinary acceptance changed the step to %q, want execution", got)
	}
}

// seedAcceptedFixtureLane stages the production accept topology on the
// retained fixture family: the owner drives execution, a lane is dispatched
// as the executing actor, and the owner accepts its result through the public
// action. Ordinary acceptance holds execution with the lane still pinned as
// the executing actor and the accepted attempt disposed behind it (CD-0242 D5).
func seedAcceptedFixtureLane(t *testing.T, workID string) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + workID
	laneActor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord", AgentRef: "agent/lane:" + lane.ID, SessionRef: "session/" + attemptID, ActorClass: ActorAgent}
	laneRef, err := WorkflowActorRef(laneActor)
	if err != nil {
		t.Fatal(err)
	}
	setup := workflowExecutionSetupEvents(t, "lane", workID, ownerRef, owner, laneRef, laneActor, lane)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	// The contract carries the criterion whose proof only Main may associate.
	contract := workflowEventWithActor("lane-contract", WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 10, "resulting_version": 11, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:lane", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{contract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 10}}); err != nil {
		t.Fatal(err)
	}
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", ownerRef)
	dispatch := Event{EventID: "lane-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{
		"attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
	})}
	completed := Event{EventID: "lane-completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID)})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch, completed}}); err != nil {
		t.Fatalf("recording the lane attempt: %v", err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 13, ActionID: "accept_worker_result", Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "lane:accept", OperationID: "lane:accept",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "lane:accept", RequestID: "request:lane:accept", ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("owner accepting the lane result was refused: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("ordinary acceptance changed the step to %q, want execution", got)
	}
	return s, laneRef
}

// The disposal that hands the owner the writing authority is the same event
// that ends the lane's claim to it. After the owner accepts, the disposed lane
// may not append checkpoint events, and the refusal mutates nothing.
func TestDisposedLaneCheckpointRefused(t *testing.T) {
	t.Parallel()
	workID := "authority-lane-disposed-checkpoint"
	s, laneRef := seedAcceptedFixtureLane(t, workID)
	before := readWorkVersion(t, s, workID)
	beforeCheckpoints := countWorkflowCheckpoints(t, s, workID)
	disposed := workflowEventWithActor("lane-disposed-checkpoint", WorkflowActionCheckpointed, workID, laneRef, map[string]any{
		"work_id": workID, "expected_version": before, "resulting_version": before + 1,
		"step_id": "execution", "step_kind": string(WorkflowStepExternalEffect), "attempt_epoch": 2,
		"checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": "disposed"},
		"resume_cursor":      "cursor:disposed", "actor_ref": laneRef, "request_id": "request:lane-disposed-checkpoint",
		"checkpoint_id": "checkpoint:lane-disposed", "accepted_inputs_digest": "sha256:" + strings.Repeat("c", 64), "idempotency_identity": "lane-disposed-checkpoint",
	})
	err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{disposed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("disposed lane checkpoint failure=%v, want %s", err, KindUnauthorized)
	}
	if got := readWorkVersion(t, s, workID); got != before {
		t.Fatalf("disposed lane checkpoint version=%d, want %d", got, before)
	}
	if got := countWorkflowCheckpoints(t, s, workID); got != beforeCheckpoints {
		t.Fatalf("disposed lane checkpoint rows=%d, want %d", got, beforeCheckpoints)
	}
}

// Disposal does not move the execution pin. The lane stays the executing
// actor after Main accepts, and remains a worker without proof authority.
func TestIndependenceUnchangedAfterAccept(t *testing.T) {
	t.Parallel()
	workID := "authority-lane-independence"
	s, laneRef := seedAcceptedFixtureLane(t, workID)
	var executing string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&executing); err != nil {
		t.Fatal(err)
	}
	if executing != laneRef {
		t.Fatalf("acceptance moved execution_actor_ref to %q, want %q", executing, laneRef)
	}
	before := readWorkVersion(t, s, workID)
	verdict := workflowEventWithActor("lane-disposed-verdict", WorkflowVerdictRecorded, workID, laneRef, map[string]any{
		"work_id": workID, "expected_version": before, "resulting_version": before + 1,
		"contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "ok",
		"verdict_actor_ref": laneRef, "evaluation_evidence": []string{"evidence:lane"},
		"incomparable_with_approved": false,
	})
	verdict.PayloadVersion = 2
	err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("disposed lane verdict failure=%v, want %s", err, KindUnauthorized)
	}
	if got := readWorkVersion(t, s, workID); got != before {
		t.Fatalf("disposed lane verdict version=%d, want %d", got, before)
	}
}

func TestWorkerCannotRecordItsOwnVerdict(t *testing.T) {
	t.Parallel()
	workID := "authority-self-verdict"
	f := rAcceptance(t, workID, nil, false, true)
	s := f.store
	pin, err := ReadWorkPin(t.Context(), s, workID)
	if err != nil || pin.Review == nil || pin.Review.Accepted == nil {
		t.Fatalf("current accepted Review is absent: %+v err=%v", pin.Review, err)
	}
	workerRef := crossAttemptDispatchedLaneActor(t, s, workID, pin.Review.Accepted.AttemptID)
	worker := workflowActorForRef(t, s, workerRef)
	verdict := qAssociationPayload(t, f)
	before := readWorkVersion(t, s, workID)
	assertRecoveryRefusalWithoutEffect(t, s, workID, KindUnauthorized, func() error {
		err = runVerdictActionAs(t, s, workID, "record_verdict", verdict, before, worker)
		return err
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("worker self-verdict failure = %v, want %s", err, KindUnauthorized)
	}
	if got := readWorkVersion(t, s, workID); got != before {
		t.Fatalf("rejected worker verdict advanced version to %d", got)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", verdict, before, f.owner); err != nil {
		t.Fatalf("Main could not associate the same current criterion proof: %v", err)
	}
}

func TestWorkflowActionStartedV2AuthorizesActorStepAndEpoch(t *testing.T) {
	t.Parallel()
	s, workerRef, owner, _ := seedWorkerAtExecution(t, "authority-start-guards")
	currentVersion := int64(12)
	wantCurrentStep := "execution"
	beforeStarts := countWorkflowActionStarts(t, s, "authority-start-guards")

	t.Run("forged actor", func(t *testing.T) {
		start := workflowEventWithActor("forged-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "forged-start", "actor_ref": owner.ActorRef})
		assertRejectedActionStart(t, s, start, currentVersion, KindUnauthorized, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("future step", func(t *testing.T) {
		start := workflowEventWithActor("future-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": "acceptance", "action_id": "record_verdict", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "future-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("undeclared current-step action", func(t *testing.T) {
		start := workflowEventWithActor("undeclared-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "record_proposal", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "undeclared-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("duplicate epoch", func(t *testing.T) {
		start := workflowEventWithActor("duplicate-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "duplicate-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("jumped epoch", func(t *testing.T) {
		start := workflowEventWithActor("jumped-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 4, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "jumped-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})

	exact := workflowEventWithActor("exact-retry-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("g", 64), "idempotency_identity": "exact-retry-start", "actor_ref": workerRef, "execution_model": preferredModelForLane(BuiltinLaneDefinitions()[0])})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{exact}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-start-guards"): currentVersion}}); err != nil {
		t.Fatalf("exact next retry epoch rejected: %v", err)
	}
	if got := currentStep(t, s, "authority-start-guards"); got != wantCurrentStep {
		t.Fatalf("exact retry changed current_step=%q, want %q", got, wantCurrentStep)
	}
	if got := readWorkVersion(t, s, "authority-start-guards"); got != currentVersion+1 {
		t.Fatalf("exact retry version=%d, want %d", got, currentVersion+1)
	}
	if got := countWorkflowActionStarts(t, s, "authority-start-guards"); got != beforeStarts+1 {
		t.Fatalf("exact retry persisted %d action starts, want %d", got, beforeStarts+1)
	}

	stale := workflowEventWithActor("stale-retry-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion + 1, "resulting_version": currentVersion + 2, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("h", 64), "idempotency_identity": "stale-retry-start", "actor_ref": workerRef})
	assertRejectedActionStart(t, s, stale, currentVersion+1, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion+1, beforeStarts+1)
}

func assertRejectedActionStart(t *testing.T, s *Store, event Event, expectedVersion int64, wantKind FailureKind, wantStep string, wantVersion int64, wantStarts int) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = func() error {
		_, err := applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-start-guards"): expectedVersion}}, newFoldScope(tx))
		return err
	}()
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("action start failure=%v, want %s", err, wantKind)
	}
	if got := currentStep(t, s, "authority-start-guards"); got != wantStep {
		t.Fatalf("rejected action start current_step=%q, want %q", got, wantStep)
	}
	if got := readWorkVersion(t, s, "authority-start-guards"); got != wantVersion {
		t.Fatalf("rejected action start version=%d, want %d", got, wantVersion)
	}
	if got := countWorkflowActionStarts(t, s, "authority-start-guards"); got != wantStarts {
		t.Fatalf("rejected action start count=%d, want %d", got, wantStarts)
	}
}

func countWorkflowActionStarts(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionStarted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCheckpointedV2BindsActorStepKindAndEpoch(t *testing.T) {
	t.Parallel()
	workID := "authority-checkpoint-guards"
	s, workerRef, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	baseVersion := int64(12)
	baseCheckpoints := countWorkflowCheckpoints(t, s, workID)
	checkpoint := func(id, eventActor, payloadActor, stepID, stepKind string, epoch int64) Event {
		return workflowEventWithActor(id, WorkflowActionCheckpointed, workID, eventActor, map[string]any{
			"work_id": workID, "expected_version": baseVersion, "resulting_version": baseVersion + 1,
			"step_id": stepID, "step_kind": stepKind, "attempt_epoch": epoch,
			"checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": id},
			"resume_cursor":      "cursor:" + id, "actor_ref": payloadActor, "request_id": "request:" + id,
			"checkpoint_id": "checkpoint:" + id, "accepted_inputs_digest": "sha256:" + strings.Repeat("i", 64), "idempotency_identity": id,
		})
	}
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-forged-actor", workerRef, ownerRef, "execution", "external_effect", 2), baseVersion, KindUnauthorized, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-step", workerRef, workerRef, "acceptance", "external_effect", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-kind", workerRef, workerRef, "execution", "human_checkpoint", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-epoch", workerRef, workerRef, "execution", "external_effect", 3), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-non-executor", ownerRef, ownerRef, "execution", "external_effect", 2), baseVersion, KindUnauthorized, baseCheckpoints)

	valid := checkpoint("checkpoint-valid", workerRef, workerRef, "execution", "external_effect", 2)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{valid}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baseVersion}}); err != nil {
		t.Fatalf("valid executor checkpoint rejected: %v", err)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("valid checkpoint changed current_step=%q", got)
	}
	if got := readWorkVersion(t, s, workID); got != baseVersion+1 {
		t.Fatalf("valid checkpoint version=%d, want %d", got, baseVersion+1)
	}
	if got := countWorkflowCheckpoints(t, s, workID); got != baseCheckpoints+1 {
		t.Fatalf("valid checkpoint count=%d, want %d", got, baseCheckpoints+1)
	}
}

func assertRejectedCheckpoint(t *testing.T, s *Store, workID string, event Event, expectedVersion int64, wantKind FailureKind, wantCheckpoints int) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}}, newFoldScope(tx))
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("checkpoint failure=%v, want %s", err, wantKind)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("rejected checkpoint current_step=%q", got)
	}
	if got := readWorkVersion(t, s, workID); got != expectedVersion {
		t.Fatalf("rejected checkpoint version=%d, want %d", got, expectedVersion)
	}
	if got := countWorkflowCheckpoints(t, s, workID); got != wantCheckpoints {
		t.Fatalf("rejected checkpoint count=%d, want %d", got, wantCheckpoints)
	}
}

func countWorkflowCheckpoints(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_checkpoints WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCompletedV2BindsPayloadActorToEventActor(t *testing.T) {
	t.Parallel()
	workID := "authority-completed-actor"
	s, workerRef, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	before := countWorkflowActionCompleted(t, s, workID)
	completed := workflowEventWithActor("forged-completed-actor", WorkflowActionCompleted, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": 12, "resulting_version": 13,
		"step_id": "execution", "action_id": "checkpoint_execution", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": ownerRef,
	})
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}, newFoldScope(tx))
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("forged completed actor failure=%v, want %s", err, KindUnauthorized)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("forged completion changed current_step=%q", got)
	}
	if got := readWorkVersion(t, s, workID); got != 12 {
		t.Fatalf("forged completion changed version=%d", got)
	}
	if got := countWorkflowActionCompleted(t, s, workID); got != before {
		t.Fatalf("forged completion count=%d, want %d", got, before)
	}
}

func TestWorkflowActionFailedV2BindsCurrentExecutorAttempt(t *testing.T) {
	t.Parallel()
	t.Run("forged actor", func(t *testing.T) {
		s, workerRef, owner, _ := seedWorkerAtExecution(t, "authority-failed-forged-actor")
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-forged-actor", "authority-failed-forged-actor", workerRef, ownerRef, "execution", 2, true), KindUnauthorized)
	})
	t.Run("wrong step", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-wrong-step")
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-wrong-step", "authority-failed-wrong-step", workerRef, workerRef, "acceptance", 2, true), KindIllegalLifecycleTransition)
	})
	t.Run("stale epoch", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-stale-epoch")
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-stale-epoch", "authority-failed-stale-epoch", workerRef, workerRef, "execution", 3, true), KindIllegalLifecycleTransition)
	})
	t.Run("non-executor actor", func(t *testing.T) {
		s, _, owner, _ := seedWorkerAtExecution(t, "authority-failed-non-executor")
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-non-executor", "authority-failed-non-executor", ownerRef, ownerRef, "execution", 2, true), KindUnauthorized)
	})
	t.Run("recoverable failure keeps workflow running", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-recoverable")
		event := actionFailedFixture("failed-recoverable", "authority-failed-recoverable", workerRef, workerRef, "execution", 2, true)
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-failed-recoverable"): 12}}); err != nil {
			t.Fatalf("current executor recoverable failure rejected: %v", err)
		}
		if got := instanceState(t, s, "authority-failed-recoverable"); got != "running" {
			t.Fatalf("recoverable failure state=%q, want running", got)
		}
		if got := readWorkVersion(t, s, "authority-failed-recoverable"); got != 13 {
			t.Fatalf("recoverable failure version=%d, want 13", got)
		}
	})
	t.Run("nonrecoverable failure blocks workflow", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-blocked")
		event := actionFailedFixture("failed-blocked", "authority-failed-blocked", workerRef, workerRef, "execution", 2, false)
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-failed-blocked"): 12}}); err != nil {
			t.Fatalf("current executor nonrecoverable failure rejected: %v", err)
		}
		if got := instanceState(t, s, "authority-failed-blocked"); got != "blocked" {
			t.Fatalf("nonrecoverable failure state=%q, want blocked", got)
		}
		if got := readWorkVersion(t, s, "authority-failed-blocked"); got != 13 {
			t.Fatalf("nonrecoverable failure version=%d, want 13", got)
		}
	})
}

// The fold rejected kinds the rest of the system accepts, so a workflow action
// failing with stale_law_revision or domain_overlap could not be recorded.
// Drive the real fold rather than the closure predicate, so the assertion binds
// to the layer where the defect bit.
// seedDisposedFailedLane stages the failure topology: the fixture workflow at
// execution with a lane dispatched as the executing actor, the lane's attempt
// failed, and the owner's record_worker_failure disposing of it. The workflow
// stays on execution with the lane still pinned as the executing actor.
func seedDisposedFailedLane(t *testing.T, workID string) (*Store, WorkflowActor, string, string) {
	t.Helper()
	s, owner, ownerRef, laneRef := seedFailedLaneAtExecution(t, workID)
	applyRecordWorkerFailureForTest(t, s, workID, owner, "attempt:"+workID, 1, readWorkVersion(t, s, workID), "failure:"+workID)
	return s, owner, ownerRef, laneRef
}

func seedFailedLaneAtExecution(t *testing.T, workID string) (*Store, WorkflowActor, string, string) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	workerActor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(workerActor)
	if err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	attemptID := "attempt:" + workID
	laneActor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord", AgentRef: "agent/lane:" + lane.ID, SessionRef: "session/" + attemptID, ActorClass: ActorAgent}
	laneRef, err := WorkflowActorRef(laneActor)
	if err != nil {
		t.Fatal(err)
	}
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("worker-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": workerActor.PrincipalRef, "client_ref": workerActor.ClientRef, "agent_ref": workerActor.AgentRef, "session_ref": workerActor.SessionRef, "actor_class": "agent"}),
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("lane-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "actor_ref": laneRef, "principal_ref": laneActor.PrincipalRef, "client_ref": laneActor.ClientRef, "agent_ref": laneActor.AgentRef, "session_ref": laneActor.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 5, "resulting_version": 6, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, workerRef, 6, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, workerRef, 7, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, workerRef, 8, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, workerRef, 9, "planning", "approve_contract"),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, laneRef, map[string]any{"work_id": workID, "expected_version": 10, "resulting_version": 11, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": laneRef, "execution_model": preferredModelForLane(lane)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{
		"attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
	})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("dispatching the lane attempt: %v", err)
	}
	failWorkerAttempt(t, s, workID, attemptID)
	return s, owner, ownerRef, laneRef
}

// The same handoff governs action failures: after the owner disposes of a
// failed lane attempt with record_worker_failure, the owner may fail the
// action and the disposed lane may not. The next dispatch re-pins a lane and
// the owner's authority ends.
func TestActionFailedSameAuthority(t *testing.T) {
	t.Parallel()
	workID := "authority-failed-handoff"
	s, _, ownerRef, laneRef := seedDisposedFailedLane(t, workID)
	before := readWorkVersion(t, s, workID)
	failureEvent := func(id, actor string, expected int64) Event {
		return workflowEventWithActor(id, WorkflowActionFailed, workID, actor, map[string]any{
			"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
			"step_id": "execution", "attempt_epoch": 1, "failure_kind": string(KindTimeout), "recoverable": true,
			"actor_ref": actor,
		})
	}

	ownerFailure := failureEvent("failed-owner-handoff", ownerRef, before)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{ownerFailure}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}}); err != nil {
		t.Fatalf("owner action failure after disposal refused: %v", err)
	}
	if got := instanceState(t, s, workID); got != "running" {
		t.Fatalf("owner action failure state=%q, want running", got)
	}

	disposed := readWorkVersion(t, s, workID)
	laneFailure := failureEvent("failed-disposed-lane", laneRef, disposed)
	err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{laneFailure}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): disposed}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("disposed lane action failure=%v, want %s", err, KindUnauthorized)
	}
	if got := readWorkVersion(t, s, workID); got != disposed {
		t.Fatalf("disposed lane action failure version=%d, want %d", got, disposed)
	}

	nextDispatch := Event{EventID: "dispatch-2-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{
		"attempt_id": "attempt:2:" + workID, "lane_id": BuiltinLaneDefinitions()[0].ID, "lane_version": BuiltinLaneDefinitions()[0].Version, "lane_digest": BuiltinLaneDefinitions()[0].Digest,
		"capability_class": BuiltinLaneDefinitions()[0].CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("d", 64), "readback_model": preferredModelForLane(BuiltinLaneDefinitions()[0]), "lane_actor_ref": laneRef,
	})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{nextDispatch}}); err != nil {
		t.Fatalf("next dispatch: %v", err)
	}
	lateOwnerFailure := failureEvent("failed-owner-late", ownerRef, disposed)
	err = applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{lateOwnerFailure}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): disposed}})
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("owner action failure after next dispatch=%v, want %s", err, KindUnauthorized)
	}
	if got := readWorkVersion(t, s, workID); got != disposed {
		t.Fatalf("owner action failure after next dispatch version=%d, want %d", got, disposed)
	}
}

func TestWorkflowActionFailedAcceptsLawFailureKinds(t *testing.T) {
	t.Parallel()
	for _, failureKind := range []FailureKind{KindStaleLawRevision, KindDomainOverlap} {
		t.Run(string(failureKind), func(t *testing.T) {
			workID := "authority-failed-" + string(failureKind)
			s, workerRef, _, _ := seedWorkerAtExecution(t, workID)
			event := actionFailedFixtureWithKind("failed-"+string(failureKind), workID, workerRef, workerRef, "execution", 2, false, failureKind)
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}); err != nil {
				t.Fatalf("current executor %s failure rejected: %v", failureKind, err)
			}
		})
	}
}

func actionFailedFixture(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool) Event {
	return actionFailedFixtureWithKind(eventID, workID, eventActor, payloadActor, stepID, epoch, recoverable, KindTimeout)
}

func actionFailedFixtureWithKind(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool, failureKind FailureKind) Event {
	return workflowEventWithActor(eventID, WorkflowActionFailed, workID, eventActor, map[string]any{
		"work_id": workID, "expected_version": 12, "resulting_version": 13,
		"step_id": stepID, "attempt_epoch": epoch, "failure_kind": string(failureKind), "recoverable": recoverable,
		"actor_ref": payloadActor,
	})
}

func assertRejectedActionFailed(t *testing.T, s *Store, event Event, wantKind FailureKind) {
	t.Helper()
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): 12}}); !hasFailureKind(err, wantKind) {
		t.Fatalf("action_failed error=%v, want %s", err, wantKind)
	}
	if got := instanceState(t, s, event.SubjectID); got != "running" {
		t.Fatalf("rejected action_failed state=%q, want running", got)
	}
	if got := readWorkVersion(t, s, event.SubjectID); got != 12 {
		t.Fatalf("rejected action_failed version=%d, want %d", got, 12)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, event.SubjectID, WorkflowActionFailed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected action_failed persisted %d events", events)
	}
}

func countWorkflowActionCompleted(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDistinctWorkflowOwnerAcceptsCompletedWorkerResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, worker, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-accept")
	request := WorkflowActionExecutionRequest{
		WorkID: "authority-accept", ExpectedVersion: 12, ActionID: "accept_worker_result",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "accept-authority", OperationID: "accept-authority",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "accept-authority", RequestID: "request:accept-authority", ContractDigest: testManifestDigest, Now: time.Unix(3, 0).UTC(),
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), request)
	if err != nil {
		tx.Rollback()
		t.Fatalf("distinct owner could not accept completed worker result: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if result.ResultingVersion != 14 {
		t.Fatalf("accept result version=%d, want 14", result.ResultingVersion)
	}
	if got := currentStep(t, s, "authority-accept"); got != "execution" {
		t.Fatalf("ordinary acceptance current_step=%q, want execution", got)
	}
	if got := readWorkVersion(t, s, "authority-accept"); got != 14 {
		t.Fatalf("accepted worker result version=%d, want 14", got)
	}
	var executionActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, "authority-accept").Scan(&executionActor); err != nil {
		t.Fatal(err)
	}
	if executionActor != worker {
		t.Fatalf("acceptance overwrote execution actor with %q, want %q", executionActor, worker)
	}
	var payloadVersion int
	var workerAttempt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version,json_extract(payload,'$.worker_attempt_id') FROM domain_events WHERE event_id=?`, "accept-authority:completed").Scan(&payloadVersion, &workerAttempt); err != nil {
		t.Fatal(err)
	}
	completion, ok := registeredEventKind(WorkflowActionCompleted)
	if !ok {
		t.Fatal("workflow.action_completed is not registered")
	}
	if payloadVersion != completion.CurrentVersion || workerAttempt != attemptID {
		t.Fatalf("accept completion evidence = version %d attempt %q, want v%d %q", payloadVersion, workerAttempt, completion.CurrentVersion, attemptID)
	}
}

func TestWorkerCannotInvokeAcceptWorkerResultAsItsOwnOwner(t *testing.T) {
	t.Parallel()
	j, _ := descendantReviewFixture(t, "authority-worker-accept")
	attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
	workerRef := crossAttemptDispatchedLaneActor(t, j.s, j.workID, attempt)
	mainRef, err := WorkflowActorRef(j.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	if workerRef == "" || workerRef == mainRef {
		t.Fatalf("dispatch did not bind a worker distinct from Main: worker=%q Main=%q", workerRef, mainRef)
	}
	worker := workflowActorForRef(t, j.s, workerRef)
	pinBefore, err := ReadWorkPin(t.Context(), j.s, j.workID)
	if err != nil || pinBefore.Review == nil || pinBefore.Review.Accepted != nil && pinBefore.Review.Accepted.AttemptID == attempt {
		t.Fatalf("pending Review already has acceptance authority: %+v err=%v", pinBefore.Review, err)
	}
	before := j.effects(t)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, worker); !hasFailureKind(err, KindUnauthorized) {
		t.Fatalf("dispatched worker self-acceptance = %v, want unauthorized", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("refused worker acceptance changed durable state")
	}
	pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
	if err != nil || string(mustJSONValue(pin.Review)) != string(mustJSONValue(pinBefore.Review)) {
		t.Fatalf("worker refusal installed Review authority: %+v err=%v", pin.Review, err)
	}
}

func TestAcceptWorkerResultRejectsWithoutMutation(t *testing.T) {
	t.Parallel()
	const (
		wantStep    = "execution"
		wantVersion = int64(12)
	)
	t.Run("missing attempt id", func(t *testing.T) {
		s, _, owner, _ := seedWorkerAtExecution(t, "authority-missing-attempt")
		assertRejectedWorkerAcceptance(t, s, "authority-missing-attempt", owner, wantVersion, map[string]any{"attempt_epoch": 1}, KindMissingEvidence, wantVersion)
	})
	t.Run("missing attempt epoch with admitted terminal result", func(t *testing.T) {
		s, _, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-missing-epoch")
		assertRejectedWorkerAcceptance(t, s, "authority-missing-epoch", owner, wantVersion, map[string]any{"attempt_id": attemptID}, KindInvalidPayload, wantVersion)
	})
	t.Run("dispatched but not completed", func(t *testing.T) {
		s, _, owner, attemptID := seedWorkerAtExecution(t, "authority-dispatched-only")
		assertRejectedWorkerAcceptance(t, s, "authority-dispatched-only", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}, KindMissingEvidence, wantVersion)
	})
	t.Run("failed attempt", func(t *testing.T) {
		s, _, owner, attemptID := seedWorkerAtExecution(t, "authority-failed-attempt")
		failWorkerAttempt(t, s, "authority-failed-attempt", attemptID)
		assertRejectedWorkerAcceptance(t, s, "authority-failed-attempt", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}, KindMissingEvidence, wantVersion)
	})
	t.Run("foreign work attempt", func(t *testing.T) {
		s, _, owner, _ := seedWorkerAtExecution(t, "authority-foreign-target")
		attemptID := "attempt:authority-foreign-work"
		authorizeWorkerFixtureDispatch(t, s, "authority-foreign-work", attemptID)
		lane := BuiltinLaneDefinitions()[0]
		dispatch := Event{EventID: "dispatch-authority-foreign-work", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "authority-foreign-work", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
			t.Fatal(err)
		}
		completed := Event{EventID: "completed-authority-foreign-work", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "authority-foreign-work", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, "authority-foreign-work")})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
			t.Fatal(err)
		}
		assertRejectedWorkerAcceptance(t, s, "authority-foreign-target", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}, KindMissingEvidence, wantVersion)
	})
	t.Run("stale dispatch before newer retry start", func(t *testing.T) {
		s, workerRef, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-stale-dispatch")
		retryStart := workflowEventWithActor("retry-start-authority-stale-dispatch", WorkflowActionStarted, "authority-stale-dispatch", workerRef, map[string]any{"work_id": "authority-stale-dispatch", "expected_version": 12, "resulting_version": 13, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("d", 64), "idempotency_identity": "retry:authority-stale-dispatch", "actor_ref": workerRef, "execution_model": preferredModelForLane(BuiltinLaneDefinitions()[0])})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{retryStart}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-stale-dispatch"): 12}}); err != nil {
			t.Fatal(err)
		}
		assertRejectedWorkerAcceptance(t, s, "authority-stale-dispatch", owner, 13, map[string]any{"attempt_id": attemptID, "attempt_epoch": 3}, KindIllegalLifecycleTransition, 13)
	})
	t.Run("wrong attempt epoch", func(t *testing.T) {
		s, _, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-wrong-epoch")
		assertRejectedWorkerAcceptance(t, s, "authority-wrong-epoch", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, KindIllegalLifecycleTransition, wantVersion)
	})
	t.Run("divergent readback still completes the attempt", func(t *testing.T) {
		// CD-0058: a completion whose readback differs from the dispatch
		// readback is accepted as a normal completion. The attempt reaches
		// 'completed' and the readback column records what the host reported.
		s, _, owner, attemptID := seedWorkerAtExecution(t, "authority-divergent-readback")
		divergent := Event{EventID: "completed-authority-divergent-readback", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "authority-divergent-readback", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/other-model", ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, "authority-divergent-readback")})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{divergent}}); err != nil {
			t.Fatal(err)
		}
		var lifecycle, readback string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle, &readback); err != nil {
			t.Fatal(err)
		}
		if lifecycle != "completed" {
			t.Fatalf("divergent readback worker lifecycle=%q, want completed", lifecycle)
		}
		if readback != "openai/other-model" {
			t.Fatalf("divergent readback readback=%q, want openai/other-model", readback)
		}
		_ = owner
	})
}

func assertRejectedWorkerAcceptance(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, payload map[string]any, wantKind FailureKind, wantVersion int64) {
	t.Helper()
	assertRejectedWorkerAction(t, s, workID, owner, expectedVersion, payload, wantKind, wantVersion, "accept_worker_result", "e", "reject:", "acceptance")
}

// assertRejectedWorkerAction proves the named worker action refuses inside
// its transaction: no completed event persists, the step stays execution,
// and the version stays put.
func assertRejectedWorkerAction(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, payload map[string]any, wantKind FailureKind, wantVersion int64, actionID, digestChar, opPrefix, label string) {
	t.Helper()
	var completedBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&completedBefore); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: actionID, Payload: mustJSONValue(payload), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat(digestChar, 64), IdempotencyIdentity: opPrefix + workID, OperationID: opPrefix + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: opPrefix + workID, RequestID: "request:" + opPrefix + workID, ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	_ = tx.Rollback()
	if err == nil {
		t.Fatal(actionID + " unexpectedly succeeded")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("%s failure=%v, want %s", actionID, err, wantKind)
	}
	// CD-0242 D6 requires the exact authorized terminal result before any
	// disposition. Absent, unfinished, failed, and foreign attempts lack it.
	if actionID == "accept_worker_result" && wantKind == KindMissingEvidence && failure.Detail != "the requested assignment has no admitted terminal result" {
		t.Fatalf("%s refused for %q, want the missing terminal result", actionID, failure.Detail)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("rejected %s current_step=%q, want execution", label, got)
	}
	if got := readWorkVersion(t, s, workID); got != wantVersion {
		t.Fatalf("rejected %s version=%d, want %d", label, got, wantVersion)
	}
	var completed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != completedBefore {
		t.Fatalf("rejected %s persisted a new workflow.action_completed event: before=%d after=%d", label, completedBefore, completed)
	}
}

func failWorkerAttempt(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	fail := Event{EventID: "failed-" + workID + "-" + attemptID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(BuiltinLaneDefinitions()[0]), FailureKind: WorkerFailureWorkerError, Detail: "worker failed"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fail}}); err != nil {
		t.Fatal(err)
	}
}

func applyRecordWorkerFailureForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, attemptID string, attemptEpoch, expectedVersion int64, operationID string) WorkflowActionExecutionResult {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: "record_worker_failure", Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("record_worker_failure: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func applyStartForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, operationID string) WorkflowActionExecutionResult {
	t.Helper()
	return applyWorkflowActionForTest(t, s, workID, owner, expectedVersion, "start_execution", "s", operationID, time.Unix(4, 0).UTC())
}

// applyWorkflowActionForTest commits one raw workflow action for a test.
func applyWorkflowActionForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, actionID, digestChar, operationID string, now time.Time) WorkflowActionExecutionResult {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: actionID, Payload: mustJSONValue(map[string]any{}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat(digestChar, 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: now,
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("%s: %v", actionID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func workflowActionCompletedFixture(eventID, workID, actor string, expected int64, stepID, actionID string) Event {
	return workflowEventWithActor(eventID, WorkflowActionCompleted, workID, actor, map[string]any{
		"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
		"step_id": stepID, "action_id": actionID, "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actor,
	})
}

func seedCompletedWorkerAtExecution(t *testing.T, workID string) (*Store, string, WorkflowActor, string) {
	t.Helper()
	s, workerRef, owner, attemptID := seedWorkerAtExecution(t, workID)
	lane := BuiltinLaneDefinitions()[0]
	completed := Event{EventID: "completed-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID)})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	return s, workerRef, owner, attemptID
}

func seedWorkerAtExecution(t *testing.T, workID string) (*Store, string, WorkflowActor, string) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	workerActor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(workerActor)
	if err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := BuiltinLaneDefinitions()[0]
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("worker-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": workerActor.PrincipalRef, "client_ref": workerActor.ClientRef, "agent_ref": workerActor.AgentRef, "session_ref": workerActor.SessionRef, "actor_class": "agent"}),
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, workerRef, 5, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, workerRef, 6, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, workerRef, 7, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, workerRef, 8, "planning", "approve_contract"),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": workerRef, "execution_model": preferredModelForLane(lane)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", workerRef)
	dispatch := Event{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatal(err)
	}
	return s, workerRef, owner, attemptID
}

func workflowActorForRef(t *testing.T, s *Store, actorRef string) WorkflowActor {
	t.Helper()
	var actor WorkflowActor
	if err := s.DatabaseForTesting().QueryRow(`SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actor.ActorRef, &actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass); err != nil {
		t.Fatal(err)
	}
	return actor
}

func seedDispatchedWorkerAtExecution(t *testing.T, workID string) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	lane := BuiltinLaneDefinitions()[0]
	workerRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/worker", "session/"+workID)
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/worker", "session_ref": "session/" + workID, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, workerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, workerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, workerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, workerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": workerRef, "execution_model": preferredModelForLane(lane)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	recordWorkerFixtureDispatchAuthorization(t, s, workID, "dispatch-"+workID, "execution", workerRef)
	dispatch := Event{
		EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
			AttemptID: "dispatch-" + workID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
			CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane),
			PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
		}),
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatalf("dispatch worker: %v", err)
	}
	return s, workerRef
}

func readWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func currentStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

func instanceState(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// The boundary is symmetric in kind: worker authority kinds are registered
// separately from workflow authority kinds, and the worker kinds are namespaced
// so no worker event can be routed to a workflow fold.
func TestWorkerEventKindsAreNotWorkflowAuthority(t *testing.T) {
	t.Parallel()
	registry := map[string]bool{}
	for kind := range eventKindRegistry {
		registry[kind] = true
	}
	for _, workflowKind := range []string{WorkflowActionStarted, WorkflowActionCompleted, WorkflowVerdictRecorded, WorkflowCompleted} {
		if !registry[workflowKind] {
			t.Fatalf("expected registered workflow authority kind %q", workflowKind)
		}
	}
	for _, workerKind := range []string{WorkerDispatched, WorkerCompleted, WorkerFailed} {
		if !registry[workerKind] {
			t.Fatalf("expected registered worker authority kind %q", workerKind)
		}
		if !strings.HasPrefix(workerKind, "worker.") {
			t.Fatalf("worker authority kind %q is not namespaced away from workflow authority", workerKind)
		}
	}
}

// The worker attempt projection structurally cannot hold workflow authority:
// its schema carries no step, verdict, completion, or actor column, so even a
// fold that wrote to it could not smuggle workflow state into the attempt
// record. Asserted against the live DDL so a later migration that adds such a
// column fails here rather than silently widening worker authority.
func TestWorkerAttemptProjectionHasNoWorkflowAuthorityColumns(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	rows, err := s.DatabaseForTesting().Query(`PRAGMA table_info(worker_attempts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"current_step", "step_id", "verdict_kind", "verdict_actor_ref", "terminal_state", "instance_state", "actor_ref", "execution_model", "contract_version"} {
		if columns[forbidden] {
			t.Fatalf("worker_attempts carries workflow authority column %q", forbidden)
		}
	}
}

// TestAcceptWorkerResultBindsTheAttemptAsEvidence: acceptance binds the
// attempt it certifies (#865). The binding names the attempt id, carries the
// lane's evidence kind, and is backed by the acceptance operation. That binding
// attributes the result; it cannot replace the criterion's producer receipt.
func TestAcceptWorkerResultBindsTheAttemptAsEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-bind")
	request := WorkflowActionExecutionRequest{
		WorkID: "authority-bind", ExpectedVersion: 12, ActionID: "accept_worker_result",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "accept-bind", OperationID: "accept-bind",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "accept-bind", RequestID: "request:accept-bind", ContractDigest: testManifestDigest, Now: time.Unix(3, 0).UTC(),
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), request); err != nil {
		tx.Rollback()
		t.Fatalf("accept: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var kind, subject, producerRun string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.evidence_kind'),json_extract(payload,'$.immutable_subject_ref'),json_extract(payload,'$.producer_run_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, "authority-bind", WorkflowEvidenceBound).Scan(&kind, &subject, &producerRun); err != nil {
		t.Fatalf("acceptance bound no evidence: %v", err)
	}
	if subject != attemptID || producerRun != "accept-bind" {
		t.Fatalf("bound evidence subject=%q producer_run=%q, want the attempt and the acceptance operation", subject, producerRun)
	}
	if kind == "" {
		t.Fatal("bound evidence has no kind")
	}
	verifyTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyTx.Rollback()
	if err := workflowCriterionReceipt(ctx, verifyTx, "authority-bind", "predicate:primary", attemptID, strings.Repeat("a", 40)); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("an accepted attempt without a criterion producer receipt must refuse: %v", err)
	}
}

// workflowExecutionSetupEvents builds the event prefix that carries one work
// item from actor registration through contract approval into a started
// execution step, with a second actor recorded for the lane.
func workflowExecutionSetupEvents(t *testing.T, prefix, workID, ownerRef string, owner WorkflowActor, laneRef string, laneActor WorkflowActor, lane LaneDefinition) []Event {
	digest := workflowFixtureDefinition(t, 2).Digest
	return []Event{
		workflowEvent(prefix+"-owner-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent(prefix+"-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture(prefix+"-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture(prefix+"-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture(prefix+"-design", workID, ownerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture(prefix+"-planning", workID, ownerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor(prefix+"-start", WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": prefix + ":start", "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
		workflowEvent(prefix+"-lane-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "actor_ref": laneRef, "principal_ref": laneActor.PrincipalRef, "client_ref": laneActor.ClientRef, "agent_ref": laneActor.AgentRef, "session_ref": laneActor.SessionRef, "actor_class": "agent"}),
	}
}

// CD-0205 P2.1 — production emitter version drift. The event registry owns the
// current payload version for each event kind; the production emitter routes
// (PrepareLaneActorDispatch for the dispatch evidence, ApplyOperation for the
// completion) must persist at the registry's current version, not at a
// hand-written competing number. A drift lands silently in the durable log
// because appendEvent persists the supplied version, so the bug is invisible
// to the fold (which upcasts at validate/replay time) and surfaces only on
// log replay where the durable row labels a job-capable shape as legacy.
func TestWorkerAuthorizePersistsRegistryCurrentDispatchVersion(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "version-dispatch")
	seedWorkflowLaw(t, s)
	lane := BuiltinLaneDefinitions()[0]
	dispatch := Event{EventID: "version-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "version-dispatch", Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt:version-dispatch", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: "sha256:" + strings.Repeat("d", 64)})}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, "principal/operator", "client/concord-1")
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("dispatch authorize failed: %v", err)
	}
	var stored int
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM domain_events WHERE event_id=?`, "version-dispatch").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	current := func() int { r, _ := registeredEventKind(WorkerDispatched); return r.CurrentVersion }()
	if stored != current {
		t.Fatalf("worker.dispatched stored payload_version=%d, want registry current %d", stored, current)
	}
}

// CD-0205 P2.1 — completion emission drift. The CLI writes the worker.completed
// event with a hand-written PayloadVersion. The store persists the supplied
// version, so the durable row should land at the registry's current version.
func TestWorkerCompletedPersistsRegistryCurrentVersion(t *testing.T) {
	t.Parallel()
	s, _ := seedDispatchedWorkerAtExecution(t, "version-completion")
	lane := BuiltinLaneDefinitions()[0]
	completed := Event{EventID: "version-completion", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "version-completion", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: "dispatch-version-completion", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, "version-completion"), EvidenceOrigin: WorkerEvidenceLegacyUnavailable})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM domain_events WHERE event_id=?`, "version-completion").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	current := func() int { r, _ := registeredEventKind(WorkerCompleted); return r.CurrentVersion }()
	if stored != current {
		t.Fatalf("worker.completed stored payload_version=%d, want registry current %d", stored, current)
	}
}

// CD-0205 P2.2 — source-version prohibition for the worker.dispatched job
// binding. The fold must refuse a payload whose recorded source version sits
// below the version the worker_job field was introduced, mirroring the
// action_completed prohibition in workflow.go. The check lives in the fold
// so the live boundary and the log-ordered replay enforce one rule.
func TestWorkerDispatchedJobBindingRefusedAtLegacySourceVersion(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "dispatch-job-legacy")
	lane := BuiltinLaneDefinitions()[0]
	current := func() int { r, _ := registeredEventKind(WorkerDispatched); return r.CurrentVersion }()
	if current <= 1 {
		t.Fatalf("dispatch current version %d is too low to stage a legacy job-binding scenario", current)
	}
	legacyVersion := current - 1
	dispatch := Event{EventID: "dispatch-job-legacy", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "dispatch-job-legacy", Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: legacyVersion, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt:dispatch-job-legacy", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: "sha256:" + strings.Repeat("e", 64), WorkerJob: &WorkerJobBinding{JobID: "job:legacy", Revision: 1, Digest: "sha256:" + strings.Repeat("f", 64)}})}
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}})
	if err == nil {
		t.Fatalf("worker.dispatched v%d with worker_job bytes was accepted; want refusal", legacyVersion)
	}
	if !strings.Contains(err.Error(), "worker_job") {
		t.Fatalf("refusal detail = %v, want the worker_job prohibition detail", err)
	}
}

// CD-0205 P2.2 — source-version prohibition for the worker.completed job
// binding. Same rule, opposite surface. The historical payload shape stays
// admissible when worker_job is absent, so a non-job-bearing completion at
// the legacy version lands; a job-bearing one refuses.
func TestWorkerCompletedJobBindingRefusedAtLegacySourceVersion(t *testing.T) {
	t.Parallel()
	s, _ := seedDispatchedWorkerAtExecution(t, "completed-job-legacy")
	lane := BuiltinLaneDefinitions()[0]
	current := func() int { r, _ := registeredEventKind(WorkerCompleted); return r.CurrentVersion }()
	if current <= 1 {
		t.Fatalf("completion current version %d is too low to stage a legacy job-binding scenario", current)
	}
	legacyVersion := current - 1
	completed := Event{EventID: "completed-job-legacy", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "completed-job-legacy", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: legacyVersion, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: "dispatch-completed-job-legacy", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, "completed-job-legacy"), EvidenceOrigin: WorkerEvidenceLegacyUnavailable, WorkerJob: &WorkerJobBinding{JobID: "job:legacy", Revision: 1, Digest: "sha256:" + strings.Repeat("f", 64)}})}
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}})
	if err == nil {
		t.Fatalf("worker.completed v%d with worker_job bytes was accepted; want refusal", legacyVersion)
	}
	if !strings.Contains(err.Error(), "worker_job") {
		t.Fatalf("refusal detail = %v, want the worker_job prohibition detail", err)
	}
}

// CD-0205 P2.3 — matched-pair immutability across dispatch and completion.
// The worker.dispatched event carries a matched packet/report schema pair
// the worker_schema_version_fault helper already validates; the worker.completed
// fold must compare its reported schema identity against the attempt row the
// dispatch fold persisted, so a completion whose report_schema_version
// disagrees with the attempt refuses. A 1.0 pair that finishes with a 1.1
// report (or the inverse) is the precise mixed legacy/current shape the
// store must reject. The retired pairs seed through the replay context —
// the lawful recorded-history route after the 2.0 cutover refuses them on
// the live boundary — and the fold-level comparison runs identically on
// replay, which is the rule under test.
func TestWorkerCompletedRefusesMixedReportSchemaPair(t *testing.T) {
	t.Parallel()
	lane := BuiltinLaneDefinitions()[0]
	current := func() int { r, _ := registeredEventKind(WorkerCompleted); return r.CurrentVersion }()

	// The mixed-shape scenario must drive both attempts through the dispatch
	// fold (the worker_attempts row is fold-only). The legacy-pair dispatch
	// admits the 1.0/1.0 identities when worker_job is absent; the current
	// dispatch records the current pair.
	seedRetiredPairDispatch := func(t *testing.T, s *Store, workID, eventID, attemptID string) {
		t.Helper()
		dispatch := Event{EventID: eventID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketDigest: "sha256:" + strings.Repeat("d", 64), PacketSchemaVersion: WorkerPacketSchemaVersionLegacy, ReportSchemaVersion: WorkerReportSchemaVersionLegacy})}
		if err := s.Transact(context.Background(), func(tx *Transaction) error {
			sqlTx, err := transactionSQL(tx, "worker_pair_seed")
			if err != nil {
				return err
			}
			_, err = applyOperationTx(workflowReplayContext(context.Background()), sqlTx, Operation{Events: []Event{dispatch}}, newFoldScope(sqlTx), false)
			return err
		}); err != nil {
			t.Fatalf("retired-pair dispatch seed refused: %v", err)
		}
	}
	appendRetiredPairCompletion := func(t *testing.T, s *Store, workID, eventID, attemptID string, reportVersion string) error {
		t.Helper()
		completed := Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: current, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: reportVersion, EvidenceOrigin: WorkerEvidenceLegacyUnavailable})}
		err := s.Transact(context.Background(), func(tx *Transaction) error {
			sqlTx, err := transactionSQL(tx, "worker_pair_seed")
			if err != nil {
				return err
			}
			_, err = applyOperationTx(workflowReplayContext(context.Background()), sqlTx, Operation{Events: []Event{completed}}, newFoldScope(sqlTx), false)
			return err
		})
		return err
	}

	// Same-identity pair lands: a 1.0/1.0 attempt admits a 1.0 completion.
	s, _ := seedDispatchedWorkerAtExecution(t, "matched-pair-legacy")
	seedRetiredPairDispatch(t, s, "matched-pair-legacy", "matched-pair-legacy-dispatch", "dispatch-matched-pair-legacy-2")
	if err := appendRetiredPairCompletion(t, s, "matched-pair-legacy", "matched-pair-legacy-ok", "dispatch-matched-pair-legacy-2", WorkerReportSchemaVersionLegacy); err != nil {
		t.Fatalf("matched 1.0 pair completion refused: %v", err)
	}

	// Legacy attempt + job-capable completion: dispatch recorded 1.0/1.0 on the
	// row; a completion that claims the job-capable 1.1 refuses closed.
	legacyAttemptID := "dispatch-mixed-pair-legacy-claim"
	seedRetiredPairDispatch(t, s, "matched-pair-legacy", "mixed-pair-legacy-claim-dispatch", legacyAttemptID)
	if err := appendRetiredPairCompletion(t, s, "matched-pair-legacy", "mixed-pair-legacy-mismatch", legacyAttemptID, WorkerReportSchemaVersionPrior); err == nil {
		t.Fatalf("worker.completed with report_schema_version %q accepted against an attempt row at %q; want refusal",
			WorkerReportSchemaVersionPrior, WorkerReportSchemaVersionLegacy)
	}

	// Inverse: a current 2.0/2.0 attempt refuses a completion that claims 1.0.
	s2, pairWorkerRef := seedDispatchedWorkerAtExecution(t, "mixed-pair-current")
	recordWorkerFixtureDispatchAuthorization(t, s2, "mixed-pair-current", "dispatch-mixed-pair-current-2", "execution", pairWorkerRef)
	currentDispatch := Event{EventID: "mixed-pair-current-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "mixed-pair-current", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "dispatch-mixed-pair-current-2", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketDigest: "sha256:" + strings.Repeat("e", 64), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(context.Background(), s2, Operation{Events: []Event{currentDispatch}}); err != nil {
		t.Fatalf("current-pair dispatch refused: %v", err)
	}
	completedLegacy := Event{EventID: "mixed-pair-current-legacy", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "mixed-pair-current", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: current, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: "dispatch-mixed-pair-current-2", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersionLegacy, EvidenceOrigin: WorkerEvidenceLegacyUnavailable})}
	if err := ApplyOperation(context.Background(), s2, Operation{Events: []Event{completedLegacy}}); err == nil {
		t.Fatalf("worker.completed with report_schema_version %q accepted against an attempt row at %q; want refusal",
			WorkerReportSchemaVersionLegacy, WorkerReportSchemaVersion)
	} else if !strings.Contains(err.Error(), "report_schema_version") {
		t.Fatalf("refusal detail = %v, want the matched-pair detail", err)
	}
}
