package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// dispatchCheckpointReviewAttempt drives one review-lane attempt at the named
// checkpoint step through the dispatch_worker action and the lane actor
// dispatch, and returns the attempt epoch the action started.
func dispatchCheckpointReviewAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, stepID, attemptID string) int64 {
	t.Helper()
	s := fixture.store
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, stepID, attemptID, "review", laneVersion, laneDigest)
	binding := attachReadyWorkerJobIfJobCapable(t, s, workID, stepID, fixture.owner, packet)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("review dispatch at the verify checkpoint refused: %v", err)
	}
	var packetDigest string
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&packetDigest); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	laneDispatch := Event{EventID: "checkpoint-failed-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest, WorkerJob: binding})}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, laneDispatch, fixture.owner.PrincipalRef, fixture.owner.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	}); err != nil {
		t.Fatalf("lane actor dispatch: %v", err)
	}
	return epoch
}

// seedCheckpointReviewAcceptedDelivery gives a review-lane dispatch its
// prerequisite: one real Git worktree claim whose qualified HEAD is the
// shared verify subject, and one implementation-lane delivery dispatched,
// completed with a clean core observation, and accepted on that claim at the
// named effect step — the accepted delivery a review of implementation work
// joins. It leaves the instance parked at parkBackStep.
func seedCheckpointReviewAcceptedDelivery(t *testing.T, s *Store, workID string, owner WorkflowActor, effectStep, parkBackStep string) {
	t.Helper()
	reviewSubjectClaim(t, s, workID)
	parkWorkflowInstanceAt(t, s, workID, effectStep)
	seedAcceptedImplementationDeliveryAt(t, s, workID, owner, effectStep)
	parkWorkflowInstanceAt(t, s, workID, parkBackStep)
}

// checkpointEffectStep derives the pinned definition's external-effect
// dispatch step the accepted implementation delivery rides: the first step
// whose kind admits the implementation lane and that is not the named
// checkpoint.
func checkpointEffectStep(t *testing.T, s *Store, workID, checkpoint string) string {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref, definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s@%d is not registered", ref, version)
	}
	for _, step := range registered.Definition.StepGraph.Steps {
		if step.Kind == WorkflowStepExternalEffect && step.ID != checkpoint && slices.Contains(step.Actions, "dispatch_worker") {
			return step.ID
		}
	}
	t.Fatalf("pinned definition %s@%d carries no external-effect dispatch step beside %q", ref, version, checkpoint)
	return ""
}

