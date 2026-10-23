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

// An operator stop resolves a standing correction: the cancelled lifecycle
// cancels the workflow instance. The correction is Main's rejection of a
// completed result (CD-0242 D5); no attempt count or escalation wall is
// needed to reach it (CD-0242 D6).
func TestCorrectionResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	f := seedCrossAttemptFixture(t, workID)
	s := f.s
	defer s.Close()
	f.buildTwoAttemptHistory(t)
	epoch := crossAttemptDispatchEpoch(t, s, workID, f.attemptR)
	if _, err := f.invokeReject(t, readWorkVersion(t, s, workID), f.attemptR, epoch, "stop", 30); err != nil {
		t.Fatalf("Main rejects the research result: %v", err)
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
