package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func seedTokenKnowledgeHome(t *testing.T, count int) (*Store, KnowledgeHome) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "token-project", HomeLocatorID: "token-locator", RepoPath: repo, HeadRef: "HEAD"}
	fixtures := []manifestFixture{{
		ID: "law-pin-rule", Kind: "decision", Path: ".concord/docs/decisions/CD-0999-token-law.md",
		Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Pin law revisions",
		Summary: "Govern revision policy", Tags: []string{"revision-tags"},
		Scopes:  KnowledgeRecordScopes{Mode: "explicit", TagIDs: []string{"pin-scopes"}},
		Content: "A body describes durable pins. One two three four five six seven eight nine.\n",
	}}
	for i := 1; i < count; i++ {
		fixtures = append(fixtures, manifestFixture{
			ID: fmt.Sprintf("other-law-%03d", i), Kind: "decision",
			Path:   fmt.Sprintf(".concord/docs/decisions/CD-%04d-other.md", i),
			Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Storage boundary",
			Summary: "A storage rule", Scopes: homeScope(), Content: "Storage remains durable.\n",
		})
	}
	writeManifestFixture(t, repo, fixtures...)
	commitKnowledgeRepo(t, repo, "token search laws")
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "token-product", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func TestQueryQ9MatchesEveryTextToken(t *testing.T) {
	t.Parallel()
	s, home := seedTokenKnowledgeHome(t, 1)
	for _, tc := range []struct {
		name, text string
		class      int
	}{
		{"reordered_title", "law revision pin", 1},
		{"title_and_body", "pin durable", 2},
		{"id_substring", "law-pin", 1},
		{"summary_tokens", "policy govern", 1},
		{"record_tag_substring", "revision-tag", 1},
		{"scope_tag_substring", "pin-scope", 1},
		{"all_fields", "law-pin policy revision-tags pin-scopes durable", 2},
		{"mixed_case_whitespace", "\tLAW\nrevision\u2003PIN ", 1},
		{"repeated_tokens", "pin pin law", 1},
		{"nine_tokens", "nine eight seven six five four three two one", 2},
		{"maximum_token_count", strings.Repeat("a ", 128), 1},
		{"empty_text", "", 0},
		{"whitespace_only", "\t\n\u2003 ", 1},
		{"exact_id", "LAW-PIN-RULE", 0},
		{"exact_title", "PIN LAW REVISIONS", 0},
		{"exact_record_tag", "REVISION-TAGS", 0},
		{"exact_scope_tag", "PIN-SCOPES", 0},
		{"exact_domain", "FIXTURE-LAW", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.QueryQ9(context.Background(), Q9Request{Text: tc.text, Kinds: []string{"decision"}, Home: home})
			if err != nil {
				t.Fatal(err)
			}
			if result.Authority != "authoritative" || len(result.Items) != 1 || result.Items[0].ID != "law-pin-rule" || result.Items[0].MatchClass != tc.class {
				t.Fatalf("text %q: authority=%s items=%+v, want law-pin-rule class %d", tc.text, result.Authority, result.Items, tc.class)
			}
		})
	}
}

func TestQueryQ9AbsentTokenIsAuthoritativeEmpty(t *testing.T) {
	t.Parallel()
	s, home := seedTokenKnowledgeHome(t, 1)
	for _, text := range []string{"law revision absent", "pin ' OR 1=1 --", "pin%", "pin_"} {
		result, err := s.QueryQ9(context.Background(), Q9Request{Text: text, Kinds: []string{"decision"}, Home: home})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 0 || result.Authority != "authoritative" || result.IndexWatermark == "" || result.NextCursor != nil || !slices.Equal(result.Omissions, []string{"knowledge_kind_not_indexed:research"}) {
			t.Fatalf("text %q: authoritative empty = %+v", text, result)
		}
	}
}

