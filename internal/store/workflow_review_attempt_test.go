package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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

func unreviewedRecordReviewRefusal(t *testing.T, err error) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Detail != "step does not require review" || failure.RetrySafe || failure.EffectPossible {
		t.Fatalf("record_review on an unreviewed step = %v, want a permanent illegal_lifecycle_transition with no effect", err)
	}
	return failure
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
	} else {
		failure := unreviewedRecordReviewRefusal(t, err)
		for _, phrase := range []string{`"plan"`, "complete_step", "step_id", "note", "omit reviewer, result, commit, and link", "steps[].reviewed", "concord_work_trace.resume"} {
			if !strings.Contains(failure.RecoveryAction, phrase) {
				t.Errorf("unreviewed frontier step recovery %q omits %q", failure.RecoveryAction, phrase)
			}
		}
		// The suggested correction is admitted: the same step, completed
		// without review fields.
		if err := workflowPlanStepAttemptAdmission(plan, "plan", "complete_step"); err != nil {
			t.Fatalf("suggested complete_step was refused: %v", err)
		}
	}
	if err := admit("review"); err == nil {
		t.Fatal("non-frontier step admitted")
	}
	if err := admit("missing"); err == nil {
		t.Fatal("missing step admitted")
	}

	// A completed unreviewed step needs no review and draws no completion
	// suggestion.
	plan.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}}
	failure := unreviewedRecordReviewRefusal(t, admit("plan"))
	if strings.Contains(failure.RecoveryAction, "complete_step") {
		t.Fatalf("a completed step drew a completion suggestion: %q", failure.RecoveryAction)
	}
	for _, phrase := range []string{"is complete", "requires no review", "steps[].reviewed", "concord_work_trace.resume"} {
		if !strings.Contains(failure.RecoveryAction, phrase) {
			t.Errorf("completed unreviewed step recovery %q omits %q", failure.RecoveryAction, phrase)
		}
	}
	// The pointer reaches the reviewed step on the frontier.
	if err := admit("review"); err != nil {
		t.Fatalf("reviewed frontier step named by steps[].reviewed was refused: %v", err)
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

// The refusal through the owning action: it records nothing, its suggested
// completion succeeds, and the completed step then points at the reviewed
// step without offering another completion.
func TestWorkflowUnreviewedRecordReviewRefusalGuidesCompletion(t *testing.T) {
	const workID = "record-review-unreviewed-step"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanResearch, "investigate")
	review := map[string]any{"step_id": "investigate", "note": "finding reviewed", "reviewer": "concord-review", "result": "pass", "commit": "commit:fixture", "link": "https://example.com/review"}
	before := contractCorrectionEffectSnapshot(t, f.store, workID)
	failure := unreviewedRecordReviewRefusal(t, runVerdictActionAs(t, f.store, workID, "record_review", mustJSONValue(review), 0, f.owner))
	if contractCorrectionEffectSnapshot(t, f.store, workID) != before {
		t.Fatal("refused review changed effects")
	}
	for _, phrase := range []string{`"investigate"`, "complete_step", "step_id", "note", "omit reviewer, result, commit, and link"} {
		if !strings.Contains(failure.RecoveryAction, phrase) {
			t.Errorf("unreviewed frontier step recovery %q omits %q", failure.RecoveryAction, phrase)
		}
	}

	// The suggested correction completes the step.
	if err := runVerdictActionAs(t, f.store, workID, "complete_step", mustJSONValue(map[string]any{"step_id": "investigate", "note": "finding investigated"}), 0, f.owner); err != nil {
		t.Fatalf("suggested complete_step was refused: %v", err)
	}
	plan, err := readWorkflowPlanTx(t.Context(), f.store.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: %v", err)
	}
	step, ok := stepByID(plan.Steps, "investigate")
	if !ok || len(step.Attempts) != 1 || step.Attempts[0].Note != "finding investigated" || step.Attempts[0].Reviewer != "" {
		t.Fatalf("suggested completion was not recorded without review fields: %+v", step)
	}

	before = contractCorrectionEffectSnapshot(t, f.store, workID)
	failure = unreviewedRecordReviewRefusal(t, runVerdictActionAs(t, f.store, workID, "record_review", mustJSONValue(review), 0, f.owner))
	if contractCorrectionEffectSnapshot(t, f.store, workID) != before {
		t.Fatal("refused review of a completed step changed effects")
	}
	if strings.Contains(failure.RecoveryAction, "complete_step") {
		t.Fatalf("a completed step drew a completion suggestion: %q", failure.RecoveryAction)
	}
	for _, phrase := range []string{"is complete", "requires no review", "steps[].reviewed", "concord_work_trace.resume"} {
		if !strings.Contains(failure.RecoveryAction, phrase) {
			t.Errorf("completed unreviewed step recovery %q omits %q", failure.RecoveryAction, phrase)
		}
	}

	// steps[].reviewed from the resume read finds the reviewed frontier step.
	resume, err := ReadWorkflowResume(t.Context(), f.store, workID)
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, step := range resume.Steps {
		if step.Reviewed && slices.Contains(resume.NextSteps, step.ID) {
			target = step.ID
		}
	}
	if target != "conclude" {
		t.Fatalf("reviewed frontier step = %q, want conclude: next_steps=%v", target, resume.NextSteps)
	}
	review["step_id"] = target
	if _, err := WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), StepID: target, ActionID: "record_review", Payload: mustJSONValue(review), Actor: f.owner}); err != nil {
		t.Fatalf("reviewed step found through steps[].reviewed was refused: %v", err)
	}
}
