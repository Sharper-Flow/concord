package store

import (
	"database/sql"
	"strings"
	"testing"
)

func workerDisposalCurrentDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE domain_events(seq INTEGER PRIMARY KEY, subject_type TEXT, subject_id TEXT, kind TEXT, payload TEXT);
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,capability_class TEXT,lifecycle_state TEXT,readback_model TEXT,dispatch_seq INTEGER,dispatch_payload TEXT,terminal_seq INTEGER);
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT);
CREATE TABLE worker_attempt_dispositions(work_id TEXT,attempt_id TEXT,seq INTEGER,event_id TEXT,action_id TEXT,payload TEXT,PRIMARY KEY(work_id,attempt_id,action_id));
CREATE TABLE workflow_step_epochs(work_id TEXT,step_id TEXT,latest_start_seq INTEGER,latest_start_event_id TEXT,latest_start_payload TEXT);
CREATE TABLE workflow_phase_exits(work_id TEXT,action_id TEXT,exit_seq INTEGER,PRIMARY KEY(work_id,action_id));
INSERT INTO worker_attempts VALUES('work','first','implementation','completed','model',2,'{"attempt_id":"first","lane_actor_ref":"lane:first"}',3);
INSERT INTO worker_authorizations VALUES('work','first',1,'{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"first"}');
INSERT INTO domain_events VALUES(1,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"first"}');
INSERT INTO domain_events VALUES(2,'work_item','work','worker.dispatched','{"attempt_id":"first","lane_actor_ref":"lane:first"}');
INSERT INTO domain_events VALUES(3,'work_item','work','worker.completed','{"attempt_id":"first"}');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func disposalWithoutLog(t *testing.T, db *sql.DB, check func()) {
	t.Helper()
	check()
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestWorkerFencedWriterWithoutLog(t *testing.T) {
	for _, action := range []string{"", "accept_worker_result", "accept_worker_evidence", "reject_worker_result", "record_worker_failure", "record_worker_job"} {
		t.Run("disposition="+action, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if action != "" {
				raw := string(mustJSONValue(map[string]any{"action_id": action, "worker_attempt_id": "first", "actor_ref": "owner"}))
				if _, err := db.Exec(`INSERT INTO worker_attempt_dispositions VALUES('work','first',4,'dispose',?,?)`, action, raw); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO domain_events VALUES(4,'work_item','work',?,?)`, WorkflowActionCompleted, raw); err != nil {
					t.Fatal(err)
				}
			}
			disposalWithoutLog(t, db, func() {
				writer, live, since, err := workflowFencedWriter(t.Context(), db, "work", "lane:first", 100)
				wantWriter, wantSince := "lane:first", int64(2)
				if action != "" {
					wantWriter, wantSince = "owner", 4
				}
				if err != nil || writer != wantWriter || live != (action == "") || since != wantSince {
					t.Fatalf("writer=%q live=%v since=%d err=%v; want %q live=%v since=%d", writer, live, since, err, wantWriter, action == "", wantSince)
				}
			})
		})
	}
}

func TestWorkerCheckpointWriterWithoutLog(t *testing.T) {
	db := workerDisposalCurrentDB(t)
	if _, err := db.Exec(`INSERT INTO worker_attempt_dispositions VALUES('work','first',4,'dispose','accept_worker_result','{"actor_ref":"owner"}'); INSERT INTO domain_events VALUES(4,'work_item','work','workflow.action_completed','{"action_id":"accept_worker_result","worker_attempt_id":"first","actor_ref":"owner"}')`); err != nil {
		t.Fatal(err)
	}
	disposalWithoutLog(t, db, func() {
		for _, actor := range []string{"owner", "coordinator", "lane:first"} {
			got, err := workflowCheckpointWriterAdmitted(t.Context(), db, "work", "lane:first", actor, false, 100)
			if err != nil || got != (actor != "lane:first") {
				t.Fatalf("actor=%q admitted=%v err=%v", actor, got, err)
			}
		}
	})
}

func TestWorkerFencedWriterLatestDispatchWithoutLog(t *testing.T) {
	db := workerDisposalCurrentDB(t)
	if _, err := db.Exec(`INSERT INTO worker_attempt_dispositions VALUES('work','first',4,'dispose','accept_worker_result','{"actor_ref":"old-owner"}');
INSERT INTO domain_events VALUES(4,'work_item','work','workflow.action_completed','{"action_id":"accept_worker_result","worker_attempt_id":"first","actor_ref":"old-owner"}');
INSERT INTO worker_attempts VALUES('work','latest','review','in_flight','',6,'{"attempt_id":"latest","lane_actor_ref":"lane:latest"}',0);
INSERT INTO worker_authorizations VALUES('work','latest',5,'{"step_id":"refine"}');
INSERT INTO domain_events VALUES(6,'work_item','work','worker.dispatched','{"attempt_id":"latest","lane_actor_ref":"lane:latest"}');`); err != nil {
		t.Fatal(err)
	}
	disposalWithoutLog(t, db, func() {
		writer, live, since, err := workflowFencedWriter(t.Context(), db, "work", "lane:latest", 100)
		if err != nil || writer != "lane:latest" || !live || since != 6 {
			t.Fatalf("writer=%q live=%v since=%d err=%v", writer, live, since, err)
		}
	})
}

func disposalMarkerForTest(t *testing.T, db *sql.DB, action string, seq int64) {
	const attempt = "first"
	t.Helper()
	raw := string(mustJSONValue(map[string]any{"action_id": action, "worker_attempt_id": attempt, "actor_ref": action, "step_id": "refine"}))
	if _, err := db.Exec(`INSERT INTO worker_attempt_dispositions VALUES('work',?,?,?, ?,?)`, attempt, seq, action, action, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,?)`, seq, WorkflowActionCompleted, raw); err != nil {
		t.Fatal(err)
	}
}

func disposalStartForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	const raw = `{"step_id":"refine","attempt_epoch":7}`
	if _, err := db.Exec(`INSERT INTO workflow_step_epochs VALUES('work','refine',10,'start',?)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domain_events VALUES(10,'work_item','work',?,?)`, WorkflowActionStarted, raw); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerFencedWriterLatestDispositionWithoutLog(t *testing.T) {
	db := workerDisposalCurrentDB(t)
	disposalMarkerForTest(t, db, "accept_worker_evidence", 4)
	disposalMarkerForTest(t, db, "record_worker_job", 5)
	disposalWithoutLog(t, db, func() {
		writer, live, since, err := workflowFencedWriter(t.Context(), db, "work", "lane:first", 100)
		if err != nil || writer != "record_worker_job" || live || since != 5 {
			t.Fatalf("writer=%q live=%v since=%d err=%v", writer, live, since, err)
		}
	})
}

func TestWorkerDispatchStepHoldWithoutLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		markers []string
		start   bool
		want    bool
	}{
		{name: "no start", state: "in_flight"},
		{name: "older dispatch", start: true},
		{name: "running", start: true, state: "in_flight", want: true},
		{name: "failed", start: true, state: "failed", want: true},
		{name: "completed without acceptance", start: true, state: "completed", want: true},
		{name: "result acceptance", start: true, state: "completed", markers: []string{"accept_worker_result"}},
		{name: "evidence acceptance then job", start: true, state: "completed", markers: []string{"accept_worker_evidence", "record_worker_job"}},
		{name: "rejection still holds", start: true, state: "completed", markers: []string{"reject_worker_result"}, want: true},
		{name: "failure record still holds effect step", start: true, state: "failed", markers: []string{"record_worker_failure"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if tc.start {
				disposalStartForTest(t, db)
			}
			if tc.state != "" {
				if _, err := db.Exec(`UPDATE worker_attempts SET dispatch_seq=12,lifecycle_state=?; UPDATE domain_events SET seq=12 WHERE seq=2`, tc.state); err != nil {
					t.Fatal(err)
				}
			}
			for i, action := range tc.markers {
				disposalMarkerForTest(t, db, action, int64(14+i))
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowDispatchHoldsStepAdvance(t.Context(), db, WorkflowDefinition{}, "work", "refine", 100)
				if err != nil || got != tc.want {
					t.Fatalf("held=%v err=%v, want %v", got, err, tc.want)
				}
			})
		})
	}
}

