package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func publishedAbsenceFixture(t *testing.T) (*Store, KnowledgeHome, string, string) {
	t.Helper()
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	path := ".concord/docs/work/work-published.md"
	content := canonicalWorkNote("work-published", "2026-08-07T00:00:00Z")
	writeKnowledgeFile(t, repo, path, content)
	commit := commitKnowledgeRepo(t, repo, "published outcome")
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home, "proj-web")
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM product_projects WHERE product_id='anchor-product-proj-web'; DELETE FROM products WHERE id='anchor-product-proj-web'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	// The archived linkage is the compaction fold's retained row, seeded here
	// from the note's own parsed metadata.
	hash := seedArchivedWorkNote(t, s, home, commit, path, content)
	return s, home, commit, hash
}

func assertPublishedAbsence(t *testing.T, err error, home KnowledgeHome, commit, hash string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionNotFound || failure.RetrySafe || failure.EffectPossible {
		t.Fatalf("want typed execution absence without effects, got %+v", err)
	}
	for _, text := range []string{"execution state is not retained", home.HomeProjectID, home.HomeLocatorID, ".concord/docs/work/work-published.md", commit, hash} {
		if !strings.Contains(failure.Detail, text) || !strings.Contains(failure.RecoveryAction, text) {
			t.Errorf("absence must carry %q in detail and recovery: %+v", text, failure)
		}
	}
}

func TestPublishedExecutionAbsenceReaders(t *testing.T) {
	s, home, commit, hash := publishedAbsenceFixture(t)
	assertPublishedExecutionAbsenceReaders(t, s, home, commit, hash)
}

func TestPublishedClassificationOnlyAbsenceReaders(t *testing.T) {
	s, home, commit, hash := publishedAbsenceFixture(t)
	terminalCleanupSQL(t, s,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time,intent_json) VALUES('work-published','bug','Classified bug','completed',0,3,'now','now','now','{"defect":{"failure_shape":"shape-fixture"}}')`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('work-published','proj-web','primary')`,
	)
	assertPublishedExecutionAbsenceReaders(t, s, home, commit, hash)
}

func assertPublishedExecutionAbsenceReaders(t *testing.T, s *Store, home KnowledgeHome, commit, hash string) {
	t.Helper()
	ctx := t.Context()
	for name, read := range map[string]func() error{
		"actionable claim": func() error { return s.RequireActionableWork(ctx, "work-published", "worktree_claim") },
		"pin":              func() error { _, err := ReadWorkPin(ctx, s, "work-published"); return err },
		"continuity": func() error {
			_, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: "work-published"})
			return err
		},
		"observations": func() error {
			_, err := s.ReadWorkObservations(ctx, WorkObservationsRequest{WorkID: "work-published"})
			return err
		},
		"external observations": func() error {
			_, err := s.ExternalObservationsForWork(ctx, "work-published", time.Now(), 10)
			return err
		},
		"retirement": func() error {
			_, err := EvaluateProjectSessionRetirement(ctx, s, "work-published", "proj-web", "session-reader", t.TempDir())
			return err
		},
		"summary":   func() error { _, err := s.ReadWorkItemSummary(ctx, "work-published"); return err },
		"history":   func() error { _, err := s.QueryQ7(ctx, Q7Request{Work: "work-published"}); return err },
		"relations": func() error { _, err := s.QueryQ8(ctx, Q8Request{Work: "work-published"}); return err },
		"scope": func() error {
			_, err := s.QueryQ6(ctx, Q6Request{Work: "work-published", Product: "prod-alpha", Project: "proj-web"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { assertPublishedAbsence(t, read(), home, commit, hash) })
	}
}

