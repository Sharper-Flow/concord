package store

import (
	"strings"
	"testing"
	"time"
)

func stepPlanTemplateOrFatal(t *testing.T, planType WorkflowStepPlanType) WorkflowStepPlan {
	t.Helper()
	plan, err := WorkflowStepPlanTemplate(planType)
	if err != nil {
		t.Fatalf("template for %s: %v", planType, err)
	}
	return plan
}

func TestWorkflowStepPlanTemplatesCopyValid(t *testing.T) {
	for _, planType := range WorkflowStepPlanTypes {
		plan := stepPlanTemplateOrFatal(t, planType)
		if err := ValidateStepPlanWrite(nil, plan); err != nil {
			t.Fatalf("type-pick write for %s refused: %v", planType, err)
		}
		if WorkflowStepPlanComplete(plan) {
			t.Fatalf("template for %s is already complete", planType)
		}
		if frontier := WorkflowStepPlanFrontier(plan); len(frontier) != 1 || frontier[0].ID != plan.Steps[0].ID {
			t.Fatalf("template frontier for %s is not its first step", planType)
		}
		for index, step := range plan.Steps {
			if !step.Required {
				t.Fatalf("template step %s.%s is not required", planType, step.ID)
			}
			if len(step.Attempts) != 0 {
				t.Fatalf("template step %s.%s carries attempts", planType, step.ID)
			}
			if index == 0 && len(step.After) != 0 {
				t.Fatalf("template first step %s.%s comes after another step", planType, step.ID)
			}
			if index > 0 && (len(step.After) != 1 || step.After[0] != plan.Steps[index-1].ID) {
				t.Fatalf("template step %s.%s does not come after its predecessor", planType, step.ID)
			}
			if actions := WorkflowStepPlanTemplateActions(planType, step.ID); len(actions) == 0 {
				t.Fatalf("template step %s.%s declares no actions", planType, step.ID)
			}
		}
	}
}

func TestWorkflowStepPlanTemplateActionsRejectUnknownStep(t *testing.T) {
	if actions := WorkflowStepPlanTemplateActions(WorkflowStepPlanChange, "nonexistent"); actions != nil {
		t.Fatalf("unknown step returned actions %v", actions)
	}
}

func TestWorkflowStepPlanTypePickRefusesNonTemplatePlan(t *testing.T) {
	plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
	tampered := stepPlanCopy(plan)
	tampered.Steps = append(tampered.Steps, WorkflowPlanStep{ID: "extra", Required: false, After: []string{plan.Steps[len(plan.Steps)-1].ID}})
	if err := ValidateStepPlanWrite(nil, tampered); err == nil {
		t.Fatal("type-pick write accepted a plan with an extra step")
	}
	tampered = stepPlanCopy(plan)
	tampered.Steps[0].After = []string{"later"}
	if err := ValidateStepPlanWrite(nil, tampered); err == nil {
		t.Fatal("type-pick write accepted changed after edges")
	}
	tampered = stepPlanCopy(plan)
	tampered.Steps[1].Attempts = []WorkflowPlanAttempt{{Note: "pre-completed", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	if err := ValidateStepPlanWrite(nil, tampered); err == nil {
		t.Fatal("type-pick write accepted a completed step")
	}
	tampered = stepPlanCopy(plan)
	tampered.Type = WorkflowStepPlanFix
	if err := ValidateStepPlanWrite(nil, tampered); err == nil {
		t.Fatal("type-pick write accepted a change template labelled fix")
	}
}

func TestWorkflowStepPlanShapeRefusals(t *testing.T) {
	base := stepPlanTemplateOrFatal(t, WorkflowStepPlanFix)

	badVersion := stepPlanCopy(base)
	badVersion.SchemaVersion = "2.0"
	if err := ValidateStepPlanWrite(nil, badVersion); err == nil {
		t.Fatal("plan with wrong schema version accepted")
	}

	unknownType := stepPlanCopy(base)
	unknownType.Type = WorkflowStepPlanType("spike")
	if err := ValidateStepPlanWrite(nil, unknownType); err == nil {
		t.Fatal("plan with unknown type accepted")
	}

	dangling := stepPlanCopy(base)
	dangling.Steps[1].After = []string{"nonexistent"}
	if err := ValidateStepPlanWrite(nil, dangling); err == nil {
		t.Fatal("plan with a dangling after edge accepted")
	}

	selfAfter := stepPlanCopy(base)
	selfAfter.Steps[1].After = []string{selfAfter.Steps[1].ID}
	if err := ValidateStepPlanWrite(nil, selfAfter); err == nil {
		t.Fatal("plan with a self after edge accepted")
	}

	cycle := WorkflowStepPlan{SchemaVersion: workflowStepPlanSchemaVersion, Type: WorkflowStepPlanFix, Steps: []WorkflowPlanStep{
		{ID: "diagnose", Required: true, After: []string{"plan"}},
		{ID: "plan", Required: true, After: []string{"diagnose"}},
	}}
	if err := ValidateStepPlanWrite(&cycle, cycle); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cyclic plan accepted: %v", err)
	}
}

func TestWorkflowStepPlanReviewedAttemptRequiresReviewReceipt(t *testing.T) {
	plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
	withPlanDone := stepPlanCopy(plan)
	withPlanDone.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "six-part plan", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	if err := ValidateStepPlanWrite(&plan, withPlanDone); err != nil {
		t.Fatalf("completing the plan step refused: %v", err)
	}
	withAttempt := stepPlanCopy(withPlanDone)
	withAttempt.Steps[1].Attempts = []WorkflowPlanAttempt{{Note: "implemented", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Reviewer: "concord-review", Result: "pass"}}
	if err := ValidateStepPlanWrite(&withPlanDone, withAttempt); err == nil || !strings.Contains(err.Error(), "reviewer") {
		t.Fatalf("reviewed attempt without commit and link accepted: %v", err)
	}
	withAttempt.Steps[1].Attempts = []WorkflowPlanAttempt{reviewedAttempt("implemented")}
	if err := ValidateStepPlanWrite(&withPlanDone, withAttempt); err != nil {
		t.Fatalf("reviewed attempt with its full receipt refused: %v", err)
	}
	unreviewed := stepPlanCopy(plan)
	unreviewed.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "planned", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Reviewer: "concord-review"}}
	if err := ValidateStepPlanWrite(&plan, unreviewed); err == nil {
		t.Fatal("unreviewed attempt carrying review fields accepted")
	}
}

