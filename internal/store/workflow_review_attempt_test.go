package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWorkflowReviewAttemptTuple(t *testing.T) {
	valid := workflowStepCompletedPayload{Reviewer: "concord-review", Result: "pass", Commit: "abc123", Link: "https://example.com/review"}
	cases := []struct {
		name    string
		edit    func(*workflowStepCompletedPayload)
		wantErr bool
	}{
		{"pass", func(*workflowStepCompletedPayload) {}, false},
		{"fixed", func(p *workflowStepCompletedPayload) { p.Result = "fixed" }, false},
		{"reviewer", func(p *workflowStepCompletedPayload) { p.Reviewer = "other" }, true},
		{"result", func(p *workflowStepCompletedPayload) { p.Result = "failed" }, true},
		{"commit", func(p *workflowStepCompletedPayload) { p.Commit = "" }, true},
		{"link", func(p *workflowStepCompletedPayload) { p.Link = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.edit(&p)
			if err := admitWorkflowPlanReview(p); (err != nil) != tc.wantErr {
				t.Fatalf("admitWorkflowPlanReview() = %v, want error %t", err, tc.wantErr)
			}
		})
	}
}

func TestWorkflowReviewFrontierAdmission(t *testing.T) {
	plan := &WorkflowStepPlan{Steps: []WorkflowPlanStep{
		{ID: "plan", After: []string{}},
		{ID: "review", After: []string{"plan"}, Reviewed: true},
	}}
	fields := func(id string) map[string]json.RawMessage {
		encoded, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]json.RawMessage{"step_id": encoded}
	}
	admit := func(id string) error {
		_, err := workflowPlanFrontierAdmissionTx(context.Background(), nil, "work", "record_review", "", plan, fields(id))
		return err
	}
	if err := admit("plan"); err == nil {
		t.Fatal("unreviewed step admitted")
	}
	if err := admit("review"); err == nil {
		t.Fatal("non-frontier step admitted")
	}
	if err := admit("missing"); err == nil {
		t.Fatal("missing step admitted")
	}
	template, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	plan = &template
	for i := range plan.Steps {
		if plan.Steps[i].Reviewed {
			if err := admit(plan.Steps[i].ID); err != nil {
				t.Fatalf("frontier reviewed step refused: %v", err)
			}
			return
		}
		plan.Steps[i].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}}
	}
	t.Fatal("template has no reviewed step")
}
