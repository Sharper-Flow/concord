package store

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func g32ReviewFixture(t *testing.T) (*sql.DB, func(string, any) int64) {
	t.Helper()
	db, appendEvent := progressHistory(t)
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY); INSERT INTO work_items VALUES('work-history');
CREATE TABLE fold_guard(active INTEGER); INSERT INTO fold_guard VALUES(1);
CREATE TABLE workflow_contracts(work_id TEXT,contract_version INTEGER,superseded_by INTEGER);
CREATE TABLE workflow_contract_predicates(work_id TEXT,contract_version INTEGER,predicate_id TEXT,outcome_kind TEXT,outcome_payload TEXT);
CREATE TABLE workflow_design_records(work_id TEXT,work_version INTEGER,approach TEXT,decisions TEXT,touched_refs TEXT,recorded_at TEXT);
` + workflowReviewFactsDDL); err != nil {
		t.Fatal(err)
	}
	return db, appendEvent
}

func TestG32ReviewBackfillUsesCallerGuard(t *testing.T) {
	db, appendEvent := g32ReviewFixture(t)
	report := legacyReviewBlock{OutputCandidate: legacyReviewCandidate{ProjectID: "project:one", Commit: "commit:one", Tree: "tree:one"}}
	appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": "review:migrate", "worker_capability_class": "review"})
	appendEvent("worker.completed", map[string]any{"attempt_id": "review:migrate", "review": report})
	appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": "review:migrate"})
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := backfillWorkflowReviewFacts(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM fold_guard WHERE active=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("caller guard: %d, %v", count, err)
	}
	var raw string
	if err := tx.QueryRow(`SELECT state FROM workflow_review_state WHERE work_id='work-history'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state legacyReviewState
	if err := json.Unmarshal([]byte(raw), &state); err != nil || state.Accepted == nil || state.Candidate == nil || *state.Candidate != report.OutputCandidate {
		t.Fatalf("historical state: %+v, %v", state, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_review_state`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback escaped: %d, %v", count, err)
	}
}