func TestWorkerCheckpointDispatchHoldWithoutLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		markers []string
		want    bool
	}{
		{name: "unaccepted", state: "completed", want: true},
		{name: "live", state: "in_flight", want: true},
		{name: "failed", state: "failed", want: true},
		{name: "accepted then job", state: "completed", markers: []string{"accept_worker_result", "record_worker_job"}},
		{name: "evidence accepted", state: "completed", markers: []string{"accept_worker_evidence"}},
		{name: "failure disposition", state: "failed", markers: []string{"record_worker_failure"}},
		{name: "rejection", state: "completed", markers: []string{"reject_worker_result"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if _, err := db.Exec(`UPDATE worker_attempts SET lifecycle_state=?;
INSERT INTO worker_authorizations VALUES('work','unused',8,'{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"unused"}');
INSERT INTO domain_events VALUES(8,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"unused"}');`, tc.state); err != nil {
				t.Fatal(err)
			}
			for i, action := range tc.markers {
				disposalMarkerForTest(t, db, action, int64(4+i))
			}
			disposalWithoutLog(t, db, func() {
				attempt, state, found, err := latestDispatchedAttemptAtStep(t.Context(), db, "work", "refine", 100)
				if err != nil || !found || attempt != "first" || state != tc.state {
					t.Fatalf("attempt=%q state=%q found=%v err=%v", attempt, state, found, err)
				}
				got, err := workflowCheckpointDispatchHoldsStepAdvance(t.Context(), db, "work", "refine", 100)
				if err != nil || got != tc.want {
					t.Fatalf("held=%v err=%v, want %v", got, err, tc.want)
				}
				definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{{ID: "refine", Kind: WorkflowStepHumanCheckpoint}}}}
				got, err = workflowDispatchHoldsStepAdvance(t.Context(), db, definition, "work", "refine", 100)
				if err != nil || got != tc.want {
					t.Fatalf("checkpoint branch: held=%v err=%v, want %v", got, err, tc.want)
				}
			})
		})
	}
}

