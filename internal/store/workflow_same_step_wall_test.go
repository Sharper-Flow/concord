package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// dispatchSameStepAttempt dispatches one worker attempt at the named step
// through the dispatch_worker action, with the current correction context when
// one is open, and records the lane dispatch. It returns the dispatch refusal,
// so the wall journeys can classify it.
func dispatchSameStepAttempt(t *testing.T, s *Store, workID, stepID, attemptID string, actor WorkflowActor, escalated bool, correction *WorkflowCorrectionContext) error {
	t.Helper()
	packet := dispatchWorkerPacket(workID, stepID, attemptID)
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = map[string]any{
			"disposition": correction.Disposition, "attempt_count": correction.AttemptCount, "attempt_limit": correction.AttemptLimit, "escalated": correction.Escalated,
			"diagnosis": correction.Diagnosis, "strategy": correction.Strategy, "failure_kind": correction.FailureKind, "failure_detail": correction.FailureDetail,
			"predicate_ids": correction.PredicateIDs, "evidence_refs": correction.EvidenceRefs,
		}
	}
	packetPayload, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	key := "same-step-" + attemptID
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker",
		Payload:         mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetPayload)}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor:           actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: "request:" + key,
		ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(), EscalatedRetryApproved: escalated,
	}); err != nil {
		return err
	}
	issue1013RecordWorkerDispatch(t, s, workID, attemptID)
	return nil
}

