package store

import (
	"context"
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

func seedCurrentDomain(t *testing.T, s *Store, productID, domainID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	homeProject, homeLocator := "domain-home-"+productID, "domain-locator-"+productID
	seedEventDerivedLocator(t, s, homeProject, homeLocator, t.TempDir())
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + repeatA64()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?,?,1,'t','t')`, homeProject, homeProject); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?, 'canonical_path', ?, ?, 't', 't')`, homeLocator, homeProject, "/test/"+homeLocator, "/test/"+homeLocator); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES(?,?,'secondary')`, productID, homeProject); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,'1.0',?,'commit')`, productID, homeProject, homeLocator, "test-"+productID, domainID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?,?)`, homeProject, homeLocator, productID, domainID, "Domain "+domainID, "test domain", nil, "current", hash, "commit"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func repeatA64() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }
