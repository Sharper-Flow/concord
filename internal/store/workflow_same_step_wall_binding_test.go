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

// The same-step wall keeps its operator-approvable escape when no correction
// record stands behind it. After three counted failed attempts at a step and
// one operator-approved attempt that completed without acceptance, the wall
// still refuses a fresh dispatch on a different lane, and the retry binding
// names the latest counted failed attempt — attempt id, the attempt epoch its
// dispatch completion recorded, and the active contract version — so the
// approval challenge the boundary mints binds a durable identity (CD-0164).
func TestSameStepWallBindingAfterACompletedAttempt(t *testing.T) {
	const workID = "same-step-wall-binding"
	const failedID = "attempt:" + workID + ":same:3"
	ctx := context.Background()
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

	// The operator-approved fourth attempt completes without acceptance: its
	// dispatch consumes the third failure record, and a completed attempt is
	// neither a counted failure nor a window reset (CD-0164 D1).
	completed := "attempt:" + workID + ":same:4"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", completed, worker, true, nil); err != nil {
		t.Fatalf("approved dispatch: %v", err)
	}
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "worker-completed-" + completed, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(11, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: completed, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}

	// The wall refuses a fresh dispatch on a different lane.
	reviewVersion, reviewDigest := registeredLaneIdentity(t, "review")
	packetPayload, err := json.Marshal(map[string]any{
		"schema_version": "1.0", "attempt_id": "attempt:" + workID + ":same:5",
		"lane_id": "review", "lane_version": reviewVersion, "lane_digest": reviewDigest,
		"work_id": workID, "step_id": "repair",
		"inputs": map[string]any{"task": "review the bounded change", "binding": map[string]any{"objective_source": "contract_premise", "work_version": 1, "contract_version": 1, "assigned_result": "contract_findings"}, "constraints": []string{"preserve the approved contract"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, retryErr := invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker",
		Payload:         mustJSONValue(map[string]any{"attempt_id": "attempt:" + workID + ":same:5", "worker_packet": json.RawMessage(packetPayload)}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor:           worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "same-step-review-5", OperationID: "same-step-review-5",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-review-5", RequestID: "request:same-step-review-5",
		ContractDigest: testManifestDigest, Now: time.Unix(12, 0).UTC(),
	})
	var refusal *Failure
	if !errors.As(retryErr, &refusal) || refusal.Kind != KindApprovalRequired {
		t.Fatalf("dispatch after the completed attempt failure=%v, want approval_required", retryErr)
	}

	// The binding keys to the latest counted failed attempt, not the
	// completed one, so the refusal mints a bound approval challenge. The
	// fixture pins no workflow contract, so the binding carries the
	// pre-contract shape; the agent journey proves the contract version
	// carries under a pinned contract.
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, failedID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	binding, err := WorkflowFailedWorkerRetryBinding(ctx, s, nil, workID)
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		t.Fatalf("the same-step wall refused %q but no retry binding exists, so no operator approval challenge can be minted", refusal.Detail)
	}
	if binding.FailedAttemptID != failedID || binding.FailedAttemptEpoch != epoch || binding.ContractVersion != 0 {
		t.Fatalf("retry binding = %+v, want %s at epoch %d with no contract pinned", binding, failedID, epoch)
	}
}
