package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOrdinaryExecutionStartDoesNotGrantCompletion(t *testing.T) {
	t.Parallel()
	workID := "authority-work"
	s, executor, _ := seedOrdinaryExecutorAtExecution(t, workID)
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("starting a step advanced it: %q", got)
	}
	before := readWorkVersion(t, s, workID)
	completion := workflowEventWithActor("authority-completion", WorkflowCompleted, workID, executor, map[string]any{
		"work_id": workID, "expected_version": before, "resulting_version": before + 1,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": executor,
		"premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("c", 64),
	})
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{completion}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}}); err == nil {
		t.Fatal("an execution start authorized workflow completion")
	}
	if got := instanceState(t, s, workID); got != "running" {
		t.Fatalf("refused completion changed instance state: %q", got)
	}
	var completed int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 || readWorkVersion(t, s, workID) != before {
		t.Fatalf("refused completion changed durable state: completed=%d", completed)
	}
}

func TestExecutorCannotAdvanceItsStepWithAnUndeclaredCompletionAction(t *testing.T) {
	t.Parallel()
	workID := "authority-step-transition"
	s, executor, _ := seedOrdinaryExecutorAtExecution(t, workID)
	before := readWorkVersion(t, s, workID)
	completed := workflowEventWithActor("authority-step-transition-completed", WorkflowActionCompleted, workID, executor, map[string]any{
		"work_id": workID, "expected_version": before, "resulting_version": before + 1,
		"step_id": "execution", "action_id": "record_proposal", "attempt_epoch": 2,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": executor,
	})
	err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): before}})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("undeclared completion = %v, want illegal_lifecycle_transition", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != before {
		t.Fatal("refused completion advanced the step or version")
	}
}

func TestWorkflowActionStartedV2AuthorizesActorStepAndEpoch(t *testing.T) {
	t.Parallel()
	workID := "authority-start-guards"
	s, executor, owner := seedOrdinaryExecutorAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	currentVersion := int64(12)
	beforeStarts := countWorkflowActionStarts(t, s, workID)
	start := func(id, step, action, actor string, epoch, version int64) Event {
		return workflowEventWithActor(id, WorkflowActionStarted, workID, executor, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": step, "action_id": action, "attempt_epoch": epoch,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("f", 64), "idempotency_identity": id,
			"actor_ref": actor, "execution_model": "model:fixture",
		})
	}
	for _, test := range []struct {
		name, step, action, actor string
		epoch                     int64
		kind                      FailureKind
	}{
		{"forged actor", "execution", "start_execution", ownerRef, 3, KindUnauthorized},
		{"future step", "acceptance", "record_verdict", executor, 3, KindIllegalLifecycleTransition},
		{"undeclared current-step action", "execution", "record_proposal", executor, 3, KindIllegalLifecycleTransition},
		{"duplicate epoch", "execution", "start_execution", executor, 1, KindIllegalLifecycleTransition},
		{"jumped epoch", "execution", "start_execution", executor, 4, KindIllegalLifecycleTransition},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertRejectedActionStart(t, s, start(strings.ReplaceAll(test.name, " ", "-"), test.step, test.action, test.actor, test.epoch, currentVersion), currentVersion, test.kind, "execution", currentVersion, beforeStarts)
		})
	}
	exact := start("exact-retry-start", "execution", "start_execution", executor, 3, currentVersion)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{exact}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): currentVersion}}); err != nil {
		t.Fatalf("exact next epoch rejected: %v", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != currentVersion+1 || countWorkflowActionStarts(t, s, workID) != beforeStarts+1 {
		t.Fatal("exact next epoch changed the step or failed to persist exactly one start/version")
	}
	stale := start("stale-retry-start", "execution", "start_execution", executor, 3, currentVersion+1)
	assertRejectedActionStart(t, s, stale, currentVersion+1, KindIllegalLifecycleTransition, "execution", currentVersion+1, beforeStarts+1)
}

func assertRejectedActionStart(t *testing.T, s *Store, event Event, expectedVersion int64, wantKind FailureKind, wantStep string, wantVersion int64, wantStarts int) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(t.Context(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): expectedVersion}}, newWriteScope(tx))
	_ = tx.Rollback()
	if !hasFailureKind(err, wantKind) {
		t.Fatalf("action start = %v, want %s", err, wantKind)
	}
	if currentStep(t, s, event.SubjectID) != wantStep || readWorkVersion(t, s, event.SubjectID) != wantVersion || countWorkflowActionStarts(t, s, event.SubjectID) != wantStarts {
		t.Fatal("refused action start changed step, version or start count")
	}
}

