package store

import (
	"context"
	"testing"
)

// An operator stop cancels the workflow despite a standing correction.
func TestCorrectionResolvesThroughOperatorStop(t *testing.T) {
	const workID = "convergence-operator-stop"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanFix, "review", []string{}, []string{})
	s := f.store
	if err := runVerdictActionAs(t, s, workID, "record_review", mustJSONValue(map[string]any{"step_id": "review", "note": "review corrected the change", "reviewer": "concord-review", "result": "fixed", "commit": "commit:fixture", "link": "https://example.com/review"}), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	plan, err := readWorkflowPlanTx(context.Background(), s.db, workID)
	if err != nil || plan == nil || WorkflowStepPlanComplete(*plan) {
		t.Fatalf("fixed review must remain open: %+v %v", plan, err)
	}
	step, ok := stepByID(plan.Steps, "review")
	if !ok || len(step.Attempts) == 0 {
		t.Fatal("review attempt missing")
	}
	var reviewRef string
	if err := s.db.QueryRow(`SELECT event_id FROM effect_receipts WHERE effect_seq=?`, step.Attempts[len(step.Attempts)-1].CompletionSeq).Scan(&reviewRef); err != nil {
		t.Fatal(err)
	}
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
