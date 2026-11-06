package store

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanPreflightIntakeSnapshot(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE work_items(id TEXT PRIMARY KEY, version INTEGER, lifecycle TEXT); CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY, workflow_type TEXT, step_plan TEXT, prelude_state TEXT, instance_state TEXT); INSERT INTO work_items VALUES('work',1,'needed'); INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake')`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	request := WorkflowActionPreflightRequest{WorkID: "work", ExpectedVersion: 1, StepID: "intake", ActionID: "record_intake", Payload: json.RawMessage(`{"outcome":"proceed"}`), Actor: stepFixtureActor()}
	if _, err := workflowActionPreflightTx(ctx, tx, request); err != nil {
		t.Fatal(err)
	}
	request.StepID = "other"
	if _, err := workflowActionPreflightTx(ctx, tx, request); err == nil {
		t.Fatal("preflight accepted a non-current step")
	}
	var version int
	var prelude *string
	if err := tx.QueryRowContext(ctx, `SELECT w.version,i.prelude_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='work'`).Scan(&version, &prelude); err != nil {
		t.Fatal(err)
	}
	if version != 1 || prelude != nil {
		t.Fatalf("preflight changed state: version=%d prelude=%v", version, prelude)
	}
}
