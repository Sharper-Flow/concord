package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCorrectionOpenFindingIDsAreValidated(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `["finding:a","finding:a"]`, `["finding/a"]`, `[1]`} {
		fields := map[string]json.RawMessage{"open_finding_ids": json.RawMessage(raw)}
		if err := validateWorkflowOpenFindingsPayload(fields); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("%s validation=%v", raw, err)
		}
	}
}

// An operator stop cancels the workflow despite a standing correction.
func TestCorrectionResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s := j.s
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatalf("record correction: %v", err)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatal("the rejection left no standing correction to resolve")
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
