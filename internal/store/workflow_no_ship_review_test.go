package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// The CON-796 shape: a review report carries a typed verdict (CD-0197), and a
// fresh accepted review settles post-rejection review debt only when its
// verdict is ship or absent. A no_ship review binds its findings and leaves
// the debt outstanding, so the delivery exits stay hidden and the parked
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
func reviewGateRunAttemptWithVerdict(t *testing.T, s *Store, workID, attemptID string, epoch int64, lane LaneDefinition, ownerRef, verdict string, at int64) {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-verdict-dispatch-action-" + attemptID, Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "refine", "action_id": "dispatch_worker", "attempt_epoch": epoch, "worker_attempt_id": attemptID,
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

// TestNoShipReviewKeepsRefineCurrentAndSettlingReviewAdvances reproduces
// CON-796 on the current break-fix definition: after a rejection, a fresh
// review completes with a no_ship verdict, and its acceptance binds the
// findings without settling the review debt. The accepted disposition is the
// non-delivery accept — a review that refused the result carries no delivery
// assertion — so the refinement step stays current: the advance waits for a
// settling review (CD-0201 D3), the accepted review's readiness clears, the
// pin keeps hiding the delivery exits while offering the fresh-review
// dispatch, and the settling review's acceptance carries the advance.
func TestNoShipReviewKeepsRefineCurrentAndSettlingReviewAdvances(t *testing.T) {
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
	reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, refineEpoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2

	// The delivery exits stay hidden while the refused result stands ready,
	// and the pin offers the accept whose identity the review gate binds.
	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("pin offered record_delivery behind a no_ship review: %#v", pin.NextValidIntents)
	}
	if !workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
		t.Fatalf("pin hid the ready no_ship review's acceptance: %#v", pin.NextValidIntents)
	}

	// The acceptance carrying a delivery assertion refuses: a review that
	// refused the result cannot carry the delivery assertion off refine
	// (CD-0201 D3).
	asserted := json.RawMessage(`{"attempt_id":"` + noShipReview + `","attempt_epoch":` + fmt.Sprint(refineEpoch) + `,"delivery_artifact":"artifact:no-ship-` + workID + `","delivery_state":"asserted"}`)
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", asserted, 0, acceptor); err == nil {
		t.Fatal("the no_ship accept carrying a delivery assertion was admitted")
	}

	// The accepted disposition of the no_ship review is the non-delivery
	// accept: it binds the findings, settles nothing, and keeps the
	// refinement step current — the advance waits for a settling review.
	if err := reviewGateAcceptResult(t, s, workID, noShipReview, refineEpoch, acceptor); err != nil {
		t.Fatalf("non-delivery accept of the no_ship review = %v, want the accepted findings-binding disposition", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")

	// The guard records its admission decision on the completion it authors,
	// and the fold honors the recorded field: the accepted attempt is the
	// ready non-settling review, so the completion carries the hold.
	var held bool
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.review_advance_held'),0) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, noShipReview).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("the guarded no_ship accept recorded no review advance hold")
	}

	// The acceptance dispositioned the review: the ready-review read returns
	// none, so no later accept can ride the dispositioned attempt's
	// identity.
	definition, defErr := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	readyAttempt, readyVerdict, readyErr := workflowReadyReviewAttemptTx(ctx, s.db, workID, definition.Definition, "workflow_no_ship_review_test")
	if readyErr != nil {
		t.Fatal(readyErr)
	}
	if readyAttempt != "" || readyVerdict != "" {
		t.Fatalf("the accepted no_ship review still stands ready: %q verdict %q", readyAttempt, readyVerdict)
	}

	// The debt is still outstanding, so the parked refinement step keeps
	// hiding the delivery exit, keeps hiding the dispositioned accept, and
	// keeps offering the fresh-review dispatch the advance waits behind.
	pin, pinErr = ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("pin offered record_delivery behind the accepted no_ship review: %#v", pin.NextValidIntents)
	}
	if workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
		t.Fatalf("pin offered an accept with no ready review: %#v", pin.NextValidIntents)
	}
	if !workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
		t.Fatalf("pin hid the fresh-review dispatch behind the accepted no_ship review: %#v", pin.NextValidIntents)
	}

	// The review debt stays outstanding: the settlement query reads no
	// accepted ship review behind the rejection.
	outstanding, outstandingErr := workflowPostRejectionReviewOutstanding(ctx, s.db, workID, definition.Definition, "workflow_no_ship_review_test")
	if outstandingErr != nil {
		t.Fatal(outstandingErr)
	}
	if !outstanding {
		t.Fatal("the no_ship review settled the post-rejection review debt")
	}

	// The wait resolves through the route the pin named: a fresh review of
	// the repaired result completes with a ship verdict, and its settling
	// acceptance carries the advance the no_ship acceptance withheld.
	shipReview := "attempt:" + workID + ":review-3"
	reviewGateRunAttemptWithVerdict(t, s, workID, shipReview, refineEpoch, reviewGateLane(t, "review"), ownerRef, "ship", at)
	at += 2
	if err := acceptRefineResult(t, s, workID, shipReview, refineEpoch, acceptor); err != nil {
		t.Fatalf("settling accept behind the accepted no_ship review: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

// TestReadyReviewAcceptCannotRideAnUnrelatedRefusal pins the deferral rule:
// the review gate's acceptance route belongs to the fresh-review refusal
// alone. Beside an unresolved Domain overlap — a refusal of another cause —
// the fold applies the overlap refusal to the ready settling review's own
// accept, and the accept neither succeeds nor returns the fresh-review
// refusal.
func TestReadyReviewAcceptCannotRideAnUnrelatedRefusal(t *testing.T) {
	const workID = "no-ship-review-overlap"
	const peerID = "no-ship-review-overlap-peer"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s, owner, operator := fixture.store, fixture.owner, fixture.operator
	defer s.Close()
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	// One shared Domain write on both sides makes the pair a real overlap,
	// and the peer's pin stays on the pre-rescan registry hash, so the
	// subject's boundary answers with the unresolved-overlap refusal that no
	// attempt-disposition admission class excuses. The pair forms only after
	// the rejection stands, so the debt and the overlap hold together.
	at := int64(100)
	refineEpoch := reviewGateDriveRejection(t, fixture, workID, &at)
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+workID+`',1,'root')`)
	driftStaleRegistryFixture(t, s)
	seedStaleRegistryRescanPeer(t, s, peerID, ownerRef)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", registryRepinSuccessorPayload(2, registryRescannedHash(), workID, 1, "root"), owner, operator); err != nil {
		t.Fatalf("subject re-pin to the current hash: %v", err)
	}

	shipReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttemptWithVerdict(t, s, workID, shipReview, refineEpoch, reviewGateLane(t, "review"), ownerRef, "ship", at)

	// The settling review's combined accept — the shape the review gate
	// admits when the fresh-review refusal is the only refusal — refuses on
	// the unresolved overlap, and the item stays parked at refine.
	err = acceptRefineResult(t, s, workID, shipReview, refineEpoch, reviewGateAcceptor(workID))
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("settling accept beside an unresolved overlap = %v, want the overlap refusal", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")
}

// TestReleasedPinKeepsNoShipAcceptRefineCurrent proves the corrected fold on
// a released definition version: the coordinator accepts the fresh no_ship
// review — the acceptance stands, binds its findings, and settles nothing —
// and the refinement step stays current on the closed-gate pin exactly as on
// the current one (CD-0201 D4: admission code is not definition content, so
// pinned instances receive the corrected admission on release). The advance
// still resolves through the settling review the step keeps admitting.
func TestReleasedPinKeepsNoShipAcceptRefineCurrent(t *testing.T) {
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
	// accepts it: the acceptance binds the findings and settles nothing
	// (CD-0201 D3). The refinement step stays current on the released pin.
	noShipReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, refineEpoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, noShipReview, refineEpoch, acceptor); err != nil {
		t.Fatalf("accept of the no_ship review at the released refine = %v, want the accepted-no_ship history", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")
	ctx := context.Background()

	// The accepted no_ship review settled none of the debt, so the parked
	// refinement step keeps hiding its delivery exit and keeps offering the
	// fresh-review dispatch the advance waits behind.
	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("pin offered record_delivery behind the accepted no_ship review: %#v", pin.NextValidIntents)
	}
	if !workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
		t.Fatalf("pin hid the fresh-review dispatch behind the accepted no_ship review: %#v", pin.NextValidIntents)
	}

	// The review debt stays outstanding: the settlement query reads no
	// accepted ship review behind the rejection.
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

	// The settling review resolves the wait the acceptance opened: its plain
	// accepted advance carries the released pin onto the delivery step.
	shipReview := "attempt:" + workID + ":review-3"
	reviewGateRunAttemptWithVerdict(t, s, workID, shipReview, refineEpoch, reviewGateLane(t, "review"), ownerRef, "ship", at)
	at += 2
	if err := acceptRefineResult(t, s, workID, shipReview, refineEpoch, acceptor); err != nil {
		t.Fatalf("settling accept on the released pin: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

// TestNoShipReviewLeavesParkedGateCorrectionAdmitted reproduces the CON-796
// parked-gate half on the released closed-gate version. The history the
// pre-correction code recorded carries the accepted no_ship review's
// advancing accept — a completion without the recorded hold field — and the
// fold advances recorded history as recorded, so the item stands parked on
// the delivery gate with the debt outstanding. The gate keeps hiding its
// delivery exit and keeps admitting the evidence-bearing corrective return,
// which returns the item to repair (CD-0201 D3).
func TestNoShipReviewLeavesParkedGateCorrectionAdmitted(t *testing.T) {
	const workID = "no-ship-review-parked-gate"
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
	at := int64(100)

	refineEpoch := reviewGateDriveRejection(t, fixture, workID, &at)

	// A fresh review completes with a no_ship verdict, and the pre-correction
	// history records its acceptance as the advancing accept: appended here
	// as the completion that code wrote, with no recorded hold field.
	noShipReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, refineEpoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2
	acceptorRef, acceptorRefErr := WorkflowActorRef(reviewGateAcceptor(workID))
	if acceptorRefErr != nil {
		t.Fatal(acceptorRefErr)
	}
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "no-ship-parked-gate-accept", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: acceptorRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "refine", "action_id": "accept_worker_result", "attempt_epoch": refineEpoch, "worker_attempt_id": noShipReview,
			"actor_ref": acceptorRef,
		}),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("replay of the recorded pre-correction accept: %v", err)
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

	if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("evidence-bearing corrective return behind a no_ship review: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "repair")
}
