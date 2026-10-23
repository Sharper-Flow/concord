package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The CD-0166 post-rejection review gate: a rejected refinement result must be
// covered by a fresh accepted review — dispatched after the rejection and
// after the repaired result's latest worker activity at the refinement step —
// before any advance toward delivery, and a parked gate cannot assert its way
// past the missing review.

func reviewGateLane(t *testing.T, capabilityClass string) LaneDefinition {
	t.Helper()
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.CapabilityClass == capabilityClass {
			return lane
		}
	}
	t.Fatalf("no builtin lane carries capability class %q", capabilityClass)
	return LaneDefinition{}
}

// reviewGateRunAttempt records the attempt's dispatch_worker authorization,
// worker.dispatched evidence, and completed report; it returns the attempt's
// recorded authorization epoch, which later accepts and rejects must name.
func reviewGateRunAttempt(t *testing.T, s *Store, workID, attemptID, stepID string, lane LaneDefinition, ownerRef string, at int64) int64 {
	t.Helper()
	recordWorkerFixtureDispatchAuthorization(t, s, workID, attemptID, stepID, ownerRef)
	// The authorization just opened the step's newest action start, so the
	// latest start epoch is this attempt's recorded authorization epoch.
	epoch := latestStepStartEpoch(t, s, workID, stepID)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-gate-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		workerCompleteEventForLane(workID, "review-gate-completed-"+attemptID, attemptID, lane, time.Unix(at+1, 0).UTC(), workerFixtureDirectory(t, s, workID)),
	}}); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func reviewGateStartStep(t *testing.T, s *Store, workID, stepID, startAction string, actor WorkflowActor) int64 {
	t.Helper()
	// CD-0198 D3: current-head fixtures derive the step start from the
	// step's qualifying checkpoint; released pins keep the callable start
	// the caller named. The pinned definition decides, so one helper serves
	// both sides of the derived-start boundary. The derivation pair — the
	// checkpoint that its own derived start opened — binds the fence from
	// the calling actor, exactly as the callable start did.
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	action := startAction
	if !definitionStepAllows(entry.Definition, stepID, startAction) {
		for candidate, step := range workflowDerivedStartCheckpoints {
			if step == stepID && definitionStepAllows(entry.Definition, stepID, candidate) {
				action = candidate
				break
			}
		}
	}
	if err := runVerdictActionAs(t, s, workID, action, json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("start %s at %s: %v", action, stepID, err)
	}
	return reviewGateStartedEpoch(t, s, workID, action)
}

func reviewGateStartedEpoch(t *testing.T, s *Store, workID, action string) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted, action).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func reviewGateRequireStep(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	if got := currentStep(t, s, workID); got != want {
		t.Fatalf("current step = %q, want %q", got, want)
	}
}

func reviewGateAcceptResult(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor) error {
	t.Helper()
	payload := json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `}`)
	return runVerdictActionAs(t, s, workID, "accept_worker_result", payload, 0, acceptor)
}

func reviewGateRecordDelivery(t *testing.T, s *Store, workID string, acceptor WorkflowActor) error {
	t.Helper()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:review-gate-` + workID + `","delivery_state":"asserted"}`)
	return runVerdictActionAs(t, s, workID, "record_delivery", payload, 0, acceptor)
}

