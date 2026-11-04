package store

import (
	"context"
	"testing"
)

func TestFullListCarriesThePersistedWorkTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "packet-task-work")
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json, '$.task') FROM work_items WHERE id=?`, "packet-task-work").Scan(new(string)); err == nil {
		t.Fatal("fixture needs a work item whose persisted task is empty before the update below")
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=json_set(intent_json, '$.task', 'Reproduce, extract the loop, and keep the budget green.') WHERE id=?`, "packet-task-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}

	result, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"packet-task-work"}, Detail: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatal("list did not return one work item")
	}
	if result.Items[0].Task != "Reproduce, extract the loop, and keep the budget green." {
		t.Fatalf("list work task = %q, want the persisted task", result.Items[0].Task)
	}
}
