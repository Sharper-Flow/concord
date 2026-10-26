package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func domainShapeProjectionFixture(t *testing.T, s *Store, suffix string) (KnowledgeHome, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	law := "CD-0001"
	if suffix == "b" {
		law = "CD-0002"
	}
	writeManifestFixture(t, repo, manifestFixture{ID: law, Kind: "decision", Path: ".concord/docs/decisions/" + law + "-synthetic.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Synthetic law " + suffix, Summary: "Synthetic owner invariant", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law", "domain-b"}}})
	writeKnowledgeFile(t, repo, knowledgePackageMapPath, `{"schema_version":"1.1","packages":[{"path":"src","role":"domain","owner_domain_id":"fixture-law"},{"path":"cmd","role":"composition"},{"path":"testsupport","role":"test_support"},{"path":"legacy","role":"mixed","domain_ids":["fixture-law","domain-b"]}]}`)
	for _, root := range []string{"src", "cmd", "testsupport", "legacy"} {
		writeKnowledgeFile(t, repo, root+"/entry.go", "package synthetic\n")
	}
	// An invocation that would visibly fail if the refresh executed it.
	writeKnowledgeFile(t, repo, projectToolingManifestPath, `{"schema_version":"1.0","project":"synthetic","tools":[{"id":"check-tests","invocation":"false domain-shape-must-not-execute"}],"domain_shape":{"required_checks":["check-tests"]}}`)
	home := KnowledgeHome{HomeProjectID: "project-" + suffix, HomeLocatorID: "locator-" + suffix, RepoPath: repo, HeadRef: "HEAD"}
	product := "product-" + suffix
	authorizeKnowledgeProductHome(t, s, product, home, home.HomeProjectID)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"database-" + suffix, "cache-" + suffix} {
		if _, err := db.Exec(`INSERT INTO managed_resources(resource_id,display_name,class,kind,purpose,stage_maturity,stage_audience_commitment,environments,locator_absence_reason,metadata_schema_version,metadata,version,created_at,updated_at) VALUES(?,?,'infrastructure','database','synthetic','prototype','operator_only','[]','planned','1.0','{}',1,'now','now')`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO resource_products(resource_id,product_id,role,purpose,environments) VALUES(?,?,'owner','synthetic','[]')`, id, product); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []DomainFactKind{DomainFactInvariant, DomainFactDataObject, DomainFactStateAuthority, DomainFactConfigCoupling} {
		fact, _ := domainFactFixture(kind)
		fact.ID, fact.OwnerDomainID = string(kind)+"-"+suffix, "fixture-law"
		fact.Standing, fact.GoverningLawIDs = "law", []string{law}
		switch kind {
		case DomainFactInvariant:
			fact.Invariant.AppliesToDomainIDs = []string{"fixture-law", "domain-b"}
		case DomainFactDataObject:
			fact.DataObject.ResourceID, fact.DataObject.WriterDomainIDs, fact.DataObject.ReaderDomainIDs, fact.DataObject.InvariantIDs = "database-"+suffix, []string{"fixture-law"}, []string{"domain-b"}, []string{"invariant-" + suffix}
		case DomainFactStateAuthority:
			fact.StateAuthority.AuthoritativeResourceID = "database-" + suffix
			fact.StateAuthority.DerivedCopies[0].ResourceID = "cache-" + suffix
			fact.StateAuthority.DerivedCopies[0].RebuildRoute.ProjectID = home.HomeProjectID
		case DomainFactConfigCoupling:
			fact.ConfigCoupling.ReadBy.ProjectID = home.HomeProjectID
		}
		writeKnowledgeFile(t, repo, knowledgeFactTreeDir+fact.ID+".json", string(marshalDomainFact(t, fact))+"\n")
	}
	commit := commitKnowledgeRepo(t, repo, "synthetic Domain Shape")
	runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", commit)
	runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return home, product
}

func TestDomainShapeProjectionSnapshotIsolationAndPinnedInputs(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a, productA := domainShapeProjectionFixture(t, s, "a")
	b, productB := domainShapeProjectionFixture(t, s, "b")
	for _, item := range []struct {
		home            KnowledgeHome
		product, suffix string
	}{{a, productA, "a"}, {b, productB, "b"}} {
		commit := headCommit(t, item.home.RepoPath)
		// Dirty data, code and tooling must never enter a committed projection.
		writeKnowledgeFile(t, item.home.RepoPath, knowledgeFactTreeDir+"invariant-"+item.suffix+".json", "not JSON")
		writeKnowledgeFile(t, item.home.RepoPath, projectToolingManifestPath, "not JSON")
		if err := s.RebuildKnowledgeIndex(ctx, item.home); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadDomainShapeSnapshot(ctx, item.home, item.product)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Roots) != 4 || len(got.Facts) != 4 || got.KnowledgeCommit != commit || got.ToolingCommit != commit || got.ReferenceVerification != "unverified" || got.ExtractorVerification != "unverified" {
			t.Fatalf("snapshot: %+v", got)
		}
		roles := map[string]string{}
		for _, root := range got.Roots {
			roles[root.Path] = root.Role
		}
		if !reflect.DeepEqual(roles, map[string]string{"src": "domain", "cmd": "composition", "testsupport": "test_support", "legacy": "mixed"}) {
			t.Fatal(roles)
		}
		if !reflect.DeepEqual(got.RequiredChecks, []string{"check-tests"}) || !domainShapeHash.MatchString(got.ContentHash) || !domainShapeHash.MatchString(got.MapContentHash) || !domainShapeHash.MatchString(got.RegistryContentHash) {
			t.Fatal("missing pinned identities", got)
		}
		for _, fact := range got.Facts {
			raw := runKnowledgeGit(t, item.home.RepoPath, "show", commit+":"+knowledgeFactTreeDir+fact.ID+".json")
			if got.FactContentHashes[fact.ID] != domainShapeBytesHash([]byte(raw)) {
				t.Fatal("fact hash does not name exact committed bytes")
			}
		}
		var owners int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_package_domains WHERE home_project_id=? AND home_locator_id=? AND package_path='src' AND domain_id='fixture-law'`, item.home.HomeProjectID, item.home.HomeLocatorID).Scan(&owners); err != nil || owners != 1 {
			t.Fatalf("explicit owner missing from existing join: %d %v", owners, err)
		}
		if err := s.RebuildKnowledgeIndex(ctx, item.home); err != nil {
			t.Fatal(err)
		}
		again, err := s.ReadDomainShapeSnapshot(ctx, item.home, item.product)
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Fatalf("non-idempotent rebuild: %v", err)
		}
	}
	if _, err := s.ReadDomainShapeSnapshot(ctx, a, productB); err == nil {
		t.Fatal("cross-Product snapshot admitted")
	}
	gotA, _ := s.ReadDomainShapeSnapshot(ctx, a, productA)
	gotB, _ := s.ReadDomainShapeSnapshot(ctx, b, productB)
	for _, fact := range gotA.Facts {
		if !strings.HasSuffix(fact.ID, "-a") || !reflect.DeepEqual(fact.GoverningLawIDs, []string{"CD-0001"}) {
			t.Fatal("foreign law/fact", fact)
		}
	}
	for _, fact := range gotB.Facts {
		if !strings.HasSuffix(fact.ID, "-b") || !reflect.DeepEqual(fact.GoverningLawIDs, []string{"CD-0002"}) {
			t.Fatal("foreign law/fact", fact)
		}
	}
}

