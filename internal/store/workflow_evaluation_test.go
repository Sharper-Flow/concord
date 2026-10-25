package store

import (
	"context"
	"strings"
	"testing"
)

func TestMainAuthorityPreservesWorkerBoundaryWithoutExecutorExclusion(t *testing.T) {
	t.Parallel()
	db, _ := correctionHistoryFixture(t)
	const workID = "main-authority"
	const owner = "actor:main"
	const worker = "actor:worker"
	for _, statement := range []string{
		`CREATE TABLE workflow_worker_actors(actor_ref TEXT PRIMARY KEY,first_dispatch_seq INTEGER NOT NULL)`,
		`CREATE TABLE workflow_member_callers(actor_ref TEXT PRIMARY KEY,work_id TEXT,project_id TEXT,group_id TEXT,member_id TEXT)`,
		`CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,execution_actor_ref TEXT)`,
		`ALTER TABLE domain_events RENAME TO domain_events_unavailable`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workflow_worker_actors VALUES(?,?)`, worker, 3); err != nil {
		t.Fatal(err)
	}
	// Mutable execution ownership cannot turn a dispatched worker into Main,
	// nor exclude Main because it started execution or accepted a result.
	if _, err := db.Exec(`INSERT INTO workflow_instances VALUES(?,?)`, workID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	var changesBefore int
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
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
	var changesAfter int
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil || changesAfter != changesBefore {
		t.Fatalf("authority checks changed recorded facts: changes before=%d, after=%d, err=%v", changesBefore, changesAfter, err)
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
