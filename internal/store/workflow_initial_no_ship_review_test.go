package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func initialReviewFixture(t *testing.T, workID, ref string, version int64) (workflowReturnRouteFixture, string, int64) {
	t.Helper()
	f := seedHistoricalWorkflowReturnRouteFixture(t, workID, ref, version, "refine")
	ownerRef, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	epoch := reviewGateStartStep(t, f.store, workID, "refine", "start_refine", f.owner)
	return f, ownerRef, epoch
}

func TestInitialNoShipReviewHoldsDeliveryBehindTheObligation(t *testing.T) {
	// The live findings-binding accept of a negative review exists on the
	// historical pins, where a plain accept exits the refinement step.
	// Current pins exit refine only through an admitted delivery assertion,
	// so a negative verdict there takes the rejection route instead.
	for _, pin := range []struct {
		ref         string
		version     int64
		target      string
		startAction string
	}{{"workflow.implementation", 19, "execution", "start_execution"}, {"workflow.break_fix", 13, "repair", "start_repair"}} {
		t.Run(fmt.Sprintf("%s-v%d", pin.ref, pin.version), func(t *testing.T) {
			const workID = "initial-no-ship-gate"
			f, ownerRef, epoch := initialReviewFixture(t, workID, pin.ref, pin.version)
			s := f.store
			definition := mustDefinitionForPin(t, pin.ref, pin.version)
			attemptID := "attempt:" + workID + ":review"
			reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", 100)
			outstanding, err := workflowPostRejectionReviewOutstanding(context.Background(), s.db, workID, definition, "initial_review_test")
			if err != nil || outstanding {
				t.Fatalf("the lane report alone opened a coordinator obligation: outstanding=%v, err=%v", outstanding, err)
			}
			if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("record_delivery crossed the pending negative review")
			}
			wrong := json.RawMessage(fmt.Sprintf(`{"attempt_id":"attempt:not-ready","attempt_epoch":%d}`, epoch))
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", wrong, 0, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("an unrelated attempt rode the negative review's findings-binding route")
			}
			asserted := json.RawMessage(fmt.Sprintf(`{"attempt_id":%q,"attempt_epoch":%d,"delivery_artifact":"artifact:initial-review","delivery_state":"asserted"}`, attemptID, epoch))
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", asserted, 0, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("negative review acceptance carried a delivery assertion")
			}
			refineProofSeedGreenRun(t, s, workID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
			if err := reviewGateAcceptResult(t, s, workID, attemptID, epoch, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("accept the completed negative review: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
			outstanding, err = workflowPostRejectionReviewOutstanding(context.Background(), s.db, workID, definition, "initial_review_test")
			if err != nil || !outstanding {
				t.Fatalf("accepted negative review opened no obligation: outstanding=%v, err=%v", outstanding, err)
			}
			pinState, err := ReadWorkPin(context.Background(), s, workID)
			if err != nil || !workPinContainsAction(pinState.NextValidIntents, "request_correction") || workPinContainsAction(pinState.NextValidIntents, "record_delivery") {
				t.Fatalf("historical negative-review pin lost the corrective return: %#v, err=%v", pinState.NextValidIntents, err)
			}
			if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("delivery behind the accepted negative review was admitted")
			}
			if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), f.owner, f.operator); err != nil {
				t.Fatalf("typed correction behind the accepted negative review: %v", err)
			}
			reviewGateRequireStep(t, s, workID, pin.target)
			repairEpoch := reviewGateStartStep(t, s, workID, pin.target, pin.startAction, f.owner)
			repairAttempt := "attempt:" + workID + ":repair"
			reviewGateRunAttempt(t, s, workID, repairAttempt, pin.target, repairEpoch, reviewGateLane(t, "implementation"), ownerRef, 110)
			if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("accept the repaired result: %v", err)
			}
			refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", f.owner)
			settling := "attempt:" + workID + ":settling"
			reviewGateRunAttemptWithVerdict(t, s, workID, settling, refineEpoch, reviewGateLane(t, "review"), ownerRef, "ship", 120)
			refineProofSeedGreenRun(t, s, workID, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
			if err := acceptRefineResult(t, s, workID, settling, refineEpoch, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("fresh settling review did not resolve the obligation: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
			if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("settled gate lost its delivery route: %v", err)
			}
		})
	}
}

