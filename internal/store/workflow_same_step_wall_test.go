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
// one is open, and records the lane dispatch. The retained bool carries the
// request's FailedRetryApproved flag, the operator approval a below-limit
// failed retry still consumes; at the convergence wall the flag admits
// nothing. It returns the dispatch refusal, so the wall journeys can classify
// it.
func dispatchSameStepAttempt(t *testing.T, s *Store, workID, stepID, attemptID string, actor WorkflowActor, escalated bool, correction *WorkflowCorrectionContext) error {
	t.Helper()
	packet := dispatchWorkerPacket(t, s, workID, stepID, attemptID)
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
		ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(), FailedRetryApproved: escalated,
	}); err != nil {
		return err
	}
	issue1013RecordWorkerDispatch(t, s, workID, attemptID)
	return nil
}

// The dispatch wall no longer depends on a recorded correction: three
// non-progress attempts since the last accepted productive result refuse the
// fourth dispatch behind the convergence wall, whatever route re-dispatched
// them and whatever was recorded. Only a store-derived basis admits the
// retry; no operator approval opens the wall.
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
		failWorkerAttemptWithKind(t, s, workID, attemptID, "the lane failed before a judgeable result existed")
	}
	err := dispatchSameStepAttempt(t, s, workID, "repair", fmt.Sprintf("attempt:%s:same:4", workID), worker, false, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("fourth same-step dispatch failure=%v, want missing_evidence", err)
	}
	if !strings.Contains(failure.Detail, "repair") || !strings.Contains(failure.Detail, "3 non-progress attempts since the last accepted productive result") {
		t.Fatalf("refusal does not name the counted population: %q", failure.Detail)
	}
}

// The convergence basis is the same-step wall's one escape: the operator
// approval flag the wall once consumed no longer admits anything, and a
// contract supersession recorded after the latest dispatch admits exactly one
// fresh dispatch whose completion carries the derived basis. The completed
// attempt that is never accepted counts as neither non-progress nor a window
// reset, and the wall re-arms behind the basis-buying dispatch, so the next
// dispatch needs a new basis.
func TestConvergenceBasisAdmitsTheSameStepDispatch(t *testing.T) {
	const workID = "same-step-wall-basis"
	s, owner, _, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	third := fmt.Sprintf("attempt:%s:same:3", workID)
	for n := 1; n <= 3; n++ {
		attemptID := fmt.Sprintf("attempt:%s:same:%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, false, nil); err != nil {
			t.Fatalf("same-step dispatch %d: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, "the lane failed before a judgeable result existed")
	}
	// The operator approval representation cannot bypass the missing basis.
	if err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":same:4", worker, true, nil); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("approved fourth same-step dispatch = %v, want the missing-basis refusal", err)
	}
	// Record the third failure so the contract-correction route the
	// supersession basis needs is available, then change the approach.
	applyRecordWorkerFailureForTest(t, s, workID, owner, third, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "same-step-basis-record")
	if err := convergenceSupersedeBasis(t, s, workID, owner); err != nil {
		t.Fatalf("supersede the contract behind the same-step wall: %v", err)
	}
	converged := "attempt:" + workID + ":same:4"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", converged, worker, false, issue1013Pin(t, s, workID).Correction); err != nil {
		t.Fatalf("converging same-step dispatch: %v", err)
	}
	var basis string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.retry_convergence.basis') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, converged).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != "approach_changed" {
		t.Fatalf("converging same-step dispatch basis = %q, want approach_changed", basis)
	}
	// The converging attempt completes without acceptance: it is neither
	// counted nor a reset, and its dispatch consumed the basis.
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "worker-completed-" + converged, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(11, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: converged, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":same:5", worker, false, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("fifth same-step dispatch failure=%v, want missing_evidence", err)
	}
	if !strings.Contains(failure.Detail, "3 non-progress attempts since the last accepted productive result") {
		t.Fatalf("refusal does not name the counted population: %q", failure.Detail)
	}
}

