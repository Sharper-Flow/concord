package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedAuthorizedFailedWorker(t *testing.T, workID string) (*Store, WorkflowActor, string) {
	t.Helper()
	return seedAuthorizedFailedWorkerWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"))
}

func seedAuthorizedFailedWorkerWithDefinition(t *testing.T, workID string, entry RegisteredDefinition) (*Store, WorkflowActor, string) {
	t.Helper()
	s := openTemp(t)
	t.Cleanup(func() { _ = s.Close() })
	seed := seedDispatchFixture(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET definition_ref=?,definition_digest=? WHERE work_id=?; DELETE FROM write_guard`, entry.Definition.Ref, entry.Digest, workID); err != nil {
		t.Fatalf("pin test definition: %v", err)
	}
	attemptID := "attempt:" + workID
	if workflowWorkerJobsActive(entry.Definition) {
		seedActiveContract(t, s, workID, 1, "Recover a failed worker authorization.\n")
	}
	job := readyWorkerJobForTest(t, s, workID, entry.Definition, seed.ownerActor, "job:authorized-failure")
	packet := dispatchWorkerPacket(t, s, workID, "execution", attemptID)
	if job != nil {
		packet["schema_version"] = WorkerPacketSchemaVersion
		packet["inputs"].(map[string]any)["worker_job"] = job
	}
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker",
		Payload:         mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": packet}),
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
	failure := authorizedAbandonment(workID, "abandon:"+workID, attemptID, "authorized worker never supplied admissible dispatch evidence", 3)
	if err := s2WorkerFailedLive(t, s, failure); err != nil {
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

// authorizedAbandonment is the host adapter's abandoned terminal for one
// authorized attempt, carrying the recorded attribution the live
// definitions require on every failed terminal.
func authorizedAbandonment(workID, eventID, attemptID, detail string, at int64) Event {
	return Event{EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, FailureKind: WorkerFailureAbandoned, Detail: detail, FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: &WorkerFailureObservation{Observer: WorkerFailureObserverHostAdapter, Phase: WorkerFailurePhaseHostExecution, Cause: WorkerFailureCauseUnknown, Basis: WorkerFailureBasisUnknown, Sources: []WorkerFailureSource{workerFailureAbandonmentSourceFixture()}}}})}
}

func TestAuthorizedWorkerFailureRecoveryNeedsNoDispatchEvidence(t *testing.T) {
	const workID = "authorized-failure-recovery"
	s, _, attemptID := seedAuthorizedFailedWorker(t, workID)
	var dispatches, dispositions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkerDispatched).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='record_worker_failure'`, workID).Scan(&dispositions); err != nil {
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
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != mustBuiltinDefinition(t, "workflow.implementation").Digest {
		t.Fatal("failure recovery changed the pinned definition")
	}
}

func TestTerminalAuthorizationRecoveryIdentityIsReadOnly(t *testing.T) {
	const workID = "authorized-failure-retry-binding"
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	s, _, attemptID := seedAuthorizedFailedWorkerWithDefinition(t, workID, entry)
	version := readWorkVersion(t, s, workID)
	failedID, err := workflowLatestFailedWorkerAuthorization(context.Background(), s.DatabaseForTesting(), workID, "execution")
	if err != nil || failedID != attemptID {
		t.Fatalf("terminal recovery identity=%q error=%v, want the exact failed authorization", failedID, err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("reading the retry binding changed work state")
	}
	var dispositions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='record_worker_failure'`, workID).Scan(&dispositions); err != nil {
		t.Fatal(err)
	}
	if dispositions != 1 {
		t.Fatalf("the terminal must derive exactly one disposition, got %d", dispositions)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(context.Background(), tx, workID, entry.Definition, "execution", "authorized_failure_test", BuiltinWorkflowRegistry())
	if err != nil {
		t.Fatal(err)
	}
	decision := workflowAdmit(entry.Definition, state, "dispatch_worker")
	if !decision.Admitted {
		t.Fatalf("terminal failure refused the dispatch route: %#v", decision)
	}
}

// The derived failure record owns the attempt's epoch: it carries the
// authorized dispatch window's epoch, and a caller-authored record at another
// epoch refuses with zero effects because the live execution step declares
// no record_worker_failure (CD-0198 D5).
func TestAuthorizedWorkerFailureRecoveryRejectsWrongEpoch(t *testing.T) {
	const workID = "authorized-failure-wrong-epoch"
	s, owner, attemptID := seedAuthorizedFailedWorker(t, workID)
	var derivedEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM worker_attempt_dispositions WHERE work_id=? AND action_id='record_worker_failure' AND attempt_id=?`, workID, attemptID).Scan(&derivedEpoch); err != nil {
		t.Fatal(err)
	}
	if derivedEpoch != 1 {
		t.Fatalf("derived failure record epoch=%d, want the dispatch window's epoch 1", derivedEpoch)
	}
	version := readWorkVersion(t, s, workID)
	_, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "record_worker_failure",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64),
		IdempotencyIdentity:  "wrong-epoch", OperationID: "wrong-epoch", PrincipalRef: owner.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "wrong-epoch", RequestID: "wrong-epoch",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("wrong epoch error=%v, want illegal_lifecycle_transition", err)
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("wrong-epoch refusal changed work state")
	}
}