func TestDomainShapeProjectionRollbackAfterClear(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	home, product := domainShapeProjectionFixture(t, s, "rollback")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	before, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		t.Fatal(err)
	}
	beforeArchitecture := architectureSnapshot(t, s, home)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_shape_fact BEFORE INSERT ON knowledge_domain_facts BEGIN SELECT RAISE(ABORT,'synthetic projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, home.RepoPath, "src/new.go", "package synthetic\n")
	commitKnowledgeRepo(t, home.RepoPath, "new root content")
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil || !strings.Contains(err.Error(), "synthetic projection failure") {
		t.Fatalf("expected post-clear insertion failure: %v", err)
	}
	after, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil || !reflect.DeepEqual(before, after) || architectureSnapshot(t, s, home) != beforeArchitecture {
		t.Fatalf("rollback changed prior projection: %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_shape_fact`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	after, err = s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil || before.KnowledgeCommit == after.KnowledgeCommit {
		t.Fatal("root-only edit did not invalidate freshness", err)
	}
}

func TestDomainShapeProjectionRejectsInvalidFactsAndRoots(t *testing.T) {
	for _, name := range []string{"foreign resource", "foreign project", "foreign law", "unknown check", "non-required check", "malformed tooling", "symlink tooling", "unknown domain", "missing invariant", "missing root", "file root", "symlink root", "symlink fact", "nested fact", "oversize fact"} {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			ctx := context.Background()
			home, product := domainShapeProjectionFixture(t, s, "a")
			_, _ = domainShapeProjectionFixture(t, s, "b")
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			before, err := s.ReadDomainShapeSnapshot(ctx, home, product)
			if err != nil {
				t.Fatal(err)
			}
			id := "invariant-a"
			if name == "foreign resource" || name == "missing invariant" {
				id = "data_object-a"
			}
			if name == "foreign project" {
				id = "config_coupling-a"
			}
			factPath := filepath.Join(home.RepoPath, knowledgeFactTreeDir, id+".json")
			raw, err := os.ReadFile(factPath)
			if err != nil {
				t.Fatal(err)
			}
			var fact DomainFact
			if err := json.Unmarshal(raw, &fact); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "foreign resource":
				fact.DataObject.ResourceID = "database-b"
			case "foreign project":
				fact.ConfigCoupling.ReadBy.ProjectID = "project-b"
			case "foreign law":
				fact.GoverningLawIDs = []string{"CD-0002"}
			case "unknown check":
				fact.Invariant.EnforcedBy[0].CheckID = "unknown"
			case "non-required check":
				fact.Invariant.EnforcedBy[0].CheckID = "candidate-check"
				writeKnowledgeFile(t, home.RepoPath, projectToolingManifestPath, `{"schema_version":"1.0","project":"synthetic","tools":[{"id":"check-tests","invocation":"false"},{"id":"candidate-check","invocation":"false"}],"domain_shape":{"required_checks":["check-tests"]}}`)
			case "malformed tooling":
				writeKnowledgeFile(t, home.RepoPath, projectToolingManifestPath, `{"schema_version":"1.0","project":"synthetic","tools":[],"domain_shape":{"required_checks":["unknown"]}}`)
			case "symlink tooling":
				if err := os.Remove(filepath.Join(home.RepoPath, projectToolingManifestPath)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("docs/knowledge/manifest.json", filepath.Join(home.RepoPath, projectToolingManifestPath)); err != nil {
					t.Fatal(err)
				}
			case "unknown domain":
				fact.OwnerDomainID = "unknown"
			case "missing invariant":
				fact.DataObject.InvariantIDs = []string{"absent"}
			case "missing root":
				if err := os.RemoveAll(filepath.Join(home.RepoPath, "src")); err != nil {
					t.Fatal(err)
				}
			case "file root":
				if err := os.RemoveAll(filepath.Join(home.RepoPath, "src")); err != nil {
					t.Fatal(err)
				}
				writeKnowledgeFile(t, home.RepoPath, "src", "not a directory")
			case "symlink root":
				if err := os.RemoveAll(filepath.Join(home.RepoPath, "src")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("cmd", filepath.Join(home.RepoPath, "src")); err != nil {
					t.Fatal(err)
				}
			}
			writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+id+".json", string(marshalDomainFact(t, fact)))
			switch name {
			case "symlink fact":
				if err := os.Remove(factPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../manifest.json", factPath); err != nil {
					t.Fatal(err)
				}
			case "nested fact":
				writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+"nested/extra.json", "{}")
			case "oversize fact":
				writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+id+".json", strings.Repeat(" ", DomainFactMaxBytes+1))
			}
			commitKnowledgeRepo(t, home.RepoPath, "invalid shape input")
			if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
				t.Fatal("invalid shape rebuilt")
			}
			after, err := s.ReadDomainShapeSnapshot(ctx, home, product)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed prior snapshot", err)
			}
		})
	}
}

