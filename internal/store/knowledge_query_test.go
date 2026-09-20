package store

import (
	"context"
	"testing"
)

func TestQueryQ9FindsLawByBodyOnlyText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "body-project", HomeLocatorID: "body-locator", RepoPath: repo, HeadRef: "HEAD"}
	writeManifestFixture(t, repo, manifestFixture{
		ID:      "body-law",
		Kind:    "decision",
		Path:    "docs/decisions/CD-0999-body-law.md",
		Status:  "accepted",
		Date:    "2026-09-20T00:00:00Z",
		Title:   "Storage decision",
		Summary: "A bounded storage rule",
		Scopes:  KnowledgeRecordScopes{Mode: "home"},
		Content: "The hidden retrieval phrase is body-only-discovery.\n",
	}, manifestFixture{
		ID:      "title-law",
		Kind:    "decision",
		Path:    "docs/decisions/CD-0998-title-law.md",
		Status:  "accepted",
		Date:    "2026-09-20T00:00:00Z",
		Title:   "Body-only-discovery title",
		Summary: "A bounded storage rule",
		Scopes:  KnowledgeRecordScopes{Mode: "home"},
		Content: "This body does not contain the search phrase.\n",
	})
	commitKnowledgeRepo(t, repo, "body-only law")
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "body-product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	var body string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT body FROM law_bodies WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, home.HomeProjectID, home.HomeLocatorID, "body-law").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "The hidden retrieval phrase is body-only-discovery.\n" {
		t.Fatalf("projected body = %q", body)
	}

	result, err := s.QueryQ9(ctx, Q9Request{Text: "BODY-ONLY-DISCOVERY", Limit: 1, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "title-law" || result.Items[0].MatchClass != 1 || result.NextCursor == nil {
		t.Fatalf("body-only result = %#v", result.Items)
	}
	next, err := s.QueryQ9(ctx, Q9Request{Text: "BODY-ONLY-DISCOVERY", Limit: 1, Cursor: *result.NextCursor, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.Items[0].ID != "body-law" || next.Items[0].MatchClass != 2 {
		t.Fatalf("body-only continuation = %#v", next.Items)
	}
}
