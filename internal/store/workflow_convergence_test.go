package store

import (
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

func TestCorrectionResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	s, _, _ := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
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
