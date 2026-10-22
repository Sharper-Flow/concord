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
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": "attempt:unrelated", "attempt_epoch": epoch}, j.fixture.owner); err == nil {
				t.Fatal("an unrelated attempt acquired the completed Review's acceptance")
			}
			if after := j.effects(t); after != before {
				t.Fatal("unrelated acceptance changed durable state")
			}
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

// A ready Review cannot bypass an unresolved Domain overlap on Main acceptance.
func TestReadyReviewAcceptCannotRideAnUnrelatedRefusal(t *testing.T) {
	const workID = "ready-review-overlap"
	const peerID = "ready-review-overlap-peer"
	j, _ := descendantReviewFixture(t, workID)
	s := j.s
	attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
	ownerRef, err := WorkflowActorRef(j.fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	// Both executing items modify root under the same current registry.
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+workID+`',1,'root')`)
	seedStaleRegistryRescanPeer(t, s, peerID, ownerRef)
	before := j.effects(t)
	err = j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("ready Review acceptance beside an unresolved overlap = %v, want the overlap refusal", err)
	}
	if after := j.effects(t); after != before {
		t.Fatal("refused acceptance changed durable state")
	}
	reviewGateRequireStep(t, s, workID, "repair")
}
