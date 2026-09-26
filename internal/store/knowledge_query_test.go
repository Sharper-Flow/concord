package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// seedSupersededLawHome writes a knowledge home whose manifest carries one
// superseded decision with a declared successor and one accepted decision.
func seedSupersededLawHome(t *testing.T) KnowledgeHome {
	t.Helper()
	repo := initKnowledgeRepo(t)
	retiredBody, currentBody := "retired storage rule\n", "current storage rule\n"
	writeKnowledgeFile(t, repo, "docs/decisions/CD-0001.md", retiredBody)
	writeKnowledgeFile(t, repo, "docs/decisions/CD-0002.md", currentBody)
	retiredSum, currentSum := sha256.Sum256([]byte(retiredBody)), sha256.Sum256([]byte(currentBody))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "storage", Name: "Storage", Purpose: "Storage law", Status: "current", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{
			{ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001.md", Status: "superseded", Successor: "CD-0002",
				Date: "2026-09-20T00:00:00Z", Title: "Retired storage rule", Summary: "A retired storage rule", Tags: []string{},
				Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				Scopes:    homeScope(), HomeDomainID: "storage", SHA256: "sha256:" + hex.EncodeToString(retiredSum[:])},
			{ID: "CD-0002", Kind: "decision", Path: "docs/decisions/CD-0002.md", Status: "accepted",
				Date: "2026-09-20T00:00:00Z", Title: "Current storage rule", Summary: "A current storage rule", Tags: []string{},
				Authority:    KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				LawRelations: []KnowledgeRelation{{Kind: "supersedes", TargetID: "CD-0001"}},
				Scopes:       homeScope(), HomeDomainID: "storage", SHA256: "sha256:" + hex.EncodeToString(currentSum[:])},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commitKnowledgeRepo(t, repo, "superseded storage law")
	return KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
}

// QueryQ9 and QueryQ10 return an indexed superseded law's status and its
// successor end to end: the fields the agent projection reads come from the
// index, not from hand-built test results.
func TestQueryQ9AndQ10ReturnIndexedSupersededLawStatusAndSuccessor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := seedSupersededLawHome(t)
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	q9, err := s.QueryQ9(ctx, Q9Request{Kinds: []string{"decision"}, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	var retired, current *KnowledgeItem
	for i := range q9.Items {
		switch q9.Items[i].ID {
		case "CD-0001":
			retired = &q9.Items[i]
		case "CD-0002":
			current = &q9.Items[i]
		}
	}
	if retired == nil || current == nil {
		t.Fatalf("Q9 decisions = %#v", q9.Items)
	}
	if retired.OutcomeTag != "superseded" || retired.SuccessorID != "CD-0002" {
		t.Fatalf("Q9 superseded item = %#v", retired)
	}
	if got := KnowledgeLawStatus(retired.Kind, retired.OutcomeTag); got != "superseded" {
		t.Fatalf("Q9 superseded law status = %q, want superseded", got)
	}
	if current.SuccessorID != "" {
		t.Fatalf("Q9 accepted item carries successor %q", current.SuccessorID)
	}
	if got := KnowledgeLawStatus(current.Kind, current.OutcomeTag); got != "accepted" {
		t.Fatalf("Q9 accepted law status = %q, want accepted", got)
	}

	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-0001", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if q10.Status != "canonical" || q10.Result == nil {
		t.Fatalf("Q10 = %#v", q10)
	}
	if q10.Result.LawStatus != "superseded" || q10.Result.SuccessorID != "CD-0002" {
		t.Fatalf("Q10 payload = %#v", q10.Result)
	}
}