func TestDomainShapeProjectionRegisteredSourceRefusesFacts(t *testing.T) {
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "shape-home", HomeLocatorID: "shape-home-locator", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "shape-source", HomeLocatorID: "shape-source-locator", HeadRef: "HEAD"}
	s, source := seedFederatedProduct(t, "shape-product", home, source, "SOURCE-LAW", ".concord/docs/decisions/CD-0902-source-law.md", "Source law")
	before, err := s.ReadDomainShapeSnapshot(ctx, home, "shape-product")
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, source.RepoPath, knowledgeFactTreeDir+"fact.json", `{}`)
	commitKnowledgeRepo(t, source.RepoPath, "source carries a fact")
	err = s.RebuildKnowledgeIndex(ctx, source)
	if err == nil || !strings.Contains(err.Error(), "registered source cannot carry facts") {
		t.Fatal(err)
	}
	after, err := s.ReadDomainShapeSnapshot(ctx, home, "shape-product")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source changed the designated shape", err)
	}
}

func TestDomainShapeProjectionClearsRemovedFactsAndKeepsOtherHome(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a, productA := domainShapeProjectionFixture(t, s, "a")
	b, productB := domainShapeProjectionFixture(t, s, "b")
	for _, home := range []KnowledgeHome{a, b} {
		if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.ReadDomainShapeSnapshot(ctx, b, productB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(a.RepoPath, knowledgeFactTreeDir)); err != nil {
		t.Fatal(err)
	}
	commit := commitKnowledgeRepo(t, a.RepoPath, "remove facts")
	if err := s.EnsureKnowledgeIndexFresh(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadDomainShapeSnapshot(ctx, a, productA)
	if err != nil || len(got.Facts) != 0 || got.Facts == nil || got.KnowledgeCommit != commit || len(got.Roots) != 4 || len(got.FactContentHashes) != 0 {
		t.Fatalf("removed facts survived: %+v %v", got, err)
	}
	after, err := s.ReadDomainShapeSnapshot(ctx, b, productB)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("clearing one home changed another", err)
	}
}

func TestDomainShapeProjectionHistoricalAndEmptyMaps(t *testing.T) {
	ctx := context.Background()
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical", true: "empty"}[empty], func(t *testing.T) {
			repo := architectureFixtureRepo(t)
			if empty {
				writeKnowledgeFile(t, repo, knowledgePackageMapPath, `{"schema_version":"1.1","packages":[]}`)
				commitKnowledgeRepo(t, repo, "empty shape")
			}
			s, home := architectureFixtureStore(t, repo)
			got, err := s.ReadDomainShapeSnapshot(ctx, home, "arch-product")
			if err != nil || got.Roots == nil || got.Facts == nil || len(got.Facts) != 0 {
				t.Fatalf("snapshot: %+v %v", got, err)
			}
			if empty {
				if len(got.Roots) != 0 {
					t.Fatal(got.Roots)
				}
				return
			}
			if len(got.Roots) != 2 {
				t.Fatal(got.Roots)
			}
			for _, root := range got.Roots {
				if root.Role != "mixed" || root.OwnerDomainID != "" || len(root.DomainIDs) != 1 {
					t.Fatal("historical ownership overstated", root)
				}
			}
		})
	}
}

