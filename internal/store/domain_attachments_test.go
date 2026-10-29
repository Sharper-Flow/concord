package store

import (
	"context"
	"testing"
	"time"
)

func TestReplaceDomainAttachmentsFoldsSets(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupProductWithProject(t, s, "domain-product", "domain-project")
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{EventID: "domain-resource-created", ResourceID: "domain-resource", ProductID: "domain-product", DisplayName: "Queue", Class: "infrastructure", Kind: "queue", Purpose: "dispatches work", StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"}, MetadataSchemaVersion: "1", Metadata: []byte(`{}`), ExpectedProductVersion: 2, Actor: "operator", OccurredAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	seedCurrentDomain(t, s, "domain-product", "domain-a")
	now := time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC)
	if err := ReplaceDomainResourceAttachments(ctx, s, DomainResourceAttachmentsRequest{EventID: "domain-resource-links", ProductID: "domain-product", DomainID: "domain-a", ExpectedVersion: 0, Attachments: []DomainResourceAttachment{{ResourceID: "domain-resource", Purpose: "uses queue", Environments: []string{"production"}}}, Actor: "operator", OccurredAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("ReplaceDomainResourceAttachments() error = %v", err)
	}
	var resourceLinks, resourceVersion int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_resource_attachment_edges WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&resourceLinks); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM domain_resource_attachment_sets WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&resourceVersion); err != nil {
		t.Fatal(err)
	}
	if resourceLinks != 1 || resourceVersion != 1 {
		t.Fatalf("resource attachments=%d version=%d", resourceLinks, resourceVersion)
	}
}

func TestDomainResourceAttachmentSchemaRequiresSameProductMembership(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupProductWithProject(t, s, "resource-owner", "resource-owner-project")
	setupProductWithProject(t, s, "domain-owner", "domain-owner-project")
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{EventID: "schema-resource", ResourceID: "schema-resource", ProductID: "resource-owner", DisplayName: "Queue", Class: "infrastructure", Kind: "queue", Purpose: "dispatches work", StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"}, MetadataSchemaVersion: "1", Metadata: []byte(`{}`), ExpectedProductVersion: 2, Actor: "operator", OccurredAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	seedCurrentDomain(t, s, "domain-owner", "domain-a")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_resource_attachment_sets(product_id,domain_id,version) VALUES('domain-owner','domain-a',1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_resource_attachment_edges(product_id,domain_id,resource_id,purpose,environments) VALUES('domain-owner','domain-a','schema-resource','uses queue','["production"]')`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("cross-Product Domain resource attachment committed without a membership FK")
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
