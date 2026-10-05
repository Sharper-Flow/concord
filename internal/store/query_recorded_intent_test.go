package store

import (
	"context"
	"testing"
)

// CON-835 regression. The single-work scope read and the full-detail list
// read are the authoritative intent reads: they must project every revisable
// value the stored intent holds (task, value statement, tags, and workflow
// type reference, beside the urgency and priority columns) so a coordinator
// can carry the complete intent into a task-only complete-replacement
// revision. The recorded workflow_type_ref comes from the intent projection
// only; the pinned workflow instance is not a substitute. Bounded summary
// reads keep their column-only shape.
func TestScopeAndFullListReadsCarryTheRecordedIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "intent-read-work")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET priority=-7, urgency='expedite', intent_json=json_set(intent_json,
		'$.task', 'Recorded instruction for the carrying coordinator',
		'$.value_statement', 'A task-only revision must not reset intent',
		'$.tags', json_array('agent-plane', 'storage'),
		'$.workflow_type_ref', 'workflow.break_fix',
		'$.external_ref', 'linear:CON-835-9') WHERE id=?`, "intent-read-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	scope, err := s.QueryQ6(ctx, Q6Request{Product: "product", Work: "intent-read-work"})
	if err != nil {
		t.Fatal(err)
	}
	if scope.Work == nil {
		t.Fatal("scope returned no work item")
	}
	assertRecordedIntent(t, *scope.Work, "scope read")

	full, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"intent-read-work"}, Detail: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Items) != 1 {
		t.Fatalf("full-detail listing returned %d items, want one", len(full.Items))
	}
	assertRecordedIntent(t, full.Items[0], "full-detail list read")

	summary, err := s.QueryQ3(ctx, Q3Request{Product: "product", WorkIDs: []string{"intent-read-work"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Items) != 1 {
		t.Fatalf("summary listing returned %d items, want one", len(summary.Items))
	}
	item := summary.Items[0]
	if item.Task != "" || item.ValueStatement != "" || item.Tags != nil || item.WorkflowTypeRef != "" {
		t.Fatalf("summary listing carried intent detail: task=%q value_statement=%q tags=%#v workflow_type_ref=%q", item.Task, item.ValueStatement, item.Tags, item.WorkflowTypeRef)
	}
	if item.Urgency != "expedite" || item.Priority != -7 {
		t.Fatalf("summary listing urgency=%q priority=%d, want the declared columns carried", item.Urgency, item.Priority)
	}

	preview, err := s.QueryQ2(ctx, Q2Request{Product: "product", PreviewLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) == 0 {
		t.Fatal("snapshot preview returned no items")
	}
	if preview.Items[0].Task != "" || preview.Items[0].ValueStatement != "" || preview.Items[0].Tags != nil || preview.Items[0].WorkflowTypeRef != "" {
		t.Fatalf("snapshot preview carried intent detail: %+v", preview.Items[0])
	}
}

// An absent recorded workflow_type_ref stays absent even though every
// capture pins a workflow instance: the intent projection is the only
// source, so the read never substitutes the pinned family. An explicitly
// empty tag array stays the empty array rather than collapsing to absent.
func TestScopeReadPreservesAbsentAndEmptyIntentStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, "intent-empty-work")
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET intent_json=json_set(intent_json, '$.tags', json_array()) WHERE id=?`, "intent-empty-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	scope, err := s.QueryQ6(ctx, Q6Request{Product: "product", Work: "intent-empty-work"})
	if err != nil {
		t.Fatal(err)
	}
	if scope.Work == nil {
		t.Fatal("scope returned no work item")
	}
	if scope.Work.WorkflowTypeRef != "" {
		t.Fatalf("scope workflow_type_ref = %q, want absent: the intent projection, not the pinned instance, is the read's source", scope.Work.WorkflowTypeRef)
	}
	if scope.Work.Tags == nil || len(scope.Work.Tags) != 0 {
		t.Fatalf("scope tags = %#v, want the recorded empty array", scope.Work.Tags)
	}
	if scope.Work.Urgency != "standard" {
		t.Fatalf("scope urgency = %q, want the recorded default band", scope.Work.Urgency)
	}
}

func assertRecordedIntent(t *testing.T, item WorkItem, surface string) {
	t.Helper()
	if item.Task != "Recorded instruction for the carrying coordinator" {
		t.Fatalf("%s task = %q, want the recorded task", surface, item.Task)
	}
	if item.ValueStatement != "A task-only revision must not reset intent" {
		t.Fatalf("%s value_statement = %q, want the recorded value statement", surface, item.ValueStatement)
	}
	if item.Urgency != "expedite" || item.Priority != -7 {
		t.Fatalf("%s urgency=%q priority=%d, want the recorded columns", surface, item.Urgency, item.Priority)
	}
	if len(item.Tags) != 2 || item.Tags[0] != "agent-plane" || item.Tags[1] != "storage" {
		t.Fatalf("%s tags = %#v, want the recorded tags", surface, item.Tags)
	}
	if item.WorkflowTypeRef != "workflow.break_fix" {
		t.Fatalf("%s workflow_type_ref = %q, want the recorded intent reference", surface, item.WorkflowTypeRef)
	}
}