func TestWorkerFailureWindowWithoutLog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		authSeq  int64
		dispatch int64
		marker   string
		exclude  bool
		want     bool
	}{
		{name: "authorization only", authSeq: 11, want: true},
		{name: "dispatch only", authSeq: 1, dispatch: 12, want: true},
		{name: "at boundary", authSeq: 10, dispatch: 10},
		{name: "old failure", authSeq: 1, dispatch: 2},
		{name: "recorded failure excluded", authSeq: 11, marker: "record_worker_failure", exclude: true},
		{name: "recorded failure included", authSeq: 11, marker: "record_worker_failure", want: true},
		{name: "job is not failure disposition", authSeq: 11, marker: "record_worker_job", exclude: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			disposalStartForTest(t, db)
			if _, err := db.Exec(`DELETE FROM domain_events WHERE seq IN (1,2); UPDATE worker_attempts SET lifecycle_state='failed',dispatch_seq=?`, tc.dispatch); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE worker_authorizations SET seq=?`, tc.authSeq); err != nil {
				t.Fatal(err)
			}
			// Equal boundary coordinates need not coexist in a real log;
			// use no duplicate event for that current-row boundary test.
			if tc.authSeq != 10 {
				if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","worker_attempt_id":"first"}')`, tc.authSeq); err != nil {
					t.Fatal(err)
				}
			}
			if tc.dispatch > 0 && tc.dispatch != 10 {
				if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work','worker.dispatched','{"attempt_id":"first"}')`, tc.dispatch); err != nil {
					t.Fatal(err)
				}
			}
			if tc.marker != "" {
				disposalMarkerForTest(t, db, tc.marker, 14)
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowFailedWorkerAttemptSince(t.Context(), db, "work", 10, "test", tc.exclude)
				if err != nil || got != tc.want {
					t.Fatalf("since: failed=%v err=%v, want %v", got, err, tc.want)
				}
				got, err = workflowFailedWorkerAttempt(t.Context(), db, "work", "refine", "test", tc.exclude)
				if err != nil || got != tc.want {
					t.Fatalf("step: failed=%v err=%v, want %v", got, err, tc.want)
				}
			})
		})
	}
}

func TestReviewCorrectionConsumptionWithoutLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		seq     int64
		exclude int64
		want    bool
	}{
		{name: "no correction"},
		{name: "before review", action: "request_correction", seq: 2},
		{name: "at review", action: "request_correction", seq: 3},
		{name: "after review", action: "request_correction", seq: 4, want: true},
		{name: "own effect excluded", action: "request_correction", seq: 4, exclude: 4},
		{name: "another exclusion", action: "request_correction", seq: 4, exclude: 5, want: true},
		{name: "another action", action: "confirm_premise", seq: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if tc.action != "" {
				if _, err := db.Exec(`INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq) VALUES('work',?,?)`, tc.action, tc.seq); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DELETE FROM domain_events WHERE seq=?`, tc.seq); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,?)`, tc.seq, WorkflowActionCompleted, string(mustJSONValue(map[string]string{"action_id": tc.action}))); err != nil {
					t.Fatal(err)
				}
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowReviewCorrectionConsumed(t.Context(), db, "work", 3, tc.exclude)
				if err != nil || got != tc.want {
					t.Fatalf("consumed=%v err=%v, want %v", got, err, tc.want)
				}
			})
		})
	}
}

func TestLatestMaterializedAttemptAtStepWithoutLog(t *testing.T) {
	db := workerDisposalCurrentDB(t)
	if _, err := db.Exec(`
INSERT INTO worker_authorizations VALUES('work','second',5,'{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"second"}');
INSERT INTO worker_attempts VALUES('work','second','review','failed','',6,'{}',0);
INSERT INTO domain_events VALUES(5,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"second"}');
INSERT INTO domain_events VALUES(6,'work_item','work','worker.dispatched','{"attempt_id":"second"}');
INSERT INTO worker_authorizations VALUES('work','other-step',7,'{"action_id":"dispatch_worker","step_id":"execution","worker_attempt_id":"other-step"}');
INSERT INTO worker_attempts VALUES('work','other-step','review','completed','model',9,'{}',0);
INSERT INTO domain_events VALUES(7,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"execution","worker_attempt_id":"other-step"}');
INSERT INTO domain_events VALUES(9,'work_item','work','worker.dispatched','{"attempt_id":"other-step"}');
INSERT INTO worker_authorizations VALUES('work','unused',8,'{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"unused"}');
INSERT INTO worker_attempts VALUES('work','unused','review','in_flight','',0,NULL,0);
INSERT INTO domain_events VALUES(8,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"unused"}');`); err != nil {
		t.Fatal(err)
	}
	disposalWithoutLog(t, db, func() {
		attempt, state, found, err := latestDispatchedAttemptAtStep(t.Context(), db, "work", "refine", 100)
		if err != nil || !found || attempt != "second" || state != "failed" {
			t.Fatalf("attempt=%q state=%q found=%v err=%v", attempt, state, found, err)
		}
		_, _, found, err = latestDispatchedAttemptAtStep(t.Context(), db, "work", "missing-step", 100)
		if err != nil || found {
			t.Fatalf("missing step: found=%v err=%v", found, err)
		}
	})
}

func TestWorkerFailureWithoutCurrentStartWithoutLog(t *testing.T) {
	db := workerDisposalCurrentDB(t)
	if _, err := db.Exec(`UPDATE worker_attempts SET lifecycle_state='failed'`); err != nil {
		t.Fatal(err)
	}
	disposalWithoutLog(t, db, func() {
		for _, step := range []string{"", "refine"} {
			got, err := workflowFailedWorkerAttempt(t.Context(), db, "work", step, "test", false)
			if err != nil || got {
				t.Fatalf("step=%q failed=%v err=%v", step, got, err)
			}
		}
	})
}

