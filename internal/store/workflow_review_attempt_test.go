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

// CD-0222 D6: an executing peer that modifies the same Domain does not block
// the reviewed-step record.
func TestReviewedStepRecordsBesideASharedDomainWriter(t *testing.T) {
	const workID = "reviewed-step-overlap"
	const peerID = "reviewed-step-overlap-peer"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanFix, "review", []string{})
	execInFold(t, f.store, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('`+workID+`',1,'root')`)
	seedRootWriterPeer(t, f.store, peerID, mustActorRef(t, f.owner))
	var writers int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(DISTINCT work_id) FROM workflow_contract_domain_modifications WHERE domain_id='root' AND work_id IN (?,?)`, workID, peerID).Scan(&writers); err != nil || writers != 2 {
		t.Fatalf("fixture has %d root writers, want 2: %v", writers, err)
	}
	payload := mustJSONValue(map[string]any{"step_id": "review", "note": "clean final review", "reviewer": "concord-review", "result": "pass", "commit": "commit:fixture", "link": "https://example.com/review"})
	if err := runVerdictActionAs(t, f.store, workID, "record_review", payload, 0, f.owner); err != nil {
		t.Fatalf("reviewed step beside a shared Domain writer = %v, want admitted", err)
	}
	plan, err := readWorkflowPlanTx(context.Background(), f.store.DatabaseForTesting(), workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: %v", err)
	}
	step, ok := stepByID(plan.Steps, "review")
	if !ok || len(step.Attempts) != 1 || step.Attempts[0].Result != "pass" || !WorkflowStepPlanComplete(*plan) {
		t.Fatalf("clean review was not recorded beside the peer: %+v", step)
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
