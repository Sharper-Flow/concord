package store

import (
	"context"
	"strings"
	"testing"
)

func TestMainAuthorityPreservesWorkerBoundaryWithoutExecutorExclusion(t *testing.T) {
	t.Parallel()
	db, _ := correctionHistoryFixture(t)
	progressFixtureTables(t, db)
	if _, err := db.Exec(`CREATE TABLE worker_attempts(work_id TEXT,dispatch_event_id TEXT,dispatch_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	const workID = "main-authority"
	const owner = "actor:main"
	const worker = "actor:worker"
	for seq, event := range []struct {
		kind, actor string
		payload     any
	}{
		{WorkflowActionStarted, owner, map[string]any{"actor_ref": owner}},
		{WorkflowActionCompleted, owner, map[string]any{"action_id": "accept_worker_result"}},
		{WorkerDispatched, owner, map[string]any{"lane_actor_ref": worker}},
	} {
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?,?,?)`, seq+1, string(SubjectWorkItem), workID, event.kind, mustJSONValue(event.payload), event.actor, event.kind); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO effect_receipts SELECT event_id,seq,kind,subject_type,subject_id FROM domain_events WHERE seq=?`, seq+1); err != nil {
			t.Fatal(err)
		}
		if event.kind == WorkerDispatched {
			if _, err := db.Exec(`INSERT INTO worker_attempts VALUES(?,?,?)`, workID, event.kind, mustJSONValue(event.payload)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Mutable execution ownership cannot turn a dispatched worker into Main,
	// nor exclude Main because it started execution or accepted a result.
	if _, err := db.Exec(`INSERT INTO workflow_instances VALUES(?,?)`, workID, owner); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actor string
		want  bool
	}{
		{owner, true},
		{worker, false},
		{"actor:resumed-main", true},
	} {
		t.Run(tc.actor, func(t *testing.T) {
			err := workflowRequireMain(t.Context(), db, workID, tc.actor)
			if (err == nil) != tc.want {
				t.Fatalf("Main authority admitted=%v, want %v: %v", err == nil, tc.want, err)
			}
			if !tc.want && (!hasFailureKind(err, KindUnauthorized) || !strings.Contains(err.Error(), "a worker cannot accept results")) {
				t.Fatalf("worker refusal lost its authority boundary: %v", err)
			}
		})
	}
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&events); err != nil || events != 3 {
		t.Fatalf("authority checks changed recorded facts: events=%d, err=%v", events, err)
	}
}

func TestWorkflowEvaluationAuthorityReadFailureIsNotIndependent(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	if err := s.DatabaseForTesting().Close(); err != nil {
		t.Fatal(err)
	}
	// Exercise the query failure, not a failure to open a snapshot.
	authority, err := workflowEvaluationAuthority(context.Background(), s.db, "work-unavailable", "actor-unavailable")
	if err == nil || authority != "" {
		t.Fatalf("unreadable authority=%q err=%v", authority, err)
	}
	if err := workflowRequireMain(t.Context(), s.db, "work-unavailable", "actor-unavailable"); !hasFailureKind(err, KindUnavailable) {
		t.Fatalf("unreadable Main authority did not fail closed: %v", err)
	}
}
