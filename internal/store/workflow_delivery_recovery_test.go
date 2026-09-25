package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Recovery of an already pinned, nonterminal work item parked on a released
// closed-gate delivery step with an outstanding post-rejection review. The
// released definition content and the recorded event history stay untouched;
// the evidence-bearing corrective return is the only route off the gate.

// reviewGateSeedAcceptAtRefine replays the CON-421 crossing: it records an
// accepted implement repair whose accept advances the refinement step into
// the delivery gate with the post-rejection review debt outstanding.
func reviewGateSeedAcceptAtRefine(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor) {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	acceptorRef, err := WorkflowActorRef(acceptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-gate-historical-accept-" + attemptID, Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: acceptorRef, OccurredAt: time.Unix(200, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "refine", "action_id": "accept_worker_result", "attempt_epoch": epoch,
			"worker_attempt_id": attemptID, "actor_ref": acceptorRef,
		}),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("seed the historical refinement accept: %v", err)
	}
}

func reviewGateCountDeliveries(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_delivery'`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func reviewGateInstancePin(t *testing.T, s *Store, workID string) (int64, string) {
	t.Helper()
	var version int64
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&version, &digest); err != nil {
		t.Fatal(err)
	}
	return version, digest
}

func reviewGateCorrectionPayload(evidence string) json.RawMessage {
	return json.RawMessage(`{"diagnosis":"the repaired result has no fresh accepted review","strategy":"return to the repair step and dispatch a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["` + evidence + `"]}`)
}

func TestPinnedDeliveryGateRecoversThroughEvidenceBearingReturn(t *testing.T) {
	const workID = "pinned-delivery-recovery"
	ctx := context.Background()
	released, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13)
	if !ok {
		t.Fatal("workflow.break_fix v13 is not registered")
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, released, "repair")
	s := fixture.store

	at := int64(100)
	refineEpoch := reviewGateDriveRejection(t, fixture, workID, &at)
	repairedAttempt := "attempt:" + workID + ":repair-1"
	ownerRef, ownerErr := WorkflowActorRef(fixture.owner)
	if ownerErr != nil {
		t.Fatal(ownerErr)
	}
	reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", refineEpoch, reviewGateLane(t, "implementation"), ownerRef, at)

	// The historical crossing replays unchanged: the accept advances the
	// pinned instance into the gate with the review debt outstanding.
	reviewGateSeedAcceptAtRefine(t, s, workID, repairedAttempt, refineEpoch, reviewGateAcceptor(workID))
	reviewGateRequireStep(t, s, workID, "delivery")

	// Released definition content is unchanged: the instance stays pinned to
	// the closed-gate version it shipped on.
	pinnedVersion, pinnedDigest := reviewGateInstancePin(t, s, workID)
	if pinnedVersion != 13 || pinnedDigest != released.Digest {
		t.Fatalf("pinned definition = v%d %s, want v13 %s", pinnedVersion, pinnedDigest, released.Digest)
	}

	// No delivery assertion exists: the parked change never claimed a merge.
	if count := reviewGateCountDeliveries(t, s, workID); count != 0 {
		t.Fatalf("parked gate carries %d delivery assertions, want none", count)
	}

	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") || workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
		t.Fatalf("pinned pin offered an advance behind an unreviewed repair: %#v", pin.NextValidIntents)
	}
	returnIntent := ""
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "request_correction" {
			returnIntent = intent.ReasonCode
		}
	}
	if returnIntent != "delivery_gate_correction" {
		t.Fatalf("pinned gate correction intent = %q, want delivery_gate_correction", returnIntent)
	}
	if pin.Correction != nil {
		t.Fatalf("consumed rejection leaked into the pin correction: %#v", pin.Correction)
	}

	// The gate refuses the unreviewed delivery exit.
	if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err == nil {
		t.Fatal("pinned gate delivery exit was admitted without a fresh review")
	}

	// Stale evidence: the return names a reference nothing durably bound.
	stale := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:not-bound"), fixture.owner, fixture.operator)
	var staleFailure *Failure
	if stale == nil || !failureAs(stale, &staleFailure) || staleFailure.Kind != KindMissingEvidence {
		t.Fatalf("unbound-evidence return = %v, want a missing-evidence refusal", stale)
	}

	// Unauthenticated: without the operator identity the return refuses.
	if err := runCorrectionActionWithoutOperator(s, workID, fixture.owner, reviewGateCorrectionPayload("evidence:return-route-verification")); err == nil {
		t.Fatal("correction return without an operator identity was admitted")
	}

	// The evidence-bearing corrective return returns the pinned instance to
	// the repair step for a fresh review.
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("evidence-bearing corrective return: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "repair")

	// The return asserted no delivery.
	if count := reviewGateCountDeliveries(t, s, workID); count != 0 {
		t.Fatalf("corrective return recorded %d delivery assertions, want none", count)
	}

	// The consumed gate correction does not re-fire: without a non-ok
	// verdict, the correction request at the repair step is unsupported.
	repeat := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator)
	if repeat == nil || !strings.Contains(repeat.Error(), "non-ok verification verdict") {
		t.Fatalf("repeat return = %v, want an unsupported-correction refusal", repeat)
	}

	// A terminal instance refuses the corrective return outright.
	version := verdictItemVersion(t, s, workID)
	cancelPayload, err := json.Marshal(map[string]any{"from": "in_progress", "to": "cancelled", "reason": "abandoned", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "review-gate-cancel-" + workID, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "operator", OccurredAt: time.Unix(300, 0).UTC(), PayloadVersion: 1, Payload: cancelPayload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	terminal := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator)
	if terminal == nil || !strings.Contains(terminal.Error(), "terminal workflow instance is immutable") {
		t.Fatalf("terminal return = %v, want the terminal refusal", terminal)
	}
}

// TestDeliveryGateCorrectionNeedsOutstandingReview proves the gate correction
// is unsupported without the post-rejection shape: a normally reviewed change
// parked on the gate offers record_delivery, not a corrective return.
func TestDeliveryGateCorrectionNeedsOutstandingReview(t *testing.T) {
	const workID = "pinned-delivery-no-debt"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	review := reviewGateLane(t, "review")
	at := int64(100)

	repairEpoch := reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	repairAttempt := "attempt:" + workID + ":repair"
	reviewGateRunAttempt(t, s, workID, repairAttempt, "repair", repairEpoch, reviewGateLane(t, "implementation"), ownerRef, at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}

	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	reviewAttempt := "attempt:" + workID + ":review"
	reviewGateRunAttempt(t, s, workID, reviewAttempt, "refine", refineEpoch, review, ownerRef, at)
	if err := reviewGateAcceptResult(t, s, workID, reviewAttempt, refineEpoch, acceptor); err != nil {
		t.Fatalf("accept review: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")

	pin, pinErr := ReadWorkPin(context.Background(), s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "request_correction" {
			t.Fatalf("gate without a post-rejection review offered request_correction: %#v", pin.NextValidIntents)
		}
	}
	if !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("reviewed gate lost its delivery exit: %#v", pin.NextValidIntents)
	}
	err = runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "outstanding post-rejection review") {
		t.Fatalf("unsupported gate return = %v, want the outstanding-review refusal", err)
	}
}
