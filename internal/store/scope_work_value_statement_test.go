package store

import (
	"context"
	"testing"
)

func TestFullListCarriesThePersistedValueStatement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "packet-value-work")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=json_set(intent_json, '$.value_statement', 'Workers read why the work matters before the how.') WHERE id=?`, "packet-value-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}

	result, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"packet-value-work"}, Detail: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatal("list did not return one work item")
	}
	if result.Items[0].ValueStatement != "Workers read why the work matters before the how." {
		t.Fatalf("list work value_statement = %q, want the persisted value", result.Items[0].ValueStatement)
	}
	if result.Items[0].Task != "" {
		t.Fatalf("list work task = %q, want empty for a fixture without a task", result.Items[0].Task)
	}
}
