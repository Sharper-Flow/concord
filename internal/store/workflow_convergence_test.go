package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCorrectionOpenFindingIDsAreValidated(t *testing.T) {
	if !validWorkflowOpenFindings([]string{"finding:a", "finding:b"}) {
		t.Fatal("valid open finding set refused")
	}
	for _, raw := range []string{`null`, `[]`, `["finding:a","finding:a"]`, `["finding/a"]`, `[1]`} {
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err == nil && validWorkflowOpenFindings(ids) {
			t.Fatalf("invalid open finding set accepted: %s", raw)
		}
	}
}

// An operator stop cancels the workflow despite a standing correction.
func TestCorrectionResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s := j.s
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Review == nil || pin.Review.Accepted == nil || pin.Review.Accepted.Report.Disposition != "correction_required" {
		t.Fatal("the rejection left no current Review")
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
}