func mustDefinitionForPin(t *testing.T, ref string, version int64) WorkflowDefinition {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("missing definition %s v%d", ref, version)
	}
	return registered.Definition
}

func initialReviewHistoricalAccept(t *testing.T, f workflowReturnRouteFixture, workID, attemptID string, epoch int64) {
	t.Helper()
	version := verdictItemVersion(t, f.store, workID)
	acceptor := reviewGateAcceptor(workID)
	acceptorRef, err := WorkflowActorRef(acceptor)
	if err != nil {
		t.Fatal(err)
	}
	actor := workflowEventWithActor("initial-no-ship-acceptor", WorkflowActorRecorded, workID, acceptorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": acceptorRef, "principal_ref": acceptor.PrincipalRef, "client_ref": acceptor.ClientRef,
		"agent_ref": acceptor.AgentRef, "session_ref": acceptor.SessionRef, "actor_class": string(acceptor.ActorClass),
	})
	if err := applyWorkflowTestOperation(context.Background(), f.store, Operation{Events: []Event{actor, {
		EventID: "initial-no-ship-historical-accept", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: acceptorRef, OccurredAt: time.Unix(102, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(map[string]any{
			"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2,
			"step_id": "refine", "action_id": "accept_worker_result", "attempt_epoch": epoch,
			"worker_attempt_id": attemptID, "actor_ref": acceptorRef,
		}),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, f.store, workID, "delivery")
}

func TestInitialNoShipHistoricalDeliveryKeepsTypedCorrection(t *testing.T) {
	const workID = "initial-no-ship-delivery"
	f, ownerRef, epoch := initialReviewFixture(t, workID, "workflow.implementation", 19)
	s := f.store
	attemptID := "attempt:" + workID + ":review"
	reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", 100)
	initialReviewHistoricalAccept(t, f, workID, attemptID, epoch)
	var rejections int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result'`, workID, WorkflowActionCompleted).Scan(&rejections); err != nil || rejections != 0 {
		t.Fatalf("fixture must contain no coordinator rejection: count=%d, err=%v", rejections, err)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "request_correction") || workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("historical negative-review pin = %#v, err=%v", pin, err)
	}
	correction, err := workflowDeliveryGateCorrectionContext(context.Background(), s.db, workID, mustDefinitionForPin(t, "workflow.implementation", 19), "delivery", "initial_review_test")
	if err != nil || correction == nil || correction.Disposition != "verification" {
		t.Fatalf("negative findings misclassified or missing: %#v, err=%v", correction, err)
	}
	payload := reviewGateCorrectionPayload("evidence:return-route-verification")
	if err := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: verdictItemVersion(t, s, workID), ActionID: "request_correction", Payload: payload, Actor: f.owner}); err != nil {
		t.Fatalf("preflight hid the corrective return: %v", err)
	}
	missing := reviewGateCorrectionPayload("evidence:not-bound")
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", missing, f.owner, f.operator); err == nil {
		t.Fatal("correction accepted unbound evidence")
	}
	if err := runVerdictActionAs(t, s, workID, "request_correction", payload, 0, f.owner); err == nil {
		t.Fatal("correction accepted no operator identity")
	}
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, f.owner, f.operator); err != nil {
		t.Fatalf("typed correction after accepted no_ship with no coordinator rejection: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "execution")
}

func TestInitialSettlingReviewDoesNotOpenHistoricalDeliveryCorrection(t *testing.T) {
	for _, verdict := range []string{"ship", ""} {
		t.Run(verdict, func(t *testing.T) {
			const workID = "initial-settling-review"
			f, ownerRef, epoch := initialReviewFixture(t, workID, "workflow.implementation", 19)
			attemptID := "attempt:" + workID + ":review"
			if verdict == "" {
				reviewGateRunAttempt(t, f.store, workID, attemptID, "refine", epoch, reviewGateLane(t, "review"), ownerRef, 100)
			} else {
				reviewGateRunAttemptWithVerdict(t, f.store, workID, attemptID, epoch, reviewGateLane(t, "review"), ownerRef, verdict, 100)
			}
			initialReviewHistoricalAccept(t, f, workID, attemptID, epoch)
			pin, err := ReadWorkPin(context.Background(), f.store, workID)
			if err != nil || workPinContainsAction(pin.NextValidIntents, "request_correction") {
				t.Fatalf("ordinary delivery gained negative-review correction: %#v, err=%v", pin, err)
			}
			if err := runIssue933OperatorAction(t, f.store, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), f.owner, f.operator); err == nil {
				t.Fatal("settling review opened negative-review correction")
			}
		})
	}
}

