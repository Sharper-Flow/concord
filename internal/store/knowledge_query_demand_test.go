package store

// Q9 reports suppressed demands through its per-source authority path.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// demandSourceFixture writes one derived lesson so the federated loop has
// rows to skip or keep.
func demandSourceFixture(t *testing.T, repo, id string) {
	t.Helper()
	path := ".concord/docs/lessons/" + id + ".md"
	writeKnowledgeFile(t, repo, path, "durable demand blob\n")
	sum := knowledgeTestSHA256([]byte("durable demand blob\n"))
	writeManifestShards(t, repo, KnowledgeManifest{
		SchemaVersion: "1.3", SupportedKinds: sortedKnowledgeKinds(), IndexedKinds: fixtureIndexedKinds(),
		Records: []KnowledgeRecord{{
			ID: id, Kind: "lesson", Path: path, Status: "published", Date: "2026-10-11T00:00:00Z",
			Title: "Durable lesson", Summary: "Durable summary", Tags: []string{},
			Authority: KnowledgeAuthority{Tier: "derived"},
			Scopes:    KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			SHA256:    sum,
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

// demandFederatedFixture builds a Product with a designated home and one
// registered source, each holding its own lesson. The source repo carries no
// Domain registry: only the shared-law home does (CD-0200).
func demandFederatedFixture(t *testing.T) (*Store, KnowledgeHome, KnowledgeHome) {
	t.Helper()
	homeRepo := initKnowledgeRepo(t)
	demandSourceFixture(t, homeRepo, "home-lesson")
	commitKnowledgeRepo(t, homeRepo, "home lesson")
	sourceRepo := initKnowledgeRepo(t)
	demandSourceFixture(t, sourceRepo, "source-lesson")
	// The zero-value DomainRegistry field marshals with the key present, so
	// the shard writer emits an empty registry the source role refuses.
	if err := os.Remove(filepath.Join(sourceRepo, ".concord", "docs", "knowledge", "domain-registry.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, sourceRepo, "source lesson")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "d-home", HomeLocatorID: "d-loc", RepoPath: homeRepo, HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "d-src", HomeLocatorID: "d-srcl", RepoPath: sourceRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "d-product", home)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('d-src','Source',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('d-srcl','d-src','canonical_path',?,?, 'now','now')`, sourceRepo, sourceRepo); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO product_knowledge_sources(product_id,project_id,locator_id,registered_at) VALUES('d-product','d-src','d-srcl','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO product_projects(product_id,project_id,role) VALUES('d-product','d-src','secondary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return s, home, source
}

func TestRegistrationsUnreadDegradesResolvedNonHomeSources(t *testing.T) {
	t.Parallel()
	s, home, _ := demandFederatedFixture(t)
	defer s.Close()
	q, err := s.QueryQ9(context.Background(), Q9Request{Product: "d-product", Home: home, RegistrationsUnread: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.Authority != "degraded" {
		t.Fatalf("an unread registration set degrades the unadmitted sources, got %q", q.Authority)
	}
	found := false
	for _, omission := range q.SourceOmissions {
		if omission == (KnowledgeSourceOmission{Kind: KnowledgeSourceDemandFailed, ProjectID: "d-src", LocatorID: "d-srcl", Cause: "registrations_unread"}) {
			found = true
		}
		if omission.ProjectID == "d-home" && omission.LocatorID == "d-loc" {
			t.Fatalf("the designated home is never marked unread: %v", q.SourceOmissions)
		}
	}
	if !found {
		t.Fatalf("the unadmitted source must carry the registrations_unread cause: %v", q.SourceOmissions)
	}
	if len(q.Items) != 1 || q.Items[0].ID != "home-lesson" {
		t.Fatalf("the unadmitted source's items are skipped and the home's kept: items=%#v", q.Items)
	}
	for _, mark := range q.SourceWatermarks {
		want := "degraded"
		if mark.ProjectID == home.HomeProjectID {
			want = "authoritative"
		}
		if mark.Authority != want {
			t.Fatalf("watermark for %s/%s = %q, want %q", mark.ProjectID, mark.LocatorID, mark.Authority, want)
		}
	}
}

func TestRegistrationsUnreadKeepsARecoveredHomeOnlySetAuthoritative(t *testing.T) {
	t.Parallel()
	// The F1 guard: degradation belongs to a source in the answered set. A
	// resolution that recovers home-only answers from admitted sources alone,
	// so the unread flag has nothing to mark and nothing to omit.
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
	q, err := s.QueryQ9(context.Background(), Q9Request{Product: "d-product", Home: home, RegistrationsUnread: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.Authority != "authoritative" || len(q.Omissions) != 1 || q.Omissions[0] != "knowledge_kind_not_indexed:research" || len(q.SourceOmissions) != 0 || q.SourceWatermarks != nil {
		t.Fatalf("the home-only shape stays unchanged: authority=%q omissions=%v watermarks=%#v", q.Authority, q.Omissions, q.SourceWatermarks)
	}
}

func TestDemandFailuresNameTheSourceAndItsExactCause(t *testing.T) {
	t.Parallel()
	s, home, source := demandFederatedFixture(t)
	defer s.Close()
	q, err := s.QueryQ9(context.Background(), Q9Request{Product: "d-product", Home: home, DemandFailures: []KnowledgeDemandFailure{{ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, FailureKind: "index_degraded"}}})
	if err != nil {
		t.Fatal(err)
	}
	if q.Authority != "degraded" {
		t.Fatalf("a suppressed demand failure degrades the source, got %q", q.Authority)
	}
	found := false
	for _, omission := range q.SourceOmissions {
		if omission == (KnowledgeSourceOmission{Kind: KnowledgeSourceDemandFailed, ProjectID: "d-src", LocatorID: "d-srcl", Cause: "index_degraded"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the omission must carry the exact cause: %v", q.SourceOmissions)
	}
	if len(q.Items) != 1 || q.Items[0].ID != "home-lesson" {
		t.Fatalf("the failed source's items are skipped in a federated answer: %#v", q.Items)
	}
}

func TestDemandFailureForUnresolvedSourceIsIgnored(t *testing.T) {
	t.Parallel()
	// F1: degradation belongs to a source in the answered set. An outcome
	// naming a source the query did not resolve marks nothing.
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
		t.Fatalf("an outcome for a source outside the answered set marks nothing, got %q", q.Authority)
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
		t.Fatalf("a single-source answer keeps its rows: authority=%q items=%d", q.Authority, len(q.Items))
	}
	// The watermark is fresh, so the freshness path adds nothing: the answer
	// carries exactly the demand cause, whose spelling is its own.
	if len(q.Omissions) != 0 || len(q.SourceOmissions) != 1 || q.SourceOmissions[0] != (KnowledgeSourceOmission{Kind: KnowledgeSourceDemandFailed, ProjectID: "d-home", LocatorID: "d-loc", Cause: "invalid_note_proof"}) {
		t.Fatalf("the demand cause is the one omission: %v / %v", q.Omissions, q.SourceOmissions)
	}
	if len(q.SourceWatermarks) != 1 || q.SourceWatermarks[0].Authority != "degraded" {
		t.Fatalf("the affected source watermark must be explicit: %#v", q.SourceWatermarks)
	}
}
