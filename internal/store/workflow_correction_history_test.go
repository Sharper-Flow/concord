package store

import (
	"context"
	"database/sql"
	"testing"
)

// These fixtures exercise the history queries without workflow admission.
// The existing worker-job witnesses cover the authorized event producers.
func correctionHistoryFixture(t *testing.T) (*sql.DB, func(string, any) int64) {
	t.Helper()
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	var seq int64
	return db, func(kind string, payload any) int64 {
		t.Helper()
		seq++
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?)`, seq, string(SubjectWorkItem), "work-history", kind, mustJSONValue(payload)); err != nil {
			t.Fatal(err)
		}
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
	got, err := workflowCorrectionWindowBoundary(context.Background(), db, "work-history", seq, "history_test")
	if err != nil || got != want {
		t.Fatalf("boundary at %d = (%d, %v), want %d", seq, got, err, want)
	}
}

func correctionHistoryRecord(t *testing.T, db *sql.DB, exclude string, wantSeq, wantCountThrough int64) {
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
	if got == nil || got.seq != wantSeq || got.countThrough != wantCountThrough {
		t.Fatalf("open record = %#v, want seq %d counting through %d", got, wantSeq, wantCountThrough)
	}
}

func TestCorrectionHistoryAcceptanceCutoffs(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	job := WorkerJobRecordedPayload{JobID: "job:cutoff", Revision: 1, Objective: "repair the recorded obligation"}
	appendEvent(WorkerJobRecorded, job)
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", job.JobID, 1))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	failureSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("record_worker_failure", "attempt:failed", "", 0))
	correctionHistoryRecord(t, db, "", failureSeq, failureSeq)
	job.Revision = 2
	appendEvent(WorkerJobRecorded, job)
	acceptedSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted", job.JobID, 2))
	correctionHistoryBoundary(t, db, acceptedSeq, 0)
	correctionHistoryBoundary(t, db, acceptedSeq+1, acceptedSeq)
	correctionHistoryRecord(t, db, "", 0, 0)

	// A later acceptance must not alter the historical prefix's open record.
	history, err := workflowCorrectionWalk(context.Background(), db, "work-history", acceptedSeq-1, "history_test")
	if err != nil {
		t.Fatal(err)
	}
	record := history.openRecord("")
	if record == nil || record.seq != failureSeq || record.countThrough != acceptedSeq-1 || history.boundary != 0 {
		t.Fatalf("historical record = %#v, boundary %d; want the unresolved failure", record, history.boundary)
	}
}

func TestCorrectionHistoryLatestUnresolvedRecord(t *testing.T) {
	db, appendEvent := correctionHistoryFixture(t)
	var failureSeqs []int64
	for _, jobID := range []string{"job:older", "job:newer"} {
		appendEvent(WorkerJobRecorded, WorkerJobRecordedPayload{JobID: jobID, Revision: 1, Objective: "the same content under separate jobs"})
		appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:"+jobID, jobID, 1))
		failureSeqs = append(failureSeqs, appendEvent(WorkflowActionCompleted, correctionHistoryAction("reject_worker_result", "attempt:"+jobID, "", 0)))
	}
	correctionHistoryRecord(t, db, "", failureSeqs[1], failureSeqs[1])
	partialSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted-newer", "job:newer", 1))
	correctionHistoryRecord(t, db, "", failureSeqs[0], partialSeq)
	correctionHistoryBoundary(t, db, partialSeq+1, 0)
	acceptedSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted-older", "job:older", 1))
	correctionHistoryRecord(t, db, "", 0, 0)
	correctionHistoryBoundary(t, db, acceptedSeq+1, acceptedSeq)
	requestSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	correctionHistoryRecord(t, db, "", requestSeq, requestSeq)
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
			correctionHistoryRecord(t, db, "", 0, 0)
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
	correctionHistoryRecord(t, db, "", requestSeq, requestSeq)
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:half"})
	correctionHistoryRecord(t, db, "", 0, 0)
	correctionHistoryRecord(t, db, "attempt:half", requestSeq, requestSeq)
	acceptedSeq := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:half", "", 0))
	correctionHistoryBoundary(t, db, acceptedSeq, 0)
	correctionHistoryBoundary(t, db, acceptedSeq+1, acceptedSeq)

	// Late evidence cannot consume a record newer than its authorization.
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:late", "", 0))
	requestSeq = appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:late"})
	correctionHistoryRecord(t, db, "", requestSeq, requestSeq)
	requestSeq = appendEvent(WorkflowActionCompleted, correctionHistoryAction("request_correction", "", "", 0))
	correctionHistoryRecord(t, db, "", requestSeq, requestSeq)
}
