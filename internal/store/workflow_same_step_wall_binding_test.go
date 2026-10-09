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

// The same-step wall no longer mints an approval challenge when no correction
// record stands behind it. Below the limit the ordinary failed-retry binding
// still binds a durable identity — the failed attempt id, the attempt epoch
// its dispatch completion recorded, and the active contract version — but once
// three non-progress attempts arm the wall, no retry approval binding exists:
// only a store-derived convergence basis admits a dispatch, an approval flag
// cannot substitute, and a completed-but-unaccepted attempt counts as neither
// non-progress nor a window reset (CD-0164 D1, CON-885).
func TestSameStepWallHasNoApprovalBindingAfterACompletedAttempt(t *testing.T) {
	const workID = "same-step-wall-binding"
	const failedID = "attempt:" + workID + ":same:1"
	ctx := context.Background()
	s, owner, _, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	if err := dispatchSameStepAttempt(t, s, workID, "repair", failedID, worker, false, nil); err != nil {
		t.Fatalf("same-step dispatch 1: %v", err)
	}
	failWorkerAttemptWithKind(t, s, workID, failedID, "the lane failed before a judgeable result existed")

	// Below the limit the ordinary failed retry keeps its operator-approvable
	// escape, and the binding keys to the durable identity the approval must
	// name. The fixture pins no workflow contract yet, so the binding carries
	// the pre-contract shape.
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, failedID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	binding, err := WorkflowFailedWorkerRetryBinding(ctx, s, nil, workID)
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || binding.FailedAttemptID != failedID || binding.FailedAttemptEpoch != epoch || binding.ContractVersion != 0 {
		t.Fatalf("below-wall retry binding = %+v, want %s at epoch %d with no contract pinned", binding, failedID, epoch)
	}

	// The approved ordinary retry runs below the wall, and two more failures
	// arm it.
	for n := 2; n <= 3; n++ {
		attemptID := fmt.Sprintf("attempt:%s:same:%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, n == 2, nil); err != nil {
			t.Fatalf("same-step dispatch %d: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, "the lane failed before a judgeable result existed")
	}
	third := fmt.Sprintf("attempt:%s:same:3", workID)
	if err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":same:4", worker, true, nil); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("approved dispatch at the armed wall = %v, want the missing-basis refusal", err)
	}
	if binding, bindingErr := WorkflowFailedWorkerRetryBinding(ctx, s, nil, workID); bindingErr != nil || binding != nil {
		t.Fatalf("retry binding at the armed wall = %+v, %v, want none", binding, bindingErr)
	}

	// A contract supersession recorded after the latest dispatch is the one
	// basis that admits the next attempt; its report completes without
	// acceptance, so it is neither counted nor a reset.
	applyRecordWorkerFailureForTest(t, s, workID, owner, third, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "same-step-binding-record")
	if err := convergenceSupersedeBasis(t, s, workID, owner); err != nil {
		t.Fatalf("supersede the contract behind the same-step wall: %v", err)
	}
	completed := "attempt:" + workID + ":same:4"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", completed, worker, false, issue1013Pin(t, s, workID).Correction); err != nil {
		t.Fatalf("converging dispatch: %v", err)
	}
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "worker-completed-" + completed, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(11, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: completed, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}

	// The wall still refuses a fresh dispatch on a different lane behind the
	// consumed basis, and no approval binding exists to challenge for.
	reviewVersion, reviewDigest := registeredLaneIdentity(t, "review")
	packetPayload, err := json.Marshal(bindPacketToRecordedState(t, s, map[string]any{
		"schema_version": "1.0", "attempt_id": "attempt:" + workID + ":same:5",
		"lane_id": "review", "lane_version": reviewVersion, "lane_digest": reviewDigest,
		"work_id": workID, "step_id": "repair",
		"inputs": map[string]any{"task": "review the bounded change", "constraints": []string{"preserve the approved contract"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, retryErr := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker",
		Payload:         mustJSONValue(map[string]any{"attempt_id": "attempt:" + workID + ":same:5", "worker_packet": json.RawMessage(packetPayload)}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor:           worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "same-step-review-5", OperationID: "same-step-review-5",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-review-5", RequestID: "request:same-step-review-5",
		ContractDigest: testManifestDigest, Now: time.Unix(12, 0).UTC(), FailedRetryApproved: true,
	})
	var refusal *Failure
	if !errors.As(retryErr, &refusal) || refusal.Kind != KindMissingEvidence {
		t.Fatalf("dispatch after the completed attempt failure=%v, want missing_evidence", retryErr)
	}
	if !strings.Contains(refusal.Detail, "3 non-progress attempts since the last accepted productive result") {
		t.Fatalf("refusal does not name the counted population: %q", refusal.Detail)
	}
	binding, err = WorkflowFailedWorkerRetryBinding(ctx, s, nil, workID)
	if err != nil {
		t.Fatal(err)
	}
	if binding != nil {
		t.Fatalf("the same-step wall refused %q but minted the retry binding %+v an operator approval could consume", refusal.Detail, binding)
	}
}
