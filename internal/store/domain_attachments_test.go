package store

import (
	"context"
	"testing"
	"time"
)

func TestReplaceDomainAttachmentsFoldsSetsAndSurvivesLogRebuild(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupProductWithProject(t, s, "domain-product", "domain-project")
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{EventID: "domain-resource-created", ResourceID: "domain-resource", ProductID: "domain-product", DisplayName: "Queue", Class: "infrastructure", Kind: "queue", Purpose: "dispatches work", StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"}, MetadataSchemaVersion: "1", Metadata: []byte(`{}`), ExpectedProductVersion: 2, Actor: "operator", OccurredAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	seedCurrentDomain(t, s, "domain-product", "domain-a")
	now := time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC)
	if err := ReplaceDomainProjectAttachments(ctx, s, DomainProjectAttachmentsRequest{EventID: "domain-project-links", ProductID: "domain-product", DomainID: "domain-a", ExpectedVersion: 0, Attachments: []DomainProjectAttachment{{ProjectID: "domain-project", Role: "primary"}}, Actor: "operator", OccurredAt: now}); err != nil {
		t.Fatalf("ReplaceDomainProjectAttachments() error = %v", err)
	}
	if err := ReplaceDomainResourceAttachments(ctx, s, DomainResourceAttachmentsRequest{EventID: "domain-resource-links", ProductID: "domain-product", DomainID: "domain-a", ExpectedVersion: 0, Attachments: []DomainResourceAttachment{{ResourceID: "domain-resource", Purpose: "uses queue", Environments: []string{"production"}}}, Actor: "operator", OccurredAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("ReplaceDomainResourceAttachments() error = %v", err)
	}
	var projectLinks, resourceLinks, projectVersion, resourceVersion int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_project_attachment_edges WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&projectLinks); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_resource_attachment_edges WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&resourceLinks); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM domain_project_attachment_sets WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM domain_resource_attachment_sets WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&resourceVersion); err != nil {
		t.Fatal(err)
	}
	if projectLinks != 1 || resourceLinks != 1 || projectVersion != 1 || resourceVersion != 1 {
		t.Fatalf("attachments project=%d resource=%d versions=%d/%d", projectLinks, resourceLinks, projectVersion, resourceVersion)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_project_attachment_edges WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&projectLinks); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_resource_attachment_edges WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&resourceLinks); err != nil {
		t.Fatal(err)
	}
	if projectLinks != 1 || resourceLinks != 1 {
		t.Fatalf("attachments after rebuild project=%d resource=%d", projectLinks, resourceLinks)
	}
}

func TestReplaceDomainProjectAttachmentsRejectsCrossProductProjectAndStaleSet(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupProductWithProject(t, s, "domain-product-cross", "domain-project-cross")
	setupProductWithProject(t, s, "other-product-cross", "other-project-cross")
	seedCurrentDomain(t, s, "domain-product-cross", "domain-cross")
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if err := ReplaceDomainProjectAttachments(ctx, s, DomainProjectAttachmentsRequest{EventID: "domain-cross-bad", ProductID: "domain-product-cross", DomainID: "domain-cross", ExpectedVersion: 0, Attachments: []DomainProjectAttachment{{ProjectID: "other-project-cross", Role: "primary"}}, Actor: "operator", OccurredAt: now}); err == nil {
		t.Fatal("cross-Product Project attachment succeeded")
	}
	if err := ReplaceDomainProjectAttachments(ctx, s, DomainProjectAttachmentsRequest{EventID: "domain-cross-good", ProductID: "domain-product-cross", DomainID: "domain-cross", ExpectedVersion: 0, Attachments: []DomainProjectAttachment{{ProjectID: "domain-project-cross", Role: "primary"}}, Actor: "operator", OccurredAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceDomainProjectAttachments(ctx, s, DomainProjectAttachmentsRequest{EventID: "domain-cross-stale", ProductID: "domain-product-cross", DomainID: "domain-cross", ExpectedVersion: 0, Attachments: nil, Actor: "operator", OccurredAt: now.Add(2 * time.Second)}); err == nil {
		t.Fatal("stale Domain attachment set replacement succeeded")
	}
}

func TestDomainResourceAttachmentSchemaRequiresSameProductMembership(t *testing.T) {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
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

func TestProductReconstructionExcludesDomainAndResourceProjectionEvents(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	setupProductWithProject(t, s, "reconstruct-product", "reconstruct-project")
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{EventID: "reconstruct-resource", ResourceID: "reconstruct-resource", ProductID: "reconstruct-product", DisplayName: "Queue", Class: "infrastructure", Kind: "queue", Purpose: "dispatches work", StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"}, MetadataSchemaVersion: "1", Metadata: []byte(`{}`), ExpectedProductVersion: 2, Actor: "operator", OccurredAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	seedCurrentDomain(t, s, "reconstruct-product", "reconstruct-domain")
	if err := ReplaceDomainResourceAttachments(ctx, s, DomainResourceAttachmentsRequest{EventID: "reconstruct-attachment", ProductID: "reconstruct-product", DomainID: "reconstruct-domain", ExpectedVersion: 0, Attachments: []DomainResourceAttachment{{ResourceID: "reconstruct-resource", Purpose: "uses queue", Environments: []string{"production"}}}, Actor: "operator", OccurredAt: time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	var asOf int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT max(seq) FROM domain_events WHERE subject_type=? AND subject_id=?`, SubjectProduct, "reconstruct-product").Scan(&asOf); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconstructSubjectAt(ctx, s, VersionRef(SubjectProduct, "reconstruct-product"), asOf, PurposeAudit); err != nil {
		t.Fatalf("Product reconstruction with unrelated Domain/C15 events failed: %v", err)
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
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func repeatA64() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }
