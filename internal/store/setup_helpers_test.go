package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// setupProductWithProject seeds one Product, one Project, and their primary
// membership through the event path. Shared test scaffolding.
func setupProductWithProject(t *testing.T, s *Store, productID, projectID string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent(productID, productID+"-created"), projectCreatedEvent(projectID, projectID+"-created"), membershipEvent(productID+"-membership", "product_project.added", SubjectProduct, productID, map[string]any{"product_id": productID, "project_id": projectID, "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, productID): 0, VersionRef(SubjectProject, projectID): 0}}); err != nil {
		t.Fatalf("setup Product %s: %v", productID, err)
	}
}

// anchorHomePair seeds one Project with its locator and an anchoring Product.
// Shared test scaffolding.
func anchorHomePair(t *testing.T, s *Store, projectID, locatorID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?,?,1,'t','t')`, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	anchorProductID := "anchor-product-" + projectID
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?,?, 'prototype', 'operator_only', 1, 't', 't')`, anchorProductID, anchorProductID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?,?, 'secondary')`, anchorProductID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?, 'canonical_path', ?, ?, 't', 't')`, locatorID, projectID, "/test/"+locatorID, "/test/"+locatorID); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedCurrentDomain designates a committed Git home for the Product whose
// Domain registry declares the one current Domain the fold reads. No SQL
// Domain row is seeded: the fold reads the committed registry through the
// pre-transaction knowledge-source proof. It returns the designated home.
func seedCurrentDomain(t *testing.T, s *Store, productID, domainID string) KnowledgeHome {
	t.Helper()
	home := KnowledgeHome{HomeProjectID: "domain-home-" + productID, HomeLocatorID: "domain-locator-" + productID, RepoPath: initKnowledgeRepo(t), HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, productID, home, home.HomeProjectID)
	repo := home.RepoPath
	registry := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "obs", RootDomainID: "product-root:obs",
		Domains: []KnowledgeDomain{
			{DomainID: "product-root:obs", Name: "Observation Product", Purpose: "Governs the observation fixture Product.", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: domainID, Name: "Domain " + domainID, Purpose: "Test domain", ParentDomainID: "product-root:obs", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		},
	}
	registryJSON, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeRegistryPath, string(registryJSON)+"\n")
	commitKnowledgeRepo(t, repo, "observation registry fixture")
	return home
}

func repeatA64() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }

// memberMigrationExec runs one seeded migration-style statement under the
// test's context, for parent-owned backup and upgrade tests and any other
// test that seeds retired projection state directly.
func memberMigrationExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