func countWorkflowActionStarts(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowActionStarted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCheckpointedV2BindsActorStepKindAndEpoch(t *testing.T) {
	t.Parallel()
	workID := "authority-checkpoint-guards"
	s, executor, owner := seedOrdinaryExecutorAtExecutionWithCheckpoint(t, workID, false)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	baseVersion := int64(11)
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
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-forged-actor", executor, ownerRef, "execution", "external_effect", 2), baseVersion, KindUnauthorized, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-step", executor, executor, "acceptance", "external_effect", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-kind", executor, executor, "execution", "human_checkpoint", 2), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-wrong-epoch", executor, executor, "execution", "external_effect", 3), baseVersion, KindIllegalLifecycleTransition, baseCheckpoints)
	assertRejectedCheckpoint(t, s, workID, checkpoint("checkpoint-non-executor", ownerRef, ownerRef, "execution", "external_effect", 2), baseVersion, KindUnauthorized, baseCheckpoints)
	valid := checkpoint("checkpoint-valid", executor, executor, "execution", "external_effect", 2)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{valid}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baseVersion}}); err != nil {
		t.Fatalf("valid executor checkpoint rejected: %v", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != baseVersion+1 || countWorkflowCheckpoints(t, s, workID) != baseCheckpoints+1 {
		t.Fatal("valid checkpoint changed the step or failed to persist exactly one checkpoint/version")
	}
}

func assertRejectedCheckpoint(t *testing.T, s *Store, workID string, event Event, expectedVersion int64, wantKind FailureKind, wantCheckpoints int) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(t.Context(), tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expectedVersion}}, newWriteScope(tx))
	_ = tx.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != wantKind {
		t.Fatalf("checkpoint failure=%v, want %s", err, wantKind)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != expectedVersion || countWorkflowCheckpoints(t, s, workID) != wantCheckpoints {
		t.Fatal("refused checkpoint changed step, version or checkpoint count")
	}
}

func countWorkflowCheckpoints(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_checkpoints WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionCompletedV2BindsPayloadActorToEventActor(t *testing.T) {
	t.Parallel()
	workID := "authority-completed-actor"
	s, executor, owner := seedOrdinaryExecutorAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	before := countWorkflowActionCompleted(t, s, workID)
	completed := workflowEventWithActor("forged-completed-actor", WorkflowActionCompleted, workID, executor, map[string]any{
		"work_id": workID, "expected_version": 12, "resulting_version": 13,
		"step_id": "execution", "action_id": "checkpoint_execution", "attempt_epoch": 1,
		"result_evidence_refs": []string{}, "changed_refs": []string{}, "actor_ref": ownerRef,
	})
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyWorkflowOperationTx(t.Context(), tx, Operation{Events: []Event{completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}, newWriteScope(tx))
	_ = tx.Rollback()
	if !hasFailureKind(err, KindUnauthorized) {
		t.Fatalf("forged completed actor = %v, want unauthorized", err)
	}
	if currentStep(t, s, workID) != "execution" || readWorkVersion(t, s, workID) != 12 || countWorkflowActionCompleted(t, s, workID) != before {
		t.Fatal("forged completion changed step, version or completion count")
	}
}

func TestWorkflowActionFailedV2BindsCurrentExecutorAttempt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, step                       string
		epoch                            int64
		forged, nonexecutor, recoverable bool
		kind                             FailureKind
		state                            string
	}{
		{name: "forged actor", step: "execution", epoch: 2, forged: true, recoverable: true, kind: KindUnauthorized},
		{name: "wrong step", step: "acceptance", epoch: 2, recoverable: true, kind: KindIllegalLifecycleTransition},
		{name: "stale epoch", step: "execution", epoch: 3, recoverable: true, kind: KindIllegalLifecycleTransition},
		{name: "non-executor actor", step: "execution", epoch: 2, nonexecutor: true, recoverable: true, kind: KindUnauthorized},
		{name: "recoverable failure keeps workflow running", step: "execution", epoch: 2, recoverable: true, state: "running"},
		{name: "nonrecoverable failure blocks workflow", step: "execution", epoch: 2, state: "blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workID := "authority-failed-" + strings.ReplaceAll(test.name, " ", "-")
			s, executor, owner := seedOrdinaryExecutorAtExecution(t, workID)
			ownerRef, err := WorkflowActorRef(owner)
			if err != nil {
				t.Fatal(err)
			}
			eventActor, payloadActor := executor, executor
			if test.forged {
				payloadActor = ownerRef
			}
			if test.nonexecutor {
				eventActor, payloadActor = ownerRef, ownerRef
			}
			event := actionFailedFixture("failed-"+workID, workID, eventActor, payloadActor, test.step, test.epoch, test.recoverable)
			if test.kind != "" {
				assertRejectedActionFailed(t, s, event, test.kind)
				return
			}
			if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}); err != nil {
				t.Fatalf("current executor failure rejected: %v", err)
			}
			if instanceState(t, s, workID) != test.state || readWorkVersion(t, s, workID) != 13 {
				t.Fatalf("failure did not persist expected state %s/version 13", test.state)
			}
		})
	}
}