// An accepted productive result resets the same-step count (CD-0164 D2): a
// failure at repair before the accepted delivery, the mismatch verdict, and
// the correction request's return to repair leave the window holding only the
// post-return non-progress attempts, so exactly three of them arm the wall.
func TestAcceptedResultResetsTheSameStepCount(t *testing.T) {
	const workID = "same-step-wall-accept-reset"
	ctx := context.Background()
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 19, "repair")
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
	failWorkerAttemptWithKind(t, s, workID, failed, "the lane failed before a judgeable result existed")

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
	failWorkerAttemptWithKind(t, s, workID, returned, "the lane failed before a judgeable result existed")
	for n := 3; n <= 4; n++ {
		attemptID := fmt.Sprintf("attempt:%s:d%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, false, nil); err != nil {
			t.Fatalf("post-return dispatch d%d admitted the wall early: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, "the lane failed before a judgeable result existed")
	}
	err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":d5", worker, false, nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("post-return dispatch d5 failure=%v, want missing_evidence", err)
	}
	if !strings.Contains(failure.Detail, "3 non-progress attempts since the last accepted productive result") {
		t.Fatalf("refusal counted outside the post-accept window: %q", failure.Detail)
	}
}

// An accepted productive review resets the same-step window: first-pass
// review failures from before the accepted review stay outside it after the
// confirm_premise failure edge re-enters refine, and the step change and the
// recorded dispositions that follow renew nothing, so exactly three
// post-acceptance failures arm the wall.
func TestAcceptedRefineReviewResetsTheSameStepCount(t *testing.T) {
	const workID = "same-step-wall-step-entry"
	fixture, acceptor := seedRefineFirstPass(t, workID)
	s, operator := fixture.store, fixture.operator
	lane := reviewGateLane(t, "review")

	firstPass := "r1"
	attemptID, _, _, err := dispatchRefineAttemptOnly(t, fixture, workID, firstPass, nil)
	if err != nil {
		t.Fatalf("first-pass refine dispatch: %v", err)
	}
	failReviewWorkerAttempt(t, s, workID, attemptID, lane)

	acceptedReview, acceptedEpoch, err := dispatchRefineAttempt(t, fixture, workID, "r2")
	if err != nil {
		t.Fatalf("accepted refine review dispatch: %v", err)
	}
	if err := acceptRefineResult(t, s, workID, acceptedReview, acceptedEpoch, acceptor); err != nil {
		t.Fatalf("accept refine review: %v", err)
	}
	refineProofSeedGreenRun(t, s, workID, strings.Repeat("a", 64))
	workerJobIntegrationGreenRun(t, s, workID, strings.Repeat("a", 64)+"-"+workID+"-entry")
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
		postEntry, _, _, dispatchErr := dispatchRefineAttemptOnly(t, fixture, workID, label, nil)
		if dispatchErr != nil {
			t.Fatalf("post-entry dispatch %s refused with only %d counted failures: %v", label, n-1, dispatchErr)
		}
		failReviewWorkerAttempt(t, s, workID, postEntry, lane)
	}
	_, _, _, err = dispatchRefineAttemptOnly(t, fixture, workID, "u4", nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("fourth post-entry dispatch failure=%v, want missing_evidence", err)
	}
	if !strings.Contains(failure.Detail, "3 non-progress attempts since the last accepted productive result (step refine)") {
		t.Fatalf("refusal does not name the counted population: %q", failure.Detail)
	}
}

// The correction-event path keeps its CD-0164 population and comparators: the
// recorded failure loop still counts every dispatch since the last accepted
// productive result, escalates at the limit on the >= comparator, and the
// fourth dispatch refuses with the convergence wall's own refusal — a missing
// store-derived basis — not an operator approval challenge.
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

	payload := issue1013CorrectionDispatchPayload(t, s, workID, "repair", "attempt:"+workID+":4", pin.Correction)
	_, dispatchErr := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "same-step-cmp-4", OperationID: "same-step-cmp-4",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-cmp-4", RequestID: "request:same-step-cmp-4",
		ContractDigest: testManifestDigest, Now: time.Unix(30, 0).UTC(),
	})
	var failure *Failure
	if !errors.As(dispatchErr, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("fourth correction dispatch failure=%v, want missing_evidence", dispatchErr)
	}
	if failure.Detail != "worker correction reached the three-attempt limit without a convergence basis: 3 non-progress attempts since the last accepted productive result (step repair)" {
		t.Fatalf("correction wall detail = %q, want the convergence refusal", failure.Detail)
	}
}