// parkWorkflowInstanceAt parks the pinned instance at one step under the
// fold guard, the fixture idiom for reaching a step a shared seeding needs
// without replaying history.
func parkWorkflowInstanceAt(t *testing.T, s *Store, workID, step string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step=? WHERE work_id=?; DELETE FROM fold_guard`, step, workID); err != nil {
		t.Fatalf("park %s at %s: %v", workID, step, err)
	}
}

// seedAcceptedImplementationDeliveryAt dispatches, completes, and accepts one
// implementation attempt at the named step: the accepted delivery with a clean
// core observation that a review-lane dispatch on implementation work joins.
// The worker-job revision records only on a job-capable pin; an older pin
// dispatches unbound.
func seedAcceptedImplementationDeliveryAt(t *testing.T, s *Store, workID string, owner WorkflowActor, stepID string) {
	t.Helper()
	ctx := context.Background()
	lane := laneForID(t)
	var registered RegisteredDefinition
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref, definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s@%d is not registered", ref, version)
	}
	var job *WorkerJobBinding
	if workflowWorkerJobsActive(registered.Definition) {
		binding := WorkerJobBinding{JobID: "job:accepted-delivery-" + workID, Revision: 1}
		recordWorkerJobRevisionForTest(t, s, workID, owner, &binding)
		job = &binding
	}
	attemptID := "attempt:accepted-delivery:" + workID
	packet := joinPacketFor(t, s, workID, stepID, attemptID, "implement", lane.Version, lane.Digest)
	if job != nil {
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "review")
	}
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, workID)
	if err := dispatchJobPacketForTest(t, s, workID, attemptID, owner, "accepted-delivery-"+workID, packet); err != nil {
		t.Fatalf("accepted-delivery dispatch refused: %v", err)
	}
	laneDispatchPayload := WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane),
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
		PacketDigest: "sha256:" + strings.Repeat("f", 64), WorkerJob: job,
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(laneDispatchPayload)}}}); err != nil {
		t.Fatal(err)
	}
	entry, err := activeWorktreeEntryForProject(ctx, s.DatabaseForTesting(), "verify_worktree", workID, "project")
	if err != nil {
		t.Fatal(err)
	}
	// The delivered edit is the dispatched job's own work at the root: the
	// retired measurement basis no longer moves the edit into an inventoried
	// path (CD-0242 D6).
	editPath := "implementation.txt"
	if directory := filepath.Dir(editPath); directory != "." {
		if err := os.MkdirAll(filepath.Join(entry.Path, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(entry.Path, editPath), []byte("implementation delivery\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, entry.Path, "add", editPath)
	runTestGit(t, entry.Path, "commit", "-m", "implementation delivery")
	subject := runTestGit(t, entry.Path, "rev-parse", "HEAD")
	git := newFakeWorktreeGit(entry.Path)
	git.worktrees[entry.Path], git.worktreeRepos[entry.Path], git.branches[entry.Branch] = entry.Branch, entry.Path, subject
	req := verifyRequest(git, "verify-accepted-delivery-"+workID, []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID = workID, "project"
	if _, err := s.VerifyWorktree(ctx, req); err != nil {
		t.Fatal(err)
	}
	if qualified, err := readCurrentOracleSubject(ctx, s.DatabaseForTesting(), workID); err != nil || qualified != subject {
		t.Fatalf("qualified subject = %q err = %v, want the delivered commit %q", qualified, err, subject)
	}
	completion := Event{EventID: "complete-accepted-delivery-" + workID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane), WorkerJob: job,
	})}
	prepared, prepErr := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: completion.EventID, Runner: qualifiedSubjectRunner{head: subject}})
	if prepErr != nil {
		t.Fatalf("accepted-delivery observation refused: %v", prepErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(ctx, prepared), s, completion)
	var epoch int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, attemptID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	acceptor := WorkflowActor{PrincipalRef: owner.PrincipalRef, ClientRef: owner.ClientRef, AgentRef: "agent/accepted-delivery-acceptor", SessionRef: "session/" + workID + "-delivery-acceptor", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch}), 0, acceptor); err != nil {
		t.Fatalf("accept implementation delivery: %v", err)
	}
}

func failCheckpointReviewAttempt(t *testing.T, s *Store, workID, attemptID string) {
	t.Helper()
	lane := reviewGateLane(t, "review")
	failed := Event{EventID: "checkpoint-failed-" + attemptID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), FailureKind: WorkerFailureWorkerError, Detail: "review lane failed"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{failed}}); err != nil {
		t.Fatal(err)
	}
}

// seedCheckpointFailedReview leaves a break-fix item at its verify checkpoint
// with an ok verdict and one failed review-lane attempt. The contract does not
// require review evidence, so the failed attempt is the only thing between
// the operator and the premise confirmation.
func seedCheckpointFailedReview(t *testing.T, workID string) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, attributionInactiveWorkflowHeadForTest(t, "workflow.break_fix"), "verify", []string{"verification"}, []string{"verification", "artifact"})
	seedCheckpointReviewAcceptedDelivery(t, fixture.store, workID, fixture.owner, checkpointEffectStep(t, fixture.store, workID, "verify"), "verify")
	reviewer := checkpointReviewReviewer(t, fixture.store, workID)
	if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}]}`), 0, reviewer); err != nil {
		t.Fatalf("record_verdict refused: %v", err)
	}
	attemptID := "attempt:" + workID + ":failed-review"
	epoch := dispatchCheckpointReviewAttempt(t, fixture, workID, "verify", attemptID)
	failCheckpointReviewAttempt(t, fixture.store, workID, attemptID)
	return fixture, attemptID, epoch
}