// The dispatch wall no longer depends on a recorded correction: three failed
// attempts dispatched at the current step since the window anchor refuse the
// fourth dispatch behind the operator approval wall, whatever route
// re-dispatched them and whatever was recorded.
func TestSameStepFailuresRefuseTheFourthDispatch(t *testing.T) {
	const workID = "same-step-wall-fourth"
	s, _, _, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	for n := 1; n <= 3; n++ {
		attemptID := fmt.Sprintf("attempt:%s:same:%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, false, nil); err != nil {
			t.Fatalf("same-step dispatch %d: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, WorkerFailureFallbackBlocked, "the lane failed before a judgeable result existed")
	}
	err := dispatchSameStepAttempt(t, s, workID, "repair", fmt.Sprintf("attempt:%s:same:4", workID), worker, false, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
		t.Fatalf("fourth same-step dispatch failure=%v, want approval_required", err)
	}
	if !strings.Contains(failure.Detail, "repair") || !strings.Contains(failure.Detail, "3 failed attempts dispatched at this step since the last accepted result or step entry") {
		t.Fatalf("refusal does not name the counted population: %q", failure.Detail)
	}
}

// The escalated retry approval is the one escape: the same wall that refuses
// the fourth dispatch admits it behind the operator approval, unchanged from
// the correction-event route.
func TestEscalatedApprovalAdmitsTheSameStepDispatch(t *testing.T) {
	const workID = "same-step-wall-approval"
	s, _, _, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	for n := 1; n <= 3; n++ {
		attemptID := fmt.Sprintf("attempt:%s:same:%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, false, nil); err != nil {
			t.Fatalf("same-step dispatch %d: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, WorkerFailureFallbackBlocked, "the lane failed before a judgeable result existed")
	}
	if err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":same:4", worker, false, nil); err == nil {
		t.Fatal("fourth same-step dispatch admitted without approval")
	}
	approved := "attempt:" + workID + ":same:5"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", approved, worker, true, nil); err != nil {
		t.Fatalf("approved same-step dispatch: %v", err)
	}
	var completions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, approved).Scan(&completions); err != nil {
		t.Fatal(err)
	}
	if completions != 1 {
		t.Fatalf("approved same-step dispatch recorded %d completions for %s, want 1", completions, approved)
	}
}

// An accepted result resets the same-step count (CD-0164 D2): a failure at
// repair before the accepted delivery, the mismatch verdict, and the
// correction request's return to repair leave the window holding only the
// post-return failures, so exactly three of them arm the wall.
func TestAcceptedResultResetsTheSameStepCount(t *testing.T) {
	const workID = "same-step-wall-accept-reset"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/same-step-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/verify-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	lane := BuiltinLaneDefinitions()[0]

	failed := "attempt:" + workID + ":d0"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", failed, worker, false, nil); err != nil {
		t.Fatalf("pre-accept dispatch: %v", err)
	}
	failWorkerAttemptWithKind(t, s, workID, failed, WorkerFailureFallbackBlocked, "the lane failed before a judgeable result existed")

	accepted := "attempt:" + workID + ":d1"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", accepted, worker, false, nil); err != nil {
		t.Fatalf("accepted delivery dispatch: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "worker-completed-"+accepted, accepted, lane, time.Unix(11, 0).UTC()),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": accepted, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept worker result: %v", err)
	}

	// The accepted result advanced the item to refine; start the refine
	// attempt, seed its green-run proof, and walk the delivery gate to verify,
	// where the mismatch verdict is declared.
	reviewGateStartStep(t, s, workID, "refine", "start_refine", owner)
	refineProofSeedGreenRun(t, s, workID, strings.Repeat("a", 64))
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:same-step-accept-reset","delivery_state":"asserted"}`)
	for i := 0; i < 2 && currentStep(t, s, workID) != "verify"; i++ {
		if err := runVerdictActionAs(t, s, workID, "record_delivery", delivery, 0, acceptor); err != nil {
			t.Fatalf("record delivery %d at %s: %v", i, currentStep(t, s, workID), err)
		}
	}
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step = %q, want verify before the mismatch verdict", step)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record mismatch verdict: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", json.RawMessage(`{"diagnosis":"the delivered subject still fails","strategy":"repeat the external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`), owner, fixture.operator); err != nil {
		t.Fatalf("request correction: %v", err)
	}
	if step := currentStep(t, s, workID); step != "repair" {
		t.Fatalf("step = %q, want repair after the correction return", step)
	}

	// The return consumed the recorded request, so the first post-return
	// dispatch carries its verification context and the rest run plain.
	pin := issue1013Pin(t, s, workID)
	returned := "attempt:" + workID + ":d2"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", returned, worker, false, pin.Correction); err != nil {
		t.Fatalf("post-return dispatch: %v", err)
	}
	failWorkerAttemptWithKind(t, s, workID, returned, WorkerFailureFallbackBlocked, "the lane failed before a judgeable result existed")
	for n := 3; n <= 4; n++ {
		attemptID := fmt.Sprintf("attempt:%s:d%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, false, nil); err != nil {
			t.Fatalf("post-return dispatch d%d admitted the wall early: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, WorkerFailureFallbackBlocked, "the lane failed before a judgeable result existed")
	}
	err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":d5", worker, false, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
		t.Fatalf("post-return dispatch d5 failure=%v, want approval_required", err)
	}
	if !strings.Contains(failure.Detail, "3 failed attempts dispatched at this step since the last accepted result or step entry") {
		t.Fatalf("refusal counted outside the post-accept window: %q", failure.Detail)
	}
}

