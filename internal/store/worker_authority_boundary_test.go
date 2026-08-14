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
			CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: RoutingPolicyVersion, RoutingPolicyDigest: RoutingPolicyManifestDigest,
			ResolvedModel: lane.PinnedModel, ResolutionRole: WorkerResolutionPreferred,
			PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
		}),
	}
	// The same actor identity is an agent-class workflow actor on the work
	// item, with the implementation definition selected — the position a worker
	// holds when it executes one step.
	workerRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/worker", "session/worker-1")
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEvent("authority-actor", WorkflowActorRecorded, "authority-work", map[string]any{"work_id": "authority-work", "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/worker", "session_ref": "session/worker-1", "actor_class": "agent"}),
		workflowEvent("authority-definition", WorkflowDefinitionSelected, "authority-work", map[string]any{"work_id": "authority-work", "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 2, "digest": digest, "work_kind": "implementation"}),
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
	start := workflowEventWithActor("authority-start", WorkflowActionStarted, "authority-work", workerRef, map[string]any{"work_id": "authority-work", "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "authority:start", "actor_ref": workerRef, "execution_model": lane.PinnedModel})
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
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, "authority-work", WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("a refused workflow completion was recorded: %d", completed)
	}
}

func TestWorkerCannotAdvanceItsStepWithAnUndeclaredCompletionAction(t *testing.T) {
	ctx := context.Background()
	s, workerRef := seedDispatchedWorkerAtExecution(t, "authority-step-transition")
	completed := workflowEventWithActor("authority-step-transition-completed", WorkflowActionCompleted, "authority-step-transition", workerRef, map[string]any{
		"work_id": "authority-step-transition", "expected_version": 9, "resulting_version": 10,
		"step_id": "execution", "action_id": "record_proposal", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": workerRef,
	})
	err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-step-transition"): 9}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("worker step transition failure = %v, want %s", err, KindIllegalLifecycleTransition)
	}
	if got := currentStep(t, s, "authority-step-transition"); got != "execution" {
		t.Fatalf("rejected worker completion advanced step to %q", got)
	}
	if got := readWorkVersion(t, s, "authority-step-transition"); got != 9 {
		t.Fatalf("rejected worker completion advanced version to %d", got)
	}
}

func TestWorkerCannotRecordItsOwnVerdict(t *testing.T) {
	ctx := context.Background()
	s, workerRef := seedDispatchedWorkerAtExecution(t, "authority-self-verdict")
	verdict := workflowEventWithActor("authority-self-verdict-recorded", WorkflowVerdictRecorded, "authority-self-verdict", workerRef, map[string]any{
		"work_id": "authority-self-verdict", "expected_version": 9, "resulting_version": 10,
		"contract_version": 1, "predicate_id": "predicate:self", "verdict_kind": "ok",
		"verdict_actor_ref": workerRef, "evaluation_evidence": []string{"evidence:self"},
		"incomparable_with_approved": false,
	})
	err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-self-verdict"): 9}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("worker self-verdict failure = %v, want %s", err, KindUnauthorized)
	}
	if got := readWorkVersion(t, s, "authority-self-verdict"); got != 9 {
		t.Fatalf("rejected worker verdict advanced version to %d", got)
	}
}

