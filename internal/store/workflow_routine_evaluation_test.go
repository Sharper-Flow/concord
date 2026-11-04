package store

import (
	"encoding/json"
	"testing"
)

// ownerRefForTest is shared by current actor-identity fixtures.
func ownerRefForTest(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestWorkflowPlanRoutinePremiseConfirmation(t *testing.T) {
	const workID = "plan-routine-evaluation"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", nil)
	moveWorkflowPlanFixtureFrontier(t, f.store, workID, "review")
	confirm := planAdmissionRequest(workID, "confirm_premise", readWorkVersion(t, f.store, workID), f.owner, json.RawMessage(`{}`))
	if err := planAdmissionDispatch(t, f.store, confirm); err != nil {
		t.Fatalf("routine coordinator confirmation: %v", err)
	}
	if actor, ok := approvalPremiseConfirmationRow(t, f.store, workID, 1); !ok || actor != mustActorRef(t, f.owner) || approvalKindEvidenceCount(t, f.store, workID) != 0 {
		t.Fatalf("confirmation actor=%q present=%v or spurious approval evidence", actor, ok)
	}
}
