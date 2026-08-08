package store

import (
	"context"
	"testing"
)

func TestResolveCompactionHomeUsesProductThenPrimaryMembership(t *testing.T) {
	s := seedQueryFixture(t)
	repo := initKnowledgeRepo(t)
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','proj','canonical_path',?,?,'now','now'); DELETE FROM fold_guard`, repo, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod','proj','locator-1')`); err != nil {
		t.Fatal(err)
	}
	home, err := s.ResolveCompactionHome(context.Background(), "blocked")
	if err != nil || home.HomeProjectID != "proj" || home.HomeLocatorID != "locator-1" || home.RepoPath != repo {
		t.Fatalf("Product home=%#v err=%v", home, err)
	}
	if _, err := s.DB().Exec(`DELETE FROM product_knowledge_homes WHERE product_id='prod'`); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.ResolveCompactionHome(context.Background(), "blocked")
	if err != nil || fallback.HomeProjectID != "proj" || fallback.HomeLocatorID != "locator-1" {
		t.Fatalf("primary fallback=%#v err=%v", fallback, err)
	}
}
