package store

import (
	"context"
	"testing"
)

// The PM1 corpus fixture has no work item whose derived scope spans two
// Products, so cross-Product scope resolution is asserted against a purpose-built
// fixture instead. It stays in package store because it reuses the shared
// package-private event constructors; the corpus driver itself now lives in
// package store_test so it can consume internal/pm1fixture.
func TestExtraCrossProductFixture(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	events := []Event{productCreatedEvent("cross-a", "cross-product-a"), projectCreatedEvent("cross-project", "cross-project-create"), operationEvent("cross-a-project", "product_project.added", SubjectProduct, "cross-a", map[string]any{"product_id": "cross-a", "project_id": "cross-project", "role": "primary", "reason": "cross fixture", "expected_version": 1, "resulting_version": 2}), productCreatedEvent("cross-b", "cross-product-b"), operationEvent("cross-b-project", "product_project.added", SubjectProduct, "cross-b", map[string]any{"product_id": "cross-b", "project_id": "cross-project", "role": "primary", "reason": "cross fixture", "expected_version": 1, "resulting_version": 2})}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "cross-a"): 0, VersionRef(SubjectProduct, "cross-b"): 0, VersionRef(SubjectProject, "cross-project"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("cross-work", "cross-work-create"), operationEvent("cross-work-project", "work_project.added", SubjectWorkItem, "cross-work", map[string]any{"work_id": "cross-work", "project_id": "cross-project", "role": "primary", "reason": "cross fixture", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cross-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	scope, err := s.ProductsForWork(ctx, "cross-work")
	if err != nil || len(scope.Products) != 2 || !scope.CrossProduct {
		t.Fatalf("ProductsForWork = %#v, err %v", scope, err)
	}
	result, err := s.QueryQ6(ctx, Q6Request{Work: "cross-work"})
	if err != nil || result.Work == nil || result.Work.ID != "cross-work" {
		t.Fatalf("cross-product Q6 = %#v, err %v", result, err)
	}
}
