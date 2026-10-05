package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedAuthorizedFailedWorker(t *testing.T, workID string) (*Store, WorkflowActor, string) {
	t.Helper()
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	return seedAuthorizedFailedWorkerWithDefinition(t, workID, entry)
}

func seedAuthorizedFailedWorkerWithDefinition(t *testing.T, workID string, entry RegisteredDefinition) (*Store, WorkflowActor, string) {
	t.Helper()
	s := openTemp(t)
	t.Cleanup(func() { _ = s.Close() })
	seed := seedDispatchFixture(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET definition_ref=?,definition_version=?,definition_digest=? WHERE work_id=?; DELETE FROM fold_guard`, entry.Definition.Ref, entry.Definition.Version, entry.Digest, workID); err != nil {
		t.Fatalf("pin test definition: %v", err)
	}
	attemptID := "attempt:" + workID
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker",
		Payload:         mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": dispatchWorkerPacket(t, s, workID, "execution", attemptID)}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: seed.ownerActor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		IdempotencyIdentity:  "authorize:" + workID, OperationID: "authorize:" + workID,
		PrincipalRef: seed.ownerActor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: "authorize:" + workID, RequestID: "request:" + workID,
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if err != nil {
		t.Fatalf("authorize worker: %v", err)
	}
	failure := Event{EventID: "abandon:" + workID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, FailureKind: WorkerFailureAbandoned, Detail: "authorized worker never supplied admissible dispatch evidence"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failure}}); err != nil {
		t.Fatalf("abandon authorized worker: %v", err)
	}
	// Failure disposition keeps the existing distinct-coordinator authority.
	owner := seed.ownerActor
	owner.AgentRef = "agent/recovery-owner"
	owner.SessionRef = "session/recovery-" + workID
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	record := workflowEvent("recovery-owner:"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{record}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return s, owner, attemptID
}

func TestAuthorizedWorkerFailureRecoveryPreservesOlderDefinition(t *testing.T) {
	const workID = "authorized-failure-older-definition"
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("older implementation definition is not registered")
	}
	s, owner, attemptID := seedAuthorizedFailedWorkerWithDefinition(t, workID, entry)
	applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, 1, readWorkVersion(t, s, workID), "record-older-authorized-failure")
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != entry.Digest {
		t.Fatal("failure recovery changed the pinned definition")
	}
}

func TestAuthorizedWorkerFailureRecoveryNeedsNoDispatchEvidence(t *testing.T) {
	const workID = "authorized-failure-recovery"
	s, owner, attemptID := seedAuthorizedFailedWorker(t, workID)
	version := readWorkVersion(t, s, workID)
	applyRecordWorkerFailureForTest(t, s, workID, owner, attemptID, 1, version, "record-authorized-failure")
	var dispatches, dispositions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkerDispatched).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure'`, workID, WorkflowActionCompleted).Scan(&dispositions); err != nil {
		t.Fatal(err)
	}
	if dispatches != 0 || dispositions != 1 {
		t.Fatalf("dispatches=%d dispositions=%d, want 0 and 1", dispatches, dispositions)
	}
	var lifecycle, readback string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&lifecycle, &readback); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "failed" || readback != "" {
		t.Fatalf("failure disposition changed terminal lifecycle=%q readback=%q", lifecycle, readback)
	}
}

func TestAuthorizedFailedWorkerRetryBindingPrecedesDisposition(t *testing.T) {
	const workID = "authorized-failure-retry-binding"
	s, _, attemptID := seedAuthorizedFailedWorker(t, workID)
	version := readWorkVersion(t, s, workID)
	binding, err := WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID)
	if err != nil || binding == nil || binding.FailedAttemptID != attemptID || binding.FailedAttemptEpoch != 1 {
		t.Fatalf("unrecorded failure binding=%#v error=%v, want the exact failed authorization", binding, err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("reading the retry binding changed work state")
	}
	var dispositions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure'`, workID, WorkflowActionCompleted).Scan(&dispositions); err != nil {
		t.Fatal(err)
	}
	if dispositions != 0 {
		t.Fatal("retry binding fabricated a failure disposition")
	}
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	state, _, err := loadWorkflowAdmissionStateTx(context.Background(), tx, workID, entry.Definition, "execution", "authorized_failure_test")
	if err != nil {
		t.Fatal(err)
	}
	decision := workflowAdmit(entry.Definition, state, "dispatch_worker")
	if !decision.ApprovalRequired || state.FailedWorkerRetry == nil || state.FailedWorkerRetry.FailedAttemptID != attemptID {
		t.Fatalf("the shared admission owner lost the failed authorization: decision=%#v binding=%#v", decision, state.FailedWorkerRetry)
	}
}

func TestAuthorizedWorkerFailureRecoveryRejectsWrongEpoch(t *testing.T) {
	const workID = "authorized-failure-wrong-epoch"
	s, owner, attemptID := seedAuthorizedFailedWorker(t, workID)
	version := readWorkVersion(t, s, workID)
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "record_worker_failure",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64),
		IdempotencyIdentity:  "wrong-epoch", OperationID: "wrong-epoch", PrincipalRef: owner.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "wrong-epoch", RequestID: "wrong-epoch",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(err.Error(), "epoch") {
		t.Fatalf("wrong epoch error=%v, want illegal_lifecycle_transition", err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("wrong-epoch refusal changed work state")
	}
}

