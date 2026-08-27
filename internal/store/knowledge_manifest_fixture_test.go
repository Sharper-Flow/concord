package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func seedEventDerivedLocator(t *testing.T, s *Store, projectID, locatorID, path string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateProductWithProject(ctx, ProductCreation{
		ProductID: "anchor-product-" + projectID, DisplayName: "Anchor Product",
		StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: projectID, ProjectDisplayName: "Anchor Project", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeProjectLocator(LocatorCanonicalPath, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, projectID, ProjectLocator{
		ID: locatorID, ProjectID: projectID, Kind: LocatorCanonicalPath,
		Value: path, NormalizedValue: normalized,
	}, 1); err != nil {
		t.Fatal(err)
	}
}

type manifestFixture struct {
	ID        string
	Kind      string
	Path      string
	Status    string
	Date      string
	Title     string
	Summary   string
	Tags      []string
	Scopes    KnowledgeRecordScopes
	Successor string
	Content   string
	// RootHomed opts a law fixture into the Product root rather than the
	// fixture child Domain, so a test can exercise the root-home claim and the
	// projection round trip that carries it.
	RootHomed bool
}

// fixtureRootHomeRationale is the claim a RootHomed fixture states.
const fixtureRootHomeRationale = "Fixture law binds every child Domain."

func writeManifestFixture(t *testing.T, repo string, fixtures ...manifestFixture) {
	t.Helper()
	digestRoot := sha256.Sum256([]byte(repo))
	fixtureProductKey := "fixture-" + hex.EncodeToString(digestRoot[:6])[:12]
	fixtureRootDomain := "product-root:" + fixtureProductKey
	// Fixture law names the child Domain that owns it. The root is reachable
	// without deciding anything, so a fixture that defaulted there would be
	// asserting a Product-wide claim none of these tests actually make.
	fixtureLawDomain := "fixture-law"
	records := make([]KnowledgeRecord, 0, len(fixtures))
	for _, fixture := range fixtures {
		content := fixture.Content
		if content == "" {
			content = "durable manifest blob\n"
		}
		writeKnowledgeFile(t, repo, fixture.Path, content)
		sum := sha256.Sum256([]byte(content))
		scopes := fixture.Scopes
		scopes.ProductIDs = append([]string{}, scopes.ProductIDs...)
		scopes.ProjectIDs = append([]string{}, scopes.ProjectIDs...)
		scopes.DomainIDs = append([]string{}, scopes.DomainIDs...)
		scopes.TagIDs = append([]string{}, scopes.TagIDs...)
		record := KnowledgeRecord{
			ID: fixture.ID, Kind: fixture.Kind, Path: fixture.Path, Status: fixture.Status,
			Date: fixture.Date, Title: fixture.Title, Summary: fixture.Summary, Tags: append([]string{}, fixture.Tags...),
			Scopes: scopes, Successor: fixture.Successor, SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		}
		if manifestLawBearingKinds[record.Kind] && record.Status == "accepted" && record.HomeDomainID == "" {
			if fixture.RootHomed {
				record.HomeDomainID = fixtureRootDomain
				record.ProductWideRationale = fixtureRootHomeRationale
			} else {
				record.HomeDomainID = fixtureLawDomain
			}
		}
		records = append(records, record)
	}
	manifest := KnowledgeManifest{SchemaVersion: "1.2", SupportedKinds: []string{"work_note", "decision", "spec", "lesson", "research"}, IndexedKinds: []string{"work_note", "decision", "spec", "lesson"}, Records: records}
	registryDomains := []KnowledgeDomain{
		{DomainID: fixtureRootDomain, Name: fixtureProductKey, Purpose: "fixture registry", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		{DomainID: fixtureLawDomain, Name: fixtureLawDomain, Purpose: "fixture law home", ParentDomainID: fixtureRootDomain, Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
	}
	declared := map[string]bool{fixtureRootDomain: true, fixtureLawDomain: true}
	for _, record := range records {
		for _, domainID := range record.Scopes.DomainIDs {
			if !declared[domainID] {
				declared[domainID] = true
				registryDomains = append(registryDomains, KnowledgeDomain{DomainID: domainID, Name: domainID, Purpose: "fixture scope domain", ParentDomainID: fixtureRootDomain, Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}})
			}
		}
	}
	manifest.DomainRegistry = KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: fixtureProductKey, RootDomainID: fixtureRootDomain,
		Domains: registryDomains,
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(content)+"\n")
}

func manifestFixtureFromFile(t *testing.T, repo string, id, kind, path, status, date, title, summary string, tags []string, scopes KnowledgeRecordScopes) manifestFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return manifestFixture{ID: id, Kind: kind, Path: path, Status: status, Date: date, Title: title, Summary: summary, Tags: tags, Scopes: scopes, Content: string(content)}
}

func authorizeKnowledgeLocator(t *testing.T, s *Store, home KnowledgeHome) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, ?, 1, 'now', 'now')`, home.HomeProjectID, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	anchorProductID := "anchor-product-" + home.HomeProjectID
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now')`, anchorProductID, anchorProductID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?,?,'secondary')`, anchorProductID, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?, ?, 'canonical_path', ?, ?, 'now', 'now')`, home.HomeLocatorID, home.HomeProjectID, home.RepoPath, home.RepoPath); err != nil {
		t.Fatal(err)
	}
}

func authorizeKnowledgeProductHome(t *testing.T, s *Store, productID string, home KnowledgeHome, membershipProjects ...string) {
	t.Helper()
	authorizeKnowledgeLocator(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now')`, productID, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?, ?, ?)`, productID, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, projectID := range membershipProjects {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, ?, 1, 'now', 'now')`, projectID, projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?, ?, 'secondary')`, productID, projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	}
}

func authorizeKnowledgeProductMembership(t *testing.T, s *Store, productID, projectID string) {
	t.Helper()
	authorizeKnowledgeLocator(t, s, KnowledgeHome{HomeProjectID: projectID, HomeLocatorID: "membership-" + projectID, RepoPath: t.TempDir()})
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now')`, productID, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?, ?, 'secondary')`, productID, projectID); err != nil {
		t.Fatal(err)
	}
}
