package store

import (
	"context"
	"testing"
)

// The knowledge index derives from two committed objects: the manifest blob
// and the canonical work-note tree. Freshness is a property of that content,
// not of the commit that happens to be HEAD. A commit that touches neither
// object changes nothing the index projects, so it must leave the index
// authoritative; a commit that changes a record must not.
func TestKnowledgeIndexFreshnessFollowsContentNotCommit(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo, manifestFixture{ID: "law-one", Kind: "lesson", Path: "docs/lessons/law-one.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Law one", Summary: "First law", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	first := commitKnowledgeRepo(t, repo, "law one")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj", HomeLocatorID: "loc", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, authority, err := validateKnowledgeHomeForQuery(ctx, s, home, false, "test"); err != nil || authority != "authoritative" {
		t.Fatalf("fresh rebuild: authority=%q err=%v", authority, err)
	}

	// A commit outside the projected content: HEAD moves, the index does not.
	writeKnowledgeFile(t, repo, "internal/unrelated.go", "package unrelated\n")
	second := commitKnowledgeRepo(t, repo, "unrelated code change")
	if second == first {
		t.Fatal("fixture: HEAD did not move")
	}
	watermark, authority, err := validateKnowledgeHomeForQuery(ctx, s, home, false, "test")
	if err != nil || authority != "authoritative" {
		t.Fatalf("HEAD moved without knowledge change: want authoritative, got authority=%q err=%v", authority, err)
	}
	if watermark != first {
		t.Fatalf("watermark should still name the scanned commit %s, got %s", first, watermark)
	}
	if _, authority, err := validateKnowledgeHomeForQueryCore(ctx, s.db, home, false, "test"); err != nil || authority != "authoritative" {
		t.Fatalf("tx-scoped predicate disagrees: authority=%q err=%v", authority, err)
	}

	// A commit that changes a record's content: the index is stale.
	writeManifestFixture(t, repo, manifestFixture{ID: "law-one", Kind: "lesson", Path: "docs/lessons/law-one.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Law one", Summary: "First law, revised", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	commitKnowledgeRepo(t, repo, "law one revised")
	if _, _, err := validateKnowledgeHomeForQuery(ctx, s, home, false, "test"); err == nil {
		t.Fatal("record content changed: want a stale refusal, got authoritative")
	} else if f, ok := err.(*Failure); !ok || f.Kind != KindIndexDegraded {
		t.Fatalf("want %s, got %v", KindIndexDegraded, err)
	}
	if _, _, err := validateKnowledgeHomeForQueryCore(ctx, s.db, home, false, "test"); err == nil {
		t.Fatal("tx-scoped predicate: want a stale refusal, got authoritative")
	}
}

// A canonical work note is projected content too: adding one without
// touching the manifest must also turn the index stale.
func TestKnowledgeIndexFreshnessTracksWorkNoteTree(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo, manifestFixture{ID: "law-one", Kind: "lesson", Path: "docs/lessons/law-one.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Law one", Summary: "First law", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	commitKnowledgeRepo(t, repo, "law one")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj", HomeLocatorID: "loc", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, "docs/work/work-1.md", canonicalWorkNote("work-1", "2026-08-11T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "add a work note")
	if _, _, err := validateKnowledgeHomeForQuery(ctx, s, home, false, "test"); err == nil {
		t.Fatal("work-note tree changed: want a stale refusal, got authoritative")
	}
}
