package store

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanReadIntakeAndFrontier(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `
CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,instance_state TEXT,workflow_type TEXT,step_plan TEXT,prelude_state TEXT,completion_payload TEXT);
CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,premise TEXT,required_evidence TEXT,route_conventions TEXT,spec_mandate TEXT,law_modifies TEXT,rigor_class TEXT,decision_domains TEXT,approved_at TEXT,approved_by TEXT,superseded_by INTEGER,changes_product_truth INTEGER);
CREATE TABLE workflow_delivery_state(work_id TEXT,assertion_seq INTEGER,assertion_id TEXT,actor TEXT,occurred_at TEXT,payload TEXT);
CREATE TABLE workflow_backlog_alignment(work_id TEXT,searched TEXT,outcome TEXT,related_work_id TEXT);
CREATE TABLE workflow_proposal_records(work_id TEXT,work_version INTEGER,problem TEXT,affected TEXT,stakes TEXT,user_outcomes TEXT,constraints TEXT,open_questions TEXT,recorded_at TEXT);
CREATE TABLE workflow_candidate_sets(work_id TEXT,contract_version INTEGER,candidate_ref TEXT);
CREATE TABLE workflow_plan_records(work_id TEXT,work_version INTEGER,payload TEXT,recorded_at TEXT,actor TEXT,event_ref TEXT);
CREATE TABLE workflow_removal_scopes(work_id TEXT,work_version INTEGER,payload TEXT,recorded_at TEXT,actor TEXT,event_ref TEXT);
CREATE TABLE work_items(id TEXT,updated_at TEXT);
INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake');`)
	if err != nil {
		t.Fatal(err)
	}
	read := func() WorkflowReadProjection {
		t.Helper()
		got, err := readWorkflowProjectionTx(ctx, db, WorkflowReadRequest{WorkID: "work"})
		if err != nil {
			t.Fatalf("read workflow: %v", err)
		}
		return got
	}
	got := read()
	if got.CurrentStep != "intake" || got.Plan != nil {
		t.Fatalf("intake current step = %q", got.CurrentStep)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_instances SET prelude_state='{"intake":{"outcome":"clarify","open_questions_found":true}}' WHERE work_id='work'`); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.CurrentStep != "clarify" || got.Prelude == nil {
		t.Fatalf("clarify projection = %+v", got)
	}
	plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanFix)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',workflow_type='fix',step_plan=? WHERE work_id='work'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.CurrentStep != WorkflowStepPlanFrontier(plan)[0].ID || got.Plan == nil || len(got.Plan.Steps) != len(plan.Steps) || got.Plan.Steps[0].Helper == nil {
		t.Fatalf("frontier current step = %q", got.CurrentStep)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='completed',workflow_type=NULL,step_plan=NULL,prelude_state=NULL WHERE work_id='work'`); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.CurrentStep != "" || got.Plan != nil {
		t.Fatalf("terminal projection = %+v", got)
	}
}
