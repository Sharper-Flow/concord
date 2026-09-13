package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFailedWorkerRecoveryIsAvailableForAnOlderPinnedDefinition(t *testing.T) {
	const workID = "worker-failure-recovery-old-definition"
	s, owner, attemptID, entry := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	failWorkerAttempt(t, s, workID, attemptID)

	_, action, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "record_worker_failure")
	if err != nil {
		t.Fatalf("resolve recovery action: %v", err)
	}
	if action.ExecutionMode != ActionHold || action.RequiredCapability != "work_transition" {
		t.Fatalf("recovery action = %#v, want hold and work_transition", action)
	}

	payload := mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1})
	if err := testWorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: 9, ActionID: "record_worker_failure", Payload: payload, Actor: owner,
	}); err != nil {
		t.Fatalf("recovery preflight: %v", err)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("read work pin before recovery: %v", err)
	}
	if !containsWorkPinAction(pin.NextValidIntents, "record_worker_failure") {
		t.Fatalf("work pin does not advertise record_worker_failure: %#v", pin.NextValidIntents)
	}

	result := applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, 1, 9, "old-definition-failure-recovery")
	if result.ResultingVersion != 10 {
		t.Fatalf("recovery result version=%d, want 10", result.ResultingVersion)
	}
	if err := testWorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: 10, ActionID: "record_delivery", Payload: mustJSONValue(map[string]any{}), Actor: owner,
	}); err != nil {
		t.Fatalf("record delivery after failure recovery: %v", err)
	}
	resolvedEntry, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "record_worker_failure")
	if err == nil || resolvedEntry.Definition.Ref != "" {
		t.Fatalf("recorded failure remained resolvable: definition=%#v error=%v", resolvedEntry.Definition, err)
	}
	pin, err = ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatalf("read work pin after recovery: %v", err)
	}
	if containsWorkPinAction(pin.NextValidIntents, "record_worker_failure") {
		t.Fatal("work pin advertised a duplicate failure recovery")
	}

	registered, ok := BuiltinWorkflowRegistry().Lookup(entry.Definition.Ref, entry.Definition.Version)
	if !ok || registered.Digest != entry.Digest {
		t.Fatalf("pinned definition changed: before=%s after=%s", entry.Digest, registered.Digest)
	}
}

func TestFailedWorkerRecoveryRefusesWithoutTheCurrentFailedAttempt(t *testing.T) {
	const workID = "worker-failure-recovery-no-failure"
	s, owner, attemptID, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()

	if _, _, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "record_worker_failure"); err == nil {
		t.Fatal("recovery action resolved without a failed attempt")
	}
	payload := mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1})
	err := testWorkflowActionPreflight(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: 9, ActionID: "record_worker_failure", Payload: payload, Actor: owner,
	})
	if err == nil {
		t.Fatal("recovery preflight admitted an active attempt")
	}
}

func containsWorkPinAction(intents []WorkPinIntent, actionID string) bool {
	for _, intent := range intents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

func seedOldDefinitionWorker(t *testing.T, workID string) (*Store, WorkflowActor, string, RegisteredDefinition) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 4)
	if !ok {
		t.Fatal("workflow.break_fix v4 is not registered")
	}
	lane := BuiltinLaneDefinitions()[0]
	events := []Event{
		workflowEvent("old-worker-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef, "agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent"}),
		workflowEvent("old-owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("old-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "ref": entry.Definition.Ref, "version": entry.Definition.Version, "digest": entry.Digest, "work_kind": string(entry.Definition.WorkKind)}),
		workflowActionCompletedFixture("old-reproduction-"+workID, workID, workerRef, 5, "reproduce", "record_reproduction"),
		workflowActionCompletedFixture("old-diagnosis-"+workID, workID, workerRef, 6, "diagnose", "record_root_cause"),
		workflowActionCompletedFixture("old-planning-"+workID, workID, workerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("old-start-"+workID, WorkflowActionStarted, workID, workerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "repair", "action_id": "start_repair", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "old-start:" + workID, "actor_ref": workerRef, "execution_model": preferredModelForLane(lane)}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt:" + workID
	dispatch := Event{EventID: "old-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch}}); err != nil {
		t.Fatal(err)
	}
	return s, owner, attemptID, entry
}
