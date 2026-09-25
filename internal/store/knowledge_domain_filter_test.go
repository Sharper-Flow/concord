package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// A law belongs to its home Domain and to every Domain it applies to. The Q9
// Domain filter, the exact Domain text match, and the returned domain_ids read
// that membership from the law Domain projection, the same source Q10 uses.
func TestQ9DomainFilterAdmitsLawByHomeAndApplicability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	records := []KnowledgeRecord{}
	for _, law := range []struct {
		id, home string
		applies  []string
	}{
		{"CD-0001", "sync", []string{"product-root:concord"}},
		{"CD-0002", "product-root:concord", []string{}},
		{"CD-0003", "storage", []string{"sync"}},
	} {
		path := "docs/decisions/" + law.id + ".md"
		content := law.id + " domain law\n"
		writeKnowledgeFile(t, repo, path, content)
		sum := sha256.Sum256([]byte(content))
		rationale := ""
		if law.home == "product-root:concord" {
			rationale = "Fixture law binds every child Domain."
		}
		records = append(records, KnowledgeRecord{
			ID: law.id, Kind: "decision", Path: path, Status: "accepted", Date: "2026-08-18T00:00:00Z",
			Title: law.id + " title", Summary: "A domain law", Tags: []string{},
			Authority:    KnowledgeAuthority{Tier: "derived"},
			Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			HomeDomainID: law.home, ProductWideRationale: rationale, AppliesToDomainIDs: law.applies, SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "sync", Name: "Sync", Purpose: "Synchronization", Status: "current", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "storage", Name: "Storage", Purpose: "Storage", Status: "current", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "unused", Name: "Unused", Purpose: "No law", Status: "current", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: records,
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

	ids := func(r Q9Result) []string {
		out := []string{}
		for _, item := range r.Items {
			out = append(out, item.ID)
		}
		return out
	}
	for domain, want := range map[string][]string{
		"sync":                 {"CD-0001", "CD-0003"},
		"storage":              {"CD-0003"},
		"product-root:concord": {"CD-0001", "CD-0002"},
		"unused":               {},
	} {
		got, err := s.QueryQ9(ctx, Q9Request{Product: "concord", Domain: domain, Home: home})
		if err != nil {
			t.Fatalf("Q9 domain %q: %v", domain, err)
		}
		if !reflect.DeepEqual(ids(got), want) {
			t.Fatalf("Q9 domain %q = %v, want %v", domain, ids(got), want)
		}
	}

	exact, err := s.QueryQ9(ctx, Q9Request{Product: "concord", Text: "STORAGE", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(exact), []string{"CD-0003"}) || exact.Items[0].MatchClass != 0 {
		t.Fatalf("Q9 exact Domain text = %#v", exact.Items)
	}
	if got := exact.Items[0].DomainIDs; !reflect.DeepEqual(got, []string{"storage", "sync"}) {
		t.Fatalf("CD-0003 domain_ids = %v, want [storage sync]", got)
	}

	page, err := s.QueryQ9(ctx, Q9Request{Product: "concord", Domain: "sync", Limit: 1, Home: home})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("Q9 domain page = %#v err=%v", page, err)
	}
	if _, err := s.QueryQ9(ctx, Q9Request{Product: "concord", Domain: "storage", Limit: 1, Cursor: *page.NextCursor, Home: home}); err == nil {
		t.Fatal("Q9 accepted a cursor issued for another Domain")
	}
	next, err := s.QueryQ9(ctx, Q9Request{Product: "concord", Domain: "sync", Limit: 1, Cursor: *page.NextCursor, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if got := append(ids(page), ids(next)...); !reflect.DeepEqual(got, []string{"CD-0001", "CD-0003"}) {
		t.Fatalf("Q9 domain pages = %v", got)
	}
}
