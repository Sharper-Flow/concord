package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanStepStart(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name, step                               string
		inserted, reviewed, wantError, execution bool
	}{
		{name: "implement", step: "implement", execution: true},
		{name: "unready", step: "verify", wantError: true},
		{name: "undeclared", step: "unknown", wantError: true},
		{name: "inserted", step: "extra", inserted: true},
		{name: "inserted reviewed", step: "extra", inserted: true, reviewed: true, execution: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sqlitedb.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(ctx, `CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,workflow_type TEXT,step_plan TEXT,instance_state TEXT,execution_actor_ref TEXT,execution_model TEXT,started_at TEXT,execution_started_at TEXT)`); err != nil {
				t.Fatal(err)
			}
			plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
			if err != nil {
				t.Fatal(err)
			}
			plan.Steps[0].Attempts = []WorkflowPlanAttempt{{Note: "done", CompletedAt: "2026-01-01T00:00:00Z"}}
			if tc.inserted {
				plan.Steps = append(plan.Steps, WorkflowPlanStep{ID: "extra", After: []string{}, Reviewed: tc.reviewed})
			}
			raw, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,workflow_type,step_plan,instance_state) VALUES('work','change',?,'planned')`, string(raw)); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			err = startWorkflowInstanceStepTx(ctx, tx, "work", tc.step, "actor", "model", at)
			if (err != nil) != tc.wantError {
				t.Fatalf("start error = %v, want error %v", err, tc.wantError)
			}
			if tc.wantError {
				assertFailureKind(t, err, KindIllegalLifecycleTransition)
			}
			var got, state string
			var actor, model, started, execution *string
			if err := tx.QueryRowContext(ctx, `SELECT step_plan,instance_state,execution_actor_ref,execution_model,started_at,execution_started_at FROM workflow_instances WHERE work_id='work'`).Scan(&got, &state, &actor, &model, &started, &execution); err != nil {
				t.Fatal(err)
			}
			if got != string(raw) {
				t.Fatal("start changed plan bytes")
			}
			if tc.wantError {
				if state != "planned" || actor != nil || model != nil || started != nil || execution != nil {
					t.Fatal("refused start changed instance")
				}
				return
			}
			if state != "running" || actor == nil || *actor != "actor" || model == nil || *model != "model" || started == nil || *started != at.Format(time.RFC3339Nano) {
				t.Fatalf("unexpected start projection: %s %v %v %v", state, actor, model, started)
			}
			if (execution != nil) != tc.execution || execution != nil && *execution != at.Format(time.RFC3339Nano) {
				t.Fatalf("execution start = %v", execution)
			}
		})
	}
}