func reviewGateRejectResult(t *testing.T, s *Store, workID, attemptID string, epoch int64, actor WorkflowActor) {
	t.Helper()
	payload := json.RawMessage(`{"attempt_id":"` + attemptID + `","attempt_epoch":` + fmt.Sprint(epoch) + `,"diagnosis":"the review rejects the refined result","strategy":"implement the review findings and resubmit","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", payload, 0, actor); err != nil {
		t.Fatalf("reject worker result %s: %v", attemptID, err)
	}
}

// reviewGateDriveRejection drives the repair pass, then the refinement pass
// whose review rejects, and returns the refinement step's start epoch.
func reviewGateDriveRejection(t *testing.T, fixture workflowReturnRouteFixture, workID string, at *int64) int64 {
	t.Helper()
	s := fixture.store
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	acceptor := reviewGateAcceptor(workID)
	implement := reviewGateLane(t, "implementation")
	review := reviewGateLane(t, "review")

	reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	repairAttempt := "attempt:" + workID + ":repair"
	repairEpoch := reviewGateRunAttempt(t, s, workID, repairAttempt, "repair", implement, ownerRef, *at)
	*at += 2
	if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}

	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	reviewAttempt := "attempt:" + workID + ":review-1"
	reviewEpoch := reviewGateRunAttempt(t, s, workID, reviewAttempt, "refine", review, ownerRef, *at)
	*at += 2
	reviewGateRejectResult(t, s, workID, reviewAttempt, reviewEpoch, acceptor)
	return refineEpoch
}

func reviewGateAcceptor(workID string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/review-gate-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
}

// TestPostRejectionReviewGateHoldsBreakFixDelivery drives the post-rejection review-gate shape
// on the current break-fix definition over both delivery entry routes: a
// rejected review, then the accepted implement repair the gate holds at
// refine until a fresh accepted review covers it. The record_delivery route
// exits refine through the standalone delivery call; the combined-accept
// route exits through an accept that carries its delivery fields (CD-0198
// D4). Both refuse on the same gate, and both settle only through the fresh
// review's combined accept, which is itself the fresh review.
func TestPostRejectionReviewGateHoldsBreakFixDelivery(t *testing.T) {
	for _, route := range deliveryEntryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			const workID = "review-gate-break-fix"
			ctx := context.Background()
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			acceptor := reviewGateAcceptor(workID)
			implement := reviewGateLane(t, "implementation")
			review := reviewGateLane(t, "review")
			at := int64(100)

			refineEpoch := reviewGateDriveRejection(t, fixture, workID, &at)

			// The repaired implement result's exit refuses while no fresh
			// accepted review covers it, on either route.
			repairedAttempt := "attempt:" + workID + ":repair-1"
			reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", implement, ownerRef, at)
			at += 2
			err = route.exit(t, s, workID, repairedAttempt, refineEpoch, acceptor, nil, true)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "fresh accepted review") {
				t.Fatalf("%s exit of the repaired result = %v, want a fresh-review refusal", route.name, err)
			}
			reviewGateRequireStep(t, s, workID, "refine")

			pin, pinErr := ReadWorkPin(ctx, s, workID)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") || workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
				t.Fatalf("pin offered an advance behind an unreviewed repair: %#v", pin.NextValidIntents)
			}

			// The correction request stays a verdict-shaped route: without a
			// recorded non-ok verdict the refinement step offers none.
			correction := json.RawMessage(`{"diagnosis":"the repaired result has no fresh accepted review","strategy":"dispatch a fresh review at the refinement step","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
			if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err == nil {
				t.Fatal("request_correction at refine was admitted without a non-ok verdict")
			}

			// A fresh accepted review of the repaired result clears the gate.
			// Its acceptance is itself the fresh review, so it rides the
			// combined accept on both routes and advances into the gate.
			freshReview := "attempt:" + workID + ":review-2"
			freshEpoch := reviewGateRunAttempt(t, s, workID, freshReview, "refine", review, ownerRef, at)
			at += 2
			if err := acceptRefineResult(t, s, workID, freshReview, freshEpoch, acceptor); err != nil {
				t.Fatalf("accept after fresh review refused: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")

			pin, pinErr = ReadWorkPin(ctx, s, workID)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
				t.Fatalf("fresh accepted review did not restore record_delivery: %#v", pin.NextValidIntents)
			}
			if err := reviewGateRecordDelivery(t, s, workID, acceptor); err != nil {
				t.Fatalf("gate crossing after fresh review refused: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "verify")
		})
	}
}

func TestDeliveryGateDeclaresCorrectiveReturn(t *testing.T) {
	for _, family := range []string{"workflow.implementation", "workflow.break_fix"} {
		entry := mustBuiltinDefinition(t, family)
		gate := workflowStep(entry.Definition, "delivery")
		if !workflowStepIsDeliveryGate(gate) || len(gate.Actions) != 4 || gate.Actions[0] != "record_delivery" || gate.Actions[1] != "request_correction" {
			t.Fatalf("%s gate actions=%v", family, gate.Actions)
		}
		if workflowCorrectionReturnTarget(entry.Definition, "delivery") == "" {
			t.Fatalf("%s gate has no producer return", family)
		}
	}
	// The registry exposes only the current gate as executable authority.
	if _, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13); ok {
		t.Fatal("retired closed gate remains registered")
	}
}