func TestAuthorizedCheckpointFailureRecoveryNeedsNoDispatchEvidence(t *testing.T) {
	const workID = "authorized-checkpoint-failure"
	const attemptID = "attempt:authorized-checkpoint-failure"
	breakFix := mustBuiltinDefinition(t, "workflow.break_fix")
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, breakFix, "verify", []string{"verification"}, []string{"verification", "artifact"})
	s := fixture.store
	// The review gate joins the accepted implementation delivery before the
	// checkpoint review authorizes; the disposition assertion below stays
	// scoped to this attempt's own dispatch population.
	seedCheckpointReviewAcceptedDelivery(t, s, workID, fixture.owner, "repair", "verify")
	laneDigest := registeredLaneIdentity(t, "review")
	job := &WorkerJobBinding{JobID: "job:checkpoint-review", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	packet := joinPacketFor(t, s, workID, "verify", attemptID, "review", laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "review")
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("authorize checkpoint review: %v", err)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	failure := authorizedAbandonment(workID, "abandon-checkpoint-review", attemptID, "checkpoint review supplied no admissible dispatch evidence", 30)
	if err := s2WorkerFailedLive(t, s, failure); err != nil {
		t.Fatal(err)
	}
	// The terminal derives the checkpoint's failure disposition at the
	// window's own epoch (CD-0198 D5, CD-0193 D1); a second, caller-authored
	// record of the same attempt refuses.
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='record_worker_failure' AND attempt_id=? AND json_extract(payload,'$.attempt_epoch')=?`, workID, attemptID, epoch).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("checkpoint failure dispositions=%d, want the one derived record", recorded)
	}
	recorder := checkpointReviewReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}), 0, recorder); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("second checkpoint failure disposition error=%v, want illegal_lifecycle_transition", err)
	}
	var dispatches int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_payload IS NOT NULL`, workID, attemptID).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if dispatches != 0 || currentStep(t, s, workID) != "verify" {
		t.Fatalf("checkpoint disposition fabricated dispatch evidence or advanced the step: dispatches=%d step=%s", dispatches, currentStep(t, s, workID))
	}
}

// The live definition derives the failure record from the terminal itself
// (CD-0198 D5), so the retry stands on a recorded failure; a registry rescan
// must still refuse the retry dispatch and keep it off the work pin.
func TestAuthorizedFailedRetryDoesNotMaskRegistryStaleness(t *testing.T) {
	const workID = "authorized-failed-retry-stale-registry"
	const attemptID = "attempt:authorized-failed-retry-stale-registry"
	breakFix := mustBuiltinDefinition(t, "workflow.break_fix")
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, breakFix, "repair", []string{"verification"}, []string{"verification", "review", "artifact"})
	s := fixture.store
	laneDigest := registeredLaneIdentity(t, "implement")
	job := readyWorkerJobForTest(t, s, workID, breakFix.Definition, fixture.owner, "job:stale-registry-retry")
	packet := joinPacketFor(t, s, workID, "repair", attemptID, "implement", laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = job
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatal(err)
	}
	failure := authorizedAbandonment(workID, "abandon-stale-registry-retry", attemptID, "no admissible dispatch evidence", 30)
	if err := s2WorkerFailedLive(t, s, failure); err != nil {
		t.Fatal(err)
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='record_worker_failure' AND attempt_id=?`, workID, attemptID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("the terminal derived %d failure records, want 1", recorded)
	}
	driftStaleRegistryFixture(t, s)
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
		t.Fatal("the work pin advertises a retry refused for registry staleness")
	}
	freshID := attemptID + ":fresh"
	freshPacket := joinPacketFor(t, s, workID, "repair", freshID, "implement", laneDigest)
	freshPacket["inputs"].(map[string]any)["worker_job"] = job
	err = WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Actor: fixture.owner,
		Payload:         mustJSONValue(map[string]any{"attempt_id": freshID, "worker_packet": freshPacket}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
	})
	if !hasFailureKind(err, KindStaleRequiresReview) {
		t.Fatalf("retry preflight error=%v, want the registry staleness refusal", err)
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
			state := WorkflowAdmissionState{Step: step, Lifecycle: "in_progress", InstanceState: "running", ActiveContracts: 1}
			if step == "execution" {
				state.LawPinStaleError = newFailure(KindStaleLawRevision, "test", "law revision is stale", false, "reread_entities")
			}
			decision := workflowAdmit(entry.Definition, state, "dispatch_worker")
			if decision.Failure == nil || decision.Admitted {
				t.Fatalf("retry approval masked another refusal: %#v", decision)
			}
		})
	}
}

func TestAuthorizedAttemptCannotBeAcceptedWithoutDispatchEvidence(t *testing.T) {
	const workID = "authorized-failure-no-success-evidence"
	s, owner, attemptID := seedAuthorizedFailedWorker(t, workID)
	// Corroborate against the event log even if a projection claims success.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worker_attempts SET lifecycle_state='completed',readback_model='model/synthetic',completed_at='2026-01-01T00:00:00Z',failed_at=NULL,failure_kind='',failure_detail='' WHERE attempt_id=?; DELETE FROM write_guard`, attemptID); err != nil {
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
