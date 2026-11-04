package store

import (
	"context"
	"testing"
)

// An operator stop cancels the workflow despite a standing correction.
func TestCorrectionResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	j := pReturnReviewFixture(t, workID, WorkflowStepPlanFix, "correction_required")
	s := j.s
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Review == nil || pin.Review.Accepted == nil || pin.Review.Accepted.Report.Disposition != "correction_required" {
		t.Fatal("the rejection left no current Review")
	}
	if pin.Review.Accepted.ActorRef != mustActorRef(t, j.fixture.owner) {
		t.Fatal("correction review lost its actor")
	}
	reviewRef := pin.Review.Accepted.ReportRef
	var reviewDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, reviewRef).Scan(&reviewDigest); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, workTransitionEvent("stop-"+workID, workID, lifecycle, "cancelled", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	var instanceState string
	if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if instanceState != "cancelled" {
		t.Fatalf("operator stop left instance %q", instanceState)
	}
	var retainedDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, reviewRef).Scan(&retainedDigest); err != nil || retainedDigest != reviewDigest {
		t.Fatalf("operator stop changed correction review receipt: %q, %v", retainedDigest, err)
	}
}
