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

// reviewGateRunAttempt records the lane dispatch, its dispatch_worker action
// completion, and the completed report for one worker attempt. The dispatch
// carries the lane's capability class, which is how the review gate tells a
// review attempt from a repair attempt.
func reviewGateRunAttempt(t *testing.T, s *Store, workID, attemptID, stepID string, epoch int64, lane LaneDefinition, ownerRef string, at int64) {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-gate-dispatch-action-" + attemptID, Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
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
		EventID: "review-gate-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: ownerRef, OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "review-gate-completed-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(at+1, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion}),
	}}}); err != nil {
		t.Fatal(err)
	}
}

func reviewGateStartStep(t *testing.T, s *Store, workID, stepID, startAction string, actor WorkflowActor) int64 {
	t.Helper()
	if err := runVerdictActionAs(t, s, workID, startAction, json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("start %s at %s: %v", startAction, stepID, err)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionStarted, startAction).Scan(&epoch); err != nil {
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
// whose review rejects, and returns the refinement attempt epoch.
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

	repairEpoch := reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	repairAttempt := "attempt:" + workID + ":repair"
	reviewGateRunAttempt(t, s, workID, repairAttempt, "repair", repairEpoch, implement, ownerRef, *at)
	*at += 2
	if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}

	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	reviewAttempt := "attempt:" + workID + ":review-1"
	reviewGateRunAttempt(t, s, workID, reviewAttempt, "refine", refineEpoch, review, ownerRef, *at)
	*at += 2
	reviewGateRejectResult(t, s, workID, reviewAttempt, refineEpoch, acceptor)
	return refineEpoch
}

func reviewGateAcceptor(workID string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/review-gate-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
}

