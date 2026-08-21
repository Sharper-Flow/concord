package store

import (
	"context"
	"testing"
	"time"
)

// TestProductMembershipOnFreshDatabaseIsEmpty pins the absence case: a
// Product that has never been created reports an empty membership slice,
// not a typed failure, so the operator verb can use it as a precondition
// without special-casing projection-not-found.
func TestProductMembershipOnFreshDatabaseIsEmpty(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	membership, err := s.ProductMembership(ctx, "absent-product")
	if err != nil {
		t.Fatalf("ProductMembership error = %v, want nil", err)
	}
	if len(membership) != 0 {
		t.Fatalf("ProductMembership on absent Product = %v, want empty", membership)
	}
}

// TestProductMembershipReflectsProjectSet pins the happy path: a Product
// with two Projects reports them in sorted order.
func TestProductMembershipReflectsProjectSet(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	when := time.Now().UTC()
	operation := Operation{
		Events: []Event{
			productCreatedEvent("prod-a", "create-prod-a"),
			projectCreatedEvent("project-a", "create-project-a"),
			projectCreatedEvent("project-b", "create-project-b"),
			membershipEvent("add-a", "product_project.added", SubjectProduct, "prod-a", map[string]any{
				"product_id": "prod-a", "project_id": "project-a", "role": "primary",
				"reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
			membershipEvent("add-b", "product_project.added", SubjectProduct, "prod-a", map[string]any{
				"product_id": "prod-a", "project_id": "project-b", "role": "secondary",
				"reason": "test", "expected_version": 2, "resulting_version": 3,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "prod-a"):    0,
			VersionRef(SubjectProject, "project-a"): 0,
			VersionRef(SubjectProject, "project-b"): 0,
		},
	}
	if err := ApplyOperation(ctx, s, operation); err != nil {
		t.Fatalf("seed Product/Projects: %v", err)
	}
	_ = when
	membership, err := s.ProductMembership(ctx, "prod-a")
	if err != nil {
		t.Fatalf("ProductMembership error = %v, want nil", err)
	}
	if len(membership) != 2 || membership[0] != "project-a" || membership[1] != "project-b" {
		t.Fatalf("ProductMembership = %v, want [project-a project-b]", membership)
	}
}

// TestProductMembershipRejectsEmptyID fails closed on an empty product id
// so the operator verb cannot accidentally pass an empty string and read
// another Product's membership.
func TestProductMembershipRejectsEmptyID(t *testing.T) {
	s := openTemp(t)
	_, err := s.ProductMembership(context.Background(), "")
	if err == nil {
		t.Fatalf("ProductMembership(empty) returned nil, want typed failure")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindInvalidOperation {
		t.Fatalf("ProductMembership(empty) error = %v, want KindInvalidOperation", err)
	}
}

// TestEventIDExistsFalseForUnknownID pins the absence case: a never-recorded
// event id reports false, not an error.
func TestEventIDExistsFalseForUnknownID(t *testing.T) {
	s := openTemp(t)
	exists, err := s.EventIDExists(context.Background(), "never-recorded")
	if err != nil {
		t.Fatalf("EventIDExists error = %v, want nil", err)
	}
	if exists {
		t.Fatalf("EventIDExists(unknown) = true, want false")
	}
}

// TestEventIDExistsTrueAfterAppend pins the happy path: an event that has
// been appended reports true on the next read.
func TestEventIDExistsTrueAfterAppend(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			productCreatedEvent("product", "create-product"),
			projectCreatedEvent("project", "create-project"),
			membershipEvent("product-project", "product_project.added", SubjectProduct, "product", map[string]any{
				"product_id": "product", "project_id": "project", "role": "primary",
				"reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
			workCreatedEvent("event-id-exists-work", "event-id-exists-event"),
			membershipEvent("work-project", "work_project.added", SubjectWorkItem, "event-id-exists-work", map[string]any{
				"work_id": "event-id-exists-work", "project_id": "project", "role": "primary",
				"reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "product"):               0,
			VersionRef(SubjectProject, "project"):               0,
			VersionRef(SubjectWorkItem, "event-id-exists-work"): 0,
		},
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	exists, err := s.EventIDExists(ctx, "event-id-exists-event")
	if err != nil {
		t.Fatalf("EventIDExists error = %v, want nil", err)
	}
	if !exists {
		t.Fatalf("EventIDExists(recorded) = false, want true")
	}
}

// TestEventIDExistsRejectsEmptyID fails closed on an empty event id so the
// operator verb cannot accidentally probe for a malformed identifier.
func TestEventIDExistsRejectsEmptyID(t *testing.T) {
	s := openTemp(t)
	_, err := s.EventIDExists(context.Background(), "")
	if err == nil {
		t.Fatalf("EventIDExists(empty) returned nil, want typed failure")
	}
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindInvalidOperation {
		t.Fatalf("EventIDExists(empty) error = %v, want KindInvalidOperation", err)
	}
}
