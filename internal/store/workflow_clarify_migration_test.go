package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkflowClarifyMigrationRetainsAndReplacesLegacyAnswers(t *testing.T) {
	useStampedBuild(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "clarify.db"), 188)
	ctx := t.Context()
	const work = "work-clarify-upgrade"
	actorRef := "actor:" + strings.Repeat("a", 64)
	legacy := `{"intake":{"outcome":"clarify","open_questions_found":true},"clarify":{"resolved_questions":["é"],"note":"Resolved."}}`
	waiting := ` { "intake": { "outcome": "clarify", "open_questions_found": true } } `
	untouched := ` { "intake": { "outcome": "proceed" } } `
	plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps = plan.Steps[1:]
	plan.Steps[0].After = []string{}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planBytes := " \n" + string(planJSON) + " \n"
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES(?,'task','Clarify upgrade','in_progress',1,'now','now')`, work)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal','client','agent','session','agent','now')`, actorRef)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,instance_state,prelude_state) VALUES(?,'running',?)`, work, legacy)
	for _, id := range []string{"planned", "untouched", "waiting"} {
		retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES(?,'task','Migration fixture','in_progress',1,'now','now')`, id)
	}
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,instance_state,workflow_type,step_plan,prelude_state) VALUES('planned','running','change',?,?)`, planBytes, legacy)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,instance_state,workflow_type,step_plan,prelude_state) VALUES('untouched','running','change',?,?)`, planBytes, untouched)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,instance_state,prelude_state) VALUES('waiting','intake',?)`, waiting)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var columns, guards, breaking int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM pragma_table_info('workflow_clarify_records') WHERE name IN ('work_id','work_version','payload','recorded_at','actor','event_ref')),(SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name='workflow_clarify_records'),(SELECT breaking FROM schema_migrations WHERE version=189)`).Scan(&columns, &guards, &breaking); err != nil || columns != 6 || guards != 3 || breaking != 1 {
		t.Fatalf("migration shape columns=%d guards=%d breaking=%d: %v", columns, guards, breaking, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_clarify_records SET payload='{}' WHERE work_id=?`, work); err == nil {
		t.Fatal("clarification record accepted an unguarded write")
	}
	var prelude, payload string
	var recorded, actor, eventRef sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT i.prelude_state,c.payload,c.recorded_at,c.actor,c.event_ref FROM workflow_instances i JOIN workflow_clarify_records c ON c.work_id=i.work_id WHERE i.work_id=?`, work).Scan(&prelude, &payload, &recorded, &actor, &eventRef); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prelude, `"outcome":"proceed"`) || strings.Contains(prelude, `"clarify"`) || !strings.Contains(payload, `é`) || recorded.Valid || actor.Valid || eventRef.Valid {
		t.Fatalf("migration lost legacy record: prelude=%s payload=%s provenance=%v/%v/%v", prelude, payload, recorded, actor, eventRef)
	}
	for _, id := range []string{"planned", "untouched"} {
		var gotPlan, gotPrelude string
		if err := db.QueryRowContext(ctx, `SELECT step_plan,prelude_state FROM workflow_instances WHERE work_id=?`, id).Scan(&gotPlan, &gotPrelude); err != nil {
			t.Fatal(err)
		}
		if gotPlan != planBytes {
			t.Errorf("%s plan bytes changed: %q", id, gotPlan)
		}
		if id == "untouched" && gotPrelude != untouched {
			t.Errorf("untouched prelude changed: %q", gotPrelude)
		}
		if id == "planned" && (!strings.Contains(gotPrelude, `"outcome":"proceed"`) || strings.Contains(gotPrelude, `"clarify"`)) {
			t.Errorf("planned prelude not rewritten: %q", gotPrelude)
		}
	}
	var waitingPrelude string
	var waitingPlan sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT prelude_state,step_plan FROM workflow_instances WHERE work_id='waiting'`).Scan(&waitingPrelude, &waitingPlan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waitingPrelude, `"outcome":"proceed"`) || waitingPlan.Valid {
		t.Errorf("waiting pre-type item: prelude=%q plan=%v", waitingPrelude, waitingPlan)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, work).Scan(&version); err != nil {
		t.Fatal(err)
	}
	apply := func(fold projectionMutation, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		event := Event{EventID: "clarify-migration-replacement", SubjectType: SubjectWorkItem, SubjectID: work, Actor: actorRef, Payload: raw, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		if err := fold(ctx, tx, event); err != nil {
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
	fields := func() WorkflowVersionFields {
		before, after := version, version+1
		return WorkflowVersionFields{WorkID: work, ExpectedVersion: &before, ResultingVersion: &after}
	}
	apply(foldWorkflowTypePicked, workflowTypePickedPayload{WorkflowVersionFields: fields(), Type: WorkflowStepPlanChange, Reason: "post-upgrade type"})
	apply(foldWorkflowStepCompleted, workflowStepCompletedPayload{WorkflowVersionFields: fields(), StepID: "clarify", Note: "Legacy answers retained."})
	apply(foldWorkflowClarifyRecorded, workflowClarifyRecordedPayload{WorkflowVersionFields: fields(), Document: json.RawMessage(`{"resolved_questions":["Question? Operator: revised answer."],"note":"Updated clarification."}`)})
	if err := db.QueryRowContext(ctx, `SELECT payload,recorded_at,actor,event_ref FROM workflow_clarify_records WHERE work_id=?`, work).Scan(&payload, &recorded, &actor, &eventRef); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "revised answer") || !recorded.Valid || !actor.Valid || actor.String != actorRef || !eventRef.Valid {
		t.Fatalf("replacement payload=%s provenance=%v/%v/%v", payload, recorded, actor, eventRef)
	}
}
