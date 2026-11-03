package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanActionFolds(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE work_items(id TEXT PRIMARY KEY, version INTEGER, updated_at TEXT, lifecycle TEXT); CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY, workflow_type TEXT, step_plan TEXT, prelude_state TEXT, instance_state TEXT); INSERT INTO work_items VALUES('work',1,'','needed'); INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake')`)
	if err != nil {
		t.Fatal(err)
	}
	version := int64(1)
	fields := func() WorkflowVersionFields {
		next := version + 1
		current := version
		return WorkflowVersionFields{WorkID: "work", ExpectedVersion: &current, ResultingVersion: &next}
	}
	apply := func(fold func(context.Context, *sql.Tx, Event) error, payload any, wantError bool) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		event := Event{SubjectType: SubjectWorkItem, SubjectID: "work", Payload: raw, Seq: 42, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		err = fold(ctx, tx, event)
		if wantError {
			if err == nil {
				t.Fatal("expected refusal")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		} else {
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			version++
		}
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work'`).Scan(&got); err != nil || got != version {
			t.Fatalf("version = %d, want %d: %v", got, version, err)
		}
	}
	read := func() WorkflowStepPlan {
		t.Helper()
		p, err := readWorkflowPlanTx(ctx, db, "work")
		if err != nil || p == nil {
			t.Fatalf("read plan: %v", err)
		}
		return *p
	}
	apply(foldWorkflowIntakeRecorded, workflowIntakeRecordedPayload{WorkflowVersionFields: fields(), workflowIntakeRecord: workflowIntakeRecord{Outcome: WorkflowIntakeProceed}}, false)
	apply(foldWorkflowTypePicked, workflowTypePickedPayload{WorkflowVersionFields: fields(), Type: WorkflowStepPlanChange, Reason: "initial pick"}, false)
	plan := read()
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: plan.Steps[1].ID, Note: "too early"}, true)
	apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "extra", After: []string{plan.Steps[0].ID}, Reviewed: true, Reason: "review needed"}, false)
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: plan.Steps[0].ID, Note: "done"}, false)
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: "extra", Note: "reviewed"}, true)
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: "extra", Note: "reviewed", Reviewer: "reviewer", Result: "pass", Commit: "commit", Link: "link"}, false)
	apply(foldWorkflowTypePicked, workflowTypePickedPayload{WorkflowVersionFields: fields(), Type: WorkflowStepPlanFix, Reason: "switch type"}, false)
	plan = read()
	step, ok := stepByID(plan.Steps, "extra")
	if !ok || len(step.Attempts) != 1 || step.Attempts[0].Reviewer != "reviewer" || step.Attempts[0].CompletionSeq != 42 || plan.Type != WorkflowStepPlanFix {
		t.Fatalf("switch lost completed step: %+v", plan)
	}
	template, err := WorkflowStepPlanTemplate(WorkflowStepPlanFix)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range template.Steps {
		if !stepIDExists(plan.Steps, s.ID) {
			t.Fatalf("switch missing %s", s.ID)
		}
	}
}

