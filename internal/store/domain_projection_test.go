package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestRebuildKnowledgeIndexProjectsDomainRegistryAndLawScope(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := "docs/decisions/CD-0001.md"
	content := "domain law\n"
	writeKnowledgeFile(t, repo, path, content)
	sum := sha256.Sum256([]byte(content))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "sync", Name: "Sync", Purpose: "Synchronization", Status: "current", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{{
			ID: "CD-0001", Kind: "decision", Path: path, Status: "accepted", Date: "2026-08-18T00:00:00Z",
			Title: "Domain law", Summary: "A domain law", Tags: []string{},
			Scopes:       KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{"sync"}, TagIDs: []string{}},
			HomeDomainID: "sync", AppliesToDomainIDs: []string{"product-root:concord"}, SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commitKnowledgeRepo(t, repo, "domain knowledge")

	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	var domainCount, relationCount, lawHomeCount, applicabilityCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domains WHERE product_id='concord'`).Scan(&domainCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_architecture_relations WHERE product_id='concord'`).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_domain_homes WHERE product_id='concord' AND law_id='CD-0001'`).Scan(&lawHomeCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_domain_applicability WHERE product_id='concord' AND law_id='CD-0001'`).Scan(&applicabilityCount); err != nil {
		t.Fatal(err)
	}
	if domainCount != 2 || relationCount != 0 || lawHomeCount != 1 || applicabilityCount != 1 {
		t.Fatalf("domain projection counts = domains %d relations %d homes %d applicability %d", domainCount, relationCount, lawHomeCount, applicabilityCount)
	}
	legacyBytes, err := json.Marshal(KnowledgeManifest{SchemaVersion: "1.0", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, Records: []KnowledgeRecord{}})
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(legacyBytes)+"\n")
	commitKnowledgeRepo(t, repo, "legacy manifest")
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
		t.Fatal("legacy manifest rebuild succeeded")
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domains WHERE product_id='concord'`).Scan(&domainCount); err != nil {
		t.Fatal(err)
	}
	if domainCount != 2 {
		t.Fatalf("rejected legacy rebuild changed Domain rows to %d", domainCount)
	}
}

func TestDomainProjectionSeparatesGitProductKeyFromLocalProductID(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := "docs/decisions/CD-0001.md"
	content := "domain law\n"
	writeKnowledgeFile(t, repo, path, content)
	sum := sha256.Sum256([]byte(content))
	manifest := KnowledgeManifest{SchemaVersion: "1.2", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, DomainRegistry: KnowledgeDomainRegistry{SchemaVersion: "1.0", ProductKey: "git-product-key", RootDomainID: "product-root:git-product-key", Domains: []KnowledgeDomain{{DomainID: "product-root:git-product-key", Name: "Root", Purpose: "Root", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}, {DomainID: "child", Name: "Child", Purpose: "Child", Status: "current", ParentDomainID: "product-root:git-product-key", ArchitectureRelations: []KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: "product-root:git-product-key", GoverningLawIDs: []string{"CD-0001"}}}}}}, Records: []KnowledgeRecord{{ID: "CD-0001", Kind: "decision", Path: path, Status: "accepted", Date: "2026-08-18T00:00:00Z", Title: "Domain law", Summary: "A domain law", Tags: []string{}, Scopes: KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}}, HomeDomainID: "child", AppliesToDomainIDs: []string{"product-root:git-product-key"}, SHA256: "sha256:" + hex.EncodeToString(sum[:])}}}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commitKnowledgeRepo(t, repo, "stable domain identity")

	first, second := openTemp(t), openTemp(t)
	firstHome := KnowledgeHome{HomeProjectID: "project-a", HomeLocatorID: "locator-a", RepoPath: repo, HeadRef: "HEAD"}
	secondHome := KnowledgeHome{HomeProjectID: "project-b", HomeLocatorID: "locator-b", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, first, "installation-a", firstHome)
	authorizeKnowledgeProductHome(t, second, "installation-b", secondHome)
	if err := first.RebuildKnowledgeIndex(ctx, firstHome); err != nil {
		t.Fatal(err)
	}
	if err := second.RebuildKnowledgeIndex(ctx, secondHome); err != nil {
		t.Fatal(err)
	}
	if result, err := first.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-0001"}); err != nil || result.Status != "canonical" {
		t.Fatalf("1.2 law Q10 result=%#v err=%v", result, err)
	}
	if got, want := domainProjectionIdentitySnapshot(t, first), domainProjectionIdentitySnapshot(t, second); got != want {
		t.Fatalf("Git-domain projection differs by local Product ID:\nfirst=%s\nsecond=%s", got, want)
	}
}

func domainProjectionIdentitySnapshot(t *testing.T, s *Store) string {
	t.Helper()
	queries := []string{
		`SELECT product_key||'|'||root_domain_id||'|'||content_hash FROM domain_registries`,
		`SELECT COALESCE(group_concat(value, ','),'') FROM (SELECT domain_id||'|'||COALESCE(parent_domain_id,'')||'|'||status AS value FROM domains ORDER BY domain_id)`,
		`SELECT COALESCE(group_concat(value, ','),'') FROM (SELECT source_domain_id||'|'||kind||'|'||target_domain_id||'|'||state AS value FROM domain_architecture_relations ORDER BY source_domain_id,kind,target_domain_id)`,
		`SELECT COALESCE(group_concat(law_id||'|'||domain_id||'|'||law_content_hash, ','),'') FROM law_domain_homes`,
		`SELECT COALESCE(group_concat(law_id||'|'||domain_id, ','),'') FROM law_domain_applicability`,
	}
	values := make([]string, 0, len(queries))
	for _, query := range queries {
		var value string
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	return strings.Join(values, "\n")
}