func TestWorkflowActionStartedV2AuthorizesActorStepAndEpoch(t *testing.T) {
	s, workerRef, owner, _ := seedWorkerAtExecution(t, "authority-start-guards")
	currentVersion := int64(10)
	wantCurrentStep := "execution"
	beforeStarts := countWorkflowActionStarts(t, s, "authority-start-guards")

	t.Run("forged actor", func(t *testing.T) {
		start := workflowEventWithActor("forged-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "forged-start", "actor_ref": owner.ActorRef})
		assertRejectedActionStart(t, s, "authority-start-guards", start, currentVersion, KindUnauthorized, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("future step", func(t *testing.T) {
		start := workflowEventWithActor("future-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": "acceptance", "action_id": "record_verdict", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "future-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, "authority-start-guards", start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("undeclared current-step action", func(t *testing.T) {
		start := workflowEventWithActor("undeclared-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "record_proposal", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "undeclared-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, "authority-start-guards", start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("duplicate epoch", func(t *testing.T) {
		start := workflowEventWithActor("duplicate-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "duplicate-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, "authority-start-guards", start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})
	t.Run("jumped epoch", func(t *testing.T) {
		start := workflowEventWithActor("jumped-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 3, "accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": "jumped-start", "actor_ref": workerRef})
		assertRejectedActionStart(t, s, "authority-start-guards", start, currentVersion, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion, beforeStarts)
	})

	exact := workflowEventWithActor("exact-retry-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion, "resulting_version": currentVersion + 1, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("g", 64), "idempotency_identity": "exact-retry-start", "actor_ref": workerRef, "execution_model": BuiltinLaneDefinitions()[0].PinnedModel})
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

	stale := workflowEventWithActor("stale-retry-start", WorkflowActionStarted, "authority-start-guards", workerRef, map[string]any{"work_id": "authority-start-guards", "expected_version": currentVersion + 1, "resulting_version": currentVersion + 2, "step_id": wantCurrentStep, "action_id": "start_execution", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("h", 64), "idempotency_identity": "stale-retry-start", "actor_ref": workerRef})
	assertRejectedActionStart(t, s, "authority-start-guards", stale, currentVersion+1, KindIllegalLifecycleTransition, wantCurrentStep, currentVersion+1, beforeStarts+1)
}

func assertRejectedActionStart(t *testing.T, s *Store, workID string, event Event, expectedVersion int64, wantKind FailureKind, wantStep string, wantVersion int64, wantStarts int) {
	t.Helper()
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	err = func() error {
		_, err := applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}})
		return err
	}()
	_ = leaveFold(context.Background(), tx)
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("action start failure=%v, want %s", err, wantKind)
	}
	if got := currentStep(t, s, workID); got != wantStep {
		t.Fatalf("rejected action start current_step=%q, want %q", got, wantStep)
	}
	if got := readWorkVersion(t, s, workID); got != wantVersion {
		t.Fatalf("rejected action start version=%d, want %d", got, wantVersion)
	}
	if got := countWorkflowActionStarts(t, s, workID); got != wantStarts {
		t.Fatalf("rejected action start count=%d, want %d", got, wantStarts)
	}
}

func countWorkflowActionStarts(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionStarted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCheckpointedV2BindsActorStepKindAndEpoch(t *testing.T) {
	workID := "authority-checkpoint-guards"
	s, workerRef, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	baseVersion := int64(10)
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
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-forged-actor", workerRef, ownerRef, "execution", "external_effect", 1), baseVersion, KindUnauthorized, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-step", workerRef, workerRef, "acceptance", "external_effect", 1), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-kind", workerRef, workerRef, "execution", "human_checkpoint", 1), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-epoch", workerRef, workerRef, "execution", "external_effect", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-non-executor", ownerRef, ownerRef, "execution", "external_effect", 1), baseVersion, KindUnauthorized, baseCheckpoints)

	valid := checkpoint("checkpoint-valid", workerRef, workerRef, "execution", "external_effect", 1)
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
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}})
	_ = leaveFold(context.Background(), tx)
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
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_checkpoints WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCompletedV2BindsPayloadActorToEventActor(t *testing.T) {
	workID := "authority-completed-actor"
	s, workerRef, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	before := countWorkflowActionCompleted(t, s, workID)
	completed := workflowEventWithActor("forged-completed-actor", WorkflowActionCompleted, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": 10, "resulting_version": 11,
		"step_id": "execution", "action_id": "checkpoint_execution", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": ownerRef,
	})
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 10}})
	_ = leaveFold(context.Background(), tx)
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("forged completed actor failure=%v, want %s", err, KindUnauthorized)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("forged completion changed current_step=%q", got)
	}
	if got := readWorkVersion(t, s, workID); got != 10 {
		t.Fatalf("forged completion changed version=%d", got)
	}
	if got := countWorkflowActionCompleted(t, s, workID); got != before {
		t.Fatalf("forged completion count=%d, want %d", got, before)
	}
}

