package store

import (
	"context"
	"testing"
)

func seedKnowledgeHomeProduct(t *testing.T, productID string, home KnowledgeHome, lawID, lawPath, lawTitle string) (*Store, KnowledgeHome) {
	t.Helper()
	home.RepoPath = initKnowledgeRepo(t)
	writeManifestFixture(t, home.RepoPath, manifestFixture{
		ID: lawID, Kind: "decision", Path: lawPath, Status: "accepted",
		Date: "2026-09-20T00:00:00Z", Title: lawTitle, Summary: "A home rule", Scopes: homeScope(),
	})
	commitKnowledgeRepo(t, home.RepoPath, "home law")
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, productID, home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func TestSingleSourceProductQ9OutputUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "solo-home", HomeLocatorID: "solo-home-loc", HeadRef: "HEAD"}
	s, home := seedKnowledgeHomeProduct(t, "solo-product", home, "HOME-LAW", ".concord/docs/decisions/CD-0909-home-law.md", "Solo home law")
	result, err := s.QueryQ9(ctx, Q9Request{Product: "solo-product", Text: "rule", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "HOME-LAW" {
		t.Fatalf("single-source items = %#v", result.Items)
	}
	if result.ResultMeta.Authority != "authoritative" {
		t.Fatalf("single-source authority = %q", result.ResultMeta.Authority)
	}
	if result.NextCursor == nil {
		t.Fatal("single-source query returned no cursor")
	}
	cursor, err := decodeKnowledgeCursor(*result.NextCursor, Q9Request{Product: "solo-product", Text: "rule"}, nil, nil, knowledgeSourceSetDigest([]KnowledgeHome{home}))
	if err != nil || cursor.Version != 3 {
		t.Fatalf("single-source cursor = %+v, err %v", cursor, err)
	}
	last, err := s.QueryQ9(ctx, Q9Request{Product: "solo-product", Text: "rule", Limit: 1, Cursor: *result.NextCursor})
	if err != nil || len(last.Items) != 0 || last.NextCursor != nil || last.Authority != "authoritative" {
		t.Fatalf("single-source final page = %+v, err %v", last, err)
	}
	_, err = s.QueryQ9(ctx, Q9Request{Project: home.HomeProjectID, Text: "rule", Cursor: *result.NextCursor})
	assertFailureKind(t, err, KindInvalidCursor)
	projectPage, err := s.QueryQ9(ctx, Q9Request{Project: home.HomeProjectID, Text: "rule", Limit: 1})
	if err != nil || len(projectPage.Items) != 1 || projectPage.NextCursor == nil {
		t.Fatalf("Project page = %+v, err %v", projectPage, err)
	}
	projectLast, err := s.QueryQ9(ctx, Q9Request{Project: home.HomeProjectID, Text: "rule", Limit: 1, Cursor: *projectPage.NextCursor})
	if err != nil || len(projectLast.Items) != 0 || projectLast.NextCursor != nil {
		t.Fatalf("Project final page = %+v, err %v", projectLast, err)
	}
}

func TestHomeProofSeparatesVerifiedHeadFromScannedRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "proof-home", HomeLocatorID: "proof-home-loc", HeadRef: "HEAD"}
	s, home := seedKnowledgeHomeProduct(t, "proof-product", home, "HOME-LAW", ".concord/docs/decisions/CD-0901-home.md", "Home law")
	seedKnowledgeWork(t, s, "proof-work", "Code-only work")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_projects SET project_id=? WHERE work_id='proof-work' AND role='primary'; DELETE FROM write_guard`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, home.RepoPath, "main.go", "package main\n")
	commitKnowledgeRepo(t, home.RepoPath, "code only")
	if err := checkMandatedLawsAtHome(ctx, s, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW"}, nil, false); err != nil {
		t.Fatal(err)
	}
	verified, err := s.EstablishKnowledgeSourceSetProof(ctx, "proof-work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(verified, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := checkMandatedLawsTxAtHome(verified, tx, home.HomeProjectID, home.HomeLocatorID, []string{"HOME-LAW"}, nil, false); err != nil {
		t.Errorf("fresh verified home refused after a code-only commit: %v", err)
	}
}
