package store

import (
	"context"
	"testing"
)

// The persisted work task (CON-271) rides the single-record scope read the
// adapter's lane packet projects from. readOneWork read the narrative but not
// the task, so concord_work_browse.scope never returned the field its own
// work_summary schema declares and a dispatched worker could not receive the
// recorded instruction.
func TestScopeReadCarriesThePersistedWorkTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "packet-task-work")
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json, '$.task') FROM work_items WHERE id=?`, "packet-task-work").Scan(new(string)); err == nil {
		t.Fatal("fixture needs a work item whose persisted task is empty before the update below")
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=json_set(intent_json, '$.task', 'Reproduce, extract the loop, and keep the budget green.') WHERE id=?`, "packet-task-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	result, err := s.QueryQ6(ctx, Q6Request{Work: "packet-task-work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Work == nil {
		t.Fatal("scope returned no work item")
	}
	if result.Work.Task != "Reproduce, extract the loop, and keep the budget green." {
		t.Fatalf("scope work task = %q, want the persisted task", result.Work.Task)
	}
}