// TestPostRejectionReviewGateRefusesPreRejectionReviewAccept pins the
// dispatch-order rule on both delivery entry routes: a review attempt
// dispatched before the rejection settles nothing, so the refine exit that
// would carry it — the combined accept, or the standalone delivery call —
// refuses and the item stays parked at refine.
func TestPostRejectionReviewGateRefusesPreRejectionReviewAccept(t *testing.T) {
	for _, route := range deliveryEntryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			const workID = "review-gate-stale-review"
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			acceptor := reviewGateAcceptor(workID)
			implement := reviewGateLane(t, "implementation")
			review := reviewGateLane(t, "review")
			at := int64(100)

			reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
			repairAttempt := "attempt:" + workID + ":repair"
			repairEpoch := reviewGateRunAttempt(t, s, workID, repairAttempt, "repair", implement, ownerRef, at)
			at += 2
			if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, acceptor); err != nil {
				t.Fatalf("accept repair: %v", err)
			}

			refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			// Two review attempts dispatch before the rejection.
			earlyReview := "attempt:" + workID + ":review-early"
			reviewGateRunAttempt(t, s, workID, earlyReview, "refine", review, ownerRef, at)
			at += 2
			rejectedReview := "attempt:" + workID + ":review-rejected"
			rejectedEpoch := reviewGateRunAttempt(t, s, workID, rejectedReview, "refine", review, ownerRef, at)
			reviewGateRejectResult(t, s, workID, rejectedReview, rejectedEpoch, acceptor)

			// The refine exit behind the pre-rejection review refuses: a
			// review settles the debt only when its dispatch postdates the
			// rejection, and the pre-rejection attempt's acceptance is not
			// that review.
			err = route.exit(t, s, workID, earlyReview, refineEpoch, acceptor, nil, true)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "fresh accepted review") {
				t.Fatalf("%s exit behind the pre-rejection review = %v, want a fresh-review refusal", route.name, err)
			}
			reviewGateRequireStep(t, s, workID, "refine")
		})
	}
}

// TestPostRejectionReviewGateRefusesReviewDispatchedBeforeRepair pins the
// repair-tied fresh-review rule on the post-rejection defect shape over both entry
// routes: a review dispatched and completed after the rejection but before
// the repaired implement result settles nothing, so the refine exit behind
// it refuses; a review dispatched after the repaired result is the fresh
// one, and its combined acceptance is the exit that asserts the delivery and
// carries the gate.
func TestPostRejectionReviewGateRefusesReviewDispatchedBeforeRepair(t *testing.T) {
	for _, route := range deliveryEntryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			const workID = "review-gate-review-before-repair"
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

			// Review B dispatches and completes after the rejection; the
			// implement repair C dispatches and completes after B.
			earlyFreshReview := "attempt:" + workID + ":review-2"
			reviewGateRunAttempt(t, s, workID, earlyFreshReview, "refine", reviewGateLane(t, "review"), ownerRef, at)
			at += 2
			repairedAttempt := "attempt:" + workID + ":repair-1"
			reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", reviewGateLane(t, "implementation"), ownerRef, at)
			at += 2

			// The refine exit behind the review that predates the repaired
			// result refuses: it covers nothing the repair produced after it.
			err = route.exit(t, s, workID, earlyFreshReview, refineEpoch, acceptor, nil, true)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "fresh accepted review") {
				t.Fatalf("%s exit behind the review dispatched before the repair = %v, want a fresh-review refusal", route.name, err)
			}
			reviewGateRequireStep(t, s, workID, "refine")

			// With the debt outstanding and no completed settling review, the
			// pin offers no advance.
			pin, pinErr := ReadWorkPin(ctx, s, workID)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") || workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
				t.Fatalf("pin offered an advance behind an unreviewed repair: %#v", pin.NextValidIntents)
			}

			// A review dispatched and completed after the repaired result is
			// the fresh review. Before its acceptance the pin advertises
			// exactly that acceptance and still withholds the delivery exit.
			settlingReview := "attempt:" + workID + ":review-3"
			settlingEpoch := reviewGateRunAttempt(t, s, workID, settlingReview, "refine", reviewGateLane(t, "review"), ownerRef, at)
			at += 2
			pin, pinErr = ReadWorkPin(ctx, s, workID)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
				t.Fatalf("pin offered record_delivery behind an unaccepted review: %#v", pin.NextValidIntents)
			}
			if !workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
				t.Fatalf("pin hid the completed fresh review's acceptance: %#v", pin.NextValidIntents)
			}

			// The settling accept advances into the gate, and the gate
			// crossing admits the reviewed change.
			if err := acceptRefineResult(t, s, workID, settlingReview, settlingEpoch, acceptor); err != nil {
				t.Fatalf("accept after the repair-postdating review refused: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "delivery")
			if err := reviewGateRecordDelivery(t, s, workID, acceptor); err != nil {
				t.Fatalf("gate crossing after the settling review refused: %v", err)
			}
			reviewGateRequireStep(t, s, workID, "verify")
		})
	}
}

