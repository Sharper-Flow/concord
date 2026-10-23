package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// dispatchSameStepAttempt authorizes and materializes one dispatch with its
// current correction context. Callers classify any admission refusal.
func dispatchSameStepAttempt(t *testing.T, s *Store, workID, stepID, attemptID string, actor WorkflowActor, correction *WorkflowCorrectionContext) error {
	t.Helper()
	packet := dispatchWorkerPacket(t, s, workID, stepID, attemptID)
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
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
		ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(),
	}); err != nil {
		return err
	}
	issue1013RecordWorkerDispatch(t, s, workID, attemptID)
	return nil
}

func TestAcceptedResultKeepsTheSameStepCorrectionReturn(t *testing.T) {
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
	if err := dispatchSameStepAttempt(t, s, workID, "repair", failed, worker, nil); err != nil {
		t.Fatalf("pre-accept dispatch: %v", err)
	}
	failWorkerAttemptWithKind(t, s, workID, failed, "the lane failed before a judgeable result existed")
	accepted := "attempt:" + workID + ":d1"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", accepted, worker, nil); err != nil {
		t.Fatalf("accepted delivery dispatch: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "worker-completed-"+accepted, accepted, lane, time.Unix(11, 0).UTC(), workerFixtureDirectory(t, s, workID)),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": accepted, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept worker result: %v", err)
	}
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
	pin := issue1013Pin(t, s, workID)
	returned := "attempt:" + workID + ":d2"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", returned, worker, pin.Correction); err != nil {
		t.Fatalf("post-return dispatch: %v", err)
	}
	failWorkerAttemptWithKind(t, s, workID, returned, "the lane failed before a judgeable result existed")
	for n := 3; n <= 4; n++ {
		attemptID := fmt.Sprintf("attempt:%s:d%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, nil); err != nil {
			t.Fatalf("post-return dispatch d%d: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, "the lane failed before a judgeable result existed")
	}
	if err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":d5", worker, nil); err != nil {
		t.Fatalf("post-return dispatch after repeated failures: %v", err)
	}
}

func TestAcceptedRefineReviewKeepsTheSameStepReentry(t *testing.T) {
	const workID = "same-step-wall-step-entry"
	fixture, acceptor := seedRefineFirstPass(t, workID)
	s, operator := fixture.store, fixture.operator
	lane := reviewGateLane(t, "review")
	attemptID, _, _, err := dispatchRefineAttemptOnly(t, fixture, workID, "r1", nil)
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
			t.Fatalf("post-entry dispatch %s: %v", label, dispatchErr)
		}
		failReviewWorkerAttempt(t, s, workID, postEntry, lane)
	}
	if _, _, _, err := dispatchRefineAttemptOnly(t, fixture, workID, "u4", nil); err != nil {
		t.Fatalf("post-entry dispatch after repeated failures: %v", err)
	}
}
