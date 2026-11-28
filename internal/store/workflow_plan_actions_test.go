package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowInsertBeforeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, payload, detail string
		kind                  FailureKind
	}{
		{"unknown", `{"id":"extra","after":["clarify"],"before":["missing"],"reason":"review"}`, `before step "missing" does not exist`, KindInvalidOperation},
		{"completed", `{"id":"extra","after":["clarify"],"before":["clarify"],"reason":"review"}`, `before step "clarify" has a completion attempt`, KindInvalidOperation},
		{"cycle", `{"id":"extra","after":["implement"],"before":["implement"],"reason":"review"}`, "cycle", KindInvalidOperation},
		{"reused id", `{"id":"plan","after":["clarify"],"before":["implement"],"reason":"review"}`, "duplicated", KindInvalidOperation},
		{"duplicate before", `{"id":"extra","after":["clarify"],"before":["implement","implement"],"reason":"review"}`, "before", KindInvalidPayload},
		{"empty before", `{"id":"extra","after":["clarify"],"before":[],"reason":"review"}`, "before", KindInvalidPayload},
		{"too many", `{"id":"extra","after":["clarify"],"before":["aa","ab","ac","ad","ae","af","ag","ah","ai","aj","ak","al","am","an","ao","ap","aq"],"reason":"review"}`, "before", KindInvalidPayload},
		{"invalid id", `{"id":"extra","after":["clarify"],"before":["not/an/id"],"reason":"review"}`, "before", KindInvalidPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const workID = "work-insert-before-refusal"
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "plan", nil)
			defer f.store.Close()
			var version int64
			var plan string
			if err := f.store.db.QueryRowContext(t.Context(), `SELECT w.version,i.step_plan FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &plan); err != nil {
				t.Fatal(err)
			}
			err := runVerdictAction(t, f.store, workID, "insert_step", json.RawMessage(tc.payload))
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind || !strings.Contains(failure.Detail, tc.detail) {
				t.Fatalf("refusal = %v; want %q", err, tc.detail)
			}
			var afterVersion int64
			var afterPlan string
			if err := f.store.db.QueryRowContext(t.Context(), `SELECT w.version,i.step_plan FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&afterVersion, &afterPlan); err != nil {
				t.Fatal(err)
			}
			if version != afterVersion || plan != afterPlan {
				t.Fatal("refusal changed version or plan")
			}
		})
	}
}

func TestWorkflowInsertBeforeUnstartedStep(t *testing.T) {
	const workID = "work-insert-before"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "plan", nil)
	defer f.store.Close()
	err := runVerdictAction(t, f.store, workID, "insert_step", json.RawMessage(`{"id":"plan-review","after":["clarify"],"before":["implement"],"helper":"concord-review","reviewed":true,"reason":"review plan"}`))
	if err != nil {
		t.Fatalf("insert before implement: %v", err)
	}
	plan, err := readWorkflowPlanTx(t.Context(), f.store.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	step, found := stepByID(plan.Steps, "implement")
	if !found || len(step.After) != 2 || step.After[1] != "plan-review" {
		t.Fatalf("implement edges = %v", step.After)
	}
}