func TestWorkflowActionFailedV2BindsCurrentExecutorAttempt(t *testing.T) {
	t.Run("forged actor", func(t *testing.T) {
		s, workerRef, owner, _ := seedWorkerAtExecution(t, "authority-failed-forged-actor")
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-forged-actor", "authority-failed-forged-actor", workerRef, ownerRef, "execution", 1, true), KindUnauthorized, "running", 10)
	})
	t.Run("wrong step", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-wrong-step")
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-wrong-step", "authority-failed-wrong-step", workerRef, workerRef, "acceptance", 1, true), KindIllegalLifecycleTransition, "running", 10)
	})
	t.Run("stale epoch", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-stale-epoch")
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-stale-epoch", "authority-failed-stale-epoch", workerRef, workerRef, "execution", 2, true), KindIllegalLifecycleTransition, "running", 10)
	})
	t.Run("non-executor actor", func(t *testing.T) {
		s, _, owner, _ := seedWorkerAtExecution(t, "authority-failed-non-executor")
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedActionFailed(t, s, actionFailedFixture("failed-non-executor", "authority-failed-non-executor", ownerRef, ownerRef, "execution", 1, true), KindUnauthorized, "running", 10)
	})
	t.Run("recoverable failure keeps workflow running", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-recoverable")
		event := actionFailedFixture("failed-recoverable", "authority-failed-recoverable", workerRef, workerRef, "execution", 1, true)
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-failed-recoverable"): 10}}); err != nil {
			t.Fatalf("current executor recoverable failure rejected: %v", err)
		}
		if got := instanceState(t, s, "authority-failed-recoverable"); got != "running" {
			t.Fatalf("recoverable failure state=%q, want running", got)
		}
		if got := readWorkVersion(t, s, "authority-failed-recoverable"); got != 11 {
			t.Fatalf("recoverable failure version=%d, want 11", got)
		}
	})
	t.Run("nonrecoverable failure blocks workflow", func(t *testing.T) {
		s, workerRef, _, _ := seedWorkerAtExecution(t, "authority-failed-blocked")
		event := actionFailedFixture("failed-blocked", "authority-failed-blocked", workerRef, workerRef, "execution", 1, false)
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-failed-blocked"): 10}}); err != nil {
			t.Fatalf("current executor nonrecoverable failure rejected: %v", err)
		}
		if got := instanceState(t, s, "authority-failed-blocked"); got != "blocked" {
			t.Fatalf("nonrecoverable failure state=%q, want blocked", got)
		}
		if got := readWorkVersion(t, s, "authority-failed-blocked"); got != 11 {
			t.Fatalf("nonrecoverable failure version=%d, want 11", got)
		}
	})
}

func actionFailedFixture(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool) Event {
	return workflowEventWithActor(eventID, WorkflowActionFailed, workID, eventActor, map[string]any{
		"work_id": workID, "expected_version": 10, "resulting_version": 11,
		"step_id": stepID, "attempt_epoch": epoch, "failure_kind": "timeout", "recoverable": recoverable,
		"actor_ref": payloadActor,
	})
}

func assertRejectedActionFailed(t *testing.T, s *Store, event Event, wantKind FailureKind, wantState string, wantVersion int64) {
	t.Helper()
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): wantVersion}}); !hasFailureKind(err, wantKind) {
		t.Fatalf("action_failed error=%v, want %s", err, wantKind)
	}
	if got := instanceState(t, s, event.SubjectID); got != wantState {
		t.Fatalf("rejected action_failed state=%q, want %q", got, wantState)
	}
	if got := readWorkVersion(t, s, event.SubjectID); got != wantVersion {
		t.Fatalf("rejected action_failed version=%d, want %d", got, wantVersion)
	}
	var events int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, event.SubjectID, WorkflowActionFailed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected action_failed persisted %d events", events)
	}
}