// A workflow action failing with stale_law_revision must be recordable. Drive
// the real fold rather than the closure predicate, so the assertion binds to the
// layer that owns it.
func TestWorkflowActionFailedAcceptsLawFailureKinds(t *testing.T) {
	t.Parallel()
	for _, kind := range []FailureKind{KindStaleLawRevision} {
		t.Run(string(kind), func(t *testing.T) {
			workID := "authority-failed-" + string(kind)
			s, executor, _ := seedOrdinaryExecutorAtExecution(t, workID)
			event := actionFailedFixtureWithKind("failed-"+string(kind), workID, executor, executor, "execution", 2, false, kind)
			if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}); err != nil {
				t.Fatalf("current executor %s failure rejected: %v", kind, err)
			}
		})
	}
}

func actionFailedFixture(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool) Event {
	return actionFailedFixtureWithKind(eventID, workID, eventActor, payloadActor, stepID, epoch, recoverable, KindTimeout)
}

func actionFailedFixtureWithKind(eventID, workID, eventActor, payloadActor, stepID string, epoch int64, recoverable bool, kind FailureKind) Event {
	return workflowEventWithActor(eventID, WorkflowActionFailed, workID, eventActor, map[string]any{
		"work_id": workID, "expected_version": 12, "resulting_version": 13, "step_id": stepID, "attempt_epoch": epoch,
		"failure_kind": string(kind), "recoverable": recoverable, "actor_ref": payloadActor,
	})
}

func assertRejectedActionFailed(t *testing.T, s *Store, event Event, wantKind FailureKind) {
	t.Helper()
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): 12}}); !hasFailureKind(err, wantKind) {
		t.Fatalf("action_failed = %v, want %s", err, wantKind)
	}
	if instanceState(t, s, event.SubjectID) != "running" || readWorkVersion(t, s, event.SubjectID) != 12 {
		t.Fatal("refused failure changed state or version")
	}
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, event.SubjectID, WorkflowActionFailed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("refused failure persisted %d events", events)
	}
}

func countWorkflowActionCompleted(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func applyStartForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, operationID string) WorkflowActionExecutionResult {
	t.Helper()
	return applyWorkflowActionForTest(t, s, workID, owner, expectedVersion, "start_execution", "s", operationID, time.Unix(4, 0).UTC())
}

func applyWorkflowActionForTest(t *testing.T, s *Store, workID string, owner WorkflowActor, expectedVersion int64, actionID, digestChar, operationID string, now time.Time) WorkflowActionExecutionResult {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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

func seedOrdinaryExecutorAtExecution(t *testing.T, workID string) (*Store, string, WorkflowActor) {
	t.Helper()
	return seedOrdinaryExecutorAtExecutionWithCheckpoint(t, workID, true)
}

func seedOrdinaryExecutorAtExecutionWithCheckpoint(t *testing.T, workID string, checkpoint bool) (*Store, string, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	executor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/executor", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	executorRef, err := WorkflowActorRef(executor)
	if err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition := workflowFixtureDefinition(t, 1)
	setup := []Event{
		workflowEvent("executor-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": executorRef, "principal_ref": executor.PrincipalRef, "client_ref": executor.ClientRef, "agent_ref": executor.AgentRef, "session_ref": executor.SessionRef, "actor_class": "agent"}),
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "ref": definition.Definition.Ref, "version": 1, "digest": definition.Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, executorRef, 5, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, executorRef, 6, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, executorRef, 7, "design", "record_design"),
		workflowActionCompletedFixture("planning-"+workID, workID, executorRef, 8, "planning", "approve_contract"),
	}
	for epoch := int64(1); epoch <= 2; epoch++ {
		version := 8 + epoch
		id := "start-" + workID + "-" + itoa(int(epoch))
		setup = append(setup, workflowEventWithActor(id, WorkflowActionStarted, workID, executorRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "execution", "action_id": "start_execution", "attempt_epoch": epoch,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": id,
			"actor_ref": executorRef, "execution_model": "model:fixture",
		}))
	}
	if checkpoint {
		setup = append(setup, workflowEventWithActor("seed-checkpoint-"+workID, WorkflowActionCheckpointed, workID, executorRef, map[string]any{
			"work_id": workID, "expected_version": 11, "resulting_version": 12, "step_id": "execution", "step_kind": "external_effect", "attempt_epoch": 2,
			"checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": "seed"}, "resume_cursor": "cursor:seed", "actor_ref": executorRef,
			"request_id": "request:seed-" + workID, "checkpoint_id": "checkpoint:seed-" + workID,
			"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "seed-" + workID,
		}))
	}
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	return s, executorRef, owner
}

func workflowActorForRef(t *testing.T, s *Store, actorRef string) WorkflowActor {
	t.Helper()
	var actor WorkflowActor
	if err := s.db.QueryRow(`SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actor.ActorRef, &actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass); err != nil {
		t.Fatal(err)
	}
	return actor
}

func readWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func currentStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

func instanceState(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var state string
	if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
