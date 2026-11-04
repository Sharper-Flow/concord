package store

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanStorage(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY, workflow_type TEXT, step_plan TEXT, instance_state TEXT NOT NULL DEFAULT 'intake'); INSERT INTO workflow_instances(work_id) VALUES('work')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	plan, err := readWorkflowPlanTx(ctx, tx, "work")
	if err != nil || plan != nil {
		t.Fatalf("intake plan = %v, err = %v", plan, err)
	}
	next, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowPlanTx(ctx, tx, "work", next); err != nil {
		t.Fatal(err)
	}
	plan, err = readWorkflowPlanTx(ctx, tx, "work")
	if err != nil || plan == nil || plan.Type != WorkflowStepPlanChange {
		t.Fatalf("stored plan = %v, err = %v", plan, err)
	}
	next.Steps = next.Steps[1:]
	if err := writeWorkflowPlanTx(ctx, tx, "work", next); err == nil {
		t.Fatal("removed a required step")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET workflow_type='fix' WHERE work_id='work'`); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkflowPlanTx(ctx, tx, "work"); err == nil {
		t.Fatal("accepted a mismatched workflow type")
	}
}
