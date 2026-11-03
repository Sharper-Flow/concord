package store

import (
	"encoding/json"
	"testing"
)

func TestWorkPinPlanProjection(t *testing.T) {
	useStampedBuild(t)
	db, _ := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 183)
	ctx := t.Context()
	const id = "work-upgrade-to"
	set := func(prelude any, plan *WorkflowStepPlan) {
		t.Helper()
		var preludeJSON, planJSON, typ any
		if prelude != nil {
			raw, err := json.Marshal(prelude)
			if err != nil {
				t.Fatal(err)
			}
			preludeJSON = string(raw)
		}
		if plan != nil {
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			planJSON, typ = string(raw), string(plan.Type)
		}
		retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
		retirementUpgradeExec(t, db, `UPDATE workflow_instances SET prelude_state=?, workflow_type=?, step_plan=? WHERE work_id=?`, preludeJSON, typ, planJSON, id)
		retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	}
	read := func(step, typ string, present, absent []string) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		pin, err := ReadWorkPinTx(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if pin.Step != step || pin.WorkflowType != typ {
			t.Fatalf("step/type = %q/%q, want %q/%q", pin.Step, pin.WorkflowType, step, typ)
		}
		for _, intent := range pin.NextValidIntents {
			if intent.ExpectedVersion != pin.Version {
				t.Fatalf("intent version %d, pin version %d", intent.ExpectedVersion, pin.Version)
			}
		}
		for _, action := range present {
			if !workPinContainsAction(pin.NextValidIntents, action) {
				t.Errorf("missing %s: %+v", action, pin.NextValidIntents)
			}
		}
		for _, action := range absent {
			if workPinContainsAction(pin.NextValidIntents, action) {
				t.Errorf("unexpected %s: %+v", action, pin.NextValidIntents)
			}
		}
	}
	set(nil, nil)
	read("intake", "", []string{"record_intake"}, []string{"pick_type", "complete"})
	clarify := workflowPreludeState{Intake: workflowIntakeRecord{Outcome: WorkflowIntakeClarify, OpenQuestionsFound: true}}
	set(clarify, nil)
	read("clarify", "", []string{"record_clarify"}, []string{"record_intake", "pick_type"})
	clarify.Clarify = &workflowClarifyRecord{ResolvedQuestions: []string{"resolved"}, Note: "resolved"}
	set(clarify, nil)
	read("intake", "", []string{"pick_type"}, []string{"record_clarify"})
	plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	set(clarify, &plan)
	read(plan.Steps[0].ID, string(plan.Type), []string{"insert_step", "pick_type", "complete_step"}, []string{"complete"})
	for i := range plan.Steps {
		plan.Steps[i].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z", Reviewer: "reviewer", Result: "pass", Commit: "commit", Link: "link"}}
		if !plan.Steps[i].Reviewed {
			plan.Steps[i].Attempts[0] = WorkflowPlanAttempt{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}
		}
		set(clarify, &plan)
		if i+1 < len(plan.Steps) {
			read(plan.Steps[i+1].ID, string(plan.Type), []string{"complete_step"}, []string{"complete"})
			if i == 0 {
				plan.Steps = append(plan.Steps, WorkflowPlanStep{ID: "extra", After: []string{plan.Steps[0].ID}})
				set(clarify, &plan)
				read(plan.Steps[1].ID, string(plan.Type), []string{"complete_step", "record_review", "record_delivery", "link_successor", "bind_evidence"}, []string{"complete"})
				plan.Steps = plan.Steps[:len(plan.Steps)-1]
			}
		}
	}
	read(plan.Steps[len(plan.Steps)-1].ID, string(plan.Type), []string{"complete", "insert_step", "pick_type"}, nil)
}
