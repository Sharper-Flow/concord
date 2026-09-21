package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// seedAuthorityTierHome writes a knowledge home carrying one legislated
// decision and one derived spec, so a rebuild has both tiers to project.
func seedAuthorityTierHome(t *testing.T) KnowledgeHome {
	t.Helper()
	repo := initKnowledgeRepo(t)

	const decisionBody = "# Tiered decision\n\nA legislated commitment.\n"
	const specBody = "# Tiered spec\n\nA derived specification.\n"
	decisionPath := "docs/decisions/CD-0001-tiered.md"
	specPath := "docs/specs/SPEC-0001-tiered.md"
	writeKnowledgeFile(t, repo, decisionPath, decisionBody)
	writeKnowledgeFile(t, repo, specPath, specBody)
	decisionSum := sha256.Sum256([]byte(decisionBody))
	specSum := sha256.Sum256([]byte(specBody))

	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"lesson", "decision", "spec"},
		IndexedKinds:   []string{"lesson", "decision", "spec"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
		Records: []KnowledgeRecord{
			{
				ID: "CD-0001", Kind: "decision", Path: decisionPath, Status: "accepted", Date: "2026-08-18T00:00:00Z",
				Title: "Tiered decision", Summary: "A legislated commitment", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				Scopes:       homeScope(),
				HomeDomainID: "product-root:concord", ProductWideRationale: "Fixture law binds every child Domain.",
				SHA256: "sha256:" + hex.EncodeToString(decisionSum[:]),
			},
			{
				ID: "SPEC-0001", Kind: "spec", Path: specPath, Status: "accepted", Date: "2026-08-18T00:00:00Z",
				Title: "Tiered spec", Summary: "A derived specification", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "derived"},
				Scopes:       homeScope(),
				HomeDomainID: "product-root:concord", ProductWideRationale: "Fixture law binds every child Domain.",
				SHA256: "sha256:" + hex.EncodeToString(specSum[:]),
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commitKnowledgeRepo(t, repo, "tiered knowledge")
	return KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
}

// TestRebuildProjectsTheDeclaredAuthorityTierIntoLawSubjects holds that the
// knowledge index rebuild carries each record's declared tier into the
// law_subjects projection. The conflict path reads that projection and never
// opens a record file, so a tier the rebuild drops is a tier the conflict path
// cannot see.
func TestRebuildProjectsTheDeclaredAuthorityTierIntoLawSubjects(t *testing.T) {
	ctx := context.Background()
	home := seedAuthorityTierHome(t)
	s := openTemp(t)
	defer s.Close()
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	tiers := map[string]string{}
	rows, err := s.DatabaseForTesting().QueryContext(ctx,
		`SELECT law_id,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=?`,
		home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var lawID, tier string
		if err := rows.Scan(&lawID, &tier); err != nil {
			t.Fatal(err)
		}
		tiers[lawID] = tier
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if tiers["CD-0001"] != "legislated" {
		t.Fatalf("a legislated decision projected tier %q", tiers["CD-0001"])
	}
	if tiers["SPEC-0001"] != "derived" {
		t.Fatalf("a derived spec projected tier %q", tiers["SPEC-0001"])
	}
}