func TestDomainShapeProjectionWithoutOriginRef(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	home, product := domainShapeProjectionFixture(t, s, "a")
	runKnowledgeGit(t, home.RepoPath, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	runKnowledgeGit(t, home.RepoPath, "update-ref", "-d", "refs/remotes/origin/main")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		t.Fatal(err)
	}
	commit := headCommit(t, home.RepoPath)
	raw := runKnowledgeGit(t, home.RepoPath, "show", commit+":"+projectToolingManifestPath)
	if got.KnowledgeCommit != commit || got.ToolingCommit != commit || got.ToolingContentHash != domainShapeBytesHash([]byte(raw)) || !reflect.DeepEqual(got.RequiredChecks, []string{"check-tests"}) || got.ReferenceVerification != "unverified" || got.ExtractorVerification != "unverified" {
		t.Fatalf("unpinned or overstated snapshot: %+v", got)
	}
}

func TestDomainShapeProjectionLocalSameCommitCheckAndFact(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	home, product := domainShapeProjectionFixture(t, s, "a")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	before, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		t.Fatal(err)
	}
	tooling := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+projectToolingManifestPath)
	tooling = strings.ReplaceAll(tooling, "check-tests", "local-check")
	writeKnowledgeFile(t, home.RepoPath, projectToolingManifestPath, tooling)
	factPath := knowledgeFactTreeDir + "invariant-a.json"
	fact := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+factPath)
	fact = strings.ReplaceAll(fact, "check-tests", "local-check")
	writeKnowledgeFile(t, home.RepoPath, factPath, fact)
	commit := commitKnowledgeRepo(t, home.RepoPath, "local check and fact")
	// A mutable declaration cannot replace the committed proof.
	writeKnowledgeFile(t, home.RepoPath, projectToolingManifestPath, "not JSON")
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		t.Fatal(err)
	}
	if got.KnowledgeCommit != commit || got.ToolingCommit != commit || got.ToolingContentHash != domainShapeBytesHash([]byte(tooling)) || got.FactContentHashes["invariant-a"] != domainShapeBytesHash([]byte(fact)) || !reflect.DeepEqual(got.RequiredChecks, []string{"local-check"}) || got.ContentHash == before.ContentHash || got.ReferenceVerification != "unverified" || got.ExtractorVerification != "unverified" {
		t.Fatalf("local declaration and fact were not pinned together: %+v", got)
	}
	defaultCommit, defaultHash, defaultChecks, err := domainShapeDefaultTooling(ctx, home)
	if err != nil || defaultCommit != before.ToolingCommit || defaultHash != before.ToolingContentHash || !reflect.DeepEqual(defaultChecks, before.RequiredChecks) {
		t.Fatalf("explicit-check default authority changed: %s %s %v %v", defaultCommit, defaultHash, defaultChecks, err)
	}
}

