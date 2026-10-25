package store

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestWorkflowArtifactBackfillPreservesPostVerdictProduction(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const work = "post-verdict-production"
	definition := mustBuiltinDefinition(t, "workflow.research").Definition
	pinWorkflowAtStep(t, s, work, definition.Ref, "conclude")
	stalenessSeedContract(t, s, work)
	stalenessEvent(t, s, work, WorkflowVerdictRecorded, stalenessVerdictPayload("outcome_mismatch"))
	stalenessEvent(t, s, work, WorkflowActionCompleted, stalenessActionPayload("late-producer", "record_finding"))
	queries := []string{`SELECT json_array(work_id,step_id,action_id,production_seq,verdict_seq,verdict_production_seq) FROM workflow_artifact_actions ORDER BY work_id,step_id,action_id`}
	want := workerFactsQueryRows(t, s, queries)
	assertWorkflowCurrentFactsWithIndependentMigration142(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill changed post-verdict production: got %v, want %v", got, want)
	}
}

func TestWorkflowWorkerProductionCurrentOrigin(t *testing.T) {
	for _, test := range []struct {
		name, mutation       string
		stale, through, want int64
	}{
		{name: "producing origin", stale: 2, want: 8},
		{name: "old authorization", stale: 3},
		{name: "old capability dispatch", stale: 2, mutation: `UPDATE worker_attempts SET dispatch_seq=2`},
		{name: "late authorization", stale: 2, mutation: `UPDATE worker_authorizations SET seq=8`},
		{name: "late dispatch", stale: 2, mutation: `UPDATE worker_attempts SET dispatch_seq=8`},
		{name: "wrong authorization step", stale: 2, mutation: `UPDATE worker_authorizations SET payload='{"step_id":"other"}'`},
		{name: "wrong acceptance step", stale: 2, mutation: `UPDATE worker_attempt_dispositions SET first_acceptance_payload='{"step_id":"other"}'`},
		{name: "repeat preserves original origin", stale: 2, through: 9, want: 8, mutation: `UPDATE worker_attempt_dispositions SET seq=80,payload='{"step_id":"other"}'`},
		{name: "review is not production", stale: 2, mutation: `UPDATE worker_attempts SET dispatch_payload='{"capability_class":"review"}'`},
		{name: "missing capability", stale: 2, mutation: `UPDATE worker_attempts SET dispatch_payload='{}'`},
		{name: "unrelated attempt", stale: 2, mutation: `UPDATE worker_attempt_dispositions SET attempt_id='other'`},
		{name: "missing authorization", stale: 2, mutation: `DELETE FROM worker_authorizations`},
		{name: "evidence acceptance", stale: 2, mutation: `UPDATE worker_attempt_dispositions SET action_id='accept_worker_evidence'`},
		{name: "before acceptance", stale: 2, through: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open(driverName, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			// Deliberately no event log: each origin has its own current owner.
			if _, err := db.Exec(`
CREATE TABLE workflow_artifact_actions(work_id TEXT,step_id TEXT,action_id TEXT,production_seq INTEGER,verdict_seq INTEGER,verdict_production_seq INTEGER);
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,dispatch_seq INTEGER,dispatch_payload TEXT);
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT);
CREATE TABLE worker_attempt_dispositions(work_id TEXT,attempt_id TEXT,action_id TEXT,seq INTEGER,payload TEXT,first_acceptance_seq INTEGER,first_acceptance_payload TEXT);
INSERT INTO worker_authorizations VALUES('work','attempt',3,'{"step_id":"execution"}');
INSERT INTO worker_attempts VALUES('work','attempt',4,'{"capability_class":"implementation"}');
INSERT INTO worker_attempt_dispositions VALUES('work','attempt','accept_worker_result',8,'{"step_id":"execution"}',8,'{"step_id":"execution"}');
`); err != nil {
				t.Fatal(err)
			}
			if test.mutation != "" {
				if _, err := db.Exec(test.mutation); err != nil {
					t.Fatal(err)
				}
			}
			definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
			got, err := workflowArtifactProductionFrontier(t.Context(), db, "work", definition, "execution", test.stale, test.through, "test")
			if err != nil || got != test.want {
				t.Fatalf("production frontier = %d,%v; want %d", got, err, test.want)
			}
		})
	}
}

func TestWorkflowAcceptedDeliveryUsesFirstAcceptance(t *testing.T) {
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	// A repeated acceptance at 80 must not hide the first acceptance at 8.
	if _, err := db.Exec(`
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,dispatch_seq INTEGER,dispatch_payload TEXT,lifecycle_state TEXT);
CREATE TABLE worker_attempt_dispositions(work_id TEXT,attempt_id TEXT,action_id TEXT,seq INTEGER,payload TEXT,first_acceptance_seq INTEGER,first_acceptance_payload TEXT);
INSERT INTO worker_attempts VALUES('work','attempt',4,'{}','completed');
INSERT INTO worker_attempt_dispositions VALUES('work','attempt','accept_worker_result',80,'{}',8,'{}');
`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		through int64
		want    bool
	}{{through: 7}, {through: 9, want: true}, {through: 80, want: true}} {
		dispatch, accepted, err := workflowAcceptedWorkerDelivery(t.Context(), db, "work", test.through, "test")
		if err != nil || dispatch != 4 || accepted != test.want {
			t.Fatalf("accepted delivery through %d = %d,%v,%v; want 4,%v", test.through, dispatch, accepted, err, test.want)
		}
	}
}