// The CON-729 journey behind the convergence wall: the operator approval
// flag the wall once consumed no longer admits anything, the retry approval
// binding the boundary once minted no longer exists, and a contract
// supersession recorded after the latest dispatch derives the one basis that
// admits the retry. The session then dies before the host dispatches the
// worker — no worker.dispatched event, no worker_attempts dispatch evidence —
// and the half-materialized dispatch still consumes the basis, so the next
// dispatch faces the wall again with no approval escape.
func TestHalfMaterializedDispatchConsumesTheConvergenceBasis(t *testing.T) {
	const workID = "same-step-wall-half-dispatch"
	s, owner, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	failedID := "attempt:" + workID + ":3"
	if pin.Correction == nil || !pin.Correction.Escalated || pin.Correction.FailedAttemptID != failedID {
		t.Fatalf("escalated correction = %#v, want the failed attempt %s", pin.Correction, failedID)
	}

	// The operator approval representation cannot bypass the missing basis,
	// and the wall mints no retry approval binding.
	interrupted := "attempt:" + workID + ":4"
	payload := issue1013CorrectionDispatchPayload(t, s, workID, "repair", interrupted, pin.Correction)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "half-dispatch-approved-" + workID, OperationID: "half-dispatch-approved-" + workID,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "half-dispatch-approved-" + workID, RequestID: "request:half-dispatch-approved-" + workID,
		ContractDigest: testManifestDigest, Now: time.Unix(40, 0).UTC(), FailedRetryApproved: true,
	}); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("approved interrupted retry = %v, want the missing-basis refusal", err)
	}
	if binding, bindingErr := WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, workID); bindingErr != nil || binding != nil {
		t.Fatalf("retry binding at the wall = %+v, %v, want none", binding, bindingErr)
	}

	// The supersession basis admits the retry, and the session dies before
	// the host dispatches the worker.
	if err := convergenceSupersedeBasis(t, s, workID, owner); err != nil {
		t.Fatalf("supersede the contract behind the escalation wall: %v", err)
	}
	pin = issue1013Pin(t, s, workID)
	payload = issue1013CorrectionDispatchPayload(t, s, workID, "repair", interrupted, pin.Correction)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "half-dispatch-converging-" + workID, OperationID: "half-dispatch-converging-" + workID,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "half-dispatch-converging-" + workID, RequestID: "request:half-dispatch-converging-" + workID,
		ContractDigest: testManifestDigest, Now: time.Unix(41, 0).UTC(),
	}); err != nil {
		t.Fatalf("converging interrupted retry: %v", err)
	}

	// The interrupted dispatch consumed the basis, so the wall re-arms with
	// no approval escape for the next retry.
	retry := "attempt:" + workID + ":5"
	payload = issue1013CorrectionDispatchPayload(t, s, workID, "repair", retry, pin.Correction)
	_, dispatchErr := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: payload, SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor: worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: "half-dispatch-retry-" + workID, OperationID: "half-dispatch-retry-" + workID,
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "half-dispatch-retry-" + workID, RequestID: "request:half-dispatch-retry-" + workID,
		ContractDigest: testManifestDigest, Now: time.Unix(42, 0).UTC(), FailedRetryApproved: true,
	})
	var refusal *Failure
	if !errors.As(dispatchErr, &refusal) || refusal.Kind != KindMissingEvidence {
		t.Fatalf("re-dispatch after the interrupted retry failure=%v, want missing_evidence", dispatchErr)
	}
}