// TestCurrentDeliveryGateAdmitsEvidenceBearingReturn exercises an admitted
// request_correction on the current break-fix and implementation gates: a
// parked gate with an outstanding post-rejection review offers the
// evidence-bearing return, and the fold returns the item to the correction
// target step for a fresh review.
func TestCurrentDeliveryGateAdmitsEvidenceBearingReturn(t *testing.T) {
	for _, tc := range []struct {
		definitionRef string
		verdictStep   string
		startAction   string
		targetStep    string
	}{
		{definitionRef: "workflow.break_fix", verdictStep: "repair", startAction: "start_repair", targetStep: "repair"},
		{definitionRef: "workflow.implementation", verdictStep: "execution", startAction: "start_execution", targetStep: "execution"},
	} {
		t.Run(tc.definitionRef, func(t *testing.T) {
			const workID = "current-gate-return"
			ctx := context.Background()
			fixture := seedWorkflowReturnRouteFixture(t, workID, tc.definitionRef, tc.verdictStep)
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			acceptor := reviewGateAcceptor(workID)
			at := int64(100)

			reviewGateStartStep(t, s, workID, tc.verdictStep, tc.startAction, fixture.owner)
			workAttempt := "attempt:" + workID + ":work"
			workEpoch := reviewGateRunAttempt(t, s, workID, workAttempt, tc.verdictStep, reviewGateLane(t, "implementation"), ownerRef, at)
			at += 2
			if err := reviewGateAcceptResult(t, s, workID, workAttempt, workEpoch, acceptor); err != nil {
				t.Fatalf("accept %s result: %v", tc.verdictStep, err)
			}

			// A rejected refinement review, then the repaired implement
			// result whose accept seeds the parked gate position.
			reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			reviewAttempt := "attempt:" + workID + ":review-1"
			reviewEpoch := reviewGateRunAttempt(t, s, workID, reviewAttempt, "refine", reviewGateLane(t, "review"), ownerRef, at)
			at += 2
			reviewGateRejectResult(t, s, workID, reviewAttempt, reviewEpoch, acceptor)
			repairedAttempt := "attempt:" + workID + ":repair-1"
			repairedEpoch := reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", reviewGateLane(t, "implementation"), ownerRef, at)
			reviewGateSeedAcceptAtRefine(t, s, workID, repairedAttempt, repairedEpoch, acceptor)
			reviewGateRequireStep(t, s, workID, "delivery")

			// The instance stays pinned to the fixture's legacy-policy
			// version, whose gate declares the corrective return.
			current := legacyApprovalPolicyDefinitionForRef(t, tc.definitionRef)
			pinnedVersion, pinnedDigest := reviewGateInstancePin(t, s, workID)
			if pinnedVersion != current.Definition.Version || pinnedDigest != current.Digest {
				t.Fatalf("pinned definition = v%d %s, want v%d %s", pinnedVersion, pinnedDigest, current.Definition.Version, current.Digest)
			}

			// The pin advertises the corrective return, not the delivery exit.
			pin, pinErr := ReadWorkPin(ctx, s, workID)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") {
				t.Fatalf("parked gate offered record_delivery: %#v", pin.NextValidIntents)
			}
			returnIntent := ""
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					returnIntent = intent.ReasonCode
				}
			}
			if returnIntent != "delivery_gate_correction" {
				t.Fatalf("gate correction intent = %q, want delivery_gate_correction", returnIntent)
			}

			// The evidence-bearing return is admitted and returns the item to
			// the correction target step for a fresh review.
			if err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator); err != nil {
				t.Fatalf("evidence-bearing corrective return on the current gate: %v", err)
			}
			reviewGateRequireStep(t, s, workID, tc.targetStep)
			if count := reviewGateCountDeliveries(t, s, workID); count != 0 {
				t.Fatalf("corrective return recorded %d delivery assertions, want none", count)
			}
		})
	}
}