func TestWorkflowStepPlanCompletionOrderingAndImmutability(t *testing.T) {
	plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanFix) // diagnose -> plan -> implement -> review

	// Completing a later step before its predecessor refuses.
	skipped := stepPlanCopy(plan)
	skipped.Steps[2].Attempts = []WorkflowPlanAttempt{reviewedAttempt("out of order")}
	if err := ValidateStepPlanWrite(&plan, skipped); err == nil || !strings.Contains(err.Error(), "after every step it comes after") {
		t.Fatalf("out-of-order completion accepted: %v", err)
	}

	// Completing the frontier step accepts and moves the frontier.
	first := stepPlanCopy(plan)
	first.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "diagnosed", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	if err := ValidateStepPlanWrite(&plan, first); err != nil {
		t.Fatalf("in-order completion refused: %v", err)
	}
	if frontier := WorkflowStepPlanFrontier(first); len(frontier) != 1 || frontier[0].ID != "plan" {
		t.Fatalf("frontier did not advance past the completed step: %+v", frontier)
	}

	// A completed step's attempt is never altered.
	altered := stepPlanCopy(first)
	altered.Steps[0].Attempts[0].Note = "rewritten history"
	if err := ValidateStepPlanWrite(&first, altered); err == nil || !strings.Contains(err.Error(), "never altered") {
		t.Fatalf("completed-step alteration accepted: %v", err)
	}

	// A repeated step appends another attempt instead of altering one.
	repeated := stepPlanCopy(first)
	repeated.Steps[0].Attempts = append(repeated.Steps[0].Attempts, WorkflowPlanAttempt{Note: "second attempt", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err := ValidateStepPlanWrite(&first, repeated); err != nil {
		t.Fatalf("attempt append refused: %v", err)
	}

	// After edges never change on an existing step.
	redialed := stepPlanCopy(first)
	redialed.Steps[1].After = []string{}
	if err := ValidateStepPlanWrite(&first, redialed); err == nil || !strings.Contains(err.Error(), "after edges never change") {
		t.Fatalf("after-edge change accepted: %v", err)
	}

	// A required step is never removed.
	removed := stepPlanCopy(first)
	removed.Steps = removed.Steps[:len(removed.Steps)-1]
	if err := ValidateStepPlanWrite(&first, removed); err == nil || !strings.Contains(err.Error(), "required step is never removed") {
		t.Fatalf("required-step removal accepted: %v", err)
	}
}

func TestWorkflowStepPlanInsertAndSendBack(t *testing.T) {
	plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)

	// An inserted step anchors itself after an existing step by declaring its
	// own after edges; the existing steps never change, so the plan keeps its
	// shape and the inserted step runs beside the steps that follow.
	inserted := stepPlanCopy(plan)
	inserted.Steps = append(inserted.Steps, WorkflowPlanStep{ID: "send-back", Required: false, After: []string{"implement"}, Reviewed: true})
	if err := ValidateStepPlanWrite(&plan, inserted); err != nil {
		t.Fatalf("inserted step refused: %v", err)
	}

	// Rewiring an existing step's after edges to point at the inserted step
	// is the forbidden shape: after edges never change.
	rewired := stepPlanCopy(inserted)
	rewired.Steps[2].After = []string{"send-back", "implement"}
	if err := ValidateStepPlanWrite(&inserted, rewired); err == nil || !strings.Contains(err.Error(), "after edges never change") {
		t.Fatalf("after-edge rewiring accepted: %v", err)
	}

	// An incomplete inserted step may be removed again.
	removed := stepPlanCopy(inserted)
	removed.Steps = removed.Steps[:len(removed.Steps)-1]
	if err := ValidateStepPlanWrite(&inserted, removed); err != nil {
		t.Fatalf("removing an incomplete inserted step refused: %v", err)
	}

	// A send-back after the current step completes once its anchor completed.
	withPlanDone := stepPlanCopy(plan)
	withPlanDone.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "six-part plan", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	sendBack := stepPlanCopy(withPlanDone)
	sendBack.Steps = append(sendBack.Steps, WorkflowPlanStep{ID: "send-back", Required: false, After: []string{"plan"}, Reviewed: true})
	if err := ValidateStepPlanWrite(&withPlanDone, sendBack); err != nil {
		t.Fatalf("inserting a send-back after the completed plan step refused: %v", err)
	}
	completed := stepPlanCopy(sendBack)
	completed.Steps[3].Attempts = []WorkflowPlanAttempt{reviewedAttempt("fixed")}
	if err := ValidateStepPlanWrite(&sendBack, completed); err != nil {
		t.Fatalf("completing the send-back with its review receipt refused: %v", err)
	}
}

