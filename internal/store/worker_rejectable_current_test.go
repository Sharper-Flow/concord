package store

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func rejectableCurrentDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload TEXT);
CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,latest_non_dispatch_start_seq INTEGER);
INSERT INTO workflow_instances VALUES('work',0);
CREATE TABLE workflow_contracts(work_id TEXT,supersession_seq INTEGER);
CREATE TABLE workflow_phase_exits(work_id TEXT,action_id TEXT,exit_seq INTEGER,PRIMARY KEY(work_id,action_id));
CREATE TABLE workflow_step_epochs(work_id TEXT,step_id TEXT,latest_start_seq INTEGER,latest_start_event_id TEXT,latest_start_payload TEXT,PRIMARY KEY(work_id,step_id));
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,lifecycle_state TEXT,readback_model TEXT,dispatch_seq INTEGER,dispatch_payload TEXT,PRIMARY KEY(work_id,attempt_id));
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT,start_seq INTEGER,start_payload TEXT,PRIMARY KEY(work_id,attempt_id));
CREATE TABLE worker_attempt_dispositions(work_id TEXT,attempt_id TEXT,action_id TEXT,seq INTEGER,payload TEXT,PRIMARY KEY(work_id,attempt_id,action_id));`); err != nil {
		t.Fatal(err)
	}
	return db
}

func rejectableDefinitionForTest(checkpoint bool) WorkflowDefinition {
	step := WorkflowStep{ID: "produce", Actions: []string{"dispatch_worker"}}
	if checkpoint {
		step.Kind = WorkflowStepHumanCheckpoint
	}
	return WorkflowDefinition{
		StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{step}},
		ActionDefinitions: []WorkflowActionDefinition{
			{ID: "accept_worker_result", ExecutionMode: ActionAdvance},
			{ID: "advance_elsewhere", ExecutionMode: ActionAdvance},
		},
	}
}

func rejectableEventForTest(t *testing.T, db *sql.DB, seq int64, kind string, payload any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,?)`, seq, kind, string(mustJSONValue(payload))); err != nil {
		t.Fatal(err)
	}
}

func rejectableAttemptForTest(t *testing.T, db *sql.DB, attempt, executor string, startSeq, epoch int64) workflowRejectableResult {
	const step = "produce"
	t.Helper()
	start := map[string]any{"action_id": "dispatch_worker", "step_id": step, "attempt_epoch": epoch}
	auth := map[string]any{"action_id": "dispatch_worker", "step_id": step, "attempt_epoch": epoch, "worker_attempt_id": attempt}
	dispatch := map[string]any{"attempt_id": attempt, "lane_actor_ref": executor}
	if _, err := db.Exec(`INSERT INTO workflow_step_epochs VALUES('work',?,?,?,?) ON CONFLICT(work_id,step_id) DO UPDATE SET latest_start_seq=excluded.latest_start_seq,latest_start_event_id=excluded.latest_start_event_id,latest_start_payload=excluded.latest_start_payload`, step, startSeq, attempt+":start", string(mustJSONValue(start))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worker_authorizations VALUES('work',?,?,?,?,?)`, attempt, startSeq+1, string(mustJSONValue(auth)), startSeq, string(mustJSONValue(start))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worker_attempts VALUES('work',?,'completed','model',?,?)`, attempt, startSeq+2, string(mustJSONValue(dispatch))); err != nil {
		t.Fatal(err)
	}
	rejectableEventForTest(t, db, startSeq, WorkflowActionStarted, start)
	rejectableEventForTest(t, db, startSeq+1, WorkflowActionCompleted, auth)
	rejectableEventForTest(t, db, startSeq+2, WorkerDispatched, dispatch)
	rejectableEventForTest(t, db, startSeq+3, WorkerCompleted, map[string]any{"attempt_id": attempt, "readback_model": "model"})
	return workflowRejectableResult{AttemptID: attempt, AttemptEpoch: epoch, ExecutorRef: executor, DispatchSeq: startSeq + 2, completed: true}
}