func TestWorkerLiveStepHoldWithoutLog(t *testing.T) {
	for _, state := range []string{"in_flight", "completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if _, err := db.Exec(`INSERT INTO worker_attempts VALUES('work','other','review',?,'',0,'',0); INSERT INTO worker_authorizations VALUES('work','other',4,'{"step_id":"refine"}'); INSERT INTO domain_events VALUES(4,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"other"}')`, state); err != nil {
				t.Fatal(err)
			}
			disposalWithoutLog(t, db, func() {
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				err = rejectStepAdvancePastLiveAttemptTx(t.Context(), tx, "work", "refine", "first", 100)
				if state == "in_flight" {
					if !hasFailureKind(err, KindIllegalLifecycleTransition) {
						t.Fatalf("live hold error=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestRefineReviewCorrectionWithoutLog(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for _, tc := range []struct {
		name     string
		extra    string
		boundary int64
		want     workflowRefineReviewCorrectionState
	}{
		{name: "accepted and settled", want: workflowRefineReviewCorrectionOpen},
		{name: "acceptance survives later job disposition", extra: `INSERT INTO worker_attempt_dispositions VALUES('work','first',5,'job','record_worker_job','{"step_id":"refine"}'); INSERT INTO domain_events VALUES(5,'work_item','work','workflow.action_completed','{"action_id":"record_worker_job","step_id":"refine","worker_attempt_id":"first"}')`, want: workflowRefineReviewCorrectionOpen},
		{name: "authorization without materialization holds", extra: `INSERT INTO worker_authorizations VALUES('work','other',5,'{"step_id":"refine"}'); INSERT INTO domain_events VALUES(5,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"other"}')`, want: workflowRefineReviewCorrectionClosed},
		{name: "another step does not hold", extra: `INSERT INTO worker_authorizations VALUES('work','other',5,'{"step_id":"execution"}'); INSERT INTO domain_events VALUES(5,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"execution","worker_attempt_id":"other"}')`, want: workflowRefineReviewCorrectionOpen},
		{name: "earlier pass does not hold", boundary: 2, extra: `INSERT INTO worker_authorizations VALUES('work','old',1,'{"step_id":"refine"}'); INSERT INTO domain_events VALUES(0,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"refine","worker_attempt_id":"old"}')`, want: workflowRefineReviewCorrectionOpen},
		{name: "acceptance at boundary is not current", boundary: 4, want: workflowRefineReviewCorrectionNotApplicable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if _, err := db.Exec(`UPDATE worker_attempts SET capability_class='review'; INSERT INTO worker_attempt_dispositions VALUES('work','first',4,'dispose','accept_worker_evidence','{"step_id":"refine"}'); INSERT INTO domain_events VALUES(4,'work_item','work','workflow.action_completed','{"action_id":"accept_worker_evidence","step_id":"refine","worker_attempt_id":"first"}')`); err != nil {
				t.Fatal(err)
			}
			if tc.extra != "" {
				if _, err := db.Exec(tc.extra); err != nil {
					t.Fatal(err)
				}
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowRefineReviewCorrectionDecision(t.Context(), db, "work", definition, "refine", "test", tc.boundary)
				if err != nil || got != tc.want {
					t.Fatalf("decision=%v err=%v, want %v", got, err, tc.want)
				}
			})
		})
	}
}

func TestRefineReviewEvidenceSelectionWithoutLog(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	for _, tc := range []struct {
		name   string
		extra  string
		marker string
		want   string
	}{
		{name: "completed current review", want: "first"},
		{name: "old pass", extra: `UPDATE worker_attempts SET dispatch_seq=2`},
		{name: "wrong authorization step", extra: `UPDATE worker_authorizations SET payload='{"step_id":"execution"}'`},
		{name: "not completed", extra: `UPDATE worker_attempts SET lifecycle_state='failed'`},
		{name: "empty readback", extra: `UPDATE worker_attempts SET readback_model=''`},
		{name: "not review", extra: `UPDATE worker_attempts SET capability_class='implementation'`},
		{name: "already accepted", marker: "accept_worker_evidence"},
		{name: "already rejected", marker: "reject_worker_result"},
		{name: "job disposition does not mask acceptance", marker: "accept_worker_evidence", extra: `INSERT INTO worker_attempt_dispositions VALUES('work','first',16,'job','record_worker_job','{"step_id":"refine"}')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			disposalStartForTest(t, db)
			if _, err := db.Exec(`CREATE TABLE workflow_contracts(work_id TEXT,supersession_seq INTEGER);
UPDATE worker_attempts SET capability_class='review',dispatch_seq=12,terminal_seq=13;
UPDATE worker_authorizations SET seq=11;`); err != nil {
				t.Fatal(err)
			}
			if tc.extra != "" {
				if _, err := db.Exec(tc.extra); err != nil {
					t.Fatal(err)
				}
			}
			if tc.marker != "" {
				disposalMarkerForTest(t, db, tc.marker, 15)
			}
			disposalWithoutLog(t, db, func() {
				got, err := workflowRefineReviewEvidenceAttempt(t.Context(), db, "work", definition, "refine", "test", 100)
				if err != nil || got != tc.want {
					t.Fatalf("attempt=%q err=%v, want %q", got, err, tc.want)
				}
				if tc.want != "" {
					err := validateRefineReviewEvidenceAttempt(t.Context(), db, "work", definition, "refine", tc.want, 8, "test", 100)
					if !hasFailureKind(err, KindIllegalLifecycleTransition) || !strings.Contains(err.Error(), "attempt epoch") {
						t.Fatalf("wrong epoch error=%v", err)
					}
				}
			})
		})
	}
}

func TestWorkerAttemptUsesAuthorizationStartWithoutLog(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorization only", true: "materialized"}[dispatched], func(t *testing.T) {
			db := workerDisposalCurrentDB(t)
			if _, err := db.Exec(`ALTER TABLE worker_authorizations ADD COLUMN start_seq INTEGER;
ALTER TABLE worker_authorizations ADD COLUMN start_payload TEXT;
UPDATE worker_authorizations SET seq=4,payload='{"step_id":"confirm"}',start_seq=1,start_payload='{"step_id":"confirm","attempt_epoch":7}';
INSERT INTO domain_events VALUES(4,'work_item','work','workflow.action_completed','{"action_id":"dispatch_worker","step_id":"confirm","worker_attempt_id":"first"}');`); err != nil {
				t.Fatal(err)
			}
			if !dispatched {
				if _, err := db.Exec(`UPDATE worker_attempts SET dispatch_seq=0,dispatch_payload=NULL; DELETE FROM domain_events WHERE kind='worker.dispatched'`); err != nil {
					t.Fatal(err)
				}
			}
			definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{{ID: "confirm", Kind: WorkflowStepHumanCheckpoint, Actions: []string{"record_worker_failure", "accept_worker_result"}}}}}
			disposalWithoutLog(t, db, func() {
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for _, epoch := range []int64{7, 8} {
					err := validateWorkerAttemptAction(t.Context(), tx, Event{SubjectID: "work", Seq: 100}, workflowActionCompletedPayload{ActionID: "record_worker_failure", WorkerAttemptID: "first", AttemptEpoch: epoch}, definition, "confirm", "failed")
					// Reaching the lifecycle guard proves the exact saved start
					// resolved without asking for a newer step start.
					want := "worker attempt is not failed"
					if epoch != 7 {
						want = "worker attempt epoch does not match the latest workflow action start"
					}
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("epoch=%d error=%v, want %q", epoch, err, want)
					}
				}
				if !dispatched {
					err := validateWorkerAttemptAction(t.Context(), tx, Event{SubjectID: "work", Seq: 100}, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "first", AttemptEpoch: 7}, definition, "confirm", "completed")
					if !hasFailureKind(err, KindProjectionNotFound) {
						t.Fatalf("acceptance without materialized dispatch error=%v", err)
					}
				}
			})
		})
	}
}

func TestReviewGateCorrectionSourceWithoutLog(t *testing.T) {
	db := workerDisposalCurrentDB(t)
	if _, err := db.Exec(`ALTER TABLE domain_events ADD COLUMN event_id TEXT;
UPDATE domain_events SET event_id='report' WHERE seq=3;
ALTER TABLE worker_attempts ADD COLUMN terminal_event_id TEXT;
UPDATE worker_attempts SET terminal_event_id='report';
CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER);
INSERT INTO workflow_contracts VALUES('work',1,NULL);
CREATE TABLE workflow_contract_predicates(work_id TEXT,contract_version INTEGER,ordinal INTEGER,predicate_id TEXT);
INSERT INTO workflow_contract_predicates VALUES('work',1,0,'predicate:result');`); err != nil {
		t.Fatal(err)
	}
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	state := WorkflowAdmissionState{Review: &WorkflowReviewState{
		Candidate: &WorkerReviewCandidate{}, Accepted: &WorkflowAcceptedReview{AttemptID: "first", ReportRef: "report"},
	}}
	disposalWithoutLog(t, db, func() {
		got, err := workflowDeliveryGateCorrectionContextFolded(t.Context(), db, "work", definition, "delivery", "test", state)
		if err != nil || got == nil || got.SourceEventID != "report" || got.SourceEventSeq != 3 {
			t.Fatalf("context=%+v err=%v, want report at sequence 3", got, err)
		}
	})
}
