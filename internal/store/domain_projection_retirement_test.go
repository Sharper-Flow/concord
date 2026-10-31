package store

import (
	"context"
	"strings"
	"testing"
)

// The committed law folder and Domain registry are the only inputs a Domain
// read answers from. Retired Domain Shape inputs in the same home are never
// read, and the retired Shape projection tables stay absent.
func TestKnowledgeIndexIgnoresRetiredDomainShapeInputs(t *testing.T) {
	ctx := context.Background()
	const fixtureID = "CD-0001"
	const fixtureLaw = "---\n" +
		`{"id":"` + fixtureID + `","title":"Fixture law","authority":{"tier":"derived"},"home_domain_id":"fixture-law","applies_to_domain_ids":[]}` + "\n" +
		"---\n\nSynthetic fixture body.\n"
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/law/decisions/CD-0001-fixture.md", fixtureLaw)
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", `{"schema_version":"1.0","product_key":"fixture-product","root_domain_id":"product-root:fixture-product","domains":[{"domain_id":"product-root:fixture-product","name":"Fixture product","purpose":"Governs the fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"fixture-law","name":"Fixture","purpose":"Governs the fixture.","status":"current","parent_domain_id":"product-root:fixture-product","architecture_relations":[{"kind":"depends_on","target_domain_id":"product-root:fixture-product","governing_law_ids":["`+fixtureID+`"]}]}]}`+"\n")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/package-domains.json", "not JSON\n")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/facts/unread.json", strings.Repeat("x", 16*1024+1))
	writeKnowledgeFile(t, repo, ".concord/tooling.v1.json", "not JSON\n")
	commitKnowledgeRepo(t, repo, "Registry without Shape inputs")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "source", HomeLocatorID: "source-home", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "fixture-product", home)
	list, err := s.QueryDomainList(ctx, DomainListRequest{Product: "fixture-product"})
	if err != nil || len(list.Domains) != 2 || list.Registry == nil || list.Registry.ContentHash == "" || list.Registry.ScannedCommit == "" {
		t.Fatalf("registry read failed: %+v %v", list, err)
	}
	detail, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "fixture-product", Domain: "fixture-law"})
	if err != nil || len(detail.CurrentLaw) != 1 || detail.CurrentLaw[0].LawID != fixtureID || len(detail.Relations) != 1 {
		t.Fatalf("law or architecture relation read failed: %+v %v", detail, err)
	}
	for _, table := range []string{"knowledge_domain_facts", "knowledge_domain_roots", "knowledge_domain_shapes", "knowledge_package_domains", "law_evidence_paths"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retired projection %s remains: %d %v", table, count, err)
		}
	}
}
