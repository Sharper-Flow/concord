package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The completion record must agree with the event log it summarizes (#856).
// The fixture binds evidence, associates current criterion proof, and
// confirms the premise; the completed payload must carry those facts, not
// the literal zero, the absent-fields default, and the constant "ok".
func TestCompletionRecordCarriesTheValuesTheGateComputed(t *testing.T) {
	t.Parallel()
	s, _ := seedCompletionGateCase(t, "record-integrity", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='record-integrity'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	actorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/owner", "session/record-integrity")
	event, err := workflowCompletionEvent(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: "record-integrity", ExpectedVersion: version, OperationID: "record-integrity-complete", Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}, actorRef, json.RawMessage(`{"impact_verdict":"non-breaking"}`))
	if err != nil {
		t.Fatalf("completion event was refused: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got, _ := payload["premise_confirmed"].(bool); !got {
		t.Error("premise_confirmed is false while the log holds an operator premise confirmation for this contract")
	}
	if got, _ := payload["evidence_count"].(float64); got != 5 {
		t.Errorf("evidence_count = %v, want five bindings (three required kinds, captured receipt, and spec)", got)
	}
	if got, _ := payload["final_verdict_kind"].(string); got != "ok" {
		t.Errorf("final_verdict_kind = %q, want the recorded verdict kind %q", got, "ok")
	}
}

// A lane dispatch rotates the executing-actor lease, not Main's authority to
// associate captured proof. The worker cannot perform Main's acceptance work.
func TestLaneDispatchRotatesTheLeaseNotTheAuthorship(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "record-authorship-rotation"
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
	setup := workflowExecutionSetupEvents(t, "rot", workID, ownerRef, owner, laneRef, laneActor, lane)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "execution", ownerRef)
	dispatch := Event{EventID: "rot-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(map[string]any{
		"attempt_id": attemptID, "lane_id": lane.ID, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
	})}
	completed := workerCompleteEventForLane(workID, "rot-worker-completed", attemptID, lane, time.Unix(3, 0).UTC(), workerFixtureDirectory(t, s, workID))
	completed.Actor = "worker:host"
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch, completed}}); err != nil {
		t.Fatalf("recording the dispatched lane attempt: %v", err)
	}

	// The lease rotates onto the lane; that behavior stays.
	var executing string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&executing); err != nil {
		t.Fatal(err)
	}
	if executing != laneRef {
		t.Fatalf("executing lease = %q, want the lane %q", executing, laneRef)
	}

	// An agent that executed nothing on this work item is recorded as a
	// workflow actor before the read transaction opens, so the single
	// connection is free for the store-level operation.
	outside := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/outsider", "session/record-authorship-outsider")
	recordOutsider := workflowEvent("rot-outsider-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 12, "resulting_version": 13, "actor_ref": outside, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/outsider", "session_ref": "session/record-authorship-outsider", "actor_class": "agent"})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{recordOutsider}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 12}}); err != nil {
		t.Fatal(err)
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Main may associate proof even after authoring a delivery; the proof's
	// captured producer, not Main's verdict word, establishes the result.
	if err := workflowCompletionActorDistinct(ctx, tx, workID, ownerRef); err != nil {
		t.Errorf("Main lost proof-association authority when the lease rotated: %v", err)
	}
	// The dispatched lane remains a worker, not Main.
	if err := workflowCompletionActorDistinct(ctx, tx, workID, laneRef); err == nil {
		t.Error("a lane that executed on the delivery step cleared the evaluator-distinctness check")
	}
	// An agent that executed nothing on this work item remains an acceptable
	// evaluator.
	if err := workflowCompletionActorDistinct(ctx, tx, workID, outside); err != nil {
		t.Errorf("an agent that executed nothing must remain an acceptable evaluator: %v", err)
	}
}

// fields.evidence_ref is a declared key; bind_evidence must honor it (#856
// follow-on). Binding through it is what lets a verdict cite the commit it
// evaluated instead of an opaque operation id.
func TestBindEvidenceHonorsTheDeclaredEvidenceRefField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "record-evidence-ref"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("eref-owner-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("eref-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("eref-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("eref-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("eref-design", workID, ownerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("eref-planning", workID, ownerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("eref-start", WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "eref:start", "actor_ref": ownerRef}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 9, ActionID: "bind_evidence", Payload: mustJSONValue(map[string]any{"evidence_ref": "commit:declared", "evidence_kind": "verification"}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "eref:bind", OperationID: "eref:bind",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "eref:bind", RequestID: "request:eref:bind", ContractDigest: testManifestDigest, Now: time.Unix(2, 0).UTC(),
	}); err != nil {
		t.Fatalf("bind_evidence with a declared evidence_ref was refused: %v", err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	var subject string
	if err := tx.QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings WHERE work_id=? ORDER BY binding_seq DESC LIMIT 1`, workID).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if subject != "commit:declared" {
		t.Errorf("immutable_subject_ref = %q, want the declared evidence_ref %q", subject, "commit:declared")
	}
}
