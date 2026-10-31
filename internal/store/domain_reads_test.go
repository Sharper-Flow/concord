package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func seedDomainReadStore(t *testing.T) *Store {
	t.Helper()
	return seedDomainReadStoreWithLawID(t, "CD-0001")
}

func seedDomainReadStoreWithLawID(t *testing.T, lawID string) *Store {
	t.Helper()
	return seedDomainReadStoreWithLawIDs(t, []string{lawID})
}

func seedDomainReadStoreWithLawIDs(t *testing.T, lawIDs []string) *Store {
	t.Helper()
	return seedDomainReadStoreWithRelation(t, lawIDs, KnowledgeArchitectureRelation{Kind: "depends_on", TargetDomainID: "product-root:concord", GoverningLawIDs: lawIDs})
}

func seedDomainReadStoreWithRelation(t *testing.T, lawIDs []string, relation KnowledgeArchitectureRelation) *Store {
	t.Helper()
	repo := initKnowledgeRepo(t)
	lawContent := "domain law\n"
	registry := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
		Domains: []KnowledgeDomain{
			{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: "sync", Name: "Sync", Purpose: "Synchronization", Status: "current", ParentDomainID: "product-root:concord",
				ArchitectureRelations: []KnowledgeArchitectureRelation{relation}},
			{DomainID: "legacy-sync", Name: "Legacy Sync", Purpose: "Retired scope", Status: "deprecated", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		},
	}
	registryBytes, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", string(registryBytes)+"\n")
	for i, lawID := range lawIDs {
		front, err := json.Marshal(map[string]any{
			"id": lawID, "title": "Domain law", "authority": map[string]string{"tier": "derived"},
			"home_domain_id": "sync", "applies_to_domain_ids": []string{"product-root:concord"},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf(".concord/law/decisions/CD-%04d.md", i+1)
		writeKnowledgeFile(t, repo, path, "---\n"+string(front)+"\n---\n\n"+lawContent)
	}
	commitKnowledgeRepo(t, repo, "domain law folder")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "concord", home)
	return s
}

func TestDomainListCoversCurrentDomainsWithRegistryWatermark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedDomainReadStore(t)
	result, err := s.QueryDomainList(ctx, DomainListRequest{Product: "concord"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Registry == nil || result.Registry.RootDomainID != "product-root:concord" || result.Registry.ContentHash == "" {
		t.Fatalf("registry watermark missing: %#v", result.Registry)
	}
	if len(result.Domains) != 2 {
		t.Fatalf("current domain count = %d, want 2 (deprecated excluded): %#v", len(result.Domains), result.Domains)
	}
	if result.Domains[0].DomainID != "product-root:concord" || !result.Domains[0].HomeDomain {
		t.Fatalf("home domain not first with home flag: %#v", result.Domains[0])
	}
	if result.Domains[1].DomainID != "sync" || result.Domains[1].ParentID != "product-root:concord" {
		t.Fatalf("child domain wrong: %#v", result.Domains[1])
	}

	absent, err := s.QueryDomainList(ctx, DomainListRequest{Product: "other-product"})
	if err == nil {
		t.Fatalf("homeless Product returned a page: %#v", absent)
	}
	assertFailureKind(t, err, KindUnknownScope)
}

func TestDomainDetailShowsCurrentLawRelationsAndRefusesUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := seedDomainReadStore(t)
	detail, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "concord", Domain: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.CurrentLaw) != 1 || detail.CurrentLaw[0].LawID != "CD-0001" {
		t.Fatalf("current law wrong: %#v", detail.CurrentLaw)
	}
	if len(detail.CurrentLaw[0].AppliesTo) != 1 || detail.CurrentLaw[0].AppliesTo[0] != "product-root:concord" {
		t.Fatalf("law applicability fan-out wrong: %#v", detail.CurrentLaw[0])
	}
	if len(detail.Relations) != 1 || detail.Relations[0].Kind != "depends_on" || detail.Relations[0].TargetDomain != "product-root:concord" {
		t.Fatalf("architecture relation wrong: %#v", detail.Relations)
	}
	if len(detail.Relations[0].GoverningLaws) != 1 || detail.Relations[0].GoverningLaws[0] != "CD-0001" {
		t.Fatalf("governing law wrong: %#v", detail.Relations[0])
	}

	unknown, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "concord", Domain: "missing"})
	if err == nil {
		t.Fatalf("unknown domain resolved: %#v", unknown)
	}
	assertFailureKind(t, err, KindUnknownDomain)
	deprecated, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "concord", Domain: "legacy-sync"})
	if err == nil {
		t.Fatalf("deprecated domain resolved: %#v", deprecated)
	}
	assertFailureKind(t, err, KindUnknownDomain)
}
