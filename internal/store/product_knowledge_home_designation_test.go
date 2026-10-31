package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func operatorKnowledgeHome(t *testing.T, s *Store, product, project string) (ProductKnowledgeHomeDesignation, string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateProductWithProject(ctx, ProductCreation{ProductID: product, DisplayName: product, StageMaturity: "prototype", StageAudienceCommitment: "operator_only", ProjectID: project, ProjectDisplayName: project, Role: "primary"}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	locator := project + "-home"
	if err := s.AddProjectLocator(ctx, project, ProjectLocator{ID: locator, ProjectID: project, Kind: LocatorCanonicalPath, Value: repo, NormalizedValue: repo}, 1); err != nil {
		t.Fatal(err)
	}
	return ProductKnowledgeHomeDesignation{ProductID: product, ProjectID: project, LocatorID: locator, ExpectedVersion: 2}, repo
}

func TestProductKnowledgeHomeDesignationRepeatsWithoutFilesystemEffects(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"alpha", "beta", "Upper_ID:123", "7b676308-7fda-43b4-9d8f-ea840bad0672", strings.Repeat("long", 20)} {
		request, repo := operatorKnowledgeHome(t, s, id, id+"-source")
		for _, expected := range []int64{2, 3} {
			request.ExpectedVersion = expected
			result, err := s.DesignateProductKnowledgeHome(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			wantEvents := 1
			if expected == 3 {
				wantEvents = 0
			}
			if len(result.EventIDs) != wantEvents {
				t.Fatalf("version %d: event IDs = %v", expected, result.EventIDs)
			}
			version, err := s.EntityVersion(ctx, SubjectProduct, id)
			if err != nil || version != 3 {
				t.Fatalf("Product version = %d: %v", version, err)
			}
			entries, err := os.ReadDir(repo)
			if err != nil || len(entries) != 0 {
				t.Fatalf("designation wrote files: %v %v", entries, err)
			}
		}
		request.ExpectedVersion = 2
		_, err := s.DesignateProductKnowledgeHome(ctx, request)
		assertFailureKind(t, err, KindVersionConflict)
	}
	if countKnowledgeHomes(t, s) != 5 {
		t.Fatal("designation replaced another Product's home")
	}
}

func TestProductKnowledgeHomeDesignationPreservesAuthoredFiles(t *testing.T) {
	s := openTemp(t)
	request, repo := operatorKnowledgeHome(t, s, "Upper_Product", "Upper_Project")
	const contents = "Authored content is not designation input.\n"
	file := filepath.Join(repo, "authored.txt")
	if err := os.WriteFile(file, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DesignateProductKnowledgeHome(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != contents {
		t.Fatalf("designation changed authored bytes: %q %v", got, err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 1 {
		t.Fatalf("designation created files: %v %v", entries, err)
	}
}

func TestProductKnowledgeHomeDesignationRejectsContestedHome(t *testing.T) {
	s := openTemp(t)
	ctx := t.Context()
	owner, repo := operatorKnowledgeHome(t, s, "alpha", "alpha-source")
	other, _ := operatorKnowledgeHome(t, s, "beta", "beta-source")
	if _, err := s.DesignateProductKnowledgeHome(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProductProjectMembership(ctx, ProductMembershipAddition{ProductID: other.ProductID, ProjectID: owner.ProjectID, Role: "secondary", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	request := ProductKnowledgeHomeDesignation{ProductID: other.ProductID, ProjectID: owner.ProjectID, LocatorID: owner.LocatorID, ExpectedVersion: 3}
	result, err := s.DesignateProductKnowledgeHome(ctx, request)
	assertFailureKind(t, err, KindProjectionConflict)
	if len(result.EventIDs) != 0 || countKnowledgeHomes(t, s) != 1 {
		t.Fatalf("contested designation changed state: %+v", result)
	}
	var product string
	if err := s.db.QueryRowContext(ctx, `SELECT product_id FROM product_knowledge_homes WHERE project_id=? AND locator_id=?`, owner.ProjectID, owner.LocatorID).Scan(&product); err != nil || product != owner.ProductID {
		t.Fatalf("contested designation took over the home: %s %v", product, err)
	}
	version, err := s.EntityVersion(ctx, SubjectProduct, request.ProductID)
	if err != nil || version != request.ExpectedVersion {
		t.Fatalf("contested designation changed version: %d %v", version, err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("contested designation wrote files: %v %v", entries, err)
	}
}

func TestProductKnowledgeHomeDesignationFailureHasNoEffects(t *testing.T) {
	s := openTemp(t)
	request, repo := operatorKnowledgeHome(t, s, "alpha", "alpha-source")
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_home_designation BEFORE INSERT ON product_knowledge_homes BEGIN SELECT RAISE(ABORT,'synthetic designation failure'); END`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := s.DesignateProductKnowledgeHome(t.Context(), request)
	if err == nil || len(result.EventIDs) != 0 || countKnowledgeHomes(t, s) != 0 {
		t.Fatalf("failed designation effects: %+v %v", result, err)
	}
	var after int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&after); err != nil || after != before {
		t.Fatalf("failed designation changed receipts: %d -> %d %v", before, after, err)
	}
	version, err := s.EntityVersion(t.Context(), SubjectProduct, request.ProductID)
	if err != nil || version != request.ExpectedVersion {
		t.Fatalf("failed designation changed Product version: %d %v", version, err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed designation wrote files: %v %v", entries, err)
	}
}
