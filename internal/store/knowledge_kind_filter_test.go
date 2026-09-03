package store

import (
	"context"
	"sort"
	"testing"
)

// The Q9 kind filter, the coverage projection, and the manifest vocabulary
// draw from one closed set. A kind the manifest can index but the filter
// refuses is unreachable by name, which is the gap that hid reference and
// constitution records from every targeted search.
func TestQ9KindFilterAdmitsEveryClosedKnowledgeKind(t *testing.T) {
	for kind := range knowledgeKindsClosed {
		if _, err := knowledgeKinds([]string{kind}); err != nil {
			t.Errorf("closed kind %q refused by the Q9 filter: %v", kind, err)
		}
	}
	if _, err := knowledgeKinds([]string{"bogus"}); err == nil {
		t.Fatal("unknown kind passed the Q9 filter")
	}
}

func TestKnowledgeCoverageRowsSpanEveryClosedKind(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "ref-1", Kind: "reference", Path: "docs/reference.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Reference", Summary: "Reference summary", Tags: []string{"nav"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "const-1", Kind: "constitution", Path: "docs/constitution.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Constitution", Summary: "Constitution summary", Tags: []string{"nav"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
	)
	commitKnowledgeRepo(t, repo, "reference and constitution")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind FROM knowledge_kind_coverage WHERE home_project_id=? AND home_locator_id=? AND head_ref=? ORDER BY kind`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		got = append(got, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(knowledgeKindsClosed))
	for kind := range knowledgeKindsClosed {
		want = append(want, kind)
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("coverage kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("coverage kinds = %v, want %v", got, want)
		}
	}
	for _, kind := range []string{"reference", "constitution"} {
		result, err := s.QueryQ9(ctx, Q9Request{Kinds: []string{kind}, Product: "product", Home: home})
		if err != nil || len(result.Items) != 1 || result.Items[0].Kind != kind {
			t.Fatalf("Q9 filtered to %q = %#v err=%v", kind, result, err)
		}
	}
}