// TestPostRejectionReviewGateHoldsBreakFixDelivery drives the CON-421 shape on
// the current break-fix definition: a rejected review, then the accepted
// implement repair the gate holds at refine until a fresh accepted review
// covers it.
func TestPostRejectionReviewGateHoldsBreakFixDelivery(t *testing.T) {
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

	// The accepted implement repair is the advance into the gate, and it
	// refuses while no fresh accepted review covers the repaired result.
	repairedAttempt := "attempt:" + workID + ":repair-1"
	reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", refineEpoch, implement, ownerRef, at)
	at += 2
	err = reviewGateAcceptResult(t, s, workID, repairedAttempt, refineEpoch, acceptor)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "fresh accepted review") {
		t.Fatalf("accept of the repaired result = %v, want a fresh-review refusal", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")

	// The no-worker refinement route refuses on the same gate.
	if err := reviewGateRecordDelivery(t, s, workID, acceptor); err == nil {
		t.Fatal("record_delivery at refine behind an unreviewed repair was admitted")
	}

	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") || workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
		t.Fatalf("pin offered an advance behind an unreviewed repair: %#v", pin.NextValidIntents)
	}

	// The correction request stays a verdict-shaped route: without a recorded
	// non-ok verdict the refinement step offers none.
	correction := json.RawMessage(`{"diagnosis":"the repaired result has no fresh accepted review","strategy":"dispatch a fresh review at the refinement step","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", correction, fixture.owner, fixture.operator); err == nil {
		t.Fatal("request_correction at refine was admitted without a non-ok verdict")
	}

	// A fresh accepted review of the repaired result clears the gate, and the
	// accept then advances into the gate.
	freshReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttempt(t, s, workID, freshReview, "refine", refineEpoch, review, ownerRef, at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, freshReview, refineEpoch, acceptor); err != nil {
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
}

// TestDeliveryGateDeclaresCorrectiveReturn pins the amended CD-0166 gate
// shape on both affected families: record_delivery stays the first action and
// the corrective return sits between it and the continuity holds, while the
// released closed-gate versions keep their original shape.
func TestDeliveryGateDeclaresCorrectiveReturn(t *testing.T) {
	for _, definitionRef := range []string{"workflow.implementation", "workflow.break_fix"} {
		current, err := BuiltinWorkflowDefinitionForRef(definitionRef)
		if err != nil {
			t.Fatal(err)
		}
		gate := workflowStep(current.Definition, "delivery")
		if !workflowStepIsDeliveryGate(gate) {
			t.Fatalf("%s current gate shape = %v, want a delivery gate", definitionRef, gate.Actions)
		}
		if len(gate.Actions) != 4 || gate.Actions[0] != "record_delivery" || gate.Actions[1] != "request_correction" {
			t.Fatalf("%s gate actions = %v, want record_delivery, request_correction, then the continuity holds", definitionRef, gate.Actions)
		}
	}
	released, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13)
	if !ok {
		t.Fatal("workflow.break_fix v13 is not registered")
	}
	releasedGate := workflowStep(released.Definition, "delivery")
	if !workflowStepIsDeliveryGate(releasedGate) || len(releasedGate.Actions) != 3 {
		t.Fatalf("released v13 gate shape = %v, want the closed three-action gate", releasedGate.Actions)
	}
}

// TestPostRejectionReviewGateRefusesPreRejectionReviewAccept pins the
// dispatch-order rule on the accept guard: a review attempt dispatched before
// the rejection settles nothing, so accepting it after the rejection refuses
// and the item stays parked at refine.
func TestPostRejectionReviewGateRefusesPreRejectionReviewAccept(t *testing.T) {
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

	repairEpoch := reviewGateStartStep(t, s, workID, "repair", "start_repair", fixture.owner)
	repairAttempt := "attempt:" + workID + ":repair"
	reviewGateRunAttempt(t, s, workID, repairAttempt, "repair", repairEpoch, implement, ownerRef, at)
	at += 2
	if err := reviewGateAcceptResult(t, s, workID, repairAttempt, repairEpoch, acceptor); err != nil {
		t.Fatalf("accept repair: %v", err)
	}

	refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	// Two review attempts dispatch before the rejection.
	earlyReview := "attempt:" + workID + ":review-early"
	reviewGateRunAttempt(t, s, workID, earlyReview, "refine", refineEpoch, review, ownerRef, at)
	at += 2
	rejectedReview := "attempt:" + workID + ":review-rejected"
	reviewGateRunAttempt(t, s, workID, rejectedReview, "refine", refineEpoch, review, ownerRef, at)
	reviewGateRejectResult(t, s, workID, rejectedReview, refineEpoch, acceptor)

	// Accepting the pre-rejection review after the rejection refuses: a
	// review settles the debt only when its dispatch postdates the rejection.
	err = reviewGateAcceptResult(t, s, workID, earlyReview, refineEpoch, acceptor)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "fresh accepted review") {
		t.Fatalf("accept of the pre-rejection review = %v, want a fresh-review refusal", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")
}

// TestPostRejectionReviewGateRefusesReviewDispatchedBeforeRepair pins the
// repair-tied fresh-review rule on the CON-421 defect shape: a review
// dispatched and completed after the rejection but before the repaired
// implement result settles nothing, so accepting it refuses; a review
// dispatched after the repaired result is the fresh one, the pin advertises
// its acceptance, and that accept carries the gate.
func TestPostRejectionReviewGateRefusesReviewDispatchedBeforeRepair(t *testing.T) {
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

	// Review B dispatches and completes after the rejection; the implement
	// repair C dispatches and completes after B.
	earlyFreshReview := "attempt:" + workID + ":review-2"
	reviewGateRunAttempt(t, s, workID, earlyFreshReview, "refine", refineEpoch, reviewGateLane(t, "review"), ownerRef, at)
	at += 2
	repairedAttempt := "attempt:" + workID + ":repair-1"
	reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", refineEpoch, reviewGateLane(t, "implementation"), ownerRef, at)
	at += 2

	// Accepting the review that predates the repaired result refuses: it
	// covers nothing the repair produced after it.
	err = reviewGateAcceptResult(t, s, workID, earlyFreshReview, refineEpoch, acceptor)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "fresh accepted review") {
		t.Fatalf("accept of the review dispatched before the repair = %v, want a fresh-review refusal", err)
	}
	reviewGateRequireStep(t, s, workID, "refine")

	// With the debt outstanding and no completed settling review, the pin
	// offers no advance.
	pin, pinErr := ReadWorkPin(ctx, s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if workPinContainsAction(pin.NextValidIntents, "record_delivery") || workPinContainsAction(pin.NextValidIntents, "accept_worker_result") {
		t.Fatalf("pin offered an advance behind an unreviewed repair: %#v", pin.NextValidIntents)
	}

	// A review dispatched and completed after the repaired result is the
	// fresh review. Before its acceptance the pin advertises exactly that
	// acceptance and still withholds the delivery exit.
	settlingReview := "attempt:" + workID + ":review-3"
	reviewGateRunAttempt(t, s, workID, settlingReview, "refine", refineEpoch, reviewGateLane(t, "review"), ownerRef, at)
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

	// The settling accept advances into the gate, and the gate crossing
	// admits the reviewed change.
	if err := reviewGateAcceptResult(t, s, workID, settlingReview, refineEpoch, acceptor); err != nil {
		t.Fatalf("accept after the repair-postdating review refused: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
	if err := reviewGateRecordDelivery(t, s, workID, acceptor); err != nil {
		t.Fatalf("gate crossing after the settling review refused: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "verify")
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

			workEpoch := reviewGateStartStep(t, s, workID, tc.verdictStep, tc.startAction, fixture.owner)
			workAttempt := "attempt:" + workID + ":work"
			reviewGateRunAttempt(t, s, workID, workAttempt, tc.verdictStep, workEpoch, reviewGateLane(t, "implementation"), ownerRef, at)
			at += 2
			if err := reviewGateAcceptResult(t, s, workID, workAttempt, workEpoch, acceptor); err != nil {
				t.Fatalf("accept %s result: %v", tc.verdictStep, err)
			}

			// A rejected refinement review, then the repaired implement
			// result whose accept seeds the parked gate position.
			refineEpoch := reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
			reviewAttempt := "attempt:" + workID + ":review-1"
			reviewGateRunAttempt(t, s, workID, reviewAttempt, "refine", refineEpoch, reviewGateLane(t, "review"), ownerRef, at)
			at += 2
			reviewGateRejectResult(t, s, workID, reviewAttempt, refineEpoch, acceptor)
			repairedAttempt := "attempt:" + workID + ":repair-1"
			reviewGateRunAttempt(t, s, workID, repairedAttempt, "refine", refineEpoch, reviewGateLane(t, "implementation"), ownerRef, at)
			reviewGateSeedAcceptAtRefine(t, s, workID, repairedAttempt, refineEpoch, acceptor)
			reviewGateRequireStep(t, s, workID, "delivery")

			// The instance stays pinned to the current version, whose gate
			// declares the corrective return.
			current, err := BuiltinWorkflowDefinitionForRef(tc.definitionRef)
			if err != nil {
				t.Fatal(err)
			}
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
