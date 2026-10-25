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

// TestRepeatedFailedAttemptsNeverWallTheStep pins CD-0242 D6: no attempt
// count, correction count, or escalation wall exists. Each failed implement
// attempt at the live break_fix repair step derives its failure record with
// the terminal (CD-0198 D5), and the next dispatch, carrying the correction
// that record opens, is admitted on its own scope however many attempts
// failed before it.
func TestRepeatedFailedAttemptsNeverWallTheStep(t *testing.T) {
	const workID = "same-step-repeated-failures"
	f, attemptID, _ := seedCurrentRepairWorker(t, workID)
	s := f.store
	lane := laneForID(t)
	job := sameStepDispatchedJob(t, s, workID, attemptID)
	for n := 1; n <= 4; n++ {
		failed := s2WorkerFailedAttributedEvent(workID, attemptID, "same-step-failed-"+attemptID, preferredModelForLane(lane), WorkerFailureWorkerError, "the lane failed before a judgeable result existed", time.Unix(int64(20+n), 0).UTC())
		if err := s2WorkerFailedLive(t, s, failed); err != nil {
			t.Fatalf("fail attempt %d: %v", n, err)
		}
		if records := s2DerivedFailureRecords(t, s, workID, attemptID); len(records) != 1 {
			t.Fatalf("failure records for attempt %d = %+v, want one derived record", n, records)
		}
		attemptID = fmt.Sprintf("attempt:%s:d%d", workID, n)
		view, err := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
		if err != nil || view == nil {
			t.Fatalf("admitted view = %+v err = %v, want a present view", view, err)
		}
		packet := workContextDispatchPacket(t, f, attemptID, job, view)
		consumeCurrentCorrection(t, s, workID, packet)
		if err := dispatchWorkContextAttempt(t, f, packet); err != nil {
			t.Fatalf("dispatch after %d failed attempts refused: %v", n, err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{implementDispatchEvidenceEvent(workID, attemptID, lane, job)}}); err != nil {
			t.Fatal(err)
		}
	}
	if step := currentStep(t, s, workID); step != "repair" {
		t.Fatalf("step after repeated failures = %q, want repair", step)
	}
}

// sameStepDispatchedJob reads the worker-job binding the named attempt's lane
// dispatch carried, so a retry re-dispatches the same job and assignment
// group instead of opening a new one.
func sameStepDispatchedJob(t *testing.T, s *Store, workID, attemptID string) WorkerJobBinding {
	t.Helper()
	var payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT dispatch_payload FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var dispatched WorkerDispatchedPayload
	if err := json.Unmarshal([]byte(payload), &dispatched); err != nil {
		t.Fatal(err)
	}
	if dispatched.WorkerJob == nil {
		t.Fatalf("attempt %s dispatched without a worker job", attemptID)
	}
	return *dispatched.WorkerJob
}
