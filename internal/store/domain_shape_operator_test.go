package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shapeOperatorHome(t *testing.T, s *Store, product, project string) (ProductKnowledgeHomeDesignation, string) {
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

func TestDomainShapeOperatorDesignationCreatesAndRepeatsTwoHomes(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"alpha", "beta"} {
		request, repo := shapeOperatorHome(t, s, id, id+"-source")
		result, err := s.DesignateProductDomainShape(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.EventIDs) != 1 || len(result.Setup.Created) == 0 {
			t.Fatalf("missing configuration/setup effects: %+v", result)
		}
		before := map[string][]byte{}
		for _, name := range []string{knowledgeHeadPath, knowledgeRegistryPath, knowledgePackageMapPath, domainShapeRatchetPath, projectToolingManifestPath} {
			raw, err := os.ReadFile(filepath.Join(repo, name))
			if err != nil {
				t.Fatal(err)
			}
			before[name] = raw
		}
		var registry KnowledgeDomainRegistry
		if err := json.Unmarshal(before[knowledgeRegistryPath], &registry); err != nil {
			t.Fatal(err)
		}
		if registry.ProductKey != id || registry.RootDomainID != "product-root:"+id {
			t.Fatalf("foreign identity: %+v", registry)
		}
		var eventsBefore int
		if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&eventsBefore); err != nil {
			t.Fatal(err)
		}
		request.ExpectedVersion = 3
		repeat, err := s.DesignateProductDomainShape(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(repeat.EventIDs) != 0 || len(repeat.Setup.Created) != 0 || len(repeat.Setup.Updated) != 0 {
			t.Fatalf("repeat changed identity: %+v", repeat)
		}
		var eventsAfter int
		if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&eventsAfter); err != nil {
			t.Fatal(err)
		}
		if eventsBefore != eventsAfter {
			t.Fatal("repeat appended events")
		}
		for name, want := range before {
			got, err := os.ReadFile(filepath.Join(repo, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("repeat rewrote %s: %v", name, err)
			}
		}
		if info, err := os.Stat(filepath.Join(repo, domainShapeFactsDir)); err != nil || !info.IsDir() {
			t.Fatalf("missing facts directory: %v", err)
		}
	}
	if countKnowledgeHomes(t, s) != 2 {
		t.Fatal("second Product replaced the first home")
	}
}

func TestDomainShapeOperatorPreflightDoesNotWrite(t *testing.T) {
	for _, scenario := range []string{"version", "locator-project", "membership", "competing", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			s := openTemp(t)
			ctx := context.Background()
			request, repo := shapeOperatorHome(t, s, "alpha", "alpha-source")
			other, _ := shapeOperatorHome(t, s, "beta", "beta-source")
			switch scenario {
			case "version":
				request.ExpectedVersion = 1
			case "locator-project":
				request.ProjectID = other.ProjectID
			case "membership":
				request.ProjectID, request.LocatorID = other.ProjectID, other.LocatorID
			case "competing":
				if _, err := s.DesignateProductKnowledgeHome(ctx, request); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AddProductProjectMembership(ctx, ProductMembershipAddition{ProductID: other.ProductID, ProjectID: request.ProjectID, Role: "secondary", ExpectedVersion: 2}); err != nil {
					t.Fatal(err)
				}
				request.ProductID, request.ExpectedVersion = other.ProductID, 3
			case "malformed":
				request.LocatorID = ""
			}
			if _, err := s.DesignateProductDomainShape(ctx, request); err == nil {
				t.Fatal("preflight accepted invalid request")
			}
			entries, err := os.ReadDir(repo)
			if err != nil || len(entries) != 0 {
				t.Fatalf("preflight wrote files: %v, %v", entries, err)
			}
		})
	}
}

func TestDomainShapeOperatorConfigRouteStillHasNoSetupEffects(t *testing.T) {
	s := openTemp(t)
	request, repo := shapeOperatorHome(t, s, "alpha", "alpha-source")
	if _, err := s.DesignateProductKnowledgeHome(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replay/config route wrote files: %v, %v", entries, err)
	}
}

