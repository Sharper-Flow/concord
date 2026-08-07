package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func membershipEvent(id, kind string, subjectType SubjectType, subjectID string, payload map[string]any) Event {
	return operationEvent(id, kind, subjectType, subjectID, payload)
}

func openTempAtPath(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s
}

func TestPM5MembershipScopeIsAtomicAndDerived(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	result, err := ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{
			productCreatedEvent("product-a", "product-a-created"),
			projectCreatedEvent("project-a", "project-a-created"),
			membershipEvent("product-a-project-a", "product_project.added", SubjectProduct, "product-a", map[string]any{
				"product_id": "product-a", "project_id": "project-a", "role": "primary",
				"reason": "initial scope", "expected_version": 1, "resulting_version": 2,
			}),
			workCreatedEvent("work-a", "work-a-created"),
			membershipEvent("work-a-project-a", "work_project.added", SubjectWorkItem, "work-a", map[string]any{
				"work_id": "work-a", "project_id": "project-a", "role": "secondary",
				"reason": "initial scope", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "product-a"): 0,
			VersionRef(SubjectProject, "project-a"): 0,
			VersionRef(SubjectWorkItem, "work-a"):   0,
		},
	})
	if err != nil {
		t.Fatalf("composite membership operation: %v", err)
	}
	if got := result.Impact.EventIDs; len(got) != 1 || got[0] != "product-a-project-a" {
		t.Fatalf("Product membership impact event IDs = %v, want [product-a-project-a]", got)
	}

	products, err := s.ProductsForWork(ctx, "work-a")
	if err != nil {
		t.Fatalf("ProductsForWork: %v", err)
	}
	if len(products.Products) != 1 || products.Products[0].ID != "product-a" || products.CrossProduct {
		t.Fatalf("derived Product scope = %+v, want one Product and CrossProduct=false", products)
	}
}

func TestPM5StandaloneCreationRejectsOrphans(t *testing.T) {
	s := openTemp(t)
	err := ApplyOperation(context.Background(), s, Operation{
		Events:           []Event{productCreatedEvent("orphan", "orphan-created")},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "orphan"): 0},
	})
	assertFailureKind(t, err, KindMembershipInvariant)
	assertTableCount(t, s, "products", 0)
	assertTableCount(t, s, "domain_events", 0)
}

func createProductProject(t *testing.T, s *Store, productID, projectID string) {
	t.Helper()
	err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent(productID, "create-"+productID),
			projectCreatedEvent(projectID, "create-"+projectID),
			membershipEvent("membership-"+productID+"-"+projectID, "product_project.added", SubjectProduct, productID, map[string]any{
				"product_id": productID, "project_id": projectID, "role": "primary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, productID): 0,
			VersionRef(SubjectProject, projectID): 0,
		},
	})
	if err != nil {
		t.Fatalf("create Product/Project: %v", err)
	}
}

func addWorkProject(t *testing.T, s *Store, workID, projectID, role string, expected int64) {
	t.Helper()
	err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{membershipEvent("membership-"+workID+"-"+projectID, "work_project.added", SubjectWorkItem, workID, map[string]any{
			"work_id": workID, "project_id": projectID, "role": role, "reason": "test",
			"expected_version": expected, "resulting_version": expected + 1,
		})},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected},
	})
	if err != nil {
		t.Fatalf("add work membership: %v", err)
	}
}

func TestPM5InvariantsPrimaryRolesAndDerivedOrdering(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product-a", "project-z")

	// A second Product may share the same Project, while one Product still has
	// only one primary.
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			productCreatedEvent("product-b", "create-product-b"),
			membershipEvent("membership-product-b-project-z", "product_project.added", SubjectProduct, "product-b", map[string]any{
				"product_id": "product-b", "project_id": "project-z", "role": "primary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-b"): 0},
	}); err != nil {
		t.Fatalf("share Project: %v", err)
	}

	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{projectCreatedEvent("project-a", "create-project-a")},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-a"): 0},
	}); err == nil {
		t.Fatal("orphan Project creation succeeded")
	}
	// Add another Product Project edge through a valid composite Project create.
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			projectCreatedEvent("project-a", "create-project-a-valid"),
			membershipEvent("membership-product-a-project-a", "product_project.added", SubjectProduct, "product-a", map[string]any{
				"product_id": "product-a", "project_id": "project-a", "role": "secondary", "reason": "test",
				"expected_version": 2, "resulting_version": 3,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-a"): 0, VersionRef(SubjectProduct, "product-a"): 2},
	}); err != nil {
		t.Fatalf("add secondary Project: %v", err)
	}

	projects, err := s.ProjectsForProduct(ctx, "product-a")
	if err != nil || len(projects) != 2 || projects[0].Role != "primary" || projects[0].ID != "project-z" || projects[1].ID != "project-a" {
		t.Fatalf("ProjectsForProduct ordering = %+v, err=%v", projects, err)
	}
	products, err := s.ProductsForProject(ctx, "project-z")
	if err != nil || len(products) != 2 || products[0].ID != "product-a" || products[1].ID != "product-b" {
		t.Fatalf("ProductsForProject ordering = %+v, err=%v", products, err)
	}

	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{workCreatedEvent("work-1", "create-work-1"), membershipEvent("work-1-project-z", "work_project.added", SubjectWorkItem, "work-1", map[string]any{
			"work_id": "work-1", "project_id": "project-z", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		})},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-1"): 0},
	}); err != nil {
		t.Fatalf("create work: %v", err)
	}
	addWorkProject(t, s, "work-1", "project-a", "secondary", 2)
	workProjects, err := s.ProjectsForWork(ctx, "work-1")
	if err != nil || len(workProjects) != 2 || workProjects[0].Role != "primary" || workProjects[1].ID != "project-a" {
		t.Fatalf("ProjectsForWork ordering = %+v, err=%v", workProjects, err)
	}
	scope, err := s.ProductsForWork(ctx, "work-1")
	if err != nil || len(scope.Products) != 2 || !scope.CrossProduct || scope.Products[0].ID != "product-a" || scope.Products[1].ID != "product-b" {
		t.Fatalf("ProductsForWork scope = %+v, err=%v", scope, err)
	}
}

