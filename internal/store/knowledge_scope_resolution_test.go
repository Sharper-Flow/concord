package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestResolveKnowledgeQueryHomeOwnsProductAndProjectAuthority(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	productHome := KnowledgeHome{HomeProjectID: "home-project", HomeLocatorID: "home-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", productHome, "ambient-project")

	resolved, err := s.ResolveKnowledgeQueryHome(ctx, "product-a", "ambient-project", KnowledgeHome{}, "PM1.Q9")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != productHome {
		t.Fatalf("resolved product home=%#v want=%#v", resolved, productHome)
	}

	wrong := KnowledgeHome{HomeProjectID: "wrong-project", HomeLocatorID: "wrong-locator", RepoPath: repo, HeadRef: "HEAD"}
	if _, err := s.ResolveKnowledgeQueryHome(ctx, "product-a", "ambient-project", wrong, "PM1.Q9"); err == nil {
		t.Fatal("mismatched caller home was accepted")
	} else {
		assertFailureKind(t, err, KindInvalidFilter)
	}
	if _, err := s.QueryQ9(ctx, Q9Request{Product: "product-a", Home: wrong}); err == nil {
		t.Fatal("Q9 accepted a mismatched caller home")
	} else {
		assertFailureKind(t, err, KindInvalidFilter)
	}
	for name, mismatch := range map[string]KnowledgeHome{
		"path": {HomeProjectID: productHome.HomeProjectID, HomeLocatorID: productHome.HomeLocatorID, RepoPath: "/not-the-authoritative-path", HeadRef: "HEAD"},
		"head": {HomeProjectID: productHome.HomeProjectID, HomeLocatorID: productHome.HomeLocatorID, RepoPath: productHome.RepoPath, HeadRef: "refs/heads/other"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.ResolveKnowledgeQueryHome(ctx, "product-a", "ambient-project", mismatch, "PM1.Q9"); err == nil {
				t.Fatal("mismatched caller home evidence was accepted")
			} else {
				assertFailureKind(t, err, KindInvalidFilter)
			}
		})
	}
	if _, err := s.ResolveKnowledgeQueryHome(ctx, "product-a", "not-a-member", KnowledgeHome{}, "PM1.Q9"); err == nil {
		t.Fatal("invalid Product/Project membership was accepted")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
	if _, err := s.ResolveKnowledgeQueryHome(ctx, "missing-product", "", KnowledgeHome{}, "PM1.Q9"); err == nil {
		t.Fatal("missing Product home was accepted")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
}

func TestResolveKnowledgeQueryHomeProjectOnlyRequiresOneCanonicalLocator(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	home := KnowledgeHome{HomeProjectID: "project-only", HomeLocatorID: "locator-one", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	resolved, err := s.ResolveKnowledgeQueryHome(ctx, "", "project-only", KnowledgeHome{}, "PM1.Q9")
	if err != nil || resolved != home {
		t.Fatalf("project-only resolution=%#v err=%v", resolved, err)
	}
	if _, err := s.ResolveKnowledgeQueryHome(ctx, "", "project-missing", KnowledgeHome{}, "PM1.Q9"); err == nil {
		t.Fatal("project without canonical locator was accepted")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
	secondRepo := initKnowledgeRepo(t)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-two','project-only','canonical_path',? ,?,'now','now')`, secondRepo, secondRepo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveKnowledgeQueryHome(ctx, "", "project-only", KnowledgeHome{}, "PM1.Q9"); err == nil {
		t.Fatal("ambiguous Project locators were accepted")
	} else {
		assertFailureKind(t, err, KindAmbiguousScope)
	}
}

func TestQ10RejectsCallerHomeMismatchBeforeHistoricalRead(t *testing.T) {
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	authoritative := KnowledgeHome{HomeProjectID: "product-home", HomeLocatorID: "product-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", authoritative, authoritative.HomeProjectID)
	wrong := KnowledgeHome{HomeProjectID: "ambient-project", HomeLocatorID: "ambient-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, wrong)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES('historical','lesson','Historical','2026-08-10T00:00:00Z','published','[]','completed',1,'summary',? ,? ,'docs/lessons/historical.md','commit','sha256:`+strings.Repeat("a", 64)+`','home')`, authoritative.HomeProjectID, authoritative.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	_, err := s.QueryQ10(context.Background(), Q10Request{KnowledgeID: "historical", Product: "product-a", Home: wrong})
	if err == nil {
		t.Fatal("Q10 accepted a caller home that overrides the Product home")
	}
	assertFailureKind(t, err, KindInvalidFilter)
}

func TestQ10HomeScopeUsesCurrentMembershipAndRecordedLocator(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo, manifestFixture{ID: "historical-home", Kind: "lesson", Path: "docs/lessons/historical-home.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Historical home", Summary: "Historical summary", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	commitKnowledgeRepo(t, repo, "historical home")
	s := openTemp(t)
	firstHome := KnowledgeHome{HomeProjectID: "first-project", HomeLocatorID: "first-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", firstHome, firstHome.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(ctx, firstHome); err != nil {
		t.Fatal(err)
	}
	secondRepo := initKnowledgeRepo(t)
	secondHome := KnowledgeHome{HomeProjectID: "second-project", HomeLocatorID: "second-locator", RepoPath: secondRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-b", secondHome, firstHome.HomeProjectID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM product_knowledge_homes WHERE product_id='product-b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, product := range []string{"product-a", "product-b"} {
		result, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "historical-home", Product: product})
		if err != nil || result.Status != "canonical" || result.Note == nil || result.Note.HomeLocatorID != firstHome.HomeLocatorID {
			t.Fatalf("shared home Project Q10 product=%s result=%#v err=%v", product, result, err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM product_projects WHERE product_id='product-a' AND project_id=?`, firstHome.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "historical-home", Product: "product-a"}); err == nil {
		t.Fatal("Product that lost the stored home Project remained in scope")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
	result, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "historical-home", Product: "product-b"})
	if err != nil || result.Status != "canonical" || result.Note == nil || result.Note.HomeProjectID != firstHome.HomeProjectID || result.Note.CommitOID == "" {
		t.Fatalf("historical Q10 after Product membership move=%#v err=%v", result, err)
	}
	result, err = s.QueryQ10(ctx, Q10Request{KnowledgeID: "historical-home"})
	if err != nil || result.Status != "canonical" || result.Note == nil || result.Note.HomeLocatorID != firstHome.HomeLocatorID {
		t.Fatalf("unscoped historical Q10 after Product membership move=%#v err=%v", result, err)
	}
}

func TestQ10WorkNoteProductScopeRemainsFrozenAfterMembershipMove(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := "docs/work/2026-08-10-frozen-work.md"
	content := canonicalWorkNote("frozen-work", "2026-08-10T00:00:00Z")
	writeKnowledgeFile(t, repo, path, content)
	commit := commitKnowledgeRepo(t, repo, "frozen work note")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "frozen-project", HomeLocatorID: "frozen-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", home, home.HomeProjectID)
	authorizeKnowledgeProductMembership(t, s, "product-b", home.HomeProjectID)
	hash := sha256.Sum256([]byte(content))
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "frozen-work", "work_note", "Frozen work", "2026-08-10T00:00:00Z", "completed", "[]", "completed", 1, "summary", home.HomeProjectID, home.HomeLocatorID, path, commit, "sha256:"+hex.EncodeToString(hash[:]), "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work_products(work_id,product_id) VALUES('frozen-work','product-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.QueryQ10(ctx, Q10Request{Work: "frozen-work", Product: "product-a"}); err != nil || result.Status != "canonical" {
		t.Fatalf("frozen work before membership move=%#v err=%v", result, err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM product_projects WHERE product_id='product-a' AND project_id=?`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.QueryQ10(ctx, Q10Request{Work: "frozen-work", Product: "product-a"}); err != nil || result.Status != "canonical" {
		t.Fatalf("frozen work after membership move=%#v err=%v", result, err)
	}
	if _, err := s.QueryQ10(ctx, Q10Request{Work: "frozen-work", Product: "product-b"}); err == nil {
		t.Fatal("work note followed current home Project membership")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
}

func TestQ10MissingCurrentLocatorIsUnavailableAndProofCanDegrade(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "historical proof\n")
	commitKnowledgeRepo(t, repo, "historical proof")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "historical-project", HomeLocatorID: "historical-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES('missing-locator','work_note','Missing locator','2026-08-10T00:00:00Z','completed','[]','completed',1,'summary',?,?, 'docs/work/missing.md','deadbeef','sha256:`+strings.Repeat("a", 64)+`','explicit'); DELETE FROM fold_guard`, home.HomeProjectID, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM project_locators WHERE locator_id=?; DELETE FROM fold_guard`, home.HomeLocatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "missing-locator"}); err == nil {
		t.Fatal("missing current locator was accepted")
	} else {
		assertFailureKind(t, err, KindKnowledgeUnavailable)
	}
	// Restore locator evidence, then prove a missing historical Git object is
	// degraded rather than reclassified as caller invalid_filter.
	authorizeKnowledgeLocator(t, s, home)
	result, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "missing-locator", AllowDegraded: true})
	if err != nil || result.Authority != "degraded" || result.Status != "missing" {
		t.Fatalf("degraded historical proof=%#v err=%v", result, err)
	}
}
