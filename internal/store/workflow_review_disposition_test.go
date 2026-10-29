package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBlockingReviewDispositionRecordsWithoutAdvancing(t *testing.T) {
	for _, disposition := range []string{"correction_required", "revision_required"} {
		t.Run(disposition, func(t *testing.T) {
			j := pReturnReviewFixture(t, "review-hold-"+disposition, "workflow.implementation", "")
			step := currentStep(t, j.s, j.workID)
			report := currentReviewBlockForTest(t, j.s, j.workID, "", disposition)
			before := j.effects(t)
			if err := j.action(t, "record_review", map[string]any{"review": report, "delivery_artifact": "artifact:blocked-review", "delivery_state": "asserted"}, j.fixture.owner); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("Review carried a delivery assertion: %v", err)
			}
			if j.effects(t) != before {
				t.Fatal("refused delivery-bearing Review changed durable state")
			}
			if err := recordReviewForTest(t, j.s, j.workID, j.fixture.owner, report); err != nil {
				t.Fatalf("could not record a corrective disposition: %v", err)
			}
			pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if currentStep(t, j.s, j.workID) != step || pin.Review == nil || pin.Review.Accepted == nil || pin.Review.Accepted.Report.Disposition != disposition {
				t.Fatalf("recording lost the disposition or advanced execution: %+v", pin.Review)
			}
			if workPinContainsAction(pin.NextValidIntents, "record_delivery") || !workPinContainsAction(pin.NextValidIntents, "record_review") || !workPinContainsAction(pin.NextValidIntents, "request_correction") {
				t.Fatalf("corrective Review lost its hold or engineering recovery: %+v", pin.NextValidIntents)
			}
			acceptCurrentReviewForTest(t, j.fixture, j.repo, "ready_for_final_proof")
			verification := j.observeRun(t, "review-ready-delivery")
			if err := j.action(t, "bind_evidence", map[string]any{
				"evidence_kind": "verification", "immutable_subject_ref": verification.OperationRef,
				"producer_id": j.fixture.owner.PrincipalRef, "producer_run_ref": verification.OperationRef,
				"producer_watermark": "request-" + strings.TrimPrefix(verification.LeaseID, "candidate-"),
			}, j.fixture.owner); err != nil {
				t.Fatal(err)
			}
			pin, err = ReadWorkPin(t.Context(), j.s, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			if err := workflowAdmitReview(*pin.Review); err != nil {
				t.Fatalf("recorded readiness did not satisfy the Review boundary: %v", err)
			}
			if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:review-ready", "delivery_state": "asserted"}, j.fixture.owner); err != nil {
				t.Fatalf("current ready Review and proof did not release delivery: %v", err)
			}
		})
	}
}

func TestPreflightAndMutationRequireAcceptedReadyDisposition(t *testing.T) {
	j := pReturnReviewFixture(t, "review-disposition-preflight", "workflow.implementation", "correction_required")
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

func TestReadyReviewRecordCannotRideAnUnrelatedRefusal(t *testing.T) {
	const workID = "ready-review-overlap"
	const peerID = "ready-review-overlap-peer"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "")
	s := j.s
	report := currentReviewBlockForTest(t, s, workID, "", "ready_for_final_proof")
	ownerRef := mustActorRef(t, j.fixture.owner)
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+workID+`',1,'root')`)
	seedStaleRegistryRescanPeer(t, s, peerID, ownerRef)
	before := j.effects(t)
	err := recordReviewForTest(t, s, workID, j.fixture.owner, report)
	if !hasFailureKind(err, KindDomainOverlap) {
		t.Fatalf("ready Review beside an unresolved overlap = %v, want the overlap refusal", err)
	}
	if j.effects(t) != before {
		t.Fatal("refused Review changed durable state")
	}
	reviewGateRequireStep(t, s, workID, "repair")
}