func countWorkflowActionCompleted(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowCompletedV2RejectsExecutorAfterIndependentVerdict(t *testing.T) {
	workID := "authority-completed-executor"
	s, workerRef, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	verdict := workflowEventWithActor("independent-verdict", WorkflowVerdictRecorded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": 10, "resulting_version": 11, "contract_version": 1,
		"predicate_id": "predicate:authority", "verdict_kind": "ok", "verdict_actor_ref": ownerRef,
		"evaluation_evidence": []string{"evidence:authority"}, "incomparable_with_approved": false,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 10}}); err != nil {
		t.Fatal(err)
	}
	completion := workflowEventWithActor("executor-completion", WorkflowCompleted, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": 11, "resulting_version": 12, "terminal_state": "completed",
		"final_verdict_kind": "ok", "verdict_actor_ref": ownerRef, "premise_confirmed": true,
		"evidence_count": 1, "changed_refs_digest": "sha256:" + strings.Repeat("j", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	err = applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 11}})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("executor completion failure=%v, want %s", err, KindUnauthorized)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("executor completion changed current_step=%q", got)
	}
	if got := readWorkVersion(t, s, workID); got != 11 {
		t.Fatalf("executor completion changed version=%d", got)
	}
	if got := instanceState(t, s, workID); got != "running" {
		t.Fatalf("executor completion changed state=%q", got)
	}
	var completed int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("executor completion persisted %d workflow.completed events", completed)
	}
}

func TestWorkflowCompletedV2AllowsDistinctOwnerFold(t *testing.T) {
	workID := "authority-completed-owner"
	s, _, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	verdict := workflowEventWithActor("owner-verdict", WorkflowVerdictRecorded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": 10, "resulting_version": 11, "contract_version": 1,
		"predicate_id": "predicate:authority", "verdict_kind": "ok", "verdict_actor_ref": ownerRef,
		"evaluation_evidence": []string{"evidence:authority"}, "incomparable_with_approved": false,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{verdict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 10}}); err != nil {
		t.Fatal(err)
	}
	completion := workflowEventWithActor("owner-completion", WorkflowCompleted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": 11, "resulting_version": 12, "terminal_state": "completed",
		"final_verdict_kind": "ok", "verdict_actor_ref": ownerRef, "premise_confirmed": true,
		"evidence_count": 1, "changed_refs_digest": "sha256:" + strings.Repeat("k", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 11}}); err != nil {
		t.Fatalf("distinct owner completion rejected: %v", err)
	}
	if got := instanceState(t, s, workID); got != "completed" {
		t.Fatalf("owner completion state=%q, want completed", got)
	}
}

func TestWorkflowCompletedV2AllowsDistinctOwnerThroughOrderedGate(t *testing.T) {
	workID := "authority-ordered-owner"
	s, completion := seedV2CompletionReady(t, workID)
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("distinct owner completion rejected by ordered gate: %v", err)
	}
	if got := instanceState(t, s, workID); got != "completed" {
		t.Fatalf("ordered owner completion state=%q, want completed", got)
	}
	if got := readWorkVersion(t, s, workID); got != 18 {
		t.Fatalf("ordered owner completion version=%d, want 18", got)
	}
}