// TestCurrentDeliveryGateReturnsWhenSuccessorStalesAcceptedReview: a
// successor contract that changes a criterion leaves the accepted Review
// inapplicable, so the parked gate offers only the evidence-bearing return.
func TestCurrentDeliveryGateReturnsWhenSuccessorStalesAcceptedReview(t *testing.T) {
	for _, family := range []string{"workflow.break_fix", "workflow.implementation"} {
		t.Run(family, func(t *testing.T) {
			j := pReturnReviewFixture(t, "current-gate-return", family, "ready_for_final_proof")
			owner, err := WorkflowActorRef(j.fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			// Park at the gate, then change the contract through its fold owner.
			if err := advanceWorkflowTestInstanceToStep(t.Context(), j.s, j.workID, "delivery", owner); err != nil {
				t.Fatal(err)
			}
			successor := pContractSuccessor(t, j.s, j.workID, 2, "changed criterion")
			delete(successor, "audit_evidence")
			delete(successor, "supersede_reason")
			successor["outcome_kind"] = "check"
			predicates := successor["outcome_predicates"].([]any)
			predicates[0].(map[string]any)["outcome_payload"].(map[string]any)["expected_result"] = "fail"
			version := readWorkVersion(t, j.s, j.workID)
			successor["law_revisions"], successor["law_boundary_version"], successor["consequence_class"] = []WorkflowLawRevision{}, 1, "internal_sqlite"
			event := workflowEventWithActor("p-gate-successor", WorkflowContractSuperseded, j.workID, owner, map[string]any{"work_id": j.workID, "expected_version": version, "resulting_version": version + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "changed criterion", "audit_evidence": []string{"evidence:return-route-artifact"}, "successor_contract": successor})
			event.PayloadVersion = 1
			event = withPinnedDecisionFact(t, pinnedWorkflowDefinitionForTest(t, j.s, j.workID), event)
			if err := applyWorkflowTestOperation(t.Context(), j.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, j.workID): version}}); err != nil {
				t.Fatal(err)
			}
			pin := issue1013Pin(t, j.s, j.workID)
			if pin.Review == nil || pin.Review.DependenciesCurrent || issue1013HasIntent(pin, "record_delivery") {
				t.Fatalf("stale gate offered delivery: %+v", pin.NextValidIntents)
			}
			if !issue1013HasIntent(pin, "request_correction") {
				t.Fatalf("stale gate hides corrective return: %+v", pin.NextValidIntents)
			}
			var reason string
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "request_correction" {
					reason = intent.ReasonCode
				}
			}
			if reason != "delivery_gate_correction" {
				t.Fatalf("gate correction intent=%q, want delivery_gate_correction", reason)
			}
			payload := pReviewCorrectionPayload(t, j)
			deliveries := reviewGateCountDeliveries(t, j.s, j.workID)
			if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
				t.Fatalf("evidence-bearing current gate return: %v", err)
			}
			effect := "execution"
			if family == "workflow.break_fix" {
				effect = "repair"
			}
			reviewGateRequireStep(t, j.s, j.workID, effect)
			if count := reviewGateCountDeliveries(t, j.s, j.workID); count != deliveries {
				t.Fatalf("corrective return changed asserted deliveries from %d to %d", deliveries, count)
			}
		})
	}
}