func TestWorkflowInsertBeforeFrontier(t *testing.T) {
	for _, planType := range []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix} {
		for _, closeFirst := range []bool{false, true} {
			name := string(planType) + "-plan-open"
			if closeFirst {
				name = string(planType) + "-plan-closed"
			}
			t.Run(name, func(t *testing.T) {
				const workID = "work-insert-frontier"
				f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, planType, "plan", nil)
				defer f.store.Close()
				closePlan := func() {
					t.Helper()
					if err := runVerdictAction(t, f.store, workID, "record_plan", json.RawMessage(testWorkflowPlanDocument)); err != nil {
						t.Fatalf("record plan: %v", err)
					}
					if err := runVerdictAction(t, f.store, workID, "complete_step", json.RawMessage(`{"step_id":"plan","note":"plan complete"}`)); err != nil {
						t.Fatalf("complete plan: %v", err)
					}
				}
				if closeFirst {
					closePlan()
				}
				if err := runVerdictAction(t, f.store, workID, "insert_step", json.RawMessage(`{"id":"plan-review","after":["clarify"],"before":["implement"],"helper":"concord-review","reviewed":true,"reason":"review plan"}`)); err != nil {
					t.Fatalf("insert review: %v", err)
				}
				if err := runVerdictAction(t, f.store, workID, "start_execution", json.RawMessage(`{}`)); !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(err), "frontier") {
					t.Fatalf("start before review = %v; want frontier refusal", err)
				}
				if err := runVerdictAction(t, f.store, workID, "complete_step", json.RawMessage(`{"step_id":"plan-review","note":"reviewed"}`)); !hasFailureKind(err, KindInvalidPayload) {
					t.Fatalf("complete without review receipt = %v; want invalid_payload", err)
				}
				if !closeFirst {
					closePlan()
					if err := runVerdictAction(t, f.store, workID, "start_execution", json.RawMessage(`{}`)); !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(errDetail(err), "frontier") {
						t.Fatalf("start after plan before review = %v; want frontier refusal", err)
					}
				}
				if err := runVerdictAction(t, f.store, workID, "record_review", json.RawMessage(`{"step_id":"plan-review","note":"review complete","reviewer":"concord-review","result":"pass","commit":"commit:fixture","link":"https://example.com/review"}`)); err != nil {
					t.Fatalf("record and complete review: %v", err)
				}
				if err := runVerdictAction(t, f.store, workID, "start_execution", json.RawMessage(`{}`)); err != nil {
					t.Fatalf("start after review: %v", err)
				}
			})
		}
	}
}

