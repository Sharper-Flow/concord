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
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work")
	db := s.db
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope := newWriteScope(tx.tx)
		if err := scope.enter(ctx); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake')`); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard VALUES(1)`); err != nil {
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
			if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
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
	projection, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: "work"})
	if err != nil || projection.Prelude == nil || projection.Prelude.TypePickReason != "initial pick" {
		t.Fatalf("initial pick reason not readable: %+v: %v", projection.Prelude, err)
	}
	plan := read()
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: plan.Steps[1].ID, Note: "too early"}, true)
	apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "extra", After: []string{plan.Steps[0].ID}, Reviewed: true, Reason: "review needed"}, false)
	var rawPlan string
	if err := db.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work'`).Scan(&rawPlan); err != nil {
		t.Fatal(err)
	}
	var planDocument struct {
		Steps []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(rawPlan), &planDocument); err != nil || planDocument.Steps[len(planDocument.Steps)-1].Reason != "review needed" {
		t.Fatalf("inserted reason not retained: %s: %v", rawPlan, err)
	}
	request := WorkflowActionExecutionRequest{WorkID: "work", ExpectedVersion: version, ActionID: "record_plan", Payload: json.RawMessage(testWorkflowPlanDocument), Actor: stepFixtureActor(), OperationID: "fold-plan-op", IdempotencyIdentity: "fold-plan-key", PrincipalRef: "principal-1", RequestID: "fold-plan-request", AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
	var result WorkflowActionExecutionResult
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var err error
		result, err = ApplyWorkflowActionTx(ctx, tx, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	version = result.ResultingVersion
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: plan.Steps[0].ID, Note: "done"}, false)
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: "extra", Note: "reviewed"}, true)
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: "extra", Note: "reviewed", Reviewer: "concord-review", Result: "pass", Commit: "commit", Link: "link"}, false)
	apply(foldWorkflowTypePicked, workflowTypePickedPayload{WorkflowVersionFields: fields(), Type: WorkflowStepPlanFix, Reason: "switch type"}, false)
	var rawPrelude string
	if err := db.QueryRowContext(ctx, `SELECT prelude_state FROM workflow_instances WHERE work_id='work'`).Scan(&rawPrelude); err != nil {
		t.Fatal(err)
	}
	var prelude struct {
		TypePickReason string `json:"type_pick_reason"`
	}
	if err := json.Unmarshal([]byte(rawPrelude), &prelude); err != nil || prelude.TypePickReason != "switch type" {
		t.Fatalf("type pick reason not retained: %s: %v", rawPrelude, err)
	}
	plan = read()
	for _, ancestor := range plan.Steps {
		if ancestor.Reviewed {
			apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "after-review", After: []string{ancestor.ID}, Reason: "additional work"}, false)
			inserted, _ := stepByID(read().Steps, "after-review")
			if !inserted.Reviewed {
				t.Fatal("inserted descendant bypassed review")
			}
			apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "after-inserted", After: []string{"after-review"}, Reason: "additional follow-up"}, false)
			transitive, _ := stepByID(read().Steps, "after-inserted")
			if !transitive.Reviewed {
				t.Fatal("transitive inserted descendant bypassed review")
			}
			break
		}
	}
	step, ok := stepByID(plan.Steps, "extra")
	if !ok || len(step.Attempts) != 1 || step.Attempts[0].Reviewer != "concord-review" || step.Attempts[0].Result != "pass" || plan.Type != WorkflowStepPlanFix {
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

func TestWorkflowPlanReviewRecordedBinding(t *testing.T) {
	useStampedBuild(t)
	db, _ := terminalStepsMigrationFixture(t)
	advanceHistoricalPrefix(t, db, 183)
	const work = "work-upgrade-to"
	for _, tc := range []struct {
		name, step, result, mutation string
		refuse                       bool
	}{
		{"intermediate pass", "implement", "pass", "", false},
		{"intermediate fixed", "implement", "fixed", "", false},
		{"final clean pass", "review", "pass", "", false},
		{"final fixed repeats", "review", "fixed", "", false},
		{"wrong reviewer", "implement", "pass", "reviewer", true},
		{"missing commit", "implement", "pass", "commit", true},
		{"missing link", "implement", "pass", "link", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, `INSERT INTO write_guard VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			plan := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
			plan.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: retirementUpgradeTime}}
			if tc.step == "review" {
				plan.Steps[1].Attempts = []WorkflowPlanAttempt{reviewedAttempt("done")}
			}
			rawPlan, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET workflow_type='change',step_plan=? WHERE work_id=?`, string(rawPlan), work); err != nil {
				t.Fatal(err)
			}
			var version int64
			if err = tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, work).Scan(&version); err != nil {
				t.Fatal(err)
			}
			next := version + 1
			p := workflowStepCompletedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: work, ExpectedVersion: &version, ResultingVersion: &next}, StepID: tc.step, Note: "review complete", Reviewer: "concord-review", Result: tc.result, Commit: "commit:fixture", Link: "event:review"}
			switch tc.mutation {
			case "reviewer":
				p.Reviewer = "actor:coordinator"
			case "commit":
				p.Commit = ""
			case "link":
				p.Link = ""
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			err = foldWorkflowStepCompleted(ctx, tx, Event{SubjectType: SubjectWorkItem, SubjectID: work, Payload: raw, Seq: 42, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
			if (err != nil) != tc.refuse {
				t.Fatalf("fold refusal=%v want %v: %v", err != nil, tc.refuse, err)
			}
			if !tc.refuse {
				stored, err := readWorkflowPlanTx(ctx, tx, work)
				if err != nil {
					t.Fatal(err)
				}
				step, ok := stepByID(stored.Steps, tc.step)
				if !ok || len(step.Attempts) != 1 || step.Attempts[0].Reviewer != p.Reviewer || step.Attempts[0].Commit != p.Commit || step.Attempts[0].Link != p.Link || step.Attempts[0].Result != p.Result {
					t.Fatalf("stored association: %+v", step)
				}
				if tc.step == "review" && WorkflowStepPlanComplete(*stored) != (tc.result == "pass") {
					t.Fatal("final review repeat state disagrees with result")
				}
			}
		})
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
			if _, err := db.ExecContext(ctx, `CREATE TABLE work_items(id TEXT PRIMARY KEY,version INTEGER,updated_at TEXT,lifecycle TEXT,transition_from TEXT,transition_reason TEXT,transition_actor TEXT,transition_evidence_refs TEXT,transition_seq INTEGER,transition_version INTEGER); CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,workflow_type TEXT,step_plan TEXT,prelude_state TEXT,instance_state TEXT); INSERT INTO work_items VALUES('work',1,'','needed','','','',  '[]',0,1); INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake')`); err != nil {
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
