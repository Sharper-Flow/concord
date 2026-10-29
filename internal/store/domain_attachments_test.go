package store

import (
	"context"
	"testing"
	"time"
)

// The managed-resource inventory and Domain-resource attachment surface are
// retired (C15 §2, §4, §5; CD-0222 D4 resource half); their
// fold and schema guarantees above those sections left with the surface.
// The Domain-Project attachment half keeps its own tests.

func TestReplaceDomainProjectAttachmentsFoldsSets(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupProductWithProject(t, s, "domain-product", "domain-project")
	seedCurrentDomain(t, s, "domain-product", "domain-a")
	now := time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC)
	if err := ReplaceDomainProjectAttachments(ctx, s, DomainProjectAttachmentsRequest{EventID: "domain-project-links", ProductID: "domain-product", DomainID: "domain-a", ExpectedVersion: 0, Attachments: []DomainProjectAttachment{{ProjectID: "domain-project", Role: "primary"}}, Actor: "operator", OccurredAt: now}); err != nil {
		t.Fatalf("ReplaceDomainProjectAttachments() error = %v", err)
	}
	var projectLinks, projectVersion int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_project_attachment_edges WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&projectLinks); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM domain_project_attachment_sets WHERE product_id='domain-product' AND domain_id='domain-a'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if projectLinks != 1 || projectVersion != 1 {
		t.Fatalf("attachments project=%d version=%d", projectLinks, projectVersion)
	}
}

func TestReplaceDomainProjectAttachmentsRejectsCrossProductProjectAndStaleSet(t *testing.T) {
	t.Parallel()
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