func TestPendingNoShipReviewDoesNotOutliveAFreshSettlingShip(t *testing.T) {
	breakFix13, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13)
	if !ok {
		t.Fatal("missing break_fix v13")
	}
	implementation19, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 19)
	if !ok {
		t.Fatal("missing implementation v19")
	}
	for _, tc := range []struct {
		name       string
		registered RegisteredDefinition
	}{
		{"break_fix-current", mustBuiltinDefinition(t, "workflow.break_fix")},
		{"implementation-current", mustBuiltinDefinition(t, "workflow.implementation")},
		{"break_fix-v13", breakFix13},
		{"implementation-v19", implementation19},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const workID = "pending-no-ship-fresh-ship"
			fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, tc.registered, "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			at := int64(100)
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			pending := "attempt:" + workID + ":pending-no-ship"
			reviewGateRunAttemptWithVerdict(t, s, workID, pending, epoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
			at += 2
			settling := "attempt:" + workID + ":settling-ship"
			reviewGateRunAttemptWithVerdict(t, s, workID, settling, epoch, reviewGateLane(t, "review"), ownerRef, "ship", at)
			if err := reviewGateAcceptResult(t, s, workID, pending, epoch, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("an older pending no_ship rode ahead of the newer completed review")
			}
			if err := acceptRefineResult(t, s, workID, settling, epoch, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("fresh settling ship after a pending no_ship failed: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
			if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("pending no_ship stranded the delivery gate: %v", err)
			}
		})
	}
}

