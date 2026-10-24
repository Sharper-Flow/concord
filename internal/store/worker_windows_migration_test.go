package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func TestWorkerWindowsMigration143Parity(t *testing.T) {
	const work = "worker-window-migration"
	f := oSeedReviewRoute(t, work, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:window", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, job)
	dispatchJobBoundAttempt(t, s, work, "repair", "attempt:window", nil, f.owner, 0, "window-dispatch", job)
	completeAndAcceptAttempt(t, s, work, "attempt:window", reviewGateLane(t, "implementation"), reviewGateAcceptor(work))
	failed := &WorkerJobBinding{JobID: "job:failed-window", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, work, f.owner, failed)
	oDispatchJobBoundAttempt(t, s, work, "attempt:failed-window", nil, f.owner, 0, "failed-window-dispatch", failed)
	failure := s2WorkerFailedAttributedEvent(work, "attempt:failed-window", "failed-window-terminal", preferredModelForLane(reviewGateLane(t, "implementation")), WorkerFailureWorkerError, "worker failed", time.Unix(31, 0).UTC())
	if err := s2WorkerFailedLive(t, s, failure); err != nil {
		t.Fatal(err)
	}
	assertWorkerWindowsMigration143Parity(t, s)
}

func assertWorkerWindowsMigration143Parity(t *testing.T, s *Store) {
	t.Helper()
	queries := []string{
		`SELECT json_array(id,effect_seq,last_progress_at) FROM work_items ORDER BY id`,
		`SELECT json_array(work_id,attempt_id,event_id,seq,start_seq,start_payload,contract_version) FROM worker_authorizations ORDER BY work_id,attempt_id`,
		`SELECT json_array(work_id,attempt_id,terminal_seq,terminal_occurred_at,failure_recorded_seq) FROM worker_attempts ORDER BY work_id,attempt_id`,
		`SELECT json_array(work_id,job_id,revision,recorded_seq,recorded_event_id,satisfied_seq) FROM worker_job_revisions ORDER BY work_id,job_id,revision`,
		`SELECT json_array(work_id,attempt_id,action_id,seq,event_id,payload,first_acceptance_seq,first_acceptance_payload) FROM worker_attempt_dispositions ORDER BY work_id,attempt_id,action_id`,
		`SELECT json_array(work_id,latest_non_dispatch_start_seq) FROM workflow_instances ORDER BY work_id`,
	}
	snapshot := func() [][]string {
		t.Helper()
		out := make([][]string, len(queries))
		for i, query := range queries {
			rows, err := s.db.Query(query)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var row string
				if err := rows.Scan(&row); err != nil {
					t.Fatal(err)
				}
				out[i] = append(out[i], row)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	want := snapshot()
	// Remove only 143's destination state. Its backfill must reconstruct the
	// same current facts the real writing paths produced above.
	if _, err := s.db.Exec(workerWindowFactsFixtureReversal); err != nil {
		t.Fatal(err)
	}
	var migration143 migration
	for _, m := range migrations {
		if m.Version == 143 {
			migration143 = m
		}
	}
	if migration143.Version != 143 || !migration143.Breaking || migration143.FoldMaintained != "advance" {
		t.Fatal("missing breaking advance migration 143")
	}
	if err := applyMigration(context.Background(), s.db, migration143); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); !reflect.DeepEqual(got, want) {
		for i := range queries {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("%s\ngot %v\nwant %v", queries[i], got[i], want[i])
			}
		}
	}
}

const workerWindowFactsFixtureReversal = `
DROP TABLE worker_attempt_dispositions;
ALTER TABLE work_items DROP COLUMN effect_seq;
ALTER TABLE work_items DROP COLUMN last_progress_at;
ALTER TABLE worker_authorizations DROP COLUMN event_id;
ALTER TABLE worker_authorizations DROP COLUMN start_seq;
ALTER TABLE worker_authorizations DROP COLUMN start_payload;
ALTER TABLE worker_authorizations DROP COLUMN contract_version;
ALTER TABLE worker_attempts DROP COLUMN terminal_seq;
ALTER TABLE worker_attempts DROP COLUMN terminal_occurred_at;
ALTER TABLE worker_attempts DROP COLUMN failure_recorded_seq;
ALTER TABLE worker_job_revisions DROP COLUMN recorded_seq;
ALTER TABLE worker_job_revisions DROP COLUMN recorded_event_id;
ALTER TABLE worker_job_revisions DROP COLUMN satisfied_seq;
ALTER TABLE workflow_instances DROP COLUMN latest_non_dispatch_start_seq;
`

func TestWorkEffectFrontierAndLastProgress(t *testing.T) {
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY,effect_seq INTEGER NOT NULL DEFAULT 0,last_progress_at TEXT NOT NULL DEFAULT ''); INSERT INTO work_items(id) VALUES('work')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	write := func(seq int64, kind, timestamp string) {
		t.Helper()
		at, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := advanceWorkEffectSeqTx(ctx, tx, Event{Seq: seq, EventID: "effect", SubjectType: SubjectWorkItem, SubjectID: "work", Kind: kind, OccurredAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	write(10, "work.created", "2026-10-01T00:00:01Z")
	write(11, "worker.dispatched", "2026-10-01T00:00:01.5Z")
	for _, kind := range []string{"work.message_sent", "work.message_withdrawn", "workflow.overlap_resolved"} {
		write(12, kind, "2026-10-02T00:00:00Z")
	}
	var seq int64
	var progress string
	if err := db.QueryRow(`SELECT effect_seq,last_progress_at FROM work_items WHERE id='work'`).Scan(&seq, &progress); err != nil || seq != 12 || progress != "2026-10-01T00:00:01Z" {
		t.Fatalf("frontier=%d progress=%q err=%v", seq, progress, err)
	}
	// A nested fold can finish after a later-sequenced effect. The frontier
	// stays monotone while the timestamp still includes that earlier effect.
	write(9, "worker.completed", "2026-10-03T00:00:00+02:00")
	if err := db.QueryRow(`SELECT effect_seq,last_progress_at FROM work_items WHERE id='work'`).Scan(&seq, &progress); err != nil || seq != 12 || progress != "2026-10-02T22:00:00Z" {
		t.Fatalf("frontier=%d progress=%q err=%v", seq, progress, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := advanceWorkEffectSeqTx(ctx, tx, Event{SubjectType: SubjectWorkItem, SubjectID: "work"}); failureKind(err) != KindInvariantViolation {
		t.Fatalf("missing coordinate admitted: %v", err)
	}
}
