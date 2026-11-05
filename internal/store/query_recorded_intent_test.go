package store

import (
	"context"
	"testing"
)

// The full-detail list read is the authoritative intent read: it projects the
// captured note the stored intent holds so a coordinator can carry it into a
// revision. Bounded summary reads keep their column-only shape.
func TestFullListReadsCarryTheRecordedIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "intent-read-work")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=json_set(intent_json,
		'$.task', 'Recorded instruction for the carrying coordinator') WHERE id=?`, "intent-read-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{operationEvent("intent-read-work-membership", "work.memberships_replaced", SubjectWorkItem, "intent-read-work", map[string]any{
		"memberships":      []map[string]any{{"project_id": "project", "role": "primary"}},
		"expected_version": 2, "resulting_version": 3,
	})}}); err != nil {
		t.Fatal(err)
	}
	full, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"intent-read-work"}, Detail: "full"})
	if err != nil || len(full.Items) != 1 {
		t.Fatalf("full read = %+v, %v", full.Items, err)
	}
	if full.Items[0].Task != "Recorded instruction for the carrying coordinator" {
		t.Fatalf("full read lost the note: %+v", full.Items[0])
	}
	preview, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"intent-read-work"}})
	if err != nil || len(preview.Items) != 1 {
		t.Fatalf("summary read = %+v, %v", preview.Items, err)
	}
	if preview.Items[0].Task != "" {
		t.Fatalf("summary read carried intent detail: %+v", preview.Items[0])
	}
}

func TestFullListPreservesAbsentNoteState(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "absent-note-work")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{operationEvent("absent-note-work-membership", "work.memberships_replaced", SubjectWorkItem, "absent-note-work", map[string]any{
		"memberships":      []map[string]any{{"project_id": "project", "role": "primary"}},
		"expected_version": 2, "resulting_version": 3,
	})}}); err != nil {
		t.Fatal(err)
	}
	empty, err := s.QueryQ3(context.Background(), Q3Request{Product: "product", WorkIDs: []string{"absent-note-work"}, Detail: "full"})
	if err != nil || len(empty.Items) != 1 {
		t.Fatalf("full read = %+v, %v", empty.Items, err)
	}
	if empty.Items[0].Task != "" {
		t.Fatalf("absent note must read empty: %+v", empty.Items[0])
	}
}