func TestPM5RolePromotionDemotesAndDemotionAllowsZeroPrimary(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product", "project-a")
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{projectCreatedEvent("project-b", "create-project-b"), membershipEvent("add-project-b", "product_project.added", SubjectProduct, "product", map[string]any{
			"product_id": "product", "project_id": "project-b", "role": "secondary", "reason": "test", "expected_version": 2, "resulting_version": 3,
		})},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 2, VersionRef(SubjectProject, "project-b"): 0},
	}); err != nil {
		t.Fatalf("add secondary: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{projectCreatedEvent("project-c", "create-project-c"), membershipEvent("duplicate-primary", "product_project.added", SubjectProduct, "product", map[string]any{
		"product_id": "product", "project_id": "project-c", "role": "primary", "reason": "test", "expected_version": 3, "resulting_version": 4,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 3, VersionRef(SubjectProject, "project-c"): 0}}); err == nil {
		t.Fatal("adding a second Product primary succeeded")
	} else {
		assertFailureKind(t, err, KindMembershipConflict)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{membershipEvent("promote", "product_project.role_changed", SubjectProduct, "product", map[string]any{
		"product_id": "product", "project_id": "project-b", "role": "primary", "reason": "test", "expected_version": 3, "resulting_version": 4,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 3}}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	var firstRole, secondRole string
	if err := s.DB().QueryRow(`SELECT role FROM product_projects WHERE product_id = 'product' AND project_id = 'project-a'`).Scan(&firstRole); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT role FROM product_projects WHERE product_id = 'product' AND project_id = 'project-b'`).Scan(&secondRole); err != nil {
		t.Fatal(err)
	}
	if firstRole != "secondary" || secondRole != "primary" {
		t.Fatalf("roles after promotion = %q, %q", firstRole, secondRole)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{membershipEvent("demote", "product_project.role_changed", SubjectProduct, "product", map[string]any{
		"product_id": "product", "project_id": "project-b", "role": "secondary", "reason": "test", "expected_version": 4, "resulting_version": 5,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 4}}); err != nil {
		t.Fatalf("demote: %v", err)
	}
	var primaries int
	if err := s.DB().QueryRow(`SELECT count(*) FROM product_projects WHERE product_id = 'product' AND role = 'primary'`).Scan(&primaries); err != nil {
		t.Fatal(err)
	}
	if primaries != 0 {
		t.Fatalf("primary count after demotion = %d, want zero", primaries)
	}
}

func TestPM5WorkPrimaryUniquenessIsPerWork(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product", "project-a")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		projectCreatedEvent("project-b", "create-project-b"),
		membershipEvent("product-project-b", "product_project.added", SubjectProduct, "product", map[string]any{
			"product_id": "product", "project_id": "project-b", "role": "secondary", "reason": "test", "expected_version": 2, "resulting_version": 3,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 2, VersionRef(SubjectProject, "project-b"): 0}}); err != nil {
		t.Fatalf("create second Project: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("work", "create-work"), membershipEvent("work-project-a", "work_project.added", SubjectWorkItem, "work", map[string]any{
		"work_id": "work", "project_id": "project-a", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work"): 0}}); err != nil {
		t.Fatalf("create work: %v", err)
	}
	err := ApplyOperation(ctx, s, Operation{Events: []Event{membershipEvent("work-project-b-primary", "work_project.added", SubjectWorkItem, "work", map[string]any{
		"work_id": "work", "project_id": "project-b", "role": "primary", "reason": "test", "expected_version": 2, "resulting_version": 3,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work"): 2}})
	assertFailureKind(t, err, KindMembershipConflict)
}

func TestPM5MembershipImpactAndTypedVersions(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product", "project-a")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("same", "create-same"), membershipEvent("same-work-project", "work_project.added", SubjectWorkItem, "same", map[string]any{
		"work_id": "same", "project_id": "project-a", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "same"): 0}}); err != nil {
		t.Fatalf("typed same-ID creation: %v", err)
	}
	result, err := ApplyOperationWithResult(ctx, s, Operation{Events: []Event{membershipEvent("move-a", "product_project.role_changed", SubjectProduct, "product", map[string]any{
		"product_id": "product", "project_id": "project-a", "role": "secondary", "reason": "test", "expected_version": 2, "resulting_version": 3,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 2, VersionRef(SubjectWorkItem, "same"): 2}})
	if err != nil {
		t.Fatalf("membership impact: %v", err)
	}
	if result.Impact.AffectedWorkCount != 1 || len(result.Impact.AffectedWorkIDs) != 1 || result.Impact.AffectedWorkIDs[0] != "same" || len(result.EventIDs) != 1 || result.EventIDs[0] != "move-a" {
		t.Fatalf("impact result = %+v", result)
	}
}

func TestPM5TypedVersionKeysAllowSameTextualIDAcrossSubjects(t *testing.T) {
	s := openTemp(t)
	err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("same", "same-product-created"),
			projectCreatedEvent("project", "same-project-created"),
			membershipEvent("same-product-project", "product_project.added", SubjectProduct, "same", map[string]any{
				"product_id": "same", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
			workCreatedEvent("same", "same-work-created"),
			membershipEvent("same-work-project", "work_project.added", SubjectWorkItem, "same", map[string]any{
				"work_id": "same", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "same"):    0,
			VersionRef(SubjectProject, "project"): 0,
			VersionRef(SubjectWorkItem, "same"):   0,
		},
	})
	if err != nil {
		t.Fatalf("same textual IDs collided: %v", err)
	}
}

func TestPM5MembershipTablesAreFoldOnlyAndProjectDeletionIsRestricted(t *testing.T) {
	s := openTemp(t)
	createProductProject(t, s, "product", "project")
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO product_projects(product_id, project_id, role) VALUES ('product', 'project', 'secondary')`); err == nil {
		t.Fatal("direct membership insert succeeded")
	}
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM projects WHERE id = 'project'`); err == nil {
		t.Fatal("deleting referenced Project succeeded")
	}
}

func TestPM5RemovingLastMembershipIsRejectedAndWorkStateIsUntouched(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	createProductProject(t, s, "product", "project")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("work", "create-work"), membershipEvent("work-project", "work_project.added", SubjectWorkItem, "work", map[string]any{
		"work_id": "work", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work"): 0}}); err != nil {
		t.Fatalf("create work: %v", err)
	}
	var beforeTitle, beforeLifecycle string
	var beforePriority, beforeVersion int64
	if err := s.DB().QueryRow(`SELECT title, lifecycle, priority, version FROM work_items WHERE id = 'work'`).Scan(&beforeTitle, &beforeLifecycle, &beforePriority, &beforeVersion); err != nil {
		t.Fatal(err)
	}
	err := ApplyOperation(ctx, s, Operation{Events: []Event{membershipEvent("remove-last-work-project", "work_project.removed", SubjectWorkItem, "work", map[string]any{
		"work_id": "work", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 2, "resulting_version": 3,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work"): 2}})
	assertFailureKind(t, err, KindMembershipInvariant)
	var afterTitle, afterLifecycle string
	var afterPriority, afterVersion int64
	if err := s.DB().QueryRow(`SELECT title, lifecycle, priority, version FROM work_items WHERE id = 'work'`).Scan(&afterTitle, &afterLifecycle, &afterPriority, &afterVersion); err != nil {
		t.Fatal(err)
	}
	if beforeTitle != afterTitle || beforeLifecycle != afterLifecycle || beforePriority != afterPriority || beforeVersion != afterVersion {
		t.Fatalf("failed membership removal changed canonical work: before=%q/%q/%d/%d after=%q/%q/%d/%d", beforeTitle, beforeLifecycle, beforePriority, beforeVersion, afterTitle, afterLifecycle, afterPriority, afterVersion)
	}

	// A Product and Project with one edge reject the same final-state removal.
	err = ApplyOperation(ctx, s, Operation{Events: []Event{membershipEvent("remove-last-product-project", "product_project.removed", SubjectProduct, "product", map[string]any{
		"product_id": "product", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 2, "resulting_version": 3,
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 2}})
	assertFailureKind(t, err, KindMembershipInvariant)
}

func TestPM5OpenRejectsExistingOrphanProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orphan.db")
	s := openTempAtPath(t, path)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO products(id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at) VALUES ('orphan', 'orphan', 'prototype', 'operator_only', 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, path)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMembershipInvariant {
		t.Fatalf("Open orphan error = %v, want membership invariant", err)
	}
}