func seedV2CompletionReady(t *testing.T, workID string) (*Store, Event) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	executor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/executor", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID, ActorClass: ActorOperator}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	executorRef, err := WorkflowActorRef(executor)
	if err != nil {
		t.Fatal(err)
	}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		workflowEvent("v2-executor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": executorRef, "principal_ref": executor.PrincipalRef, "client_ref": executor.ClientRef, "agent_ref": executor.AgentRef, "session_ref": executor.SessionRef, "actor_class": "agent"}),
		workflowEvent("v2-operator-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"}),
		workflowEvent("v2-owner-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("v2-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 5, "resulting_version": 6, "ref": "workflow.implementation", "version": 2, "digest": digest, "work_kind": "implementation"}),
		workflowActionCompletedFixture("v2-proposal-"+workID, workID, executorRef, 6, "proposal", "record_proposal"),
		workflowActionCompletedFixture("v2-discovery-"+workID, workID, executorRef, 7, "discovery", "record_discovery"),
		workflowActionCompletedFixture("v2-design-"+workID, workID, executorRef, 8, "design", "record_design"),
		workflowActionCompletedFixture("v2-planning-"+workID, workID, executorRef, 9, "planning", "approve_contract"),
		workflowEventWithActor("v2-contract-"+workID, WorkflowContractApproved, workID, executorRef, map[string]any{"work_id": workID, "expected_version": 10, "resulting_version": 11, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("v2-start-"+workID, WorkflowActionStarted, workID, executorRef, map[string]any{"work_id": workID, "expected_version": 11, "resulting_version": 12, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("l", 64), "idempotency_identity": "v2-start:" + workID, "actor_ref": executorRef, "execution_model": BuiltinLaneDefinitions()[0].PinnedModel}),
		workflowEventWithActor("v2-impact-"+workID, WorkflowImpactDeclared, workID, executorRef, map[string]any{"work_id": workID, "expected_version": 12, "resulting_version": 13, "edge_id": "edge:" + workID, "edge_kind": "modifies", "edge_class": "none", "target_work_id": workID, "target_kind": "work_item", "severity": "breaking"}),
	}
	seedWorkflowAuthority(t, s, "v2-verification-"+workID, workID, "principal/verify", "request/verify", []string{"evidence:verification"})
	seedWorkflowAuthority(t, s, "v2-review-"+workID, workID, "principal/review", "request/review", []string{"evidence:review"})
	events = append(events,
		workflowEventWithActor("v2-verification-"+workID, WorkflowEvidenceBound, workID, executorRef, map[string]any{"work_id": workID, "expected_version": 13, "resulting_version": 14, "evidence_kind": "verification", "immutable_subject_ref": "evidence:verification", "producer_id": "principal/verify", "producer_run_ref": "v2-verification-" + workID, "producer_watermark": "request/verify", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEventWithActor("v2-review-"+workID, WorkflowEvidenceBound, workID, executorRef, map[string]any{"work_id": workID, "expected_version": 14, "resulting_version": 15, "evidence_kind": "review", "immutable_subject_ref": "evidence:review", "producer_id": "principal/review", "producer_run_ref": "v2-review-" + workID, "producer_watermark": "request/review", "observed_at": "2026-08-09T00:00:00Z"}),
		workflowEventWithActor("v2-verdict-"+workID, WorkflowVerdictRecorded, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": 15, "resulting_version": 16, "contract_version": 1, "predicate_id": "predicate:gate", "verdict_kind": "ok", "verdict_actor_ref": operatorRef, "evaluation_evidence": []string{"evidence:verification"}, "incomparable_with_approved": false}),
		workflowEventWithActor("v2-premise-"+workID, WorkflowPremiseConfirmed, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": 16, "resulting_version": 17, "contract_version": 1, "confirming_actor_ref": operatorRef}),
	)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	completion := workflowEventWithActor("v2-completion-"+workID, WorkflowCompleted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 17, "resulting_version": 18, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": operatorRef, "premise_confirmed": true, "evidence_count": 2, "changed_refs_digest": "sha256:" + strings.Repeat("m", 64), "impact_verdict": "non-breaking"})
	completion.PayloadVersion = 2
	return s, completion
}

func TestDistinctWorkflowOwnerAcceptsCompletedWorkerResult(t *testing.T) {
	ctx := context.Background()
	s, worker, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-accept")
	request := WorkflowActionExecutionRequest{
		WorkID: "authority-accept", ExpectedVersion: 10, ActionID: "accept_worker_result",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "accept-authority", OperationID: "accept-authority",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "accept-authority", RequestID: "request:accept-authority", ContractVersion: "2.0.0", Now: time.Unix(3, 0).UTC(),
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	result, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
	_ = leaveFold(ctx, tx)
	if err != nil {
		tx.Rollback()
		t.Fatalf("distinct owner could not accept completed worker result: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if result.ResultingVersion != 11 {
		t.Fatalf("accept result version=%d, want 11", result.ResultingVersion)
	}
	if got := currentStep(t, s, "authority-accept"); got != "acceptance" {
		t.Fatalf("accepted worker result current_step=%q, want acceptance", got)
	}
	if got := readWorkVersion(t, s, "authority-accept"); got != 11 {
		t.Fatalf("accepted worker result version=%d, want 7", got)
	}
	var executionActor string
	if err := s.DB().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, "authority-accept").Scan(&executionActor); err != nil {
		t.Fatal(err)
	}
	if executionActor != worker {
		t.Fatalf("acceptance overwrote execution actor with %q, want %q", executionActor, worker)
	}
	var payloadVersion int
	var workerAttempt string
	if err := s.DB().QueryRow(`SELECT payload_version,json_extract(payload,'$.worker_attempt_id') FROM domain_events WHERE event_id=?`, "accept-authority:completed").Scan(&payloadVersion, &workerAttempt); err != nil {
		t.Fatal(err)
	}
	if payloadVersion != 2 || workerAttempt != attemptID {
		t.Fatalf("accept completion evidence = version %d attempt %q, want v2 %q", payloadVersion, workerAttempt, attemptID)
	}
}

func TestWorkerCannotInvokeAcceptWorkerResultAsItsOwnOwner(t *testing.T) {
	ctx := context.Background()
	s, worker, _, attemptID := seedCompletedWorkerAtExecution(t, "authority-worker-accept")
	request := WorkflowActionExecutionRequest{
		WorkID: "authority-worker-accept", ExpectedVersion: 10, ActionID: "accept_worker_result",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}), Actor: workflowActorForRef(t, s, worker),
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "worker-accept-authority", OperationID: "worker-accept-authority",
		PrincipalRef: "principal/operator", Tool: "concord_work_transition", IdempotencyKey: "worker-accept-authority", RequestID: "request:worker-accept-authority", ContractVersion: "2.0.0", Now: time.Unix(3, 0).UTC(),
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
	_ = leaveFold(ctx, tx)
	if err == nil {
		tx.Rollback()
		t.Fatal("worker accepted its own result")
	}
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("worker self-accept failure = %v, want %s", err, KindUnauthorized)
	}
	if got := currentStep(t, s, "authority-worker-accept"); got != "execution" {
		t.Fatalf("rejected worker acceptance advanced current_step=%q", got)
	}
	if got := readWorkVersion(t, s, "authority-worker-accept"); got != 10 {
		t.Fatalf("rejected worker acceptance advanced version=%d", got)
	}
}

func TestAcceptWorkerResultRejectsWithoutMutation(t *testing.T) {
	const (
		wantStep    = "execution"
		wantVersion = int64(10)
	)
	t.Run("missing attempt id", func(t *testing.T) {
		s, _, owner, _ := seedWorkerAtExecution(t, "authority-missing-attempt")
		assertRejectedWorkerAcceptance(t, s, "authority-missing-attempt", owner, wantVersion, map[string]any{"attempt_epoch": 1}, KindInvalidPayload, wantStep, wantVersion)
	})
	t.Run("dispatched but not completed", func(t *testing.T) {
		s, _, owner, attemptID := seedWorkerAtExecution(t, "authority-dispatched-only")
		assertRejectedWorkerAcceptance(t, s, "authority-dispatched-only", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, KindIllegalLifecycleTransition, wantStep, wantVersion)
	})
	t.Run("failed attempt", func(t *testing.T) {
		s, _, owner, attemptID := seedWorkerAtExecution(t, "authority-failed-attempt")
		fail := Event{EventID: "failed-authority-failed-attempt", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: "authority-failed-attempt", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: BuiltinLaneDefinitions()[0].PinnedModel, FailureKind: WorkerFailureWorkerError, Detail: "worker failed"})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{fail}}); err != nil {
			t.Fatal(err)
		}
		var lifecycle string
		if err := s.DB().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if lifecycle != "failed" {
			t.Fatalf("failed worker lifecycle=%q, want failed", lifecycle)
		}
		assertRejectedWorkerAcceptance(t, s, "authority-failed-attempt", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, KindIllegalLifecycleTransition, wantStep, wantVersion)
	})
	t.Run("foreign work attempt", func(t *testing.T) {
		s, _, owner, _ := seedWorkerAtExecution(t, "authority-foreign-target")
		seedWork(t, s, "authority-foreign-work")
		attemptID := "attempt:authority-foreign-work"
		lane := BuiltinLaneDefinitions()[0]
		dispatch := Event{EventID: "dispatch-authority-foreign-work", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "authority-foreign-work", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: RoutingPolicyVersion, RoutingPolicyDigest: RoutingPolicyManifestDigest, ResolvedModel: lane.PinnedModel, ResolutionRole: WorkerResolutionPreferred, PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
			t.Fatal(err)
		}
		completed := Event{EventID: "completed-authority-foreign-work", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "authority-foreign-work", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: lane.PinnedModel, ReportSchemaVersion: WorkerReportSchemaVersion})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
			t.Fatal(err)
		}
		assertRejectedWorkerAcceptance(t, s, "authority-foreign-target", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, KindIllegalLifecycleTransition, wantStep, wantVersion)
	})
	t.Run("stale dispatch before newer retry start", func(t *testing.T) {
		s, workerRef, owner, attemptID := seedWorkerAtExecution(t, "authority-stale-dispatch")
		retryStart := workflowEventWithActor("retry-start-authority-stale-dispatch", WorkflowActionStarted, "authority-stale-dispatch", workerRef, map[string]any{"work_id": "authority-stale-dispatch", "expected_version": 10, "resulting_version": 11, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 2, "accepted_inputs_digest": "sha256:" + strings.Repeat("d", 64), "idempotency_identity": "retry:authority-stale-dispatch", "actor_ref": workerRef, "execution_model": BuiltinLaneDefinitions()[0].PinnedModel})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{retryStart}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "authority-stale-dispatch"): 10}}); err != nil {
			t.Fatal(err)
		}
		assertRejectedWorkerAcceptance(t, s, "authority-stale-dispatch", owner, 11, map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}, KindIllegalLifecycleTransition, wantStep, 11)
	})
	t.Run("wrong attempt epoch", func(t *testing.T) {
		s, _, owner, attemptID := seedCompletedWorkerAtExecution(t, "authority-wrong-epoch")
		assertRejectedWorkerAcceptance(t, s, "authority-wrong-epoch", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}, KindIllegalLifecycleTransition, wantStep, wantVersion)
	})
	t.Run("model mismatch leaves failed identity state", func(t *testing.T) {
		s, _, owner, attemptID := seedWorkerAtExecution(t, "authority-model-mismatch")
		mismatch := Event{EventID: "completed-authority-model-mismatch", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "authority-model-mismatch", Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/other-model", ReportSchemaVersion: WorkerReportSchemaVersion})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{mismatch}}); err != nil {
			t.Fatal(err)
		}
		var lifecycle string
		if err := s.DB().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if lifecycle != "failed" {
			t.Fatalf("model-mismatch worker lifecycle=%q, want failed", lifecycle)
		}
		assertRejectedWorkerAcceptance(t, s, "authority-model-mismatch", owner, wantVersion, map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, KindIllegalLifecycleTransition, wantStep, wantVersion)
	})
}

