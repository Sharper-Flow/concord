package store

import (
	"context"
	"testing"
)

func seedImportedWork(t *testing.T, s *Store, changeID, actor string) {
	t.Helper()
	workID := "import-advance-work-" + changeID
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product", "create-product"),
			projectCreatedEvent("project", "create-project"),
			operationEvent("product-project", "product_project.added", SubjectProduct, "product", map[string]any{
				"product_id": "product", "project_id": "project", "role": "primary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "product"): 0,
			VersionRef(SubjectProject, "project"): 0,
		},
	}); err != nil {
		t.Fatalf("seed test Project: %v", err)
	}
	created := operationEvent(workID, "work.created", SubjectWorkItem, workID, map[string]any{
		"work_kind": "task", "title": "imported", "value_statement": "imported", "priority": 3,
		"tags": []string{"predecessor-migrated"}, "external_ref": "advance:" + changeID,
	})
	created.Actor = actor
	membership := operationEvent("import-advance-work-membership-"+workID, "work.memberships_replaced", SubjectWorkItem, workID, map[string]any{
		"memberships":      []map[string]any{{"project_id": "project", "role": "primary"}},
		"expected_version": 1, "resulting_version": 2,
	})
	membership.Actor = actor
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{created, membership}}); err != nil {
		t.Fatalf("seed imported work: %v", err)
	}
}

func TestImportedWorkRemainsReadable(t *testing.T) {
	s := openTemp(t)
	const id = "import-advance-work-change-1"
	seedImportedWork(t, s, "change-1", "operator:predecessor-import")
	result, err := s.QueryQ3(t.Context(), Q3Request{Product: "product", WorkIDs: []string{id}, Detail: "full"})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("work read = %+v, %v", result.Items, err)
	}
	w := result.Items[0]
	if w.Lifecycle != "needed" || w.Version != 2 {
		t.Fatalf("imported state changed: %+v", w)
	}
	if w.ID != id || w.Title != "imported" || len(w.Projects) != 1 || w.Projects[0].ID != "project" || w.Projects[0].Role != "primary" {
		t.Fatalf("import-shaped work changed: %+v", w)
	}
	summary, err := s.ReadWorkItemSummary(t.Context(), id)
	if err != nil || summary.Kind != "task" || summary.Title != "imported" {
		t.Fatalf("ordinary work summary = %+v, %v", summary, err)
	}
}
