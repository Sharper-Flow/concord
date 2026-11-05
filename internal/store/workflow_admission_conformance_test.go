package store

import (
	"os"
	"path/filepath"
	"testing"
)

func conformanceImplementChange(t *testing.T, j *candidateJourney, label string) {
	t.Helper()
	path := filepath.Join(j.repo, "internal", "store", "value.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package fixture\nvar Value = "+itoa(len(label))+" // "+label+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "author@example.invalid", "produce bounded implementation")
}

func TestWorkflowPlanAdmissionReviewAndMandate(t *testing.T) {
	const workID = "plan-admission-review-mandate"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", nil)
	payload := mustJSONValue(map[string]any{"step_id": "implement", "note": "independent review", "reviewer": "concord-review", "result": "pass", "commit": "commit:fixture", "link": "https://example.com/review"})
	version := readWorkVersion(t, f.store, workID)
	request := planAdmissionRequest(workID, "record_review", version, f.owner, payload)
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("review preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("review dispatch: %v", err)
	}
	plan, err := readWorkflowPlanTx(t.Context(), f.store.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: %v", err)
	}
	step, ok := stepByID(plan.Steps, "implement")
	if !ok || len(step.Attempts) != 1 || step.Attempts[0].Reviewer != "concord-review" || step.Attempts[0].Result != "pass" {
		t.Fatalf("recorded review attempt: %+v", step)
	}
}
