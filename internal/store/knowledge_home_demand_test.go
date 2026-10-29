package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func demandSourceFixture(t *testing.T, repo, id string) {
	t.Helper()
	path := ".concord/docs/lessons/" + id + ".md"
	writeKnowledgeFile(t, repo, path, "durable demand blob\n")
	writeManifestShards(t, repo, KnowledgeManifest{
		SchemaVersion: "1.3", SupportedKinds: sortedKnowledgeKinds(), IndexedKinds: fixtureIndexedKinds(),
		Records: []KnowledgeRecord{{
			ID: id, Kind: "lesson", Path: path, Status: "published", Date: "2026-10-11T00:00:00Z",
			Title: "Durable lesson", Summary: "Durable summary", Tags: []string{},
			Authority: KnowledgeAuthority{Tier: "derived"},
			Scopes:    KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			SHA256:    knowledgeTestSHA256([]byte("durable demand blob\n")),
		}},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "demand-fixture", RootDomainID: "product-root:demand-fixture",
			Domains: []KnowledgeDomain{{DomainID: "product-root:demand-fixture", Name: "Demand fixture", Purpose: "fixture registry", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
	})
}

func knowledgeTestSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestDemandFailureForUnresolvedSourceIsIgnored(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	demandSourceFixture(t, repo, "home-lesson")
	commitKnowledgeRepo(t, repo, "home lesson")
	s := openTemp(t)
	defer s.Close()
	home := KnowledgeHome{HomeProjectID: "d-home", HomeLocatorID: "d-loc", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "d-product", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	q, err := s.QueryQ9(context.Background(), Q9Request{Product: "d-product", Home: home, DemandFailures: []KnowledgeDemandFailure{{ProjectID: "gone", LocatorID: "gone", FailureKind: "unreachable"}}})
	if err != nil {
		t.Fatal(err)
	}
	if q.Authority != "authoritative" || len(q.SourceOmissions) != 0 {
		t.Fatalf("an outcome for a home outside the answer marks nothing, got %q", q.Authority)
	}
}

func TestDemandFailureKeepsSingleSourceRowsAndEmitsItsWatermark(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	demandSourceFixture(t, repo, "home-lesson")
	commitKnowledgeRepo(t, repo, "home lesson")
	s := openTemp(t)
	defer s.Close()
	home := KnowledgeHome{HomeProjectID: "d-home", HomeLocatorID: "d-loc", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "d-product", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	q, err := s.QueryQ9(context.Background(), Q9Request{Product: "d-product", Home: home, DemandFailures: []KnowledgeDemandFailure{{ProjectID: home.HomeProjectID, LocatorID: home.HomeLocatorID, FailureKind: "invalid_note_proof"}}})
	if err != nil {
		t.Fatal(err)
	}
	if q.Authority != "degraded" || len(q.Items) != 1 {
		t.Fatalf("a single-home answer keeps its rows: authority=%q items=%d", q.Authority, len(q.Items))
	}
	if len(q.Omissions) != 0 || len(q.SourceOmissions) != 1 || q.SourceOmissions[0] != (KnowledgeSourceOmission{Kind: KnowledgeSourceDemandFailed, ProjectID: "d-home", LocatorID: "d-loc", Cause: "invalid_note_proof"}) {
		t.Fatalf("the demand cause is the one omission: %v / %v", q.Omissions, q.SourceOmissions)
	}
}