func TestRejectableWorkerResultsCurrentWithoutLog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
		want   string
	}{
		{name: "exact authorization survives newer unused start", want: "both"},
		{name: "acceptance consumes only its result", want: "second", mutate: func(t *testing.T, db *sql.DB) {
			for i, action := range []string{"accept_worker_evidence", "record_worker_job"} {
				payload := map[string]any{"action_id": action, "worker_attempt_id": "first"}
				if _, err := db.Exec(`INSERT INTO worker_attempt_dispositions VALUES('work','first',?,?,?)`, action, 10+i, string(mustJSONValue(payload))); err != nil {
					t.Fatal(err)
				}
				rejectableEventForTest(t, db, int64(10+i), WorkflowActionCompleted, payload)
			}
		}},
		{name: "rejection consumes only its result", want: "second", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`INSERT INTO worker_attempt_dispositions VALUES('work','first','reject_worker_result',10,'{}')`); err != nil {
				t.Fatal(err)
			}
			rejectableEventForTest(t, db, 10, WorkflowActionCompleted, map[string]any{"action_id": "reject_worker_result", "worker_attempt_id": "first"})
		}},
		{name: "failed attempt is absent", want: "second", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE worker_attempts SET lifecycle_state='failed' WHERE attempt_id='first'`); err != nil {
				t.Fatal(err)
			}
			rejectableEventForTest(t, db, 10, WorkerFailed, map[string]any{"attempt_id": "first"})
		}},
		{name: "wrong authorization step", want: "second", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE worker_authorizations SET payload=json_set(payload,'$.step_id','elsewhere'),start_payload=json_set(start_payload,'$.step_id','elsewhere') WHERE attempt_id='first'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "empty readback", want: "second", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE worker_attempts SET readback_model='' WHERE attempt_id='first'; UPDATE domain_events SET payload=json_set(payload,'$.readback_model','') WHERE seq=4`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rejectableCurrentDB(t)
			first := rejectableAttemptForTest(t, db, "first", "lane:first", 1, 1)
			second := rejectableAttemptForTest(t, db, "second", "lane:second", 5, 2)
			// A later dispatch start is unused; non-checkpoint rejection
			// must retain both exact older authorization epochs.
			start := map[string]any{"action_id": "dispatch_worker", "step_id": "produce", "attempt_epoch": 3}
			if _, err := db.Exec(`UPDATE workflow_step_epochs SET latest_start_seq=9,latest_start_payload=?`, string(mustJSONValue(start))); err != nil {
				t.Fatal(err)
			}
			rejectableEventForTest(t, db, 9, WorkflowActionStarted, start)
			if tc.mutate != nil {
				tc.mutate(t, db)
			}
			want := []workflowRejectableResult{first, second}
			if tc.want == "second" {
				want = []workflowRejectableResult{second}
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowRejectableWorkerResults(t.Context(), db, "work", rejectableDefinitionForTest(false), "produce", "test", 100)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("results=%+v err=%v, want %+v", got, err, want)
				}
			})
		})
	}
}

func TestRejectableWorkerResultsBoundaryWithoutLog(t *testing.T) {
	for _, boundary := range []string{"non-dispatch start", "supersession", "advance outside step", "request correction", "checkpoint start at another step"} {
		t.Run(boundary, func(t *testing.T) {
			db := rejectableCurrentDB(t)
			rejectableAttemptForTest(t, db, "old", "lane:old", 1, 1)
			checkpoint := boundary == "checkpoint start at another step"
			switch boundary {
			case "non-dispatch start", "checkpoint start at another step":
				action := "record_finding"
				if checkpoint {
					action = "dispatch_worker"
				} else if _, err := db.Exec(`UPDATE workflow_instances SET latest_non_dispatch_start_seq=5`); err != nil {
					t.Fatal(err)
				}
				payload := map[string]any{"action_id": action, "step_id": "elsewhere", "attempt_epoch": 1}
				if _, err := db.Exec(`INSERT INTO workflow_step_epochs VALUES('work','elsewhere',5,'elsewhere:start',?)`, string(mustJSONValue(payload))); err != nil {
					t.Fatal(err)
				}
				rejectableEventForTest(t, db, 5, WorkflowActionStarted, payload)
			case "supersession":
				if _, err := db.Exec(`INSERT INTO workflow_contracts VALUES('work',5)`); err != nil {
					t.Fatal(err)
				}
				rejectableEventForTest(t, db, 5, WorkflowContractSuperseded, map[string]any{"new_contract_version": 2})
			default:
				action := "advance_elsewhere"
				if boundary == "request correction" {
					action = "request_correction"
				}
				if _, err := db.Exec(`INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq) VALUES('work',?,5)`, action); err != nil {
					t.Fatal(err)
				}
				rejectableEventForTest(t, db, 5, WorkflowActionCompleted, map[string]any{"action_id": action, "step_id": "elsewhere"})
			}
			fresh := rejectableAttemptForTest(t, db, "fresh", "lane:fresh", 6, 2)
			disposalWithoutLog(t, db, func() {
				got, err := workflowRejectableWorkerResults(t.Context(), db, "work", rejectableDefinitionForTest(checkpoint), "produce", "test", 100)
				if err != nil || !reflect.DeepEqual(got, []workflowRejectableResult{fresh}) {
					t.Fatalf("results=%+v err=%v, want fresh result %+v", got, err, fresh)
				}
			})
		})
	}
}