func TestWorkflowInsertBeforeStartedOrCheckpointed(t *testing.T) {
	for _, action := range []string{"start_execution", "checkpoint_execution"} {
		t.Run(action, func(t *testing.T) {
			const workID = "work-insert-observed"
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", nil)
			defer f.store.Close()
			if err := runVerdictAction(t, f.store, workID, action, json.RawMessage(`{}`)); err != nil {
				t.Fatalf("record %s: %v", action, err)
			}
			var starts, checkpoints int
			if err := f.store.db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM workflow_step_epochs WHERE work_id=? AND step_id='implement' AND latest_start_seq>0),(SELECT count(*) FROM workflow_checkpoints WHERE work_id=? AND step_id='implement')`, workID, workID).Scan(&starts, &checkpoints); err != nil {
				t.Fatal(err)
			}
			if action == "start_execution" && (starts != 1 || checkpoints != 0) || action == "checkpoint_execution" && (starts != 0 || checkpoints != 1) {
				t.Fatalf("fixture starts=%d checkpoints=%d", starts, checkpoints)
			}
			var version, afterVersion int64
			var plan, afterPlan string
			query := `SELECT w.version,i.step_plan FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`
			if err := f.store.db.QueryRowContext(t.Context(), query, workID).Scan(&version, &plan); err != nil {
				t.Fatal(err)
			}
			err := runVerdictAction(t, f.store, workID, "insert_step", json.RawMessage(`{"id":"extra","after":["clarify"],"before":["implement"],"reason":"review"}`))
			detail := `before step "implement" has an execution start`
			if action == "checkpoint_execution" {
				detail = `before step "implement" has a checkpoint`
			}
			if !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(errDetail(err), detail) {
				t.Fatalf("insert refusal = %v; want %s", err, detail)
			}
			if err := f.store.db.QueryRowContext(t.Context(), query, workID).Scan(&afterVersion, &afterPlan); err != nil {
				t.Fatal(err)
			}
			if version != afterVersion || plan != afterPlan {
				t.Fatal("refusal changed version or plan")
			}
		})
	}
}

func TestWorkflowInsertUnreviewedHelperAfterReviewedStep(t *testing.T) {
	const workID = "work-insert-unreviewed-helper"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
	defer f.store.Close()
	if err := runVerdictAction(t, f.store, workID, "insert_step", json.RawMessage(`{"id":"helper","after":["implement"],"helper":"concord-implement","reviewed":false,"reason":"additional work"}`)); err != nil {
		t.Fatalf("insert helper: %v", err)
	}
	plan, err := readWorkflowPlanTx(t.Context(), f.store.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: %v", err)
	}
	step, found := stepByID(plan.Steps, "helper")
	if !found || step.Reviewed {
		t.Fatalf("inserted helper = %+v, found=%v; want reviewed=false", step, found)
	}
	if err := runVerdictAction(t, f.store, workID, "complete_step", json.RawMessage(`{"step_id":"helper","note":"helper completed"}`)); err != nil {
		t.Fatalf("complete helper with note: %v", err)
	}
	plan, err = readWorkflowPlanTx(t.Context(), f.store.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read completed plan: %v", err)
	}
	step, found = stepByID(plan.Steps, "helper")
	if !found || len(step.Attempts) != 1 || step.Attempts[0].Note != "helper completed" {
		t.Fatalf("completed helper = %+v, found=%v", step, found)
	}
}

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
	var preludeJSON string
	if err := db.QueryRowContext(ctx, `SELECT prelude_state FROM workflow_instances WHERE work_id='work'`).Scan(&preludeJSON); err != nil {
		t.Fatal(err)
	}
	var initialPrelude workflowPreludeState
	if err := json.Unmarshal([]byte(preludeJSON), &initialPrelude); err != nil || initialPrelude.TypePickReason != "initial pick" {
		t.Fatalf("initial pick reason not readable: %+v: %v", initialPrelude, err)
	}
	plan := read()
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: plan.Steps[1].ID, Note: "too early"}, true)
	apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "extra", After: []string{plan.Steps[0].ID}, Reviewed: true, Reason: "review needed"}, false)
	insertedReviewed, found := stepByID(read().Steps, "extra")
	if !found || !insertedReviewed.Reviewed {
		t.Fatal("inserted step did not retain reviewed=true")
	}
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
	request := WorkflowActionExecutionRequest{WorkID: "work", ExpectedVersion: version, ActionID: "record_clarify", Payload: json.RawMessage(`{"resolved_questions":[],"note":"No operator questions remain."}`), Actor: stepFixtureActor(), OperationID: "fold-clarify-op", IdempotencyIdentity: "fold-clarify-key", PrincipalRef: "principal-1", RequestID: "fold-clarify-request", AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
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
	request.ExpectedVersion, request.ActionID, request.Payload = version, "record_plan", json.RawMessage(testWorkflowPlanDocument)
	request.OperationID, request.IdempotencyIdentity, request.RequestID = "fold-plan-op", "fold-plan-key", "fold-plan-request"
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var err error
		result, err = ApplyWorkflowActionTx(ctx, tx, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	version = result.ResultingVersion
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
			inserted, found := stepByID(read().Steps, "after-review")
			if !found || inserted.Reviewed {
				t.Fatal("inserted step did not retain reviewed=false")
			}
			apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "after-inserted", After: []string{"after-review"}, Reason: "additional follow-up"}, false)
			transitive, found := stepByID(read().Steps, "after-inserted")
			if !found || transitive.Reviewed {
				t.Fatal("transitive inserted step did not retain reviewed=false")
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
	db := terminalStepsMigrationFixture(t)
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
			plan.Steps[1].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: retirementUpgradeTime}}
			if tc.step == "review" {
				plan.Steps[2].Attempts = []WorkflowPlanAttempt{reviewedAttempt("done")}
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

func TestWorkflowPreludeFolds(t *testing.T) {
	useStampedBuild(t)
	db := terminalStepsMigrationFixture(t)
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
	intake := func(outcome WorkflowIntakeOutcome) workflowIntakeRecordedPayload {
		return workflowIntakeRecordedPayload{WorkflowVersionFields: fields(), workflowIntakeRecord: workflowIntakeRecord{Outcome: outcome}}
	}
	apply(foldWorkflowTypePicked, pick(), true)
	apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeOutcome("clarify")), true)
	apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeProceed), false)
	apply(foldWorkflowIntakeRecorded, intake(WorkflowIntakeProceed), true)
	apply(foldWorkflowTypePicked, pick(), false)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE work_items SET lifecycle='cancelled' WHERE id='work-upgrade-to'; DELETE FROM write_guard`)
	apply(foldWorkflowTypePicked, pick(), true)
	apply(foldWorkflowStepInserted, workflowStepInsertedPayload{WorkflowVersionFields: fields(), ID: "extra", After: []string{"shape"}, Reason: "add step"}, true)
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
