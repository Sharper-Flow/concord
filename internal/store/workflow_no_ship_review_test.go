package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// reviewLaneVerdictCompleteEvent builds one worker.completed fixture event
// whose typed review block carries the named verdict, the shape a review lane
// bound to the CD-0197 report contract completes with.
func reviewLaneVerdictCompleteEvent(workID, eventID, attemptID string, lane LaneDefinition, verdict string, occurredAt time.Time, directory string) Event {
	payload := WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		EvidenceOrigin:  WorkerEvidenceLegacyUnavailable,
		WorkerDirectory: directory,
		Review: &WorkerReviewBlock{Disposition: verdict, Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high",
			Detail: "the delivered result does not satisfy the contract's outcome predicates",
		}}},
	}
	return Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: occurredAt, PayloadVersion: 3, Payload: mustJSONValue(payload)}
}

// reviewGateRunAttemptWithVerdict records the attempt's dispatch_worker
// authorization, worker.dispatched evidence, and a completed report whose
// typed review block carries the named verdict; it returns the attempt's
// recorded authorization epoch, which later accepts and rejects must name.
func reviewGateRunAttemptWithVerdict(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition, ownerRef, verdict string, at int64) int64 {
	t.Helper()
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, "refine", ownerRef)
	// The authorization just opened the step's newest action start, so the
	// latest start epoch is this attempt's recorded authorization epoch.
	epoch := latestStepStartEpoch(t, s, workID, "refine")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-verdict-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		reviewLaneVerdictCompleteEvent(workID, "review-verdict-completed-"+attemptID, attemptID, lane, verdict, time.Unix(at+1, 0).UTC(), workerFixtureDirectory(t, s, workID)),
	}}); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func TestBlockingReviewDispositionAcceptsWithoutAdvancing(t *testing.T) {
	for _, disposition := range []string{"correction_required", "revision_required"} {
		t.Run(disposition, func(t *testing.T) {
			j, _ := descendantReviewFixture(t, "review-hold-"+disposition)
			step := currentStep(t, j.s, j.workID)
			attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, disposition)
			pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") || !workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
				t.Fatalf("report intake and delivery permission were conflated: %+v", pin.NextValidIntents)
			}
			before := j.effects(t)
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch + 1}, j.fixture.owner); err == nil {
				t.Fatal("a stale attempt epoch accepted the report")
			}
			if after := j.effects(t); after != before {
				t.Fatal("refused acceptance changed durable state")
			}
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch, "delivery_artifact": "artifact:blocked-review", "delivery_state": "asserted"}, j.fixture.owner); err == nil {
				t.Fatal("acceptance of a blocking report carried a delivery assertion")
			}
			if after := j.effects(t); after != before {
				t.Fatal("refused delivery-bearing acceptance changed durable state")
			}
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
				t.Fatalf("Main could not accept a blocking disposition: %v", err)
			}
			pin, err = ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if currentStep(t, j.s, j.workID) != step || pin.Review.Accepted == nil || pin.Review.Accepted.Report.Disposition != disposition {
				t.Fatalf("acceptance lost the disposition or advanced execution: %+v", pin.Review)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") || workPinContainsAction(pin.NextValidIntents, "accept_worker_result") || !workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
				t.Fatalf("accepted report lost its hold, once-only intake, or recovery route: %+v", pin.NextValidIntents)
			}
			before = j.effects(t)
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err == nil {
				t.Fatal("the same result acquired another disposition")
			}
			if after := j.effects(t); after != before {
				t.Fatal("duplicate acceptance changed durable state")
			}
			acceptCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
			verification := j.observeRun(t, "review-ready-delivery")
			if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": verification.OperationRef}, j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			pin, err = ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if err := workflowAdmitReview(*pin.Review, ""); err != nil {
				t.Fatalf("accepted readiness did not satisfy the Review boundary: %v", err)
			}
			// This fixture also holds a separate, unfinished assignment. Review
			// readiness cannot settle it; the full delivery spine covers the exit
			// after all assignments and the other prerequisites are satisfied.
			before = j.effects(t)
			deliveryErr := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:review-ready", "delivery_state": "asserted"}, j.fixture.owner)
			if !hasFailureKind(deliveryErr, KindMissingEvidence) || !strings.Contains(deliveryErr.Error(), "every recorded worker job satisfied") || j.effects(t) != before {
				t.Fatalf("readiness bypassed the unfinished assignment or changed state: %v", deliveryErr)
			}
		})
	}
}

func TestPreflightAndMutationRequireAcceptedReadyDisposition(t *testing.T) {
	j, _ := descendantReviewFixture(t, "review-disposition-preflight")
	acceptCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	before := j.effects(t)
	deliveryPayload := json.RawMessage(`{"delivery_artifact":"artifact:review-preflight","delivery_state":"asserted"}`)
	preflightErr := WorkflowActionPreflightWithRegistry(t.Context(), j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: j.workID, ExpectedVersion: readWorkVersion(t, j.s, j.workID), ActionID: "record_delivery", Payload: deliveryPayload, Actor: j.fixture.owner})
	var failure *Failure
	if preflightErr == nil || !failureAs(preflightErr, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "ready_for_final_proof") || failure.RecoveryAction == "" {
		t.Fatalf("preflight lost the readiness requirement or recovery: %v", preflightErr)
	}
	deliveryErr := j.action(t, "record_delivery", deliveryPayload, j.fixture.owner)
	var deliveryFailure *Failure
	if deliveryErr == nil || !failureAs(deliveryErr, &deliveryFailure) || deliveryFailure.Kind != failure.Kind || deliveryFailure.Detail != failure.Detail || deliveryFailure.RecoveryAction != failure.RecoveryAction {
		t.Fatalf("preflight and execution disagree: preflight=%v execution=%v", preflightErr, deliveryErr)
	}
	pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") || j.effects(t) != before {
		t.Fatal("refused delivery was advertised or changed durable state")
	}
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
	reviewGateRunAttemptWithVerdict(t, s, workID, shipReview, reviewGateLane(t, "review"), ownerRef, "ship", at)

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

	reviewGateDriveRejection(t, fixture, workID, &at)

	// A fresh review completes with a no_ship verdict, and the coordinator
	// accepts it: the acceptance binds the findings and settles nothing
	// (CD-0201 D3). The refinement step stays current on the released pin.
	noShipReview := "attempt:" + workID + ":review-2"
	noShipEpoch := reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, noShipReview, noShipEpoch, acceptor); err != nil {
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
	shipEpoch := reviewGateRunAttemptWithVerdict(t, s, workID, shipReview, reviewGateLane(t, "review"), ownerRef, "ship", at)
	at += 2
	if err := acceptRefineResult(t, s, workID, shipReview, shipEpoch, acceptor); err != nil {
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

	reviewGateDriveRejection(t, fixture, workID, &at)

	// A fresh review completes with a no_ship verdict, and the pre-correction
	// history records its acceptance as the advancing accept: appended here
	// as the completion that code wrote, with no recorded hold field.
	noShipReview := "attempt:" + workID + ":review-2"
	noShipEpoch := reviewGateRunAttemptWithVerdict(t, s, workID, noShipReview, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2
	acceptorRef, acceptorRefErr := WorkflowActorRef(reviewGateAcceptor(workID))
	if acceptorRefErr != nil {
		t.Fatal(acceptorRefErr)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "no-ship-parked-gate-accept", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: acceptorRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "refine", "action_id": "accept_worker_result", "attempt_epoch": noShipEpoch, "worker_attempt_id": noShipReview,
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