func TestDomainShapeProjectionUnrelatedRemoteAdvance(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	home, product := domainShapeProjectionFixture(t, s, "a")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	before, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil {
		t.Fatal(err)
	}
	// The code symbols in the fixture do not exist; structural indexing stays
	// unverified. Fetching unrelated default tooling must not change its inputs.
	raw := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+projectToolingManifestPath)
	writeKnowledgeFile(t, home.RepoPath, projectToolingManifestPath, strings.Replace(raw, "false domain-shape-must-not-execute", "false different-required-invocation", 1))
	toolCommit := commitKnowledgeRepo(t, home.RepoPath, "default tooling changes")
	runKnowledgeGit(t, home.RepoPath, "update-ref", "refs/remotes/origin/main", toolCommit)
	runKnowledgeGit(t, home.RepoPath, "checkout", "--detach", before.KnowledgeCommit)
	watermark, err := readKnowledgeWatermark(ctx, s.db, home, before.KnowledgeCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !watermark.Fresh {
		t.Error("unrelated remote advance invalidated the knowledge digest")
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	after, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("unrelated remote advance changed the snapshot: %+v %v", after, err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := s.ReadDomainShapeSnapshot(ctx, home, product)
	if err != nil || !reflect.DeepEqual(before, rebuilt) {
		t.Fatalf("unrelated remote advance changed the rebuilt snapshot: %+v %v", rebuilt, err)
	}
}

func TestDomainShapeSnapshotReadAdmission(t *testing.T) {
	ctx := context.Background()
	var missing *Store
	if _, err := missing.ReadDomainShapeSnapshot(ctx, KnowledgeHome{}, "product"); err == nil {
		t.Fatal("nil store admitted")
	}
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	if _, err := s.ReadDomainShapeSnapshot(ctx, home, product); err == nil {
		t.Fatal("unbuilt projection admitted")
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE knowledge_index_watermark SET projection_version=?`, knowledgeProjectionVersion-1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDomainShapeSnapshot(ctx, home, product); err == nil {
		t.Fatal("older projection admitted")
	}
}

func TestDomainShapeProjectionRootTreeBatchAndFactBound(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "src/nested/file.go", "package synthetic\n")
	commit := commitKnowledgeRepo(t, repo, "nested root fixture")
	roots := []knowledgePackageEntry{{Path: "src"}, {Path: "src/nested"}}
	identity, err := domainShapeRootTreeIdentity(ctx, KnowledgeHome{RepoPath: repo}, commit, roots)
	if err != nil || !strings.Contains(string(identity), "src=") || !strings.Contains(string(identity), "src/nested=") {
		t.Fatalf("nested batch root proof: %s %v", identity, err)
	}
	facts := map[string][]byte{}
	for i := 0; i <= DomainShapeMaxItems; i++ {
		facts["fact-"+strconv.Itoa(i)+".json"] = []byte(`{}`)
	}
	_, err = prepareDomainShapeProjection(ctx, KnowledgeHome{}, "", knowledgeShards{facts: facts}, KnowledgeManifest{}, knowledgeArchitecture{}, true)
	if err == nil || !strings.Contains(err.Error(), "fact shard count exceeds 512") {
		t.Fatal("unbounded fact inventory admitted", err)
	}
}

func TestDomainShapeProjectionFactsRequireManifest(t *testing.T) {
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, knowledgeFactTreeDir+"fact.json", `{}`)
	commit := commitKnowledgeRepo(t, repo, "orphan fact tree")
	_, _, err := readKnowledgeShardsAtCommitRunner(context.Background(), runGitRunner{}, repo, commit)
	if err == nil || !strings.Contains(err.Error(), "fact shards require a committed knowledge manifest head") {
		t.Fatal(err)
	}
}

func TestDomainShapeSnapshotUsesCurrentModelOwnership(t *testing.T) {
	for _, scope := range []string{"resource", "project"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			home, product := domainShapeProjectionFixture(t, s, "a")
			_, _ = domainShapeProjectionFixture(t, s, "b")
			if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadDomainShapeSnapshot(ctx, home, product); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE resource_products SET product_id='product-b' WHERE resource_id='database-a' AND product_id='product-a'`
			if scope == "project" {
				query = `DELETE FROM product_projects WHERE product_id='product-a' AND project_id='project-a'`
			}
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadDomainShapeSnapshot(ctx, home, product); err == nil || !strings.Contains(err.Error(), "current Product-owned resources or member Projects") {
				t.Fatalf("stale model scope admitted: %v", err)
			}
		})
	}
}