// A step entry opens a fresh window: review failures from the first refine
// pass stay outside it after the confirm_premise failure edge re-enters
// refine, so exactly three post-entry failures arm the wall.
func TestStepEntryResetsTheSameStepCount(t *testing.T) {
	const workID = "same-step-wall-step-entry"
	fixture, acceptor := seedRefineFirstPass(t, workID)
	s, operator := fixture.store, fixture.operator
	lane := reviewGateLane(t, "review")

	firstPass := "r1"
	attemptID, _, err := dispatchRefineAttemptOnly(t, fixture, workID, firstPass, nil)
	if err != nil {
		t.Fatalf("first-pass refine dispatch: %v", err)
	}
	failReviewWorkerAttempt(t, s, workID, attemptID, lane)

	acceptedReview, acceptedEpoch, err := dispatchRefineAttempt(t, fixture, workID, "r2")
	if err != nil {
		t.Fatalf("accepted refine review dispatch: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"`+acceptedReview+`","attempt_epoch":`+fmt.Sprint(acceptedEpoch)+`}`), 0, acceptor); err != nil {
		t.Fatalf("accept refine review: %v", err)
	}
	refineProofSeedGreenRun(t, s, workID, strings.Repeat("a", 64))
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:same-step-entry","delivery_state":"asserted"}`)
	for i := 0; i < 2 && currentStep(t, s, workID) != "acceptance"; i++ {
		if err := runVerdictActionAs(t, s, workID, "record_delivery", delivery, 0, acceptor); err != nil {
			t.Fatalf("record delivery %d at %s: %v", i, currentStep(t, s, workID), err)
		}
	}
	if step := currentStep(t, s, workID); step != "acceptance" {
		t.Fatalf("first pass ended at %q, want acceptance", step)
	}

	returnRefineFromAcceptance(t, fixture, workID, acceptor, operator)

	for n := 1; n <= 3; n++ {
		label := fmt.Sprintf("u%d", n)
		postEntry, _, dispatchErr := dispatchRefineAttemptOnly(t, fixture, workID, label, nil)
		if dispatchErr != nil {
			t.Fatalf("post-entry dispatch %s refused with only %d counted failures: %v", label, n-1, dispatchErr)
		}
		failReviewWorkerAttempt(t, s, workID, postEntry, lane)
	}
	_, _, err = dispatchRefineAttemptOnly(t, fixture, workID, "u4", nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindApprovalRequired {
		t.Fatalf("fourth post-entry dispatch failure=%v, want approval_required", err)
	}
	if !strings.Contains(failure.Detail, "3 failed attempts dispatched at this step since the last accepted result or step entry") {
		t.Fatalf("refusal does not name the counted population: %q", failure.Detail)
	}
}

// The correction-event path keeps its CD-0164 population and comparators: the
// recorded failure loop still counts every dispatch since the last accepted
// result, escalates at the limit on the >= comparator, and the fourth
// dispatch refuses with the correction wall's own message, not the same-step
// wall's.
func TestCorrectionComparatorsKeepTheirPopulations(t *testing.T) {
	const workID = "same-step-wall-correction-unchanged"
	s, owner, attemptID, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}

	pin := correctionCountingJourney(t, s, workID, owner, worker, attemptID, "attempt:"+workID+":2", 1, "same-step-cmp-1")
	if pin.Correction == nil || pin.Correction.AttemptCount != 1 || pin.Correction.Escalated {
		t.Fatalf("correction after one recorded failure = %#v, want one counted attempt", pin.Correction)
	}
	pin = correctionCountingJourney(t, s, workID, owner, worker, "attempt:"+workID+":2", "attempt:"+workID+":3", 3, "same-step-cmp-2")
	if pin.Correction == nil || pin.Correction.AttemptCount != 2 || pin.Correction.Escalated {
		t.Fatalf("correction after two recorded failures = %#v, want two counted attempts", pin.Correction)
	}
	pin = correctionCountingJourney(t, s, workID, owner, worker, "attempt:"+workID+":3", "", 5, "same-step-cmp-3")
	if pin.Correction == nil || pin.Correction.AttemptCount != 3 || !pin.Correction.Escalated {
		t.Fatalf("correction after three recorded failures = %#v, want escalation at the limit", pin.Correction)
	}

	var maxSeq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_id=?`, workID).Scan(&maxSeq); err != nil {
		t.Fatal(err)
	}
	correctionCount, err := workflowCorrectionAttemptCount(context.Background(), s.db, workID, maxSeq, "same_step_wall_test")
	if err != nil {
		t.Fatal(err)
	}
	if correctionCount != 3 {
		t.Fatalf("workflowCorrectionAttemptCount = %d, want the CD-0164 population of three dispatches", correctionCount)
	}

	payload := issue1013CorrectionDispatchPayload(t, workID, "repair", "attempt:"+workID+":4", pin.Correction)
	_, dispatchErr := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "same-step-cmp-4", OperationID: "same-step-cmp-4",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-cmp-4", RequestID: "request:same-step-cmp-4",
		ContractDigest: testManifestDigest, Now: time.Unix(30, 0).UTC(),
	})
	var failure *Failure
	if !errors.As(dispatchErr, &failure) || failure.Kind != KindApprovalRequired {
		t.Fatalf("fourth correction dispatch failure=%v, want approval_required", dispatchErr)
	}
	if failure.Detail != "worker correction reached the three-attempt limit" {
		t.Fatalf("correction wall detail = %q, want the unchanged CD-0164 refusal", failure.Detail)
	}
}

// The CON-729 journey: the escalated retry approval arms at three failed
// attempts, the approved retry dispatch folds, and the session dies before the
// host dispatches the worker. The half-materialized dispatch must leave the
// correction record live, so the retry binding keeps naming the failed attempt
// identity and epoch, the unapproved re-dispatch still refuses at the wall, and
// the approved re-dispatch passes it exactly as the refusal message promises.
func TestHalfMaterializedDispatchKeepsTheWallOperatorApprovable(t *testing.T) {
	const workID = "same-step-wall-half-dispatch"
	s, _, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	failedID := "attempt:" + workID + ":3"
	if pin.Correction == nil || !pin.Correction.Escalated || pin.Correction.FailedAttemptID != failedID {
		t.Fatalf("escalated correction = %#v, want the failed attempt %s", pin.Correction, failedID)
	}

	// The approved retry folds, then the interruption: no worker.dispatched
	// event, no worker_attempts row.
	interrupted := "attempt:" + workID + ":4"
	payload := issue1013CorrectionDispatchPayload(t, workID, "repair", interrupted, pin.Correction)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "half-dispatch-approved-" + workID, OperationID: "half-dispatch-approved-" + workID,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "half-dispatch-approved-" + workID, RequestID: "request:half-dispatch-approved-" + workID,
		ContractDigest: testManifestDigest, Now: time.Unix(40, 0).UTC(), EscalatedRetryApproved: true,
	}); err != nil {
		t.Fatalf("approved interrupted retry: %v", err)
	}

	binding, err := WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID)
	if err != nil {
		t.Fatalf("read retry binding after the interruption: %v", err)
	}
	if binding == nil || binding.FailedAttemptID != failedID || binding.FailedAttemptEpoch != 5 {
		t.Fatalf("retry binding after the interruption = %+v, want the failed attempt identity and epoch", binding)
	}

	// The wall keeps refusing the re-dispatch without the operator approval.
	retry := "attempt:" + workID + ":5"
	payload = issue1013CorrectionDispatchPayload(t, workID, "repair", retry, pin.Correction)
	_, dispatchErr := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "half-dispatch-retry-" + workID, OperationID: "half-dispatch-retry-" + workID,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "half-dispatch-retry-" + workID, RequestID: "request:half-dispatch-retry-" + workID,
		ContractDigest: testManifestDigest, Now: time.Unix(41, 0).UTC(),
	})
	var refusal *Failure
	if !errors.As(dispatchErr, &refusal) || refusal.Kind != KindApprovalRequired {
		t.Fatalf("unapproved re-dispatch failure=%v, want approval_required", dispatchErr)
	}

	// The approved re-dispatch passes the wall, and its materialization
	// consumes the record.
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "half-dispatch-retry-approved-" + workID, OperationID: "half-dispatch-retry-approved-" + workID,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "half-dispatch-retry-approved-" + workID, RequestID: "request:half-dispatch-retry-approved-" + workID,
		ContractDigest: testManifestDigest, Now: time.Unix(42, 0).UTC(), EscalatedRetryApproved: true,
	}); err != nil {
		t.Fatalf("approved re-dispatch after the interruption: %v", err)
	}
	issue1013RecordWorkerDispatch(t, s, workID, retry)
	binding, err = WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID)
	if err != nil {
		t.Fatalf("read retry binding after the materialized re-dispatch: %v", err)
	}
	if binding != nil {
		t.Fatalf("retry binding after the materialized re-dispatch = %+v, want the consumed record", binding)
	}
}