func TestWorkflowPreludeFoldsMigration183(t *testing.T) {
	for _, clarify := range []bool{false, true} {
		t.Run(map[bool]string{false: "proceed", true: "clarify"}[clarify], func(t *testing.T) {
			useStampedBuild(t)
			db, _ := terminalStepsMigrationFixture(t)
			advanceHistoricalPrefix(t, db, 183)
			ctx := t.Context()
			var version int64
			if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-upgrade-to'`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			fields := func() WorkflowVersionFields {
				current, next := version, version+1
				return WorkflowVersionFields{WorkID: "work-upgrade-to", ExpectedVersion: &current, ResultingVersion: &next}
			}
			apply := func(fold projectionMutation, payload any, refuse bool) {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO write_guard VALUES(1)`); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				event := Event{SubjectType: SubjectWorkItem, SubjectID: "work-upgrade-to", Payload: raw, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
				err = fold(ctx, tx, event)
				if refuse {
					if err == nil {
						tx.Rollback()
						t.Fatal("expected refusal")
					}
					if err = tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				} else {
					if err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if _, err = tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
					version++
				}
				var got int64
				if err = db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-upgrade-to'`).Scan(&got); err != nil || got != version {
					t.Fatalf("version %d want %d: %v", got, version, err)
				}
			}
			pick := func() workflowTypePickedPayload {
				return workflowTypePickedPayload{WorkflowVersionFields: fields(), Type: WorkflowStepPlanChange, Reason: "initial type"}
			}
			clarification := func() workflowClarifyRecordedPayload {
				return workflowClarifyRecordedPayload{WorkflowVersionFields: fields(), workflowClarifyRecord: workflowClarifyRecord{ResolvedQuestions: []string{"question resolved"}, Note: "resolved"}}
			}
			intake := func(outcome WorkflowIntakeOutcome, open bool) workflowIntakeRecordedPayload {
				return workflowIntakeRecordedPayload{WorkflowVersionFields: fields(), workflowIntakeRecord: workflowIntakeRecord{ChecksFound: []string{"freshness"}, Outcome: outcome, OpenQuestionsFound: open}}
			}
			apply(foldWorkflowTypePicked, pick(), true)
			apply(foldWorkflowClarifyRecorded, clarification(), true)
			apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeProceed, true), true)
			apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeClarify, false), true)
			if clarify {
				apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeClarify, true), false)
				apply(foldWorkflowTypePicked, pick(), true)
				apply(foldWorkflowClarifyRecorded, clarification(), false)
				apply(foldWorkflowClarifyRecorded, clarification(), true)
			} else {
				apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeProceed, false), false)
				apply(foldWorkflowClarifyRecorded, clarification(), true)
			}
			apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeProceed, false), true)
			apply(foldWorkflowTypePicked, pick(), false)
			var prelude string
			if err := db.QueryRowContext(ctx, `SELECT prelude_state FROM workflow_instances WHERE work_id='work-upgrade-to'`).Scan(&prelude); err != nil {
				t.Fatal(err)
			}
			var state workflowPreludeState
			if err := json.Unmarshal([]byte(prelude), &state); err != nil || (state.Clarify != nil) != clarify {
				t.Fatalf("prelude %s: %v", prelude, err)
			}
			retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE work_items SET lifecycle='cancelled' WHERE id='work-upgrade-to'; DELETE FROM write_guard`)
			apply(foldWorkflowTypePicked, pick(), true)
			apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "extra", After: []string{"shape"}, Reason: "add step"}, true)
		})
	}
}

func TestWorkflowPreludeNonProceedOutcomes(t *testing.T) {
	for _, outcome := range []WorkflowIntakeOutcome{WorkflowIntakeAlreadyHandled, WorkflowIntakeDecline, WorkflowIntakeSplitOrMerge} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := t.Context()
			db, err := sqlitedb.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(ctx, `CREATE TABLE work_items(id TEXT PRIMARY KEY,version INTEGER,updated_at TEXT,lifecycle TEXT); CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,workflow_type TEXT,step_plan TEXT,prelude_state TEXT,instance_state TEXT); INSERT INTO work_items VALUES('work',1,'','needed'); INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake')`); err != nil {
				t.Fatal(err)
			}
			apply := func(fold projectionMutation, payload any) error {
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				err = fold(ctx, tx, Event{SubjectType: SubjectWorkItem, SubjectID: "work", Payload: raw, OccurredAt: time.Now()})
				if err != nil {
					tx.Rollback()
					return err
				}
				return tx.Commit()
			}
			one, two, three := int64(1), int64(2), int64(3)
			if err := apply(foldWorkflowIntakeRecorded, workflowIntakeRecordedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &one, ResultingVersion: &two}, workflowIntakeRecord: workflowIntakeRecord{Outcome: outcome}}); err != nil {
				t.Fatal(err)
			}
			if err := apply(foldWorkflowTypePicked, workflowTypePickedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work", ExpectedVersion: &two, ResultingVersion: &three}, Type: WorkflowStepPlanChange, Reason: "not allowed"}); err == nil {
				t.Fatal("non-proceed outcome allowed a type pick")
			}
			var version int64
			if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work'`).Scan(&version); err != nil || version != two {
				t.Fatalf("version %d: %v", version, err)
			}
		})
	}
}
