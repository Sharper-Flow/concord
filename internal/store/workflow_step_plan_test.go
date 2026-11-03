package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkflowStepPlanWriteRegression(t *testing.T) {
	base := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
	attempt := WorkflowPlanAttempt{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}
	tests := map[string]func(*WorkflowStepPlan){
		"precompleted insertion": func(p *WorkflowStepPlan) {
			p.Steps = append(p.Steps, WorkflowPlanStep{ID: "extra", After: []string{"plan"}, Attempts: []WorkflowPlanAttempt{attempt}})
		},
		"batch prerequisites": func(p *WorkflowStepPlan) {
			p.Steps[0].Attempts = []WorkflowPlanAttempt{attempt}
			p.Steps[1].Attempts = []WorkflowPlanAttempt{reviewedAttempt("done")}
		},
		"multiple attempts": func(p *WorkflowStepPlan) { p.Steps[0].Attempts = []WorkflowPlanAttempt{attempt, attempt} },
		"multiple independent completions": func(p *WorkflowStepPlan) {
			p.Steps[0].Attempts = []WorkflowPlanAttempt{attempt}
			p.Steps = append(p.Steps, WorkflowPlanStep{ID: "extra", After: []string{}, Attempts: []WorkflowPlanAttempt{attempt}})
		},
		"required addition without switch": func(p *WorkflowStepPlan) { p.Steps = append(p.Steps, WorkflowPlanStep{ID: "diagnose", Required: true}) },
		"duplicate edge": func(p *WorkflowStepPlan) {
			p.Steps = append(p.Steps, WorkflowPlanStep{ID: "extra", After: []string{"plan", "plan"}})
		},
		"one character id": func(p *WorkflowStepPlan) { p.Steps = append(p.Steps, WorkflowPlanStep{ID: "x"}) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			next := stepPlanCopy(base)
			mutate(&next)
			if err := ValidateStepPlanWrite(&base, next); err == nil {
				t.Fatal("invalid write accepted")
			}
		})
	}
	completed := stepPlanCopy(base)
	completed.Steps[0].Attempts = []WorkflowPlanAttempt{attempt}
	for _, field := range []string{"skill", "helper"} {
		t.Run("completed "+field, func(t *testing.T) {
			next := stepPlanCopy(completed)
			changed := "changed"
			if field == "skill" {
				next.Steps[0].Skill = &changed
			} else {
				next.Steps[0].Helper = &changed
			}
			if err := ValidateStepPlanWrite(&completed, next); err == nil {
				t.Fatal("metadata change accepted")
			}
		})
	}
	t.Run("negative completion sequence", func(t *testing.T) {
		var a WorkflowPlanAttempt
		if err := json.Unmarshal([]byte(`{"note":"done","completed_at":"2026-01-01","completion_seq":-1}`), &a); err != nil {
			t.Fatal(err)
		}
		next := stepPlanCopy(base)
		next.Steps[0].Attempts = []WorkflowPlanAttempt{a}
		if err := ValidateStepPlanWrite(&base, next); err == nil {
			t.Fatal("negative sequence accepted")
		}
	})
	t.Run("two existing independent completions", func(t *testing.T) {
		old := stepPlanCopy(base)
		old.Steps = append(old.Steps, WorkflowPlanStep{ID: "extra", After: []string{}})
		next := stepPlanCopy(old)
		next.Steps[0].Attempts = []WorkflowPlanAttempt{attempt}
		next.Steps[len(next.Steps)-1].Attempts = []WorkflowPlanAttempt{attempt}
		if err := ValidateStepPlanWrite(&old, next); err == nil {
			t.Fatal("multiple step completions accepted")
		}
	})
	t.Run("completion sequence round trip", func(t *testing.T) {
		for _, seq := range []int64{0, 42} {
			a := WorkflowPlanAttempt{Note: "done", CompletedAt: "2026-01-01", CompletionSeq: seq}
			encoded, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			var decoded WorkflowPlanAttempt
			if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != a {
				t.Fatalf("round trip %d: %+v, %v", seq, decoded, err)
			}
			next := stepPlanCopy(base)
			next.Steps[0].Attempts = []WorkflowPlanAttempt{decoded}
			if err := ValidateStepPlanWrite(&base, next); err != nil {
				t.Fatalf("sequence %d refused: %v", seq, err)
			}
		}
	})
	for _, change := range []string{"optional", "skill", "helper", "reviewed"} {
		t.Run("switch missing template "+change, func(t *testing.T) {
			next := stepPlanCopy(base)
			next.Type = WorkflowStepPlanFix
			fix := stepPlanTemplateOrFatal(t, WorkflowStepPlanFix)
			added := fix.Steps[0]
			switch change {
			case "optional":
				added.Required = false
			case "skill":
				added.Skill = nil
			case "helper":
				added.Helper = nil
			case "reviewed":
				added.Reviewed = true
			}
			next.Steps = append(next.Steps, added)
			if err := ValidateStepPlanWrite(&base, next); err == nil {
				t.Fatal("malformed template addition accepted")
			}
		})
	}
	t.Run("switch adds unrelated step", func(t *testing.T) {
		next := stepPlanCopy(base)
		next.Type = WorkflowStepPlanFix
		next.Steps = append(next.Steps, stepPlanTemplateOrFatal(t, WorkflowStepPlanFix).Steps[0], WorkflowPlanStep{ID: "extra"})
		if err := ValidateStepPlanWrite(&base, next); err == nil {
			t.Fatal("unrelated addition accepted during switch")
		}
	})
}

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

	diagnose := stepPlanTemplateOrFatal(t, WorkflowStepPlanFix).Steps[0]
	switched := WorkflowStepPlan{SchemaVersion: workflowStepPlanSchemaVersion, Type: WorkflowStepPlanFix, Steps: []WorkflowPlanStep{
		diagnose,
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
		previous := stepPlanCopy(done)
		attempt := WorkflowPlanAttempt{Note: "done", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if done.Steps[index].Reviewed {
			attempt.Reviewer, attempt.Result, attempt.Commit, attempt.Link = "concord-review", "pass", "f4b78e3c14275a9fae58a964bc24e303ebdf02ad", "https://example.com/review"
		}
		done.Steps[index].Attempts = []WorkflowPlanAttempt{attempt}
		if err := ValidateStepPlanWrite(&previous, done); err != nil {
			t.Fatalf("completing step %d in order refused: %v", index, err)
		}
	}
	if !WorkflowStepPlanComplete(done) {
		t.Fatal("plan with every step completed reports incomplete")
	}
	if frontier := WorkflowStepPlanFrontier(done); len(frontier) != 0 {
		t.Fatalf("complete plan still reports a frontier: %+v", frontier)
	}
}

func TestWorkflowStepPlanFinalCleanReview(t *testing.T) {
	plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
	for index := range plan.Steps {
		attempt := WorkflowPlanAttempt{Note: "done", CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if plan.Steps[index].Reviewed {
			attempt = reviewedAttempt("done")
		}
		plan.Steps[index].Attempts = []WorkflowPlanAttempt{attempt}
	}
	final := len(plan.Steps) - 1
	plan.Steps[final].Attempts[0].Result = "fixed"
	if WorkflowStepPlanComplete(plan) {
		t.Fatal("a final review that needed fixes completes the plan")
	}
	if frontier := WorkflowStepPlanFrontier(plan); len(frontier) != 1 || frontier[0].ID != plan.Steps[final].ID {
		t.Fatalf("a fixed final review does not reopen it: %+v", frontier)
	}
	// An earlier reviewed step passes on a fixed result.
	implement := final - 1
	plan.Steps[implement].Attempts[0].Result = "fixed"
	repeated := stepPlanCopy(plan)
	repeated.Steps[final].Attempts = append(repeated.Steps[final].Attempts, reviewedAttempt("clean"))
	if err := ValidateStepPlanWrite(&plan, repeated); err != nil {
		t.Fatalf("repeating the final review refused: %v", err)
	}
	if !WorkflowStepPlanComplete(repeated) {
		t.Fatal("a clean repeated final review leaves the plan incomplete")
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