func TestPublishedExecutionAbsenceProjectGuards(t *testing.T) {
	s, _, _, _ := publishedAbsenceFixture(t)
	ctx := t.Context()
	for name, read := range map[string]func() error{
		"retirement": func() error {
			_, err := EvaluateProjectSessionRetirement(ctx, s, "work-published", "proj-other", "session-reader", t.TempDir())
			return err
		},
		"scope Product": func() error {
			_, err := s.QueryQ6(ctx, Q6Request{Work: "work-published", Product: "prod-other"})
			return err
		},
		"scope Project": func() error {
			_, err := s.QueryQ6(ctx, Q6Request{Work: "work-published", Project: "proj-other"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := read()
			if err == nil || strings.Contains(err.Error(), ".concord/docs/work/") {
				t.Fatalf("out-of-scope read leaked outcome or succeeded: %v", err)
			}
		})
	}
}

// The published linkage is retained history: it survives the source note's
// deletion, because the compaction linkage is not derived from a rescanned
// projection (folder law holds publication in Git and the archived row).
func TestPublishedIssueLinksSurviveWithRetainedLinkage(t *testing.T) {
	s, home, commit, hash := publishedAbsenceFixture(t)
	ctx := t.Context()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("work-live-link", "create-live-link"), operationEvent("member-live-link", "work_project.added", SubjectWorkItem, "work-live-link", map[string]any{"work_id": "work-live-link", "project_id": "proj-web", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-live-link"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"work-published", "work-live-link", "work-orphan-link"} {
		if _, err := s.db.Exec(`INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES(?,?,?,'https://linear.app/example/issue/EX-3','now','now')`, id, "remote-"+id, map[string]string{"work-published": "EX-3", "work-live-link": "EX-4", "work-orphan-link": "EX-5"}[id]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	link, found, err := readLinearLinkCore(ctx, s.db, "work-published")
	if err != nil || !found || link.WorkID != "work-published" || link.HumanKey != "EX-3" {
		t.Fatalf("published link = %+v, found=%v, %v", link, found, err)
	}
	_, err = ReadWorkPin(ctx, s, link.WorkID)
	assertPublishedAbsence(t, err, home, commit, hash)
	if link, found, err := readLinearLinkCore(ctx, s.db, "work-live-link"); err != nil || !found || link.HumanKey != "EX-4" {
		t.Fatalf("live link pruned: %+v found=%v err=%v", link, found, err)
	}
	orphan, found, err := readLinearLinkCore(ctx, s.db, "work-orphan-link")
	if err != nil || !found || orphan.WorkID != "work-orphan-link" || orphan.HumanKey != "EX-5" {
		t.Fatalf("unindexed work lost its issue identity: %+v found=%v %v", orphan, found, err)
	}
	_, err = ReadWorkPin(ctx, s, orphan.WorkID)
	assertFailureKind(t, err, KindProjectionNotFound)
	if !strings.Contains(err.Error(), orphan.WorkID) || !strings.Contains(err.Error(), "execution state is not retained") {
		t.Fatalf("unindexed issue identity lost typed absence: %v", err)
	}
	products, err := s.ProductsForWorkIDs(ctx, []string{"work-published"})
	if err != nil || len(products["work-published"]) != 1 || products["work-published"][0] != "prod-alpha" {
		t.Fatalf("published authorization scope = %v, %v", products, err)
	}
	runKnowledgeGit(t, home.RepoPath, "rm", "--", ".concord/docs/work/work-published.md")
	commitKnowledgeRepo(t, home.RepoPath, "remove outcome")
	if link, found, err := readLinearLinkCore(ctx, s.db, "work-published"); err != nil || !found || link.HumanKey != "EX-3" {
		t.Fatalf("source note deletion lost the published issue identity: %+v found=%v %v", link, found, err)
	}
	_, err = ReadWorkPin(ctx, s, "work-published")
	assertPublishedAbsence(t, err, home, commit, hash)
	if link, found, err := readLinearLinkCore(ctx, s.db, "work-live-link"); err != nil || !found || link.HumanKey != "EX-4" {
		t.Fatalf("live link pruned after outcome removal: %+v found=%v err=%v", link, found, err)
	}
}

func TestPublishedAbsenceHelperUsesCallerTransaction(t *testing.T) {
	s, home, commit, hash := publishedAbsenceFixture(t)
	ctx := t.Context()
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "absence_test")
		if err != nil {
			return err
		}
		assertPublishedAbsence(t, publishedWorkAbsenceQ(ctx, tx, "work-published", "absence_test"), home, commit, hash)
		if err := publishedWorkAbsenceQ(ctx, tx, "work-unknown", "absence_test"); err != nil {
			t.Errorf("unknown work changed: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO work_items(id,kind,title,lifecycle,version,priority,created_at,updated_at) VALUES('work-published','task','Live','needed',1,0,'now','now'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := publishedWorkAbsenceQ(ctx, s.db, "work-published", "absence_test"); err != nil {
		t.Fatalf("live execution must win: %v", err)
	}
	products, err := s.ProductsForWorkIDs(ctx, []string{"work-published"})
	if err != nil || len(products["work-published"]) != 0 {
		t.Fatalf("live work inherited archived authority: %v %v", products, err)
	}
}

func TestPublishedAbsenceRefusesAmbiguousCanonicalIdentity(t *testing.T) {
	s, _, _, _ := publishedAbsenceFixture(t)
	repo := initKnowledgeRepo(t)
	note := canonicalWorkNote("work-published", "2026-08-07T00:00:00Z")
	writeKnowledgeFile(t, repo, ".concord/docs/work/other.md", note)
	commit := commitKnowledgeRepo(t, repo, "second source")
	home := KnowledgeHome{HomeProjectID: "proj-other", HomeLocatorID: "repo-other", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	seedArchivedWorkNote(t, s, home, commit, ".concord/docs/work/other.md", note)
	err := publishedWorkAbsenceQ(t.Context(), s.db, "work-published", "absence_test")
	assertFailureKind(t, err, KindAmbiguousScope)
	if strings.Contains(err.Error(), ".concord/docs/work/") {
		t.Fatalf("ambiguous identity selected a locator: %v", err)
	}
}

func TestPublishedAbsenceAmbiguityNamesOnlyInScopeHomes(t *testing.T) {
	s, _, _, _ := publishedAbsenceFixture(t)
	repo := initKnowledgeRepo(t)
	note := strings.Replace(canonicalWorkNote("work-published", "2026-08-07T00:00:00Z"), "project_ids: [proj-web]", "project_ids: [proj-other]", 1)
	writeKnowledgeFile(t, repo, ".concord/docs/work/other.md", note)
	commit := commitKnowledgeRepo(t, repo, "second source")
	home := KnowledgeHome{HomeProjectID: "proj-other", HomeLocatorID: "repo-other", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	seedArchivedWorkNote(t, s, home, commit, ".concord/docs/work/other.md", note)
	err := publishedWorkAbsenceInScopeQ(t.Context(), s.db, "work-published", "prod-alpha", "proj-web", "absence_test")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindAmbiguousScope || len(failure.CandidateIDs) != 1 || failure.CandidateIDs[0] != "repo-alpha-web" {
		t.Fatalf("ambiguity must name only in-scope homes: %+v", err)
	}
	_, err = s.ProductsForWorkIDs(t.Context(), []string{"work-published"})
	assertFailureKind(t, err, KindUnknownScope)
	if strings.Contains(err.Error(), "repo-other") {
		t.Fatalf("unscoped authorization exposed an outcome home: %v", err)
	}
}
