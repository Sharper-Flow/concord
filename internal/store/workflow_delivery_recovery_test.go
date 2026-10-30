package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Delivery-gate correction support shared with the Review correction suite.
// The current gate's corrective return answers to an accepted Review that no
// longer covers the current candidate or depended-on revisions; an applicable
// accepted Review opens none.

// reviewGateSeedAcceptAtRefine replays the refine-crossing route: it records an
// accepted implement repair whose accept advances the refinement step into
// the delivery gate before any Review covers the repaired candidate.
func reviewGateSeedAcceptAtRefine(t *testing.T, s *Store, workID, attemptID string, epoch int64, acceptor WorkflowActor) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=? AND delivery_assertion=1`, workID, WorkflowActionCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func reviewGateInstancePin(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	var ref string
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &digest); err != nil {
		t.Fatal(err)
	}
	return ref, digest
}

func reviewGateCorrectionPayload(evidence string) json.RawMessage {
	return json.RawMessage(`{"diagnosis":"the repaired result has no fresh accepted review","strategy":"return to the repair step and dispatch a fresh review","predicate_ids":["predicate:return-route"],"evidence_refs":["` + evidence + `"]}`)
}

func TestPinnedDeliveryGateRecoversThroughEvidenceBearingReturn(t *testing.T) {
	const workID = "pinned-delivery-recovery"
	ctx := context.Background()
	c := acceptDeliveryCandidateAt(t, workID, "workflow.break_fix", "repair", false)
	fixture := workflowReturnRouteFixture{store: c.f.store, owner: c.f.owner, operator: c.f.operator}
	s := fixture.store
	setWorkflowStepForOperatorVerdictTest(t, s, workID, "refine")
	reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	c.j.bindVerification(t, c.early)
	acceptCurrentReviewForTest(t, c.f, c.j.repo, "continue")
	// Exercise gate recovery after the repair changed the reviewed candidate. A plain result
	// disposition cannot itself assert delivery or move refine (CD-0242 D5).
	setWorkflowStepForOperatorVerdictTest(t, s, workID, "delivery")
	reviewGateRequireStep(t, s, workID, "delivery")

	pinnedRef, pinnedDigest := reviewGateInstancePin(t, s, workID)
	if pinnedRef != c.definition.Definition.Ref || pinnedDigest != c.definition.Digest {
		t.Fatalf("pinned definition = %s %s, want %s %s", pinnedRef, pinnedDigest, c.definition.Definition.Ref, c.definition.Digest)
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
	stale := runVerdictActionAs(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:not-bound"), 0, fixture.owner)
	var staleFailure *Failure
	if stale == nil || !failureAs(stale, &staleFailure) || staleFailure.Kind != KindMissingEvidence {
		t.Fatalf("unbound-evidence return = %v, want a missing-evidence refusal", stale)
	}

	// The evidence-bearing corrective return returns the pinned instance to
	// the repair step for a fresh review.
	if err := runVerdictActionAs(t, s, workID, "request_correction", reviewGateCorrectionPayload(c.early.OperationRef), 0, fixture.owner); err != nil {
		t.Fatalf("evidence-bearing corrective return: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "repair")

	// The return asserted no delivery.
	if count := reviewGateCountDeliveries(t, s, workID); count != 0 {
		t.Fatalf("corrective return recorded %d delivery assertions, want none", count)
	}

	// The consumed gate correction does not re-fire: without a non-ok
	// verdict, the correction request at the repair step is unsupported.
	repeat := runVerdictActionAs(t, s, workID, "request_correction", reviewGateCorrectionPayload(c.early.OperationRef), 0, fixture.owner)
	if repeat == nil || !strings.Contains(repeat.Error(), "non-ok verification verdict") {
		t.Fatalf("repeat return = %v, want an unsupported-correction refusal", repeat)
	}

	// A terminal instance refuses the corrective return outright.
	version := readWorkVersion(t, s, workID)
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
	terminal := runVerdictActionAs(t, s, workID, "request_correction", reviewGateCorrectionPayload(c.early.OperationRef), 0, fixture.owner)
	if terminal == nil || !strings.Contains(terminal.Error(), "terminal workflow instance is immutable") {
		t.Fatalf("terminal return = %v, want the terminal refusal", terminal)
	}
}

// TestDeliveryGateCorrectionNeedsOutstandingReview proves the gate correction
// is unsupported while Main's accepted Review still covers the delivered
// candidate: the reviewed change parked on the gate offers its delivery exit,
// not a corrective return.
func TestDeliveryGateCorrectionNeedsOutstandingReview(t *testing.T) {
	const workID = "pinned-delivery-no-debt"
	fixture := refineProofCandidateFixture(t, workID, "workflow.break_fix")
	s := fixture.store
	refineProofStartRefine(t, fixture, workID)
	refineProofAcceptReadyReview(t, fixture, workID)
	refineProofCandidateRun(t, s, workID, "gate", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery with the reviewed candidate: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")

	pin, pinErr := ReadWorkPin(context.Background(), s, workID)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "request_correction" {
			t.Fatalf("gate under an applicable Review offered request_correction: %#v", pin.NextValidIntents)
		}
	}
	if !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("reviewed gate lost its delivery exit: %#v", pin.NextValidIntents)
	}
	err := runIssue933OperatorAction(t, s, workID, "request_correction", reviewGateCorrectionPayload("evidence:return-route-verification"), fixture.owner, fixture.operator)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "requires an outstanding Review correction") {
		t.Fatalf("unsupported gate return = %v, want the outstanding-review refusal", err)
	}
}
