package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type correctionCountQueryRecorder struct {
	*sql.DB
	statement string
	arguments []any
}

func (q *correctionCountQueryRecorder) QueryRowContext(ctx context.Context, statement string, arguments ...any) *sql.Row {
	q.statement = statement
	q.arguments = append([]any(nil), arguments...)
	return q.DB.QueryRowContext(ctx, statement, arguments...)
}

func TestWorkflowCorrectionAttemptCountEvaluatesWindowOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	q := &correctionCountQueryRecorder{DB: s.db}
	if _, err := workflowCorrectionAttemptCount(ctx, q, "work-count-plan", 100, "count_test"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q.statement, q.arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sawWindow bool
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "CORRELATED SCALAR SUBQUERY") {
			t.Errorf("a work-scoped acceptance window must not rescan history for each opening: %s", detail)
		}
		if strings.Contains(detail, "SCALAR SUBQUERY") {
			sawWindow = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawWindow {
		t.Fatal("query plan did not exercise the acceptance-window subquery")
	}
}

func TestWorkflowCorrectionAttemptCountMatchesWindowPopulation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// The count reads only these columns. The real-schema integration and plan
	// tests cover projection constraints; this fixture exhausts window bounds.
	for _, statement := range []string{
		`CREATE TABLE worker_attempts(work_id TEXT NOT NULL,attempt_id TEXT PRIMARY KEY)`,
		`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload BLOB)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	type opening struct {
		seq         int64
		work        string
		subjectType string
		kind        string
		action      string
		attempt     string
	}
	known := map[string]string{}
	for _, work := range []string{"work-count-a", "work-count-b"} {
		for i := 0; i < 4; i++ {
			attempt := fmt.Sprintf("%s-attempt-%d", work, i)
			known[attempt] = work
			if _, err := db.ExecContext(ctx, `INSERT INTO worker_attempts VALUES(?,?)`, work, attempt); err != nil {
				t.Fatal(err)
			}
		}
	}
	events := []opening{
		{1, "work-count-a", "work_item", WorkerDispatched, "", "work-count-a-attempt-0"},
		{2, "work-count-a", "work_item", WorkflowActionCompleted, "dispatch_worker", "work-count-a-attempt-0"},
		{3, "work-count-a", "work_item", WorkflowActionCompleted, "dispatch_worker", "work-count-a-attempt-1"},
		{4, "work-count-b", "work_item", WorkflowActionCompleted, "accept_worker_result", ""},
		{5, "work-count-a", "project", WorkflowActionCompleted, "accept_worker_result", ""},
		{6, "work-count-a", "work_item", WorkflowActionCompleted, "accept_worker_result", ""},
		{7, "work-count-a", "work_item", WorkerDispatched, "", "work-count-a-attempt-1"},
		{8, "work-count-a", "work_item", WorkflowActionCompleted, "dispatch_worker", "work-count-a-attempt-2"},
		{9, "work-count-a", "work_item", WorkerDispatched, "", "unknown-attempt"},
		{10, "work-count-a", "work_item", WorkerDispatched, "", "work-count-b-attempt-0"},
		{11, "work-count-a", "project", WorkerDispatched, "", "work-count-a-attempt-3"},
		{12, "work-count-a", "work_item", WorkflowActionCompleted, "checkpoint_execution", "work-count-a-attempt-3"},
		{13, "work-count-a", "work_item", WorkflowActionCompleted, "accept_worker_result", ""},
		{14, "work-count-a", "work_item", WorkflowActionCompleted, "dispatch_worker", "work-count-a-attempt-3"},
		{15, "work-count-b", "work_item", WorkerDispatched, "", "work-count-b-attempt-0"},
		{16, "work-count-b", "work_item", WorkflowActionCompleted, "dispatch_worker", "work-count-b-attempt-0"},
	}
	for _, event := range events {
		fields := map[string]string{"attempt_id": "unbound", "worker_attempt_id": "unbound", "action_id": event.action}
		if event.kind == WorkerDispatched {
			fields["attempt_id"] = event.attempt
		} else {
			fields["worker_attempt_id"] = event.attempt
		}
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO domain_events VALUES(?,?,?,?,?)`, event.seq, event.subjectType, event.work, event.kind, payload); err != nil {
			t.Fatal(err)
		}
	}
	for _, work := range []string{"work-count-a", "work-count-b", "work-count-absent"} {
		for bound := int64(-1); bound <= 18; bound++ {
			var accepted int64
			for _, event := range events {
				if event.subjectType == "work_item" && event.work == work && event.kind == WorkflowActionCompleted && event.action == "accept_worker_result" && event.seq < bound {
					accepted = event.seq
				}
			}
			population := map[string]bool{}
			for _, event := range events {
				isOpening := event.kind == WorkerDispatched || event.kind == WorkflowActionCompleted && event.action == "dispatch_worker"
				if event.subjectType == "work_item" && event.work == work && isOpening && event.seq > accepted && event.seq <= bound && known[event.attempt] == work {
					population[event.attempt] = true
				}
			}
			got, err := workflowCorrectionAttemptCount(ctx, db, work, bound, "count_test")
			if err != nil {
				t.Fatal(err)
			}
			if got != int64(len(population)) {
				t.Errorf("work=%s bound=%d count=%d want=%d", work, bound, got, len(population))
			}
		}
	}
}