func TestWorkflowStepPlanTypeSwitchAddsOnly(t *testing.T) {
	change := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
	// Complete change.plan, then switch to fix: diagnose is added, nothing removed.
	withPlanDone := stepPlanCopy(change)
	withPlanDone.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "six-part plan", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}}

	switched := WorkflowStepPlan{SchemaVersion: workflowStepPlanSchemaVersion, Type: WorkflowStepPlanFix, Steps: []WorkflowPlanStep{
		{ID: "diagnose", Required: true, After: []string{}, Skill: withPlanDone.Steps[0].Skill, Reviewed: false},
		{ID: "plan", Required: true, After: []string{}, Skill: withPlanDone.Steps[0].Skill, Attempts: withPlanDone.Steps[0].Attempts},
		{ID: "implement", Required: true, After: []string{"plan"}, Skill: withPlanDone.Steps[1].Skill, Helper: withPlanDone.Steps[1].Helper, Reviewed: true},
		{ID: "review", Required: true, After: []string{"implement"}, Skill: withPlanDone.Steps[2].Skill, Helper: withPlanDone.Steps[2].Helper, Reviewed: true},
	}}
	if err := ValidateStepPlanWrite(&withPlanDone, switched); err != nil {
		t.Fatalf("type switch adding the missing diagnose refused: %v", err)
	}

	// A switch that drops a step refuses.
	dropping := stepPlanCopy(switched)
	dropping.Steps = dropping.Steps[1:]
	if err := ValidateStepPlanWrite(&withPlanDone, dropping); err == nil {
		t.Fatal("type switch dropping a step accepted")
	}
}

func TestWorkflowStepPlanCompletionGate(t *testing.T) {
	plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanResearch)
	done := stepPlanCopy(plan)
	for index := range done.Steps {
		attempt := WorkflowPlanAttempt{Note: "done", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if done.Steps[index].Reviewed {
			attempt.Reviewer, attempt.Result, attempt.Commit, attempt.Link = "concord-review", "pass", "f4b78e3c14275a9fae58a964bc24e303ebdf02ad", "https://example.com/review"
		}
		done.Steps[index].Attempts = []WorkflowPlanAttempt{attempt}
	}
	if err := ValidateStepPlanWrite(&plan, done); err != nil {
		t.Fatalf("completing every step in order refused: %v", err)
	}
	if !WorkflowStepPlanComplete(done) {
		t.Fatal("plan with every step completed reports incomplete")
	}
	if frontier := WorkflowStepPlanFrontier(done); len(frontier) != 0 {
		t.Fatalf("complete plan still reports a frontier: %+v", frontier)
	}
}

func reviewedAttempt(note string) WorkflowPlanAttempt {
	return WorkflowPlanAttempt{Note: note, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Reviewer: "concord-review", Result: "pass", Commit: "f4b78e3c14275a9fae58a964bc24e303ebdf02ad", Link: "https://example.com/review"}
}

func stepPlanCopy(plan WorkflowStepPlan) WorkflowStepPlan {
	copied := WorkflowStepPlan{SchemaVersion: plan.SchemaVersion, Type: plan.Type, Steps: make([]WorkflowPlanStep, len(plan.Steps))}
	for index, step := range plan.Steps {
		stepCopy := WorkflowPlanStep{ID: step.ID, Required: step.Required, After: append([]string{}, step.After...), Skill: step.Skill, Helper: step.Helper, Reviewed: step.Reviewed}
		if step.Attempts != nil {
			stepCopy.Attempts = append([]WorkflowPlanAttempt{}, step.Attempts...)
		}
		copied.Steps[index] = stepCopy
	}
	return copied
}
