package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductKnowledgeHomeRejectsOffModeWithoutEffects(t *testing.T) {
	s := openTemp(t)
	request, repo := operatorOffModeKnowledgeHome(t, s, "off-product", "off-project")
	var before int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := s.DesignateProductKnowledgeHome(t.Context(), request)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Errorf("off-mode designation error = %v, want invalid operation", err)
	} else if failure.RecoveryAction == "" || failure.RetrySafe {
		t.Errorf("off-mode failure lacks actionable recovery: %+v", failure)
	}
	var after int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	version, versionErr := s.EntityVersion(t.Context(), SubjectProduct, request.ProductID)
	entries, readErr := os.ReadDir(repo)
	if len(result.EventIDs) != 0 || countKnowledgeHomes(t, s) != 0 || before != after || versionErr != nil || version != request.ExpectedVersion || readErr != nil || len(entries) != 0 {
		t.Errorf("refusal changed state: result=%+v receipts=%d->%d version=%d (%v) files=%v (%v)", result, before, after, version, versionErr, entries, readErr)
	}
}

func TestProductKnowledgeHomeDirectEventRejectsOffMode(t *testing.T) {
	s := openTemp(t)
	request, _ := operatorOffModeKnowledgeHome(t, s, "event-product", "event-project")
	event := operationEvent("off-home-event", "product.knowledge_home_designated", SubjectProduct, request.ProductID, map[string]any{
		"product_id": request.ProductID, "project_id": request.ProjectID, "locator_id": request.LocatorID,
		"reason": "operator designation", "expected_version": 2, "resulting_version": 3,
	})
	_, err := ApplyOperationWithResult(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): 2}})
	assertFailureKind(t, err, KindInvalidOperation)
	var receipts int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	version, err := s.EntityVersion(t.Context(), SubjectProduct, request.ProductID)
	if err != nil || version != 2 || receipts != 0 || countKnowledgeHomes(t, s) != 0 {
		t.Fatalf("direct event changed state: version=%d receipts=%d error=%v", version, receipts, err)
	}
}

func TestProductKnowledgeHomeOffModeMemberAndInvalidHomeRecovery(t *testing.T) {
	s := openTemp(t)
	ctx := t.Context()
	old, _ := operatorKnowledgeHome(t, s, "recovery-product", "old-home")
	if _, err := s.DesignateProductKnowledgeHome(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectRepositoryMode(ctx, ProjectRepositoryModeChange{ProjectID: old.ProjectID, RepositoryMode: "off", ExpectedVersion: 3}); err != nil {
		t.Fatal(err)
	}
	old.ExpectedVersion = 3
	_, err := s.DesignateProductKnowledgeHome(ctx, old)
	assertFailureKind(t, err, KindInvalidOperation)
	var stored string
	if err := s.db.QueryRow(`SELECT project_id FROM product_knowledge_homes WHERE product_id=?`, old.ProductID).Scan(&stored); err != nil || stored != old.ProjectID {
		t.Fatalf("invalid home changed on refusal: %q %v", stored, err)
	}
	if _, err := s.ClearProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{ProductID: old.ProductID, ExpectedVersion: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProjectForProduct(ctx, ProjectCreation{ProjectID: "new-home", DisplayName: "New Home", ProductID: old.ProductID, Role: "secondary", ExpectedProductVersion: 4}); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if err := s.AddProjectLocator(ctx, "new-home", ProjectLocator{ID: "new-home-locator", ProjectID: "new-home", Kind: LocatorCanonicalPath, Value: path, NormalizedValue: path}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectRepositoryMode(ctx, ProjectRepositoryModeChange{ProjectID: "new-home", RepositoryMode: "on", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{ProductID: old.ProductID, ProjectID: "new-home", LocatorID: "new-home-locator", ExpectedVersion: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT project_id FROM product_knowledge_homes WHERE product_id=?`, old.ProductID).Scan(&stored); err != nil || stored != "new-home" {
		t.Fatalf("replacement home = %q: %v", stored, err)
	}
}

func operatorKnowledgeHome(t *testing.T, s *Store, product, project string) (ProductKnowledgeHomeDesignation, string) {
	t.Helper()
	request, repo := operatorOffModeKnowledgeHome(t, s, product, project)
	if _, err := s.SetProjectRepositoryMode(t.Context(), ProjectRepositoryModeChange{ProjectID: project, RepositoryMode: "on", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	return request, repo
}

func operatorOffModeKnowledgeHome(t *testing.T, s *Store, product, project string) (ProductKnowledgeHomeDesignation, string) {
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
