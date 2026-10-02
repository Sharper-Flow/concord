package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The CON-796 shape: a review report carries a typed verdict (CD-0197), and a
// fresh accepted review settles post-rejection review debt only when its
// verdict is ship or absent. A no_ship review binds its findings and leaves
// the debt outstanding, so refine does not advance to delivery and a parked
// delivery gate keeps admitting the evidence-bearing corrective return.

// reviewLaneVerdictCompleteEvent builds one worker.completed fixture event
// whose typed review block carries the named verdict, the shape a review lane
// bound to the CD-0197 report contract completes with.
func reviewLaneVerdictCompleteEvent(workID, eventID, attemptID string, lane LaneDefinition, verdict string, occurredAt time.Time) Event {
	payload := WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin: WorkerEvidenceLegacyUnavailable,
		Review: &WorkerReviewBlock{Verdict: verdict, Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high",
			Detail: "the delivered result does not satisfy the contract's outcome predicates",
		}}},
	}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: occurredAt, PayloadVersion: 3, Payload: mustJSONValue(payload)}
}

// reviewGateRunAttemptWithVerdict records the lane dispatch, its
// dispatch_worker action completion, and a completed report whose typed review
// block carries the named verdict.
func reviewGateRunAttemptWithVerdict(t *testing.T, s *Store, workID, attemptID, stepID string, epoch int64, lane LaneDefinition, ownerRef, verdict string, at int64) {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-verdict-dispatch-action-" + attemptID, Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": stepID, "action_id": "dispatch_worker", "attempt_epoch": epoch, "worker_attempt_id": attemptID,
			"actor_ref": ownerRef,
		}),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-verdict-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		reviewLaneVerdictCompleteEvent(workID, "review-verdict-completed-"+attemptID, attemptID, lane, verdict, time.Unix(at+1, 0).UTC()),
	}}); err != nil {
		t.Fatal(err)
	}
}

// TestNoShipReviewKeepsRefineExitAndGateCorrectionHonest reproduces CON-796
// on the current break-fix definition: after a rejection, a fresh review
// completes with a no_ship verdict, and its acceptance must neither settle the
// review debt nor advance the refinement step toward delivery. The combined
// accept — the only accept the delivery-admitting refinement step takes —
// refuses, the pin keeps hiding the delivery exits, and the completed review
// result keeps its rejection route, so the item waits for a ship review with
// every recovery route intact.
func TestNoShipReviewKeepsRefineExitAndGateCorrectionHonest(t *testing.T) {
	const workID = "no-ship-review-current"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	at := int64(100)

	refineEpoch := reviewGateDriveRejection(t, fixture, workID, &at)

	// A fresh review of the repaired result completes with a no_ship verdict.
	noShipReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, "refine", refineEpoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2

	// The combined accept of the no_ship review refuses: a review that
	// refused the result cannot carry the delivery assertion off refine
	// (CD-0201 D3).
	err = acceptRefineResult(t, s, workID, noShipReview, refineEpoch, acceptor)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, "fresh accepted review") {
		t.Fatalf("combined accept of the no_ship review = %v, want a fresh-review refusal", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")

	// The debt is still outstanding, so the pin keeps hiding the delivery
	// exits and does not offer the accept: the ready no_ship review settles
	// nothing, so its acceptance carries no delivery assertion.
	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("pin offered record_delivery behind a no_ship review: %#v", pin.NextValidIntents)
	}
	if workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
		t.Fatalf("pin offered the accept of a ready no_ship review: %#v", pin.NextValidIntents)
	}
	// The review refused the result, and its findings keep their typed route:
	// the pin advertises the rejection that preserves them in the correction.
	if !workPinContainsAction(pin.NextValidIntents, "reject_worker_result") {
		t.Fatalf("pin hid the no_ship review's rejection route: %#v", pin.NextValidIntents)
	}

	// The review debt stays outstanding, so an item later parked on the
	// delivery gate keeps the evidence-bearing corrective return: the
	// settlement query reads no accepted ship review behind the rejection.
	definition, defErr := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	outstanding, outstandingErr := workflowPostRejectionReviewOutstanding(ctx, s.db, workID, definition.Definition, "workflow_no_ship_review_test")
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if !outstanding {
		t.Fatal("the no_ship review settled the post-rejection review debt")
	}
}

// TestNoShipReviewLeavesParkedGateCorrectionAdmitted reproduces the CON-796
// parked-gate half on the released closed-gate version: the coordinator
// accepts the fresh no_ship review — the acceptance binds its findings and
// settles nothing — so the refinement step keeps its debt, and an item later
// parked on the delivery gate keeps admitting the evidence-bearing corrective
// return.
func TestNoShipReviewLeavesParkedGateCorrectionAdmitted(t *testing.T) {
	const workID = "no-ship-review-parked"
	released, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13)
	if !ok {
		t.Fatal("workflow.break_fix v13 is not registered")
	}
	fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, released, "repair", []string{"verification"}, []string{"verification", "review", "artifact"})
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	at := int64(100)

	refineEpoch := reviewGateDriveRejection(t, fixture, workID, &at)

	// A fresh review completes with a no_ship verdict, and the coordinator
	// accepts it: the acceptance stands, binds the findings, and settles
	// nothing (CD-0201 D3). On the released version the accept itself is the
	// advancing action, so the accepted no_ship review crosses the item onto
	// the closed gate — the CON-796 sequence itself.
	noShipReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, "refine", refineEpoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, noShipReview, refineEpoch, acceptor); err != nil {
		t.Fatalf("accept of the no_ship review at the released refine = %v, want the accepted-no_ship history", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
	ctx := context.Background()

	// The accepted no_ship review settled none of the debt, so the parked
	// gate keeps hiding its delivery exit and keeps admitting the
	// evidence-bearing corrective return.
	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("pin offered record_delivery behind the accepted no_ship review: %#v", pin.NextValidIntents)
	}
	if !workPinContainsAction(pin.NextValidIntents, "request_correction") {
		t.Fatalf("pin hid the parked gate's corrective return behind a no_ship review: %#v", pin.NextValidIntents)
	}

	if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("evidence-bearing corrective return behind a no_ship review: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "repair")
}