func requireDispatchHoldRefusal(t *testing.T, err error, context string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, "dispatched worker attempt") {
		t.Fatalf("%s = %v, want the dispatched-attempt hold refusal", context, err)
	}
}

// TestCheckpointFailedReviewRecordsAndReleasesTheGate pins the recovery route
// for a failed review at a confirmation step: the failure record is admitted
// there, holds the step, and dispositions the attempt, so the operator's own
// gate opens.
func TestCheckpointFailedReviewRecordsAndReleasesTheGate(t *testing.T) {
	const workID = "checkpoint-failed-review-recovery"
	fixture, attemptID, epoch := seedCheckpointFailedReview(t, workID)
	s := fixture.store

	err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	requireDispatchHoldRefusal(t, err, "confirm_premise over the unrecorded failed review")

	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure at the verify checkpoint refused: %v", err)
	}
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step after record_worker_failure = %q, want verify", step)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm_premise refused after the failed review was recorded: %v", err)
	}
	if step := currentStep(t, s, workID); step != "complete" {
		t.Fatalf("step after premise confirmation = %q, want complete", step)
	}
}

// TestCheckpointUnspawnedDispatchKeepsTheFailedReviewHold pins that a
// dispatch window no worker ever used is not a disposition: the failed
// attempt keeps holding the operator's gate.
func TestCheckpointUnspawnedDispatchKeepsTheFailedReviewHold(t *testing.T) {
	const workID = "checkpoint-failed-review-unspawned"
	fixture, _, _ := seedCheckpointFailedReview(t, workID)
	s := fixture.store

	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "verify", "attempt:"+workID+":unspawned", "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("second review dispatch at the verify checkpoint refused: %v", err)
	}
	err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	requireDispatchHoldRefusal(t, err, "confirm_premise after an unspawned dispatch window")
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step after the refused confirmation = %q, want verify", step)
	}
}

