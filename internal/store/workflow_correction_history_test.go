package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
)

// These fixtures exercise correction transitions without workflow admission.
// The worker-job witnesses cover the authorized producers.
func correctionHistoryDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload BLOB,event_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func correctionFixtureTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE work_items(id TEXT PRIMARY KEY);
INSERT INTO work_items VALUES('work-history');
CREATE TABLE workflow_correction_state(work_id TEXT PRIMARY KEY,state TEXT);
CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,recorded_payload TEXT,PRIMARY KEY(work_id,job_id,revision));
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT,PRIMARY KEY(work_id,attempt_id));
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,dispatch_payload TEXT,terminal_payload TEXT,PRIMARY KEY(work_id,attempt_id));
`); err != nil {
		t.Fatal(err)
	}
}

func applyCorrectionFixture(t *testing.T, db *sql.DB, seq int64, kind string, payload any) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	raw := mustJSONValue(payload)
	var fields workflowActionCompletedPayload
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case WorkerJobRecorded:
		var job WorkerJobRecordedPayload
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(`INSERT INTO worker_job_revisions VALUES('work-history',?,?,?) ON CONFLICT(work_id,job_id,revision) DO UPDATE SET recorded_payload=excluded.recorded_payload`, job.JobID, job.Revision, string(raw))
	case WorkflowActionCompleted:
		if fields.ActionID == "dispatch_worker" {
			_, err = tx.Exec(`INSERT INTO worker_authorizations VALUES('work-history',?,?,?) ON CONFLICT(work_id,attempt_id) DO UPDATE SET seq=excluded.seq,payload=excluded.payload`, fields.WorkerAttemptID, seq, string(raw))
		}
	case WorkerDispatched, WorkerCompleted, WorkerFailed:
		var attempt struct {
			AttemptID string `json:"attempt_id"`
		}
		if err := json.Unmarshal(raw, &attempt); err != nil {
			t.Fatal(err)
		}
		column := "terminal_payload"
		if kind == WorkerDispatched {
			column = "dispatch_payload"
		}
		_, err = tx.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,`+column+`) VALUES('work-history',?,?) ON CONFLICT(work_id,attempt_id) DO UPDATE SET `+column+`=excluded.`+column, attempt.AttemptID, string(raw))
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowCorrectionFactsTx(t.Context(), tx, Event{Seq: seq, EventID: fmt.Sprintf("history-e%d", seq), SubjectID: "work-history", Kind: kind, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func correctionHistoryFixture(t *testing.T) (*sql.DB, func(string, any) int64) {
	t.Helper()
	db := correctionHistoryDatabase(t)
	correctionFixtureTables(t, db)
	var seq int64
	return db, func(kind string, payload any) int64 {
		t.Helper()
		seq++
		applyCorrectionFixture(t, db, seq, kind, payload)
		return seq
	}
}

func correctionHistoryAction(action, attempt, job string, revision int64) map[string]any {
	fields := map[string]any{"action_id": action, "worker_attempt_id": attempt}
	if job != "" {
		fields["worker_job"] = WorkerJobBinding{JobID: job, Revision: revision}
	}
	return fields
}

func correctionHistoryBoundary(t *testing.T, db *sql.DB, seq, want int64) {
	t.Helper()
	got, err := workflowCorrectionAcceptanceBefore(context.Background(), db, "work-history", seq, "history_test")
	if err != nil || got != want {
		t.Fatalf("boundary at %d = (%d, %v), want %d", seq, got, err, want)
	}
}

func correctionHistoryRecord(t *testing.T, db *sql.DB, exclude string, wantSeq int64) {
	t.Helper()
	got, err := workflowCorrectionOpenRecord(context.Background(), db, "work-history", exclude, "history_test")
	if err != nil {
		t.Fatal(err)
	}
	if wantSeq == 0 {
		if got != nil {
			t.Fatalf("open record = %#v, want none", got)
		}
		return
	}
	if got == nil || got.seq != wantSeq {
		t.Fatalf("open record = %#v, want seq %d", got, wantSeq)
	}
}

func TestCorrectionHistoryAcceptanceCutoffs(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	job := WorkerJobRecordedPayload{JobID: "job:cutoff", Revision: 1, Objective: "repair the recorded obligation"}
	appendEvent(WorkerJobRecorded, job)
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", job.JobID, 1))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	failureSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("record_worker_failure", "attempt:failed", "", 0))
	correctionHistoryRecord(t, db, "", failureSeq)
	job.Revision = 2
	appendEvent(WorkerJobRecorded, job)
	correctionHistoryRecord(t, db, "", failureSeq)
	correctionHistoryBoundary(t, db, 0, 0)
	acceptedSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted", job.JobID, 2))
	correctionHistoryBoundary(t, db, acceptedSeq+1, acceptedSeq)
	correctionHistoryRecord(t, db, "", 0)
}

func TestCorrectionHistoryLatestUnresolvedRecord(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	var failureSeqs []int64
	for _, jobID := range []string{"job:older", "job:newer"} {
		appendEvent(WorkerJobRecorded, WorkerJobRecordedPayload{JobID: jobID, Revision: 1, Objective: "the same content under separate jobs"})
		appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:"+jobID, jobID, 1))
		failureSeqs = append(failureSeqs, appendEvent(WorkflowActionCompleted, correctionHistoryAction("reject_worker_result", "attempt:"+jobID, "", 0)))
	}
	correctionHistoryRecord(t, db, "", failureSeqs[1])
	partialSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted-newer", "job:newer", 1))
	correctionHistoryRecord(t, db, "", failureSeqs[0])
	correctionHistoryBoundary(t, db, partialSeq+1, 0)
	acceptedSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted-older", "job:older", 1))
	correctionHistoryRecord(t, db, "", 0)
	correctionHistoryBoundary(t, db, acceptedSeq+1, acceptedSeq)
	requestSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	correctionHistoryRecord(t, db, "", requestSeq)
}

func TestCorrectionHistoryFailureDebtBeforeDisposition(t *testing.T) {
	for _, materialized := range []bool{false, true} {
		name := "authorized"
		if materialized {
			name = "materialized"
		}
		t.Run(name, func(t *testing.T) {
			db, appendEvent := correctionHistoryFixture(t)
			job := WorkerJobRecordedPayload{JobID: "job:debt", Revision: 1, Objective: "repair the original obligation"}
			appendEvent(WorkerJobRecorded, job)
			appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", job.JobID, 1))
			if materialized {
				appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:failed", "worker_job": WorkerJobBinding{JobID: job.JobID, Revision: 1}})
			}
			appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
			for _, acceptedJob := range []string{"", "job:debt-other", job.JobID} {
				recorded := job
				recorded.JobID = acceptedJob
				recorded.Revision = 2
				if acceptedJob == job.JobID {
					recorded.Objective = "an unrelated rewritten obligation"
				}
				if acceptedJob != "" {
					appendEvent(WorkerJobRecorded, recorded)
				}
				seq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:unrelated", acceptedJob, 2))
				correctionHistoryBoundary(t, db, seq+1, 0)
			}
			correctionHistoryRecord(t, db, "", 0)
			job.Revision = 3
			appendEvent(WorkerJobRecorded, job)
			seq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:satisfying", job.JobID, 3))
			correctionHistoryBoundary(t, db, seq+1, seq)
		})
	}
}

func TestCorrectionHistoryLegacyMaterializedConsumption(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	requestSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:evidence-only"})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:half", "", 0))
	correctionHistoryRecord(t, db, "", requestSeq)
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:half"})
	correctionHistoryRecord(t, db, "", 0)
	correctionHistoryRecord(t, db, "attempt:half", requestSeq)
	correctionHistoryBoundary(t, db, 0, 0)
	acceptedSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:half", "", 0))
	correctionHistoryBoundary(t, db, acceptedSeq+1, acceptedSeq)

	// Late evidence cannot consume a record newer than its authorization.
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:late", "", 0))
	requestSeq = appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:late"})
	correctionHistoryRecord(t, db, "", requestSeq)
	requestSeq = appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	correctionHistoryRecord(t, db, "", requestSeq)
}
