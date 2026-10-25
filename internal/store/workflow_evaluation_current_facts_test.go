package store

import (
	"reflect"
	"testing"
)

func TestWorkflowEvaluationAuthorityCurrentFactsWithoutLog(t *testing.T) {
	f := seedCrossAttemptFixture(t, "evaluation-current-facts")
	defer f.s.Close()
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	actor := crossAttemptDispatchedLaneActor(t, f.s, f.workID, f.attemptR)
	var seq int64
	if err := f.s.db.QueryRow(`SELECT dispatch_seq FROM worker_attempts WHERE work_id=? AND attempt_id=?`, f.workID, f.attemptR).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	before, err := workflowRecordedEvaluationFacts(t.Context(), f.s.db, f.workID, actor, seq-1)
	if err != nil || before {
		t.Fatalf("worker identity before dispatch: %v, %v", before, err)
	}
	after, err := workflowRecordedEvaluationFacts(t.Context(), f.s.db, f.workID, actor, seq)
	if err != nil || !after {
		t.Fatalf("worker identity at dispatch: %v, %v", after, err)
	}
	queries := []string{`SELECT json_array(actor_ref,first_dispatch_seq) FROM workflow_worker_actors ORDER BY actor_ref`}
	want := workerFactsQueryRows(t, f.s, queries)
	assertWorkflowCurrentFactsWithIndependentMigration142(t, f.s)
	if got := workerFactsQueryRows(t, f.s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("worker-role backfill changed identity: got %v, want %v", got, want)
	}
	tx, err := f.s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM worker_attempts WHERE work_id=?`, f.workID); err != nil {
		t.Fatal(err)
	}
	role, err := workflowEvaluationAuthority(t.Context(), tx, f.workID, actor)
	if err != nil || role != WorkflowWorkerEvaluator {
		t.Fatalf("attempt cleanup erased worker identity: %s, %v", role, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertWorkerFactsLogBlind(t, f.s, map[string]func() (any, error){
		"evaluator-worker-role": func() (any, error) { return workflowEvaluationAuthority(t.Context(), f.s.db, f.workID, actor) },
		"evaluator-independent-role": func() (any, error) {
			return workflowEvaluationAuthority(t.Context(), f.s.db, f.workID, crossAttemptOwnerRef(t, f.fixture.owner))
		},
	})
}