func TestAuthorizedCheckpointFailureRecoveryNeedsNoDispatchEvidence(t *testing.T) {
	const workID = "authorized-checkpoint-failure"
	const attemptID = "attempt:authorized-checkpoint-failure"
	fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification"}, []string{"verification", "artifact"})
	s := fixture.store
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "verify", attemptID, "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("authorize checkpoint review: %v", err)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	failure := Event{EventID: "abandon-checkpoint-review", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, FailureKind: WorkerFailureAbandoned, Detail: "checkpoint review supplied no admissible dispatch evidence"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failure}}); err != nil {
		t.Fatal(err)
	}
	recorder := checkpointReviewReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}), 0, recorder); err != nil {
		t.Fatalf("checkpoint failure disposition: %v", err)
	}
	var dispatches int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerDispatched, attemptID).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if dispatches != 0 || currentStep(t, s, workID) != "verify" {
		t.Fatalf("checkpoint disposition fabricated dispatch evidence or advanced the step: dispatches=%d step=%s", dispatches, currentStep(t, s, workID))
	}
}

func TestAuthorizedFailedRetryDoesNotMaskRegistryStaleness(t *testing.T) {
	for _, stage := range []string{"unrecorded", "recorded"} {
		t.Run(stage, func(t *testing.T) {
			testAuthorizedFailedRetryRegistryStaleness(t, stage == "recorded")
		})
	}
}

func testAuthorizedFailedRetryRegistryStaleness(t *testing.T, recordDisposition bool) {
	t.Helper()
	const workID = "authorized-failed-retry-stale-registry"
	const attemptID = "attempt:authorized-failed-retry-stale-registry"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	laneVersion, laneDigest := registeredLaneIdentity(t, "implement")
	packet := joinPacketFor(t, s, workID, "repair", attemptID, "implement", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatal(err)
	}
	failure := Event{EventID: "abandon-stale-registry-retry", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, FailureKind: WorkerFailureAbandoned, Detail: "no admissible dispatch evidence"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failure}}); err != nil {
		t.Fatal(err)
	}
	if recordDisposition {
		var epoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		recorder := checkpointReviewReviewer(t, s, workID)
		if err := runVerdictActionAs(t, s, workID, "record_worker_failure", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}), 0, recorder); err != nil {
			t.Fatal(err)
		}
	}
	driftStaleRegistryFixture(t, s)
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if containsWorkPinAction(pin.NextValidIntents, "dispatch_worker") {
		t.Fatal("the work pin advertises a retry refused for registry staleness")
	}
	freshID := attemptID + ":fresh"
	freshPacket := joinPacketFor(t, s, workID, "repair", freshID, "implement", laneVersion, laneDigest)
	err = InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Actor: fixture.owner,
		Payload:         mustJSONValue(map[string]any{"attempt_id": freshID, "worker_packet": freshPacket}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
	})
	if !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("retry preflight error=%v, want the registry staleness refusal", err)
	}
	binding, err := WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID)
	if err != nil || binding != nil {
		t.Fatalf("a refused retry must not request a wasted approval: binding=%#v error=%v", binding, err)
	}
}

func TestAuthorizedFailedRetryApprovalNeverOverridesAnotherRefusal(t *testing.T) {
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	offStep := ""
	for _, candidate := range entry.Definition.StepGraph.Steps {
		if !definitionStepAllows(entry.Definition, candidate.ID, "dispatch_worker") {
			offStep = candidate.ID
			break
		}
	}
	if offStep == "" {
		t.Fatal("test requires a step that does not declare dispatch_worker")
	}
	for _, step := range []string{"execution", offStep} {
		t.Run(step, func(t *testing.T) {
			state := WorkflowAdmissionState{Step: step, Lifecycle: "in_progress", InstanceState: "running", ActiveContracts: 1,
				FailedWorkerRetry: &WorkflowRetryApprovalBinding{FailedAttemptID: "attempt:failed", FailedAttemptEpoch: 1}}
			if step == "execution" {
				state.LawPinStaleError = newFailure(KindStaleLawRevision, "test", "law revision is stale", false, "reread_entities")
			}
			decision := workflowAdmit(entry.Definition, state, "dispatch_worker")
			if decision.Failure == nil || decision.Admitted || decision.ApprovalRequired {
				t.Fatalf("retry approval masked another refusal: %#v", decision)
			}
		})
	}
}

func TestAuthorizedAttemptCannotBeAcceptedWithoutDispatchEvidence(t *testing.T) {
	const workID = "authorized-failure-no-success-evidence"
	s, owner, attemptID := seedAuthorizedFailedWorker(t, workID)
	// Corroborate against the event log even if a projection claims success.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_attempts SET lifecycle_state='completed',readback_model='model/synthetic',completed_at='2026-01-01T00:00:00Z',failed_at=NULL,failure_kind='',failure_detail='' WHERE attempt_id=?; DELETE FROM fold_guard`, attemptID); err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM domain_events`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	err = validateWorkerAttemptAction(context.Background(), tx,
		Event{SubjectID: workID, Actor: ownerRef, Seq: seq},
		workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: attemptID, AttemptEpoch: 1, ActorRef: ownerRef},
		entry.Definition, "execution", "completed")
	if !hasFailureKind(err, KindProjectionNotFound) {
		t.Fatalf("acceptance without dispatch evidence error=%v, want missing dispatch evidence", err)
	}
}