// TestCheckpointFailedReviewPinMatchesTheFoldGate pins the pin side of the
// recovery: the pin advertises the hold-mode failure record exactly while the
// fold admits it, stops advertising once the record dispositions the attempt,
// and the recovery leaves the pinned definition digest unchanged.
func TestCheckpointFailedReviewPinMatchesTheFoldGate(t *testing.T) {
	const workID = "checkpoint-failed-review-pin"
	fixture, attemptID, epoch := seedCheckpointFailedReview(t, workID)
	s := fixture.store

	pin := issue1013Pin(t, s, workID)
	if !issue1013HasIntent(pin, "record_worker_failure") {
		t.Fatalf("pin hides the checkpoint failure record the fold admits: %#v", pin.NextValidIntents)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "record_worker_failure" && intent.ReasonCode != "worker_failure_recovery" {
			t.Fatalf("failure record reason = %q, want worker_failure_recovery", intent.ReasonCode)
		}
	}
	var digestBefore string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&digestBefore); err != nil {
		t.Fatal(err)
	}

	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+attemptID+`","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure at the verify checkpoint refused: %v", err)
	}

	pin = issue1013Pin(t, s, workID)
	if issue1013HasIntent(pin, "record_worker_failure") {
		t.Fatalf("pin advertises a failure record the fold would refuse twice: %#v", pin.NextValidIntents)
	}
	var digestAfter string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&digestAfter); err != nil {
		t.Fatal(err)
	}
	if digestAfter != digestBefore {
		t.Fatalf("recovery moved the pinned digest: before=%s after=%s", digestBefore, digestAfter)
	}
}

// TestEffectStepStaleCompletedAttemptCannotAdvance pins the effect-step side
// of the epoch fence, which the checkpoint recovery leaves unchanged: once a
// newer dispatch_worker window has opened and a second worker has dispatched,
// the earlier completed attempt cannot advance the step with its own epoch.
func TestEffectStepStaleCompletedAttemptCannotAdvance(t *testing.T) {
	const workID = "effect-step-stale-completed-attempt"
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, attributionInactiveWorkflowHeadForTest(t, "workflow.break_fix"), "repair", []string{"verification"}, []string{"verification", "review", "artifact"})
	s := fixture.store
	seedCheckpointReviewAcceptedDelivery(t, s, workID, fixture.owner, "repair", "repair")
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	lane := reviewGateLane(t, "review")

	// The first window opens, its worker dispatches, and the report lands
	// completed but unaccepted. The derived-start head opens the repair
	// window through the qualifying checkpoint, whose started event fences
	// the step exactly as the callable start did. The accepted-delivery
	// prerequisite consumed the earlier epochs, so the window opens at the
	// next epoch.
	version := readWorkVersion(t, s, workID)
	var nextEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(json_extract(payload,'$.attempt_epoch')),0)+1 FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker'`, workID, WorkflowActionStarted).Scan(&nextEpoch); err != nil {
		t.Fatal(err)
	}
	start := workflowEventWithActor("effect-stale-start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "repair", "action_id": "checkpoint_repair", "attempt_epoch": nextEpoch,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "start:" + workID, "actor_ref": ownerRef,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	staleAttemptID := "attempt:" + workID + ":stale"
	// The stale attempt's own authorization proves its original Project; the
	// later dispatch and completion keep their recorded order.
	recordWorkerFixtureDispatchAuthorization(t, s, workID, staleAttemptID, "repair", ownerRef)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "effect-stale-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: staleAttemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})},
		workerCompleteEventForLane(workID, "effect-stale-completed-"+workID, staleAttemptID, lane, time.Unix(31, 0).UTC(), workerFixtureDirectory(t, s, workID)),
	}}); err != nil {
		t.Fatal(err)
	}

	// A newer dispatch_worker window opens and a second worker actually
	// dispatches on the step.
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "repair", "attempt:"+workID+":current", "review", laneVersion, laneDigest)
	attachReadyWorkerJobIfJobCapable(t, s, workID, "repair", fixture.owner, packet)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, readWorkVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("second dispatch at the repair step refused: %v", err)
	}

	// The acceptor is distinct from the worker, so the epoch fence is the
	// only guard between the stale report and the step advance.
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/effect-stale-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	err = runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+staleAttemptID+`","attempt_epoch":1}`), 0, acceptor)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || (!strings.Contains(failure.Detail, "epoch does not match") && !strings.Contains(failure.Detail, "dispatch is stale")) {
		t.Fatalf("stale accept = %v, want the epoch-mismatch or stale refusal", err)
	}
	if step := currentStep(t, s, workID); step != "repair" {
		t.Fatalf("step after the refused stale accept = %q, want repair", step)
	}
}

// TestCheckpointRetryDispatchSupersedesTheFailedReview pins the third
// release condition: a later worker actually dispatching on the checkpoint
// supersedes the failed attempt as the holder, so the failed attempt's own
// record refuses and the new attempt's record is the one that opens the gate.
func TestCheckpointRetryDispatchSupersedesTheFailedReview(t *testing.T) {
	const workID = "checkpoint-failed-review-retry"
	fixture, failedAttemptID, _ := seedCheckpointFailedReview(t, workID)
	s := fixture.store

	retryID := "attempt:" + workID + ":retry"
	retryEpoch := dispatchCheckpointReviewAttempt(t, fixture, workID, "verify", retryID)

	err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+failedAttemptID+`","attempt_epoch":1}`), 0, fixture.owner)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, "not declared on the current step") {
		t.Fatalf("record of the superseded attempt = %v, want the current-step refusal", err)
	}
	err = runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	requireDispatchHoldRefusal(t, err, "confirm_premise over the live retry attempt")

	failCheckpointReviewAttempt(t, s, workID, retryID)
	if err := runVerdictActionAs(t, s, workID, "record_worker_failure", json.RawMessage(`{"attempt_id":"`+retryID+`","attempt_epoch":`+fmt.Sprint(retryEpoch)+`}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_worker_failure for the retry attempt refused: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm_premise refused after the retry attempt was recorded: %v", err)
	}
	if step := currentStep(t, s, workID); step != "complete" {
		t.Fatalf("step after premise confirmation = %q, want complete", step)
	}
}
