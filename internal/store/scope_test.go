package store

import (
	"context"
	"testing"
)

func TestResolveCompactionHomeUsesProductThenPrimaryMembership(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	repo := initKnowledgeRepo(t)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','proj','canonical_path',?,?,'now','now'); DELETE FROM fold_guard`, repo, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod','proj','locator-1'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	home, err := s.ResolveCompactionHome(context.Background(), "blocked")
	if err != nil || home.HomeProjectID != "proj" || home.HomeLocatorID != "locator-1" || home.RepoPath != repo {
		t.Fatalf("Product home=%#v err=%v", home, err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM product_knowledge_homes WHERE product_id='prod'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.ResolveCompactionHome(context.Background(), "blocked")
	if err != nil || fallback.HomeProjectID != "proj" || fallback.HomeLocatorID != "locator-1" {
		t.Fatalf("primary fallback=%#v err=%v", fallback, err)
	}
}

func TestScopeVersionMovesWhenProductMembershipChanges(t *testing.T) {
	s := seedQueryFixture(t)
	ctx := context.Background()
	before, products, err := s.ScopeVersion(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0] != "prod" {
		t.Fatalf("initial products = %v, want [prod]", products)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		operationEvent("q-project-sibling", "project.created", SubjectProject, "proj-2", map[string]any{"display_name": "Sibling"}),
		operationEvent("q-membership-sibling", "product_project.added", SubjectProduct, "prod", map[string]any{
			"product_id": "prod", "project_id": "proj-2", "role": "secondary", "reason": "scope test", "expected_version": 2, "resulting_version": 3,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectProject, "proj-2"): 0,
		VersionRef(SubjectProduct, "prod"):   2,
	}}); err != nil {
		t.Fatal(err)
	}
	after, products, err := s.ScopeVersion(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("adding a Product sibling did not change the scope version")
	}
	if len(products) != 1 || products[0] != "prod" {
		t.Fatalf("products after sibling add = %v, want [prod]", products)
	}
}
