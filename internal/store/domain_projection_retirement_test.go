package store

import (
	"context"
	"strings"
	"testing"
)

func TestKnowledgeIndexIgnoresRetiredDomainShapeInputs(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo, manifestFixture{
		ID: "CD-0001", Kind: "decision", Path: ".concord/law/decisions/CD-0001.md", Status: "accepted", Date: "2026-08-10T00:00:00Z",
		Title: "Fixture law", Summary: "Governs the fixture", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law"}},
	})
	manifest := composeWorkingTreeManifest(t, repo)
	setManifestGoverningLaw(t, repo, "fixture-law", manifest.DomainRegistry.RootDomainID, "CD-0001")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/package-domains.json", "not JSON\n")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/facts/unread.json", strings.Repeat("x", maxKnowledgeRecord+1))
	writeKnowledgeFile(t, repo, ".concord/tooling.v1.json", "not JSON\n")
	commitKnowledgeRepo(t, repo, "Registry without Shape inputs")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "source", HomeLocatorID: "source-home", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "fixture-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	list, err := s.QueryDomainList(ctx, DomainListRequest{Product: "fixture-product"})
	if err != nil || len(list.Domains) != 2 || list.Registry == nil || list.Registry.ContentHash == "" {
		t.Fatalf("registry read failed: %+v %v", list, err)
	}
	detail, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "fixture-product", Domain: "fixture-law"})
	if err != nil || len(detail.CurrentLaw) != 1 || detail.CurrentLaw[0].LawID != "CD-0001" || len(detail.Relations) != 1 {
		t.Fatalf("law or architecture relation read failed: %+v %v", detail, err)
	}
	for _, table := range []string{"knowledge_domain_facts", "knowledge_domain_roots", "knowledge_domain_shapes", "knowledge_package_domains", "law_evidence_paths"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retired projection %s remains: %d %v", table, count, err)
		}
	}
}