func assertRejectedWorkerAcceptance(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, payload map[string]any, wantKind FailureKind, wantStep string, wantVersion int64) {
	t.Helper()
	var completedBefore int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&completedBefore); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = ApplyWorkflowActionTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expectedVersion, ActionID: "accept_worker_result", Payload: mustJSONValue(payload), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "reject:" + workID, OperationID: "reject:" + workID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "reject:" + workID, RequestID: "request:reject:" + workID, ContractVersion: "2.0.0", Now: time.Unix(4, 0).UTC(),
	})
	_ = leaveFold(context.Background(), tx)
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("accept_worker_result unexpectedly succeeded")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("accept_worker_result failure=%v, want %s", err, wantKind)
	}
	if got := currentStep(t, s, workID); got != wantStep {
		t.Fatalf("rejected acceptance current_step=%q, want %q", got, wantStep)
	}
	if got := readWorkVersion(t, s, workID); got != wantVersion {
		t.Fatalf("rejected acceptance version=%d, want %d", got, wantVersion)
	}
	var completed int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != completedBefore {
		t.Fatalf("rejected acceptance persisted a new workflow.action_completed event: before=%d after=%d", completedBefore, completed)
	}
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
	completed := Event{EventID: "completed-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: lane.PinnedModel, ReportSchemaVersion: WorkerReportSchemaVersion})}
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
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEvent("worker-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": workerActor.PrincipalRef, "client_ref": workerActor.ClientRef, "agent_ref": workerActor.AgentRef, "session_ref": workerActor.SessionRef, "actor_class": "agent"}),
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "ref": "workflow.implementation", "version": 2, "digest": digest, "work_kind": "implementation"}),
		workflowActionCompletedFixture("proposal-"+workID, workID, workerRef, 5, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, workerRef, 6, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, workerRef, 7, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, workerRef, 8, "planning", "approve_contract"),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": workerRef, "execution_model": lane.PinnedModel}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID
	dispatch := Event{EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: RoutingPolicyVersion, RoutingPolicyDigest: RoutingPolicyManifestDigest, ResolvedModel: lane.PinnedModel, ResolutionRole: WorkerResolutionPreferred, PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatal(err)
	}
	return s, workerRef, owner, attemptID
}

func workflowActorForRef(t *testing.T, s *Store, actorRef string) WorkflowActor {
	t.Helper()
	var actor WorkflowActor
	if err := s.DB().QueryRow(`SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actor.ActorRef, &actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass); err != nil {
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
	digest, err := WorkflowDefinitionDigest(BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEvent("actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/worker", "session_ref": "session/" + workID, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 2, "digest": digest, "work_kind": "implementation"}),
		workflowActionCompletedFixture("proposal-"+workID, workID, workerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, workerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, workerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, workerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("start-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": workerRef, "execution_model": lane.PinnedModel}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{
		EventID: "dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
			AttemptID: "dispatch-" + workID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
			CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: RoutingPolicyVersion, RoutingPolicyDigest: RoutingPolicyManifestDigest,
			ResolvedModel: lane.PinnedModel, ResolutionRole: WorkerResolutionPreferred,
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
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func currentStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.DB().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

func instanceState(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var state string
	if err := s.DB().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// The boundary is symmetric in kind: worker authority kinds are registered
// separately from workflow authority kinds, and the worker kinds are namespaced
// so no worker event can be routed to a workflow fold.
func TestWorkerEventKindsAreNotWorkflowAuthority(t *testing.T) {
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
	s := openTemp(t)
	rows, err := s.DB().Query(`PRAGMA table_info(worker_attempts)`)
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
