package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// dispatchCheckpointReviewAttempt drives one review-lane attempt at the verify
// checkpoint through the dispatch_worker action and the lane actor dispatch,
// and returns the attempt epoch the action started.
func dispatchCheckpointReviewAttempt(t *testing.T, fixture workflowReturnRouteFixture, workID, attemptID string) int64 {
	t.Helper()
	s := fixture.store
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(workID, "verify", attemptID, "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
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
	laneDispatch := Event{EventID: "checkpoint-failed-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: packetDigest})}
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
	fixture := seedWorkflowReturnRouteFixtureRequiring(t, workID, "workflow.break_fix", "verify", []string{"verification"}, []string{"verification", "artifact"})
	reviewer := checkpointReviewReviewer(t, fixture.store, workID)
	if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}`), 0, reviewer); err != nil {
		t.Fatalf("record_verdict refused: %v", err)
	}
	attemptID := "attempt:" + workID + ":failed-review"
	epoch := dispatchCheckpointReviewAttempt(t, fixture, workID, attemptID)
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
	packet := joinPacketFor(workID, "verify", "attempt:"+workID+":unspawned", "review", laneVersion, laneDigest)
	if _, err := dispatchJoinAttempt(context.Background(), t, s, workID, verdictItemVersion(t, s, workID), fixture.owner, packet); err != nil {
		t.Fatalf("second review dispatch at the verify checkpoint refused: %v", err)
	}
	err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	requireDispatchHoldRefusal(t, err, "confirm_premise after an unspawned dispatch window")
	if step := currentStep(t, s, workID); step != "verify" {
		t.Fatalf("step after the refused confirmation = %q, want verify", step)
	}
}
