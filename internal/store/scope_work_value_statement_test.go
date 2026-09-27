package store

import (
	"context"
	"testing"
)

// The recorded value statement rides the single-record scope read, the way the
// persisted task does: the adapter's lane packet renders it as the why line
// from the work object it already reads, so readOneWork must project the
// intent_json field the capture and revise folds already store.
func TestScopeReadCarriesThePersistedValueStatement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "packet-value-work")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=json_set(intent_json, '$.value_statement', 'Workers read why the work matters before the how.') WHERE id=?`, "packet-value-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	result, err := s.QueryQ6(ctx, Q6Request{Work: "packet-value-work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Work == nil {
		t.Fatal("scope returned no work item")
	}
	if result.Work.ValueStatement != "Workers read why the work matters before the how." {
		t.Fatalf("scope work value_statement = %q, want the persisted value", result.Work.ValueStatement)
	}
	if result.Work.Task != "" {
		t.Fatalf("scope work task = %q, want empty for a fixture without a task", result.Work.Task)
	}
}