func TestQueryQ9TokenSearchStaleWatermarkOmitsAuthority(t *testing.T) {
	t.Parallel()
	s, home := seedTokenKnowledgeHome(t, 1)
	writeKnowledgeFile(t, home.RepoPath, ".concord/docs/work/token-work.md", canonicalWorkNote("token-work", "2026-09-21T00:00:00Z"))
	commitKnowledgeRepo(t, home.RepoPath, "add projected work note")
	req := Q9Request{Text: "law revision absent", Kinds: []string{"decision"}, Home: home}
	_, err := s.QueryQ9(context.Background(), req)
	assertFailureKind(t, err, KindIndexDegraded)
	req.AllowDegraded = true
	result, err := s.QueryQ9(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || result.Authority != "degraded" || !slices.Contains(result.Omissions, "knowledge_index_lagging_or_unreachable") {
		t.Fatalf("stale token search = %+v", result)
	}
}

func TestQueryQ9TokenSearchFederatedPaging(t *testing.T) {
	t.Parallel()
	home := KnowledgeHome{HomeProjectID: "token-home", HomeLocatorID: "token-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "token-source", HomeLocatorID: "token-source-loc", HeadRef: "HEAD"}
	s, _ := seedFederatedProduct(t, "token-federated", home, source,
		"HOME-LAW", ".concord/docs/decisions/CD-0902-source-law.md", "Law bounded home")
	req := Q9Request{Product: "token-federated", Text: "law home", Kinds: []string{"decision"}, Limit: 1}
	for _, project := range []string{home.HomeProjectID, source.HomeProjectID} {
		page, err := s.QueryQ9(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if page.Authority != "authoritative" || len(page.Items) != 1 || page.Items[0].ID != "HOME-LAW" || page.Items[0].HomeProjectID != project || page.Items[0].MatchClass != 1 || page.NextCursor == nil {
			t.Fatalf("page for %s = %+v", project, page)
		}
		req.Cursor = *page.NextCursor
	}
	last, err := s.QueryQ9(context.Background(), req)
	if err != nil || len(last.Items) != 0 || last.NextCursor != nil || last.Authority != "authoritative" {
		t.Fatalf("final page = %+v, err %v", last, err)
	}
}

func TestQueryQ9TokenSearchQueryPlan(t *testing.T) {
	s, home := seedTokenKnowledgeHome(t, 255)
	ctx := context.Background()
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, "SELECT COUNT(*) FROM archived_work").Scan(&count); err != nil || count != 255 {
		t.Fatalf("record count = %d, err %v", count, err)
	}
	query, args := buildKnowledgeQueryForScope(Q9Request{Text: "law revision pin", Home: home}, []string{"decision"}, nil, 25, nil)
	rows, err := s.DatabaseForTesting().QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	t.Logf("records=%d text=%q parameters=%d", count, "law revision pin", len(args))
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d|%d|%s", id, parent, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryQ9FindsLawByBodyOnlyText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "body-project", HomeLocatorID: "body-locator", RepoPath: repo, HeadRef: "HEAD"}
	writeManifestFixture(t, repo, manifestFixture{
		ID:      "body-law",
		Kind:    "decision",
		Path:    ".concord/docs/decisions/CD-0999-body-law.md",
		Status:  "accepted",
		Date:    "2026-09-20T00:00:00Z",
		Title:   "Storage decision",
		Summary: "A bounded storage rule",
		Scopes:  KnowledgeRecordScopes{Mode: "home"},
		Content: "The hidden retrieval phrase is body-only-discovery.\n",
	}, manifestFixture{
		ID:      "title-law",
		Kind:    "decision",
		Path:    ".concord/docs/decisions/CD-0998-title-law.md",
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
	writeKnowledgeFile(t, repo, ".concord/docs/decisions/CD-0001.md", retiredBody)
	writeKnowledgeFile(t, repo, ".concord/docs/decisions/CD-0002.md", currentBody)
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
			{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001.md", Status: "superseded", Successor: "CD-0002",
				Date: "2026-09-20T00:00:00Z", Title: "Retired storage rule", Summary: "A retired storage rule", Tags: []string{},
				Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "fixture-authority", ContractVersion: 1},
				Scopes:    homeScope(), HomeDomainID: "storage", SHA256: "sha256:" + hex.EncodeToString(retiredSum[:])},
			{ID: "CD-0002", Kind: "decision", Path: ".concord/docs/decisions/CD-0002.md", Status: "accepted",
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

// A work lookup reads the historical corpus, not the current registered
// source set (CD-0200): an archived work note is frozen evidence whose
// recorded home is part of its identity, so a later home designation
// changes neither its answer nor its locator.
func TestQ10WorkNoteSurvivesHomeDesignationChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := ".concord/docs/work/2026-08-10-review-historical.md"
	content := canonicalWorkNote("review-historical-work", "2026-08-10T00:00:00Z")
	writeKnowledgeFile(t, repo, path, content)
	commit := commitKnowledgeRepo(t, repo, "historical work note")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "review-old-home", HomeLocatorID: "review-old-loc", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "review-history-product", home, home.HomeProjectID)
	hash := sha256.Sum256([]byte(content))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"review-historical-work", "work_note", "Historical work", "2026-08-10T00:00:00Z", "completed", "[]", "completed", 1, "summary",
		home.HomeProjectID, home.HomeLocatorID, path, commit, "sha256:"+hex.EncodeToString(hash[:]), "explicit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work_products(home_project_id,home_locator_id,work_id,product_id) VALUES(?,?,?,?)`,
		home.HomeProjectID, home.HomeLocatorID, "review-historical-work", "review-history-product"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	before, err := s.QueryQ10(ctx, Q10Request{Work: "review-historical-work", Product: "review-history-product"})
	if err != nil || before.Status != "canonical" {
		t.Fatalf("setup: before the home change Q10 = %+v, %v", before, err)
	}
	replacement := KnowledgeHome{HomeProjectID: "review-new-home", HomeLocatorID: "review-new-loc", RepoPath: initKnowledgeRepo(t), HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "review-history-product", replacement, replacement.HomeProjectID)
	if _, err := s.DesignateProductKnowledgeHome(ctx, ProductKnowledgeHomeDesignation{ProductID: "review-history-product", ProjectID: replacement.HomeProjectID, LocatorID: replacement.HomeLocatorID, ExpectedVersion: 1, Reason: "new shared-law home"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.QueryQ10(ctx, Q10Request{Work: "review-historical-work", Product: "review-history-product"})
	if err != nil || after.Status != "canonical" {
		t.Fatalf("the frozen work note lost its answer after only the designation changed: status=%s err=%v", after.Status, err)
	}
}