func TestRejectableWorkerResultsLegacyAndAuthorizationErrorsWithoutLog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		executor string
		mutate   string
		wantKind FailureKind
	}{
		{name: "executor-less fallback", mutate: `DELETE FROM worker_authorizations`},
		{name: "missing authorization falls back", executor: "lane:first", mutate: `DELETE FROM worker_authorizations`},
		{name: "start missing is invariant failure", executor: "lane:first", mutate: `UPDATE worker_authorizations SET start_seq=0`, wantKind: KindInvariantViolation},
		{name: "epoch mismatch is invariant failure", executor: "lane:first", mutate: `UPDATE worker_authorizations SET payload=json_set(payload,'$.attempt_epoch',2)`, wantKind: KindInvariantViolation},
		{name: "malformed authorization is unavailable", executor: "lane:first", mutate: `UPDATE worker_authorizations SET payload='not json'`, wantKind: KindUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rejectableCurrentDB(t)
			want := rejectableAttemptForTest(t, db, "first", tc.executor, 1, 1)
			if _, err := db.Exec(tc.mutate); err != nil {
				t.Fatal(err)
			}
			want.ExecutorRef = ""
			disposalWithoutLog(t, db, func() {
				got, err := workflowRejectableWorkerResults(t.Context(), db, "work", rejectableDefinitionForTest(false), "produce", "test", 100)
				if tc.wantKind != "" {
					if !hasFailureKind(err, tc.wantKind) {
						t.Fatalf("results=%+v err=%v, want %s", got, err, tc.wantKind)
					}
					if tc.wantKind == KindUnavailable && !strings.Contains(err.Error(), "cannot read the dispatch authorization window") {
						t.Fatalf("unavailable authorization error lost its source: %v", err)
					}
				} else if err != nil || !reflect.DeepEqual(got, []workflowRejectableResult{want}) {
					t.Fatalf("results=%+v err=%v, want legacy result %+v", got, err, want)
				}
			})
		})
	}
}

func TestRejectableWorkerResultsStaleAuthorityWithoutLog(t *testing.T) {
	for _, scenario := range []string{"authorization before boundary", "checkpoint start at another step", "legacy result before current start"} {
		t.Run(scenario, func(t *testing.T) {
			db := rejectableCurrentDB(t)
			executor := "lane:first"
			if scenario == "legacy result before current start" {
				executor = ""
			}
			rejectableAttemptForTest(t, db, "first", executor, 1, 1)
			checkpoint := scenario == "checkpoint start at another step"
			if scenario == "authorization before boundary" {
				if _, err := db.Exec(`INSERT INTO workflow_contracts VALUES('work',5); UPDATE worker_attempts SET dispatch_seq=8; UPDATE domain_events SET seq=8 WHERE seq=3; UPDATE domain_events SET seq=9 WHERE seq=4`); err != nil {
					t.Fatal(err)
				}
				rejectableEventForTest(t, db, 5, WorkflowContractSuperseded, map[string]any{"new_contract_version": 2})
			} else {
				step := "produce"
				if checkpoint {
					step = "elsewhere"
				}
				start := map[string]any{"action_id": "dispatch_worker", "step_id": step, "attempt_epoch": 2}
				if _, err := db.Exec(`INSERT INTO workflow_step_epochs VALUES('work',?,5,'new-start',?) ON CONFLICT(work_id,step_id) DO UPDATE SET latest_start_seq=excluded.latest_start_seq,latest_start_payload=excluded.latest_start_payload`, step, string(mustJSONValue(start))); err != nil {
					t.Fatal(err)
				}
				rejectableEventForTest(t, db, 5, WorkflowActionStarted, start)
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowRejectableWorkerResults(t.Context(), db, "work", rejectableDefinitionForTest(checkpoint), "produce", "test", 100)
				if err != nil || len(got) != 0 {
					t.Fatalf("results=%+v err=%v, want no eligible result", got, err)
				}
			})
		})
	}
}
