package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func seedDomainReadStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	lawContent := "domain law\n"
	writeKnowledgeFile(t, repo, "docs/decisions/CD-0001.md", lawContent)
	sum := sha256.Sum256([]byte(lawContent))
	legacyContent := "retired law\n"
	writeKnowledgeFile(t, repo, "docs/decisions/CD-0002.md", legacyContent)
	legacySum := sha256.Sum256([]byte(legacyContent))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "sync", Name: "Sync", Purpose: "Synchronization", Status: "current", ParentDomainID: "product-root:concord",
					ArchitectureRelations: []KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: "product-root:concord", GoverningLawIDs: []string{"CD-0001"}}}},
				{DomainID: "legacy-sync", Name: "Legacy Sync", Purpose: "Retired scope", Status: "deprecated", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{
			{ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001.md", Status: "accepted", Date: "2026-08-18T00:00:00Z",
				Title: "Domain law", Summary: "A domain law", Tags: []string{},
				LawRelations: []KnowledgeRelation{{Kind: "supersedes", TargetID: "CD-0002"}},
				Scopes:       KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{"sync"}, TagIDs: []string{}},
				HomeDomainID: "sync", AppliesToDomainIDs: []string{"product-root:concord"}, SHA256: "sha256:" + hex.EncodeToString(sum[:])},
			{ID: "CD-0002", Kind: "decision", Path: "docs/decisions/CD-0002.md", Status: "superseded", Date: "2026-08-18T00:00:00Z",
				Title: "Retired law", Summary: "A retired law", Tags: []string{}, Successor: "CD-0001",
				Scopes:       KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{"sync"}, TagIDs: []string{}},
				HomeDomainID: "sync", SHA256: "sha256:" + hex.EncodeToString(legacySum[:])},
		},
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
	return s
}

func TestDomainListCoversCurrentDomainsWithRegistryWatermark(t *testing.T) {
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
		t.Fatalf("absent registry returned a page: %#v", absent)
	}
	assertFailureKind(t, err, KindDomainRegistryAbsent)
}

func TestDomainDetailShowsCurrentLawRelationsAndRefusesUnknown(t *testing.T) {
	ctx := context.Background()
	s := seedDomainReadStore(t)
	detail, err := s.QueryDomainDetail(ctx, DomainDetailRequest{Product: "concord", Domain: "sync"})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.CurrentLaw) != 1 || detail.CurrentLaw[0].LawID != "CD-0001" {
		t.Fatalf("current law wrong (superseded must be absent): %#v", detail.CurrentLaw)
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

func TestDomainActiveWorkAndOverlapsDeriveFromCurrentContracts(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "overlap-left", "overlap-right", false)

	work, err := s.QueryDomainActiveWork(ctx, DomainActiveWorkRequest{Product: "product", Domain: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if len(work.Work) != 2 {
		t.Fatalf("active work count = %d, want 2: %#v", len(work.Work), work.Work)
	}
	for _, item := range work.Work {
		if !item.HomeDomain {
			t.Fatalf("fixture work must read as home-bound: %#v", item)
		}
	}
	_, err = s.QueryDomainActiveWork(ctx, DomainActiveWorkRequest{Product: "product", Domain: "missing"})
	assertFailureKind(t, err, KindUnknownDomain)

	overlaps, err := s.QueryDomainOverlaps(ctx, DomainOverlapsRequest{Product: "product"})
	if err != nil {
		t.Fatal(err)
	}
	if len(overlaps.Pairs) != 1 || overlaps.Pairs[0].ResolutionState != "absent" {
		t.Fatalf("unresolved overlap pair wrong: %#v", overlaps.Pairs)
	}
	if overlaps.Pairs[0].FromWorkID != "overlap-left" || overlaps.Pairs[0].ToWorkID != "overlap-right" || len(overlaps.Pairs[0].SharedDomainIDs) != 1 || overlaps.Pairs[0].SharedDomainIDs[0] != "child" {
		t.Fatalf("overlap pair identity wrong: %#v", overlaps.Pairs[0])
	}

	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{EventID: "read-overlap-compatible", FromWorkID: "overlap-left", ToWorkID: "overlap-right", FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved", ApprovalRef: "approval:read", Actor: actor})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := s.QueryDomainOverlaps(ctx, DomainOverlapsRequest{Product: "product", Domain: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Pairs) != 1 || resolved.Pairs[0].ResolutionState != "current" || resolved.Pairs[0].ResolutionKind != ResolutionCompatibleWith {
		t.Fatalf("resolved overlap pair wrong: %#v", resolved.Pairs)
	}
	otherDomain, err := s.QueryDomainOverlaps(ctx, DomainOverlapsRequest{Product: "product", Domain: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherDomain.Pairs) != 0 {
		t.Fatalf("root domain reported the child overlap: %#v", otherDomain.Pairs)
	}
}

func TestDomainAttachmentsReadLocalSets(t *testing.T) {
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "attach-work", "attach-other", false)
	if err := ReplaceDomainProjectAttachments(ctx, s, DomainProjectAttachmentsRequest{EventID: "attach-projects", ProductID: "product", DomainID: "root", Attachments: []DomainProjectAttachment{{ProjectID: "project", Role: "primary"}}, ExpectedVersion: 0, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryDomainAttachments(ctx, DomainAttachmentsRequest{Product: "product", Domain: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Domain.DomainID != "root" || result.Attachments.ProjectVersion != 1 || len(result.Attachments.ProjectEdges) != 1 || result.Attachments.ProjectEdges[0].ProjectID != "project" {
		t.Fatalf("attachment read wrong: %#v", result)
	}
	if len(result.Attachments.ResourceEdges) != 0 || result.Attachments.ResourceVersion != 0 {
		t.Fatalf("unset resource set must read as zero-version empty: %#v", result.Attachments)
	}
}
