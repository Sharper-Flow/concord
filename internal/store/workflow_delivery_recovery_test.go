package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Delivery-gate correction support shared with the Review correction suite.
// The current gate's corrective return answers to an outstanding
// post-rejection Review or an accepted Review that no longer covers the
// current candidate; an applicable accepted Review opens none.

// reviewGateSeedAcceptAtRefine replays the refine-crossing route: it records an
// accepted implement repair whose accept advances the refinement step into
// the delivery gate with the post-rejection review debt outstanding.
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
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "outstanding post-rejection review") {
		t.Fatalf("unsupported gate return = %v, want the outstanding-review refusal", err)
	}
}
