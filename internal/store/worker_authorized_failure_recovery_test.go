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

// An abandoned authorized worker job binds its recorded obligation from the
// core's own dispatch_worker completion alone (CD-0205 D2: the completion
// records worker_job and the attempt before any worker evidence exists). With
// no worker.dispatched evidence, the failure disposition still resolves the
// job, so an acceptance of an unrelated job cannot consume the open record,
// reset the counted window, or erase the exact failed-attempt retry binding
// (CD-0164 D1 counted the abandoned authorization; CD-0205 D4 keeps an
// unrelated success from discharging another job's obligation).
func TestWitnessAbandonedJobFailureKeepsBindingThroughUnrelatedSuccess(t *testing.T) {
	const workID = "witness-abandoned-job-unrelated-success"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID + "-worker", ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)
	corrective := &WorkerJobBinding{JobID: "job:witness-abandoned", Revision: 1}
	abandoned := "attempt:" + workID + ":abandoned"
	// Authorization only: the dispatch_worker completion records the job
	// binding, and no worker.dispatched evidence ever lands.
	packet := dispatchWorkerPacket(t, s, workID, "repair", abandoned)
	packet["schema_version"] = WorkerPacketSchemaVersion
	recordWorkerJobRevisionForTest(t, s, workID, worker, corrective)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *corrective)
	if err := dispatchJobPacketForTest(t, s, workID, abandoned, worker, "witness-abandoned-dispatch", packet); err != nil {
		t.Fatalf("authorize the abandoned job: %v", err)
	}
	epoch := latestStepStartEpoch(t, s, workID, "repair")
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{Event{EventID: "witness-abandon:" + workID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: abandoned, FailureKind: WorkerFailureAbandoned, Detail: "authorized worker never supplied admissible dispatch evidence"})}}}); err != nil {
		t.Fatalf("abandon the authorized job: %v", err)
	}
	applyRecordWorkerFailureForTest(t, s, workID, owner, abandoned, epoch, readWorkVersion(t, s, workID), "witness-abandoned-failure")
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != abandoned || pin.Correction.FailedAttemptEpoch != epoch {
		t.Fatalf("correction after the abandoned job disposition = %#v, want the failed attempt bound with its exact epoch", pin.Correction)
	}
	// An unrelated job's successful dispatch and acceptance discharges only
	// the job it was bound under. The abandoned job's window stays open with
	// both dispatches counted and the exact retry binding.
	unrelated := &WorkerJobBinding{JobID: "job:witness-unrelated", Revision: 1}
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.FailedAttemptID != abandoned {
		t.Fatalf("the correction the unrelated dispatch consumes = %#v, want the abandoned attempt's own record", pin.Correction)
	}
	unrelatedAttempt := "attempt:" + workID + ":unrelated"
	dispatchJobBoundAttempt(t, s, workID, "repair", unrelatedAttempt, pin.Correction, worker, pin.Version, "witness-unrelated-dispatch", unrelated)
	completeAndAcceptAttempt(t, s, workID, unrelatedAttempt, reviewGateLane(t, "implementation"), acceptor)
	anchor, anchorErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil || anchor.FailedAttemptID != abandoned || anchor.FailedAttemptEpoch != epoch {
		t.Fatalf("anchor after the unrelated acceptance = (%#v, %v), want the abandoned job's exact binding", anchor, anchorErr)
	}
	if count := jobWindowCorrectionCount(t, s, workID); count != 2 {
		t.Fatalf("count after the unrelated acceptance = %d, want both dispatches preserved", count)
	}
}

// A materialized job-bound attempt that failed mid-window — the worker's own
// worker.failed terminal event — marks its recorded obligation unresolved at
// the failure itself, before the coordinator's record_worker_failure
// disposition exists (CD-0205 D4 refining CD-0164 D2: the acceptance that
// opens the counting window is acceptance of the recorded obligation, not
// acceptance in general; a failure the coordinator has not yet dispositioned
// is exactly the unresolved state the protection stands on). An unrelated
// job's success that arrives before the disposition therefore resets neither
// the counted window nor the exact approval-free retry the failed attempt
// still owns.
func TestWitnessJobFailureBeforeDispositionKeepsWindowThroughUnrelatedSuccess(t *testing.T) {
	const workID = "witness-unrecorded-job-failure"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID + "-worker", ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)
	corrective := &WorkerJobBinding{JobID: "job:witness-midwindow", Revision: 1}
	failed := "attempt:" + workID + ":failed"
	// The attempt materialized (dispatch evidence + the failure the worker
	// itself reported), and the coordinator never dispositions it: no
	// record_worker_failure exists in the walked prefix.
	dispatchJobBoundAttempt(t, s, workID, "repair", failed, nil, worker, 0, "witness-midwindow-dispatch", corrective)
	failWorkerAttemptWithKind(t, s, workID, failed, "the worker reported the job failed mid-window")
	// An unrelated job's acceptance lands while the failed job still owes
	// its obligation, with no coordinator disposition in between.
	unrelated := &WorkerJobBinding{JobID: "job:witness-midwindow-unrelated", Revision: 1}
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, worker, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	unrelatedAttempt := "attempt:" + workID + ":unrelated"
	dispatchJobBoundAttempt(t, s, workID, "repair", unrelatedAttempt, nil, worker, pin.Version, "witness-midwindow-unrelated", unrelated)
	completeAndAcceptAttempt(t, s, workID, unrelatedAttempt, reviewGateLane(t, "implementation"), acceptor)
	if count := jobWindowCorrectionCount(t, s, workID); count != 2 {
		t.Fatalf("count after the unrelated acceptance = %d, want both dispatches preserved before any disposition", count)
	}
	// The same failed dispatch stays counted in the same-step wall's window
	// too: the wall anchor and the counting window share the same walk's
	// opening, and the unrelated success moved neither.
	def := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	before, err := workflowSameStepFailedAttemptCount(context.Background(), s.DatabaseForTesting(), def, workID, "repair", "witness")
	if err != nil || before != 1 {
		t.Fatalf("same-step failed count after the unrelated acceptance = (%d, %v), want the one failed dispatch still counted", before, err)
	}
	// A satisfying follow-up leg of the failed job — the exact obligation
	// re-recorded as a later revision and accepted — is the one resolution
	// CD-0205 leaves open, so the walk's protection excludes only the reset
	// through an unrelated success.
	unchanged, anchorErr := workflowCorrectionAttemptCount(context.Background(), s.DatabaseForTesting(), workID, maxEventSeq(t, s, workID), "witness")
	if anchorErr != nil || unchanged != 2 {
		t.Fatalf("pre-disposition recount = (%d, %v), want the same preserved window the unrelated acceptance left", unchanged, anchorErr)
	}
	// The late record_worker_failure leg is not exercised here because
	// CD-0133's epoch fence refuses a failure disposition for an attempt
	// whose dispatch predates the latest step start, so a disposition cannot
	// legally arrive after the unrelated start_repair this journey already
	// consumed; the abandonment journey above carries the
	// dispositioned-failure half of the protection.
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
	state, _, _, err := loadWorkflowAdmissionStateTx(context.Background(), tx, workID, entry.Definition, "execution", "authorized_failure_test")
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
	breakFix, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	job := readyWorkerJobForTest(t, s, workID, breakFix.Definition, fixture.owner, "job:stale-registry-retry")
	packet := joinPacketFor(t, s, workID, "repair", attemptID, "implement", laneVersion, laneDigest)
	if job != nil {
		packet["inputs"].(map[string]any)["worker_job"] = job
	}
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
