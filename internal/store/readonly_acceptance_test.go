package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// CD-0117 D2 composes the dispatch/accept pair onto the step kinds the
// lane-step join admits, including read-only internal_sqlite and
// cross_authority steps. The acceptance fold must follow the pinned
// definition's declared actions (#961): a completed read-only lane result is
// acceptable at a joined read step, while every step the definition does not
// equip with the pair still refuses.

func staticAnalysisDefinition(t *testing.T) RegisteredDefinition {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.static_analysis", 3)
	if !ok {
		t.Fatal("workflow.static_analysis v3 is not registered")
	}
	return entry
}

// seedStaticAnalysisAtReport drives a static-analysis work item to its
// internal_sqlite `report` step with a completed read-only lane attempt
// recorded there, mirroring the production dispatch flow the join admits.
func seedStaticAnalysisAtReport(t *testing.T, workID string) (*Store, WorkflowActor, WorkflowActor, string, int64) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registered := staticAnalysisDefinition(t)
	lane := BuiltinLaneDefinitions()[0]
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	laneActor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord", AgentRef: "agent/lane:" + lane.ID, SessionRef: "session/attempt-" + workID, ActorClass: ActorAgent}
	laneRef, err := WorkflowActorRef(laneActor)
	if err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEvent("ro-owner-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("ro-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.static_analysis", "version": registered.Definition.Version, "digest": registered.Digest, "work_kind": "static_analysis"}),
		workflowActionCompletedFixture("ro-scope", workID, ownerRef, 4, "scope", "approve_contract"),
		workflowEventWithActor("ro-analyze-start", WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 5, "resulting_version": 6, "step_id": "analyze", "action_id": "run_analysis", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "ro:analyze-start", "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
		workflowActionCompletedFixture("ro-analyze-delivery", workID, ownerRef, 6, "analyze", "record_delivery"),
		workflowEventWithActor("ro-report-start", WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 7, "resulting_version": 8, "step_id": "report", "action_id": "dispatch_worker", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("c", 64), "idempotency_identity": "ro:report-dispatch", "actor_ref": ownerRef, "execution_model": preferredModelForLane(lane)}),
		workflowEvent("ro-lane-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "actor_ref": laneRef, "principal_ref": laneActor.PrincipalRef, "client_ref": laneActor.ClientRef, "agent_ref": laneActor.AgentRef, "session_ref": laneActor.SessionRef, "actor_class": "agent"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	if got := currentStep(t, s, workID); got != "report" {
		t.Fatalf("fixture step = %q, want report", got)
	}
	attemptID := "attempt:ro-" + workID
	dispatch := Event{EventID: "ro-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 4, Payload: mustJSONValue(map[string]any{
		"attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"capability_class": lane.CapabilityClass, "packet_schema_version": WorkerPacketSchemaVersion, "report_schema_version": WorkerReportSchemaVersion,
		"packet_digest": "sha256:" + strings.Repeat("b", 64), "readback_model": preferredModelForLane(lane), "lane_actor_ref": laneRef,
	})}
	completed := Event{EventID: "ro-worker-completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:host", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch, completed}}); err != nil {
		t.Fatalf("recording the read-only lane attempt: %v", err)
	}
	version := readWorkVersion(t, s, workID)
	return s, owner, laneActor, attemptID, version
}

func TestAcceptWorkerResultSucceedsAtAJoinedReadOnlyStep(t *testing.T) {
	ctx := context.Background()
	workID := "readonly-accept-report"
	s, owner, _, attemptID, version := seedStaticAnalysisAtReport(t, workID)

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "accept_worker_result", Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "ro:accept", OperationID: "ro:accept",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "ro:accept", RequestID: "request:ro:accept", ContractDigest: testManifestDigest, Now: time.Unix(4, 0).UTC(),
	})
	if err != nil {
		_ = leaveFold(ctx, tx)
		_ = tx.Rollback()
		t.Fatalf("accepting the read-only lane result at the report step was refused: %v", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := currentStep(t, s, workID); got != "review" {
		t.Fatalf("accepted read-only result advanced to %q, want review", got)
	}
}

func TestAcceptWorkerResultRefusesAtStepsWithoutTheDeclaredPair(t *testing.T) {
	staticAnalysis := staticAnalysisDefinition(t).Definition
	var failure *Failure
	err := validateAcceptedWorkerResult(context.Background(), nil, Event{SubjectID: "ro-guard"}, workflowActionCompletedPayload{WorkerAttemptID: "attempt:ro-guard"}, staticAnalysis, "scope")
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("undeclared-step acceptance failure = %v, want %s", err, KindIllegalLifecycleTransition)
	}
	if !strings.Contains(failure.Detail, "not declared at the current workflow step") {
		t.Fatalf("failure detail = %q, want the declared-step refusal", failure.Detail)
	}
	// A definition whose steps carry no worker pair at all must keep
	// refusing: the fixture family v1 predates the join.
	legacy := workflowFixtureDefinition(t, 1).Definition
	if err := validateAcceptedWorkerResult(context.Background(), nil, Event{SubjectID: "ro-guard-legacy"}, workflowActionCompletedPayload{WorkerAttemptID: "attempt:ro-guard-legacy"}, legacy, "execution"); !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("pre-join definition acceptance failure = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

func TestAcceptWorkerResultAtReadOnlyStepPreservesIdentityRefusals(t *testing.T) {
	ctx := context.Background()
	workID := "readonly-accept-refusals"
	s, owner, laneActor, attemptID, version := seedStaticAnalysisAtReport(t, workID)

	cases := []struct {
		name    string
		payload map[string]any
		actor   WorkflowActor
		wantMsg string
	}{
		{"stale epoch", map[string]any{"attempt_id": attemptID, "attempt_epoch": 3}, owner, "attempt epoch does not match"},
		{"missing attempt", map[string]any{"attempt_id": "attempt:absent", "attempt_epoch": 1}, owner, "does not exist"},
		{"lane accepts its own result", map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}, laneActor, "actor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := enterFold(ctx, tx); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			_, err = applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
				WorkID: workID, ExpectedVersion: version, ActionID: "accept_worker_result", Payload: mustJSONValue(tc.payload), Actor: tc.actor,
				AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "ro:refuse:" + tc.name, OperationID: "ro:refuse:" + tc.name,
				PrincipalRef: tc.actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "ro:refuse:" + tc.name, RequestID: "request:ro:refuse:" + tc.name, ContractDigest: testManifestDigest, Now: time.Unix(5, 0).UTC(),
			})
			_ = leaveFold(ctx, tx)
			_ = tx.Rollback()
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("acceptance refusal = %v, want typed failure", err)
			}
			if !strings.Contains(failure.Detail, tc.wantMsg) {
				t.Fatalf("refusal detail = %q, want %q", failure.Detail, tc.wantMsg)
			}
			if got := currentStep(t, s, workID); got != "report" {
				t.Fatalf("refused acceptance advanced step to %q", got)
			}
		})
	}
}