func TestRenewedNoShipObligationCarriesItsOwnEvidence(t *testing.T) {
	const workID = "renewed-no-ship-origin"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	at := int64(100)
	epoch := reviewGateDriveRejection(t, fixture, workID, &at)
	oldShip := "attempt:" + workID + ":old-ship"
	reviewGateRunAttemptWithVerdict(t, s, workID, oldShip, epoch, reviewGateLane(t, "review"), ownerRef, "ship", at)
	at += 2
	reviewGateSeedAcceptAtRefine(t, s, workID, oldShip, epoch, reviewGateAcceptor(workID))
	def := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	settled, err := workflowPostRejectionReviewOutstanding(context.Background(), s.db, workID, def, "initial_review_test")
	if err != nil || settled {
		t.Fatalf("the old rejection did not settle: outstanding=%v, err=%v", settled, err)
	}
	if err := reviewGateRecordDelivery(t, s, workID, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("settled gate lost its delivery route: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "verify")
	reviewer := checkpointReviewReviewer(t, s, workID)
	verdict := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", verdict, 0, reviewer); err != nil {
		t.Fatal(err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("typed correction after a settled rejection: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "repair")
	repairEpoch := reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	repairAttempt := "attempt:" + workID + ":repair-2"
	reviewGateRunAttempt(t, s, workID, repairAttempt, "repair", repairEpoch, reviewGateLane(t, "implementation"), ownerRef, at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("accept the repaired result: %v", err)
	}
	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	noShip := "attempt:" + workID + ":later-no-ship"
	reviewGateRunAttemptWithVerdict(t, s, workID, noShip, refineEpoch, reviewGateLane(t, "review"), ownerRef, "no_ship", at)
	at += 2
	reviewGateSeedAcceptAtRefine(t, s, workID, noShip, refineEpoch, reviewGateAcceptor(workID))
	renewed, err := workflowPostRejectionReviewOutstanding(context.Background(), s.db, workID, def, "initial_review_test")
	if err != nil || !renewed {
		t.Fatalf("later no_ship did not renew the obligation: outstanding=%v, err=%v", renewed, err)
	}
	correction, err := workflowDeliveryGateCorrectionContext(context.Background(), s.db, workID, def, "delivery", "initial_review_test")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.Disposition != "verification" || correction.FailedAttemptID != "" {
		t.Fatalf("renewed no_ship origin lost its own provenance: %#v", correction)
	}
}

func TestAcceptedNonReviewReportDoesNotOpenReviewObligation(t *testing.T) {
	const workID = "non-review-negative-report"
	f, ownerRef, epoch := initialReviewFixture(t, workID, "workflow.implementation", 19)
	s := f.store
	attemptID := "attempt:" + workID + ":implement"
	reviewGateRunAttemptWithVerdict(t, s, workID, attemptID, epoch, reviewGateLane(t, "implementation"), ownerRef, "no_ship", 100)
	initialReviewHistoricalAccept(t, f, workID, attemptID, epoch)
	seq, raw, err := workflowRefinementReviewFailure(context.Background(), s.db, workID, mustDefinitionForPin(t, "workflow.implementation", 19), "initial_review_test")
	if err != nil || seq != 0 || raw != nil {
		t.Fatalf("non-review capability gained review authority: seq=%d, err=%v", seq, err)
	}
}

func TestOutOfOrderCompletionRetiresTheReadyReview(t *testing.T) {
	breakFix13, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13)
	if !ok {
		t.Fatal("missing break_fix v13")
	}
	implementation19, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 19)
	if !ok {
		t.Fatal("missing implementation v19")
	}
	for _, pin := range []struct {
		name       string
		registered RegisteredDefinition
	}{
		{"break_fix-current", mustBuiltinDefinition(t, "workflow.break_fix")},
		{"implementation-current", mustBuiltinDefinition(t, "workflow.implementation")},
		{"break_fix-v13", breakFix13},
		{"implementation-v19", implementation19},
	} {
		t.Run(pin.name, func(t *testing.T) {
			const workID = "out-of-order-completion"
			f := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, pin.registered, "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
			s := f.store
			ownerRef, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			epoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", f.owner)
			lane := reviewGateLane(t, "review")
			// A delayed review dispatches before the rejection, but its
			// report completes after the ready review: the newest completion
			// is pre-frontier evidence the ready fold refuses to name.
			delayed := "attempt:" + workID + ":delayed"
			version := verdictItemVersion(t, s, workID)
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
				EventID: "out-of-order-delayed-action", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(100, 0).UTC(), PayloadVersion: 2,
				Payload: mustJSONValue(map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": "refine", "action_id": "dispatch_worker", "attempt_epoch": epoch, "worker_attempt_id": delayed, "actor_ref": ownerRef}),
			}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
				t.Fatal(err)
			}
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
				EventID: "out-of-order-delayed-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: ownerRef, OccurredAt: time.Unix(100, 0).UTC(), PayloadVersion: 2,
				Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: delayed, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
			}}}); err != nil {
				t.Fatal(err)
			}
			reviewGateRunAttemptWithVerdict(t, s, workID, "attempt:"+workID+":reject", epoch, lane, ownerRef, "no_ship", 102)
			reviewGateRejectResult(t, s, workID, "attempt:"+workID+":reject", epoch, reviewGateAcceptor(workID))
			ready := "attempt:" + workID + ":ready"
			reviewGateRunAttemptWithVerdict(t, s, workID, ready, epoch, lane, ownerRef, "no_ship", 104)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
				workerCompleteEventForLane(workID, "out-of-order-delayed-complete", delayed, lane, time.Unix(106, 0).UTC()),
			}}); err != nil {
				t.Fatal(err)
			}
			if err := reviewGateAcceptResult(t, s, workID, ready, epoch, reviewGateAcceptor(workID)); err == nil {
				t.Fatal("the ready review was accepted while a newer completed review existed")
			}
			fresh := "attempt:" + workID + ":fresh"
			reviewGateRunAttemptWithVerdict(t, s, workID, fresh, epoch, lane, ownerRef, "ship", 108)
			refineProofSeedGreenRun(t, s, workID, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
			if err := acceptRefineResult(t, s, workID, fresh, epoch, reviewGateAcceptor(workID)); err != nil {
				t.Fatalf("the newest completed review did not resolve the obligation: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
		})
	}
}
