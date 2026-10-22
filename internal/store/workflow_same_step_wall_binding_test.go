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

func TestSameStepWallAfterCompletedAttemptRefusesAnotherLane(t *testing.T) {
	const workID = "same-step-wall-binding"
	ctx := context.Background()
	s, owner, _, _ := seedOldDefinitionWorker(t, workID)
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	for n := 1; n <= 3; n++ {
		attemptID := fmt.Sprintf("attempt:%s:same:%d", workID, n)
		if err := dispatchSameStepAttempt(t, s, workID, "repair", attemptID, worker, nil); err != nil {
			t.Fatalf("same-step dispatch %d: %v", n, err)
		}
		failWorkerAttemptWithKind(t, s, workID, attemptID, "the lane failed before a judgeable result existed")
	}
	if err := dispatchSameStepAttempt(t, s, workID, "repair", "attempt:"+workID+":same:4", worker, nil); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("dispatch at the armed wall = %v, want the missing-basis refusal", err)
	}
	third := fmt.Sprintf("attempt:%s:same:3", workID)
	applyRecordWorkerFailureForTest(t, s, workID, owner, third, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "same-step-binding-record")
	if err := convergenceSupersedeBasis(t, s, workID, owner); err != nil {
		t.Fatalf("supersede the contract behind the same-step wall: %v", err)
	}
	completed := "attempt:" + workID + ":same:4"
	if err := dispatchSameStepAttempt(t, s, workID, "repair", completed, worker, issue1013Pin(t, s, workID).Correction); err != nil {
		t.Fatalf("converging dispatch: %v", err)
	}
	lane := BuiltinLaneDefinitions()[0]
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "worker-completed-" + completed, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(11, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: completed, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: workerFixtureDirectory(t, s, workID)}),
	}}}); err != nil {
		t.Fatal(err)
	}
	reviewVersion, reviewDigest := registeredLaneIdentity(t, "review")
	packetPayload, err := json.Marshal(bindPacketToRecordedState(t, s, map[string]any{
		"schema_version": WorkerPacketSchemaVersion, "attempt_id": "attempt:" + workID + ":same:5",
		"lane_id": "review", "lane_version": reviewVersion, "lane_digest": reviewDigest,
		"work_id": workID, "step_id": "repair",
		"inputs": map[string]any{"task": "review the bounded change", "constraints": []string{"preserve the approved contract"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, retryErr := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker",
		Payload:         mustJSONValue(map[string]any{"attempt_id": "attempt:" + workID + ":same:5", "worker_packet": json.RawMessage(packetPayload)}),
		SessionWorktree: dispatchSessionWorktree(t, s, workID),
		Actor:           worker, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "same-step-review-5", OperationID: "same-step-review-5",
		PrincipalRef: worker.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-review-5", RequestID: "request:same-step-review-5",
		ContractDigest: testManifestDigest, Now: time.Unix(12, 0).UTC(),
	})
	var refusal *Failure
	if !errors.As(retryErr, &refusal) || refusal.Kind != KindMissingEvidence {
		t.Fatalf("dispatch after the completed attempt failure=%v, want missing_evidence", retryErr)
	}
	if !strings.Contains(refusal.Detail, "3 non-progress attempts since the applicable nonprogress anchor") {
		t.Fatalf("refusal does not name the counted population: %q", refusal.Detail)
	}
}
