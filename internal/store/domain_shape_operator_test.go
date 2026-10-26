package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestDomainShapeOperatorArbitraryDatabaseIdentities(t *testing.T) {
	for _, id := range []string{"Upper_ID:123", "7b676308-7fda-43b4-9d8f-ea840bad0672", strings.Repeat("long", 20)} {
		t.Run(id, func(t *testing.T) {
			s := openTemp(t)
			request, repo := shapeOperatorHome(t, s, id, id+"-source")
			first, err := domainShapeOperatorSetupIdentity(repo, request.ProductID, request.ProjectID)
			if err != nil || !validProductKey(first.ProductKey) || !validProductKey(first.ProjectKey) {
				t.Fatalf("database IDs did not produce authored keys: %+v %v", first, err)
			}
			second, err := domainShapeOperatorSetupIdentity(repo, request.ProductID, request.ProjectID)
			if err != nil || first != second || first.ProductKey == first.ProjectKey {
				t.Fatalf("unstable/colliding keys: %+v %+v %v", first, second, err)
			}
			if _, err := s.DesignateProductDomainShape(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			request.ExpectedVersion = 3
			if repeat, err := s.DesignateProductDomainShape(context.Background(), request); err != nil || len(repeat.Setup.Created)+len(repeat.Setup.Updated)+len(repeat.EventIDs) != 0 {
				t.Fatalf("repeat: %+v %v", repeat, err)
			}
		})
	}
}

func TestDomainShapeOperatorPreservesAuthoredKeysForArbitraryIDs(t *testing.T) {
	s := openTemp(t)
	request, repo := shapeOperatorHome(t, s, "Upper_Product", "Upper_Project")
	plan, err := PrepareDomainShapeSetup(DomainShapeSetupRequest{RepoPath: repo, ProductKey: "authored-product", ProjectKey: "authored-project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	before := setupShapeSnapshot(t, repo)
	if _, err := s.DesignateProductDomainShape(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, setupShapeSnapshot(t, repo)) {
		t.Fatal("designation rewrote valid authored keys")
	}
}

func TestDomainShapeOperatorPinsRatchetAndCheckAuthority(t *testing.T) {
	const empty = `{"schema_version":"1.0","mixed_roots":[],"imports":[]}`
	const mixed = `{"schema_version":"1.0","mixed_roots":["legacy"],"imports":[]}`
	const imported = `{"schema_version":"1.0","mixed_roots":[],"imports":[{"source_root":"src","target_root":"cmd"}]}`
	for _, test := range []struct{ name, baseline, candidate, want string }{
		{"mixed-addition", empty, mixed, "ratchet adds"},
		{"import-addition", empty, imported, "ratchet adds"},
		{"mixed-removal", mixed, empty, ""},
		{"import-removal", imported, empty, ""},
		{"missing-baseline-empty", "", empty, ""},
		{"missing-baseline-addition", "", mixed, "ratchet adds"},
		{"malformed-baseline", `{}`, empty, domainShapeRatchetPath},
		{"null-baseline", `{"schema_version":"1.0","mixed_roots":null,"imports":[]}`, empty, domainShapeRatchetPath},
		{"duplicate-baseline", `{"schema_version":"1.0","mixed_roots":[],"mixed_roots":[],"imports":[]}`, empty, domainShapeRatchetPath},
		{"invalid-baseline-root", `{"schema_version":"1.0","mixed_roots":["../outside"],"imports":[]}`, empty, domainShapeRatchetPath},
		{"candidate-tooling", mixed, mixed, ""},
		{"candidate-check", mixed, mixed, "required check"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, product, snapshot, baseline := shapeOperatorAuthorityCandidate(t, test.name, test.baseline, test.candidate)
			input, tooling, commit, _, hash, err := prepareDomainShapeOperatorInput(context.Background(), home, product, snapshot)
			if test.want != "" {
				var failure *Failure
				if err == nil || !strings.Contains(err.Error(), test.want) || !errors.As(err, &failure) {
					t.Fatalf("wanted typed %q refusal, got %v", test.want, err)
				}
				return
			}
			if err != nil || commit != baseline || hash != domainShapeBytesHash([]byte(test.candidate)) {
				t.Fatalf("pinned input: commit=%s hash=%s err=%v", commit, hash, err)
			}
			var want DomainShapeRatchet
			if err := json.Unmarshal([]byte(test.candidate), &want); err != nil || !reflect.DeepEqual(input.Ratchet, want) {
				t.Fatalf("candidate reduction lost: %+v %v", input.Ratchet, err)
			}
			if test.name == "candidate-tooling" && tooling.Declared[0].Invocation != "false domain-shape-must-not-execute" {
				t.Fatal("candidate command replaced default authority")
			}
		})
	}
}

func shapeOperatorAuthorityCandidate(t *testing.T, name, baselineRatchet, candidateRatchet string) (KnowledgeHome, string, DomainShapeSnapshot, string) {
	t.Helper()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "authority")
	if baselineRatchet != "" {
		writeKnowledgeFile(t, home.RepoPath, domainShapeRatchetPath, baselineRatchet)
	}
	writeKnowledgeFile(t, home.RepoPath, "revision.txt", "baseline\n")
	baseline := commitKnowledgeRepo(t, home.RepoPath, "Synthetic operator baseline")
	runKnowledgeGit(t, home.RepoPath, "update-ref", "refs/remotes/origin/main", baseline)
	writeKnowledgeFile(t, home.RepoPath, domainShapeRatchetPath, candidateRatchet)
	writeKnowledgeFile(t, home.RepoPath, "revision.txt", "candidate\n")
	if strings.HasPrefix(name, "candidate-") {
		checks := `["check-tests"]`
		if name == "candidate-check" {
			checks = `["check-tests","candidate-only"]`
		}
		writeKnowledgeFile(t, home.RepoPath, projectToolingManifestPath, `{"schema_version":"1.0","project":"synthetic","tools":[{"id":"check-tests","invocation":"true"},{"id":"candidate-only","invocation":"true"}],"domain_shape":{"required_checks":`+checks+`}}`)
	}
	commitKnowledgeRepo(t, home.RepoPath, "Synthetic operator candidate")
	if err := s.EnsureKnowledgeIndexFresh(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ReadDomainShapeSnapshot(context.Background(), home, product)
	if err != nil {
		t.Fatal(err)
	}
	return home, product, snapshot, baseline
}

func TestDomainShapeOperatorRatchetAbsenceRequiresTreeProof(t *testing.T) {
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "revision.txt", "baseline\n")
	commit := commitKnowledgeRepo(t, repo, "Synthetic empty baseline")
	home := KnowledgeHome{RepoPath: repo}
	for _, test := range []struct {
		name, commit string
		allowAbsent  bool
	}{
		{"baseline-absence", commit, true},
		{"candidate-absence", commit, false},
		{"unavailable-baseline", strings.Repeat("f", 40), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ratchet, _, err := readDomainShapeOperatorRatchet(context.Background(), home, test.commit, test.allowAbsent)
			if test.name == "baseline-absence" {
				if err != nil || ratchet.MixedRoots == nil || ratchet.Imports == nil {
					t.Fatalf("proved absence: %+v %v", ratchet, err)
				}
				return
			}
			var failure *Failure
			if err == nil || !errors.As(err, &failure) || !strings.Contains(err.Error(), domainShapeRatchetPath) {
				t.Fatalf("missing typed path refusal: %v", err)
			}
		})
	}
}
