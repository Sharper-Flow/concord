package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The completion record must agree with the event log it summarizes (#856).
// The fixture binds three evidence events, records one ok verdict, and
// confirms the premise; the completed payload must carry those facts, not
// the literal zero, the absent-fields default, and the constant "ok".
func TestCompletionRecordCarriesTheValuesTheGateComputed(t *testing.T) {
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
	definition := BuiltinWorkflowDefinitions()[0]
	actorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/record-integrity")
	event, err := workflowCompletionEvent(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: "record-integrity", ExpectedVersion: version, OperationID: "record-integrity-complete", Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}, definition, "release", actorRef, json.RawMessage(`{"impact_verdict":"non-breaking"}`))
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
	if got, _ := payload["evidence_count"].(float64); got != 3 {
		t.Errorf("evidence_count = %v, want 3 (the log binds verification, review, and artifact evidence)", got)
	}
	if got, _ := payload["final_verdict_kind"].(string); got != "ok" {
		t.Errorf("final_verdict_kind = %q, want the recorded verdict kind %q", got, "ok")
	}
}

// A lane dispatch rotates the executing-actor lease, not authorship (#801).
// The owner who started the delivery step stays the delivery author of
// record, so the evaluator-distinctness check must refuse that owner even
// while the lane holds the executing lease.
func TestLaneDispatchRotatesTheLeaseNotTheAuthorship(t *testing.T) {
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
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("rot-owner-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("rot-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("rot-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("rot-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("rot-design", workID, ownerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("rot-planning", workID, ownerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("rot-start", WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "rot:start", "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
		workflowEvent("rot-lane-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 9, "resulting_version": 10, "actor_ref": laneRef, "principal_ref": laneActor.PrincipalRef, "client_ref": laneActor.ClientRef, "agent_ref": laneActor.AgentRef, "session_ref": laneActor.SessionRef, "actor_class": "agent"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{EventID: "rot-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{
		"attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
	})}
	completed := Event{EventID: "rot-worker-completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion})}
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
	recordOutsider := workflowEvent("rot-outsider-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 10, "resulting_version": 11, "actor_ref": outside, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/outsider", "session_ref": "session/record-authorship-outsider", "actor_class": "agent"})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{recordOutsider}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 10}}); err != nil {
		t.Fatal(err)
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The owner authored the delivery: evaluating it is self-evaluation and
	// must refuse, lane lease or no lane lease.
	if err := workflowCompletionActorDistinct(ctx, tx, workID, ownerRef, "", false); err == nil {
		t.Error("the delivery author cleared the evaluator-distinctness check while the lane held the executing lease")
	} else if !strings.Contains(err.Error(), "evaluate") && !strings.Contains(err.Error(), "author") && !strings.Contains(err.Error(), "distinct") {
		t.Errorf("owner verdict refusal came from an unexpected gate: %v", err)
	}
	// The dispatched lane executed on the step, so it cannot sign the verdict
	// either: independence holds against every step executor.
	if err := workflowCompletionActorDistinct(ctx, tx, workID, laneRef, "", false); err == nil {
		t.Error("a lane that executed on the delivery step cleared the evaluator-distinctness check")
	}
	// An agent that executed nothing on this work item remains an acceptable
	// evaluator.
	if err := workflowCompletionActorDistinct(ctx, tx, workID, outside, "", false); err != nil {
		t.Errorf("an agent that executed nothing must remain an acceptable evaluator: %v", err)
	}
}

// fields.evidence_ref is a declared key; bind_evidence must honor it (#856
// follow-on). Binding through it is what lets a verdict cite the commit it
// evaluated instead of an opaque operation id.
func TestBindEvidenceHonorsTheDeclaredEvidenceRefField(t *testing.T) {
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
		workflowEvent("eref-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
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
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 9, ActionID: "bind_evidence", Payload: mustJSONValue(map[string]any{"evidence_ref": "commit:declared", "evidence_kind": "verification"}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "eref:bind", OperationID: "eref:bind",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "eref:bind", RequestID: "request:eref:bind", ContractDigest: testManifestDigest, Now: time.Unix(2, 0).UTC(),
	}); err != nil {
		_ = leaveFold(ctx, tx)
		t.Fatalf("bind_evidence with a declared evidence_ref was refused: %v", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	var subject string
	if err := tx.QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowEvidenceBound).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if subject != "commit:declared" {
		t.Errorf("immutable_subject_ref = %q, want the declared evidence_ref %q", subject, "commit:declared")
	}
}