func TestDomainShapeOperatorReportsFilesystemEffectsOnConfigurationFailure(t *testing.T) {
	s := openTemp(t)
	request, repo := shapeOperatorHome(t, s, "alpha", "alpha-source")
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_shape_designation BEFORE INSERT ON product_knowledge_homes BEGIN SELECT RAISE(ABORT,'synthetic designation failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.DesignateProductDomainShape(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "filesystem changes remain") || len(result.Setup.Created) == 0 || len(result.EventIDs) != 0 {
		t.Fatalf("lost partial effects: %+v %v", result, err)
	}
	if countKnowledgeHomes(t, s) != 0 {
		t.Fatal("failed transaction left a designation")
	}
	if _, err := os.Stat(filepath.Join(repo, knowledgeRegistryPath)); err != nil {
		t.Fatal("filesystem effects were falsely rolled back", err)
	}
}

func TestDomainShapeOperatorFinalTransactionRechecksLocator(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	request, repo := shapeOperatorHome(t, s, "alpha", "alpha-source")
	state, err := preflightDomainShapeDesignation(ctx, s.db, request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareDomainShapeSetup(DomainShapeSetupRequest{RepoPath: repo, ProductKey: request.ProductID, ProjectKey: request.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := plan.Apply()
	if err != nil {
		t.Fatal(err)
	}
	// A Project mutation can change the path without bumping Product.version.
	// Product optimistic concurrency alone would therefore not catch this race.
	replacement := t.TempDir()
	if err := s.UpdateProjectLocator(ctx, request.ProjectID, ProjectLocator{ID: request.LocatorID, ProjectID: request.ProjectID, Kind: LocatorCanonicalPath, Value: replacement, NormalizedValue: replacement}, 2); err != nil {
		t.Fatal(err)
	}
	result := DomainShapeDesignationResult{Setup: setup}
	event := Event{EventID: operatorEventID("product.knowledge_home_designated", request.ProductID), Kind: "product.knowledge_home_designated", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1}
	err = s.Transact(ctx, func(tx *Transaction) error { return designateDomainShapeTx(ctx, tx, request, state, event, &result) })
	if err == nil || !strings.Contains(err.Error(), "configuration changed during setup") {
		t.Fatalf("locator race accepted: %v", err)
	}
	if countKnowledgeHomes(t, s) != 0 || len(result.EventIDs) != 0 {
		t.Fatal("refused final transaction recorded a designation")
	}
	if _, err := os.Stat(filepath.Join(repo, knowledgeRegistryPath)); err != nil {
		t.Fatal("lost reported filesystem effects", err)
	}
	entries, err := os.ReadDir(replacement)
	if err != nil || len(entries) != 0 {
		t.Fatalf("setup followed the changed locator: %v %v", entries, err)
	}
}

func TestDomainShapeOperatorPreparesAllFactAnchorsFromSelectedSnapshot(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	home, product := domainShapeProjectionFixture(t, s, "a")
	writeKnowledgeFile(t, home.RepoPath, domainShapeRatchetPath, `{"schema_version":"1.0","mixed_roots":["legacy"],"imports":[]}`)
	commit := commitKnowledgeRepo(t, home.RepoPath, "Synthetic strict check baseline")
	runKnowledgeGit(t, home.RepoPath, "update-ref", "refs/remotes/origin/main", commit)
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		t.Fatal(err)
	}
	input, tooling, toolCommit, toolHash, ratchetHash, err := prepareDomainShapeOperatorInput(ctx, home, product, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Facts) != 4 || len(input.RepositoryAnchors) != 1 || len(input.EnforcementAnchors) != 1 || len(input.RequiredChecks) != 1 || !input.Strict {
		t.Fatalf("incomplete/doubled requested inventory: %+v", input)
	}
	if input.RepositoryAnchors[0].Symbol != "Rebuild" || input.EnforcementAnchors[0].CheckID != "check-tests" {
		t.Fatal("anchors were not selected from facts")
	}
	if tooling == nil || toolCommit != commit || toolHash != snapshot.ToolingContentHash || !domainShapeHash.MatchString(ratchetHash) || input.Subject.ContentHash == snapshot.ContentHash {
		t.Fatal("missing pinned default-tooling/ratchet identity")
	}
	if tooling.Project == home.HomeProjectID {
		t.Fatal("fixture failed to distinguish repository key from SQLite Project ID")
	}
}
