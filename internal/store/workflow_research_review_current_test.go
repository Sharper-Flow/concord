package store

import (
	"encoding/json"
	"testing"
)

func TestResearchPlanRecordsReview(t *testing.T) {
	const workID = "research-review-current"
	fixture := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "conclude", []string{}, []string{})
	version := readWorkVersion(t, fixture.store, workID)
	payload := json.RawMessage(`{"step_id":"conclude","note":"independent research review","reviewer":"concord-review","result":"pass","commit":"commit:research","link":"https://example.com/review"}`)
	issue31WorkflowActionWithPayload(t, fixture.store, workID, version, "record_review", "research-review", fixture.owner, payload)
	plan, err := readWorkflowPlanTx(t.Context(), fixture.store.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: %+v, %v", plan, err)
	}
	step, ok := stepByID(plan.Steps, "conclude")
	if !ok || len(step.Attempts) != 1 || step.Attempts[0].Reviewer != "concord-review" || step.Attempts[0].Result != "pass" || step.Attempts[0].Commit != "commit:research" || step.Attempts[0].Link != "https://example.com/review" {
		t.Fatalf("recorded review attempt: %+v", step.Attempts)
	}
}
