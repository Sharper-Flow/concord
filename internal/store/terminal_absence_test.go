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
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("work-published", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "published outcome")
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home, "proj-web")
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM product_projects WHERE product_id='anchor-product-proj-web'; DELETE FROM products WHERE id='anchor-product-proj-web'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.db.QueryRow(`SELECT content_hash FROM archived_work WHERE id='work-published'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
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
			_, err := EvaluateProjectSessionRetirement(ctx, s, "work-published", "proj-web", "session-reader")
			return err
		},
		"worktree": func() error {
			_, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-published", ProjectID: "proj-web", Mode: "status"})
			return err
		},
		"oracle output": func() error {
			_, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-published", ProjectID: "proj-web", Mode: "oracle_output", RunRef: "native:old", Stream: "stdout", Length: 20})
			return err
		},
		"resume": func() error {
			_, err := s.ResumeWorktreeLocation(ctx, "prod-alpha", "proj-web", "work-published")
			return err
		},
		"bootstrap existing": func() error {
			_, err := s.BootstrapExistingWorktree(ctx, ExistingBootstrapRequest{ProductID: "prod-alpha", ProjectID: "proj-web", WorkID: "work-published"}, nil)
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
		"resume Project": func() error {
			_, err := s.ResumeWorktreeLocation(ctx, "prod-alpha", "proj-other", "work-published")
			return err
		},
		"resume Product": func() error {
			_, err := s.ResumeWorktreeLocation(ctx, "prod-other", "proj-web", "work-published")
			return err
		},
		"inspect": func() error {
			_, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-published", ProjectID: "proj-other", Mode: "status"})
			return err
		},
		"oracle": func() error {
			_, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-published", ProjectID: "proj-other", Mode: "oracle_output", RunRef: "native:old", Stream: "stdout", Length: 20})
			return err
		},
		"retirement": func() error {
			_, err := EvaluateProjectSessionRetirement(ctx, s, "work-published", "proj-other", "session-reader")
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

func TestPublishedIssueLinksSurviveIndexRebuildAndMissingNotes(t *testing.T) {
	s, home, commit, hash := publishedAbsenceFixture(t)
	ctx := t.Context()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workCreatedEvent("work-live-link", "create-live-link"), operationEvent("member-live-link", "work_project.added", SubjectWorkItem, "work-live-link", map[string]any{"work_id": "work-live-link", "project_id": "proj-web", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-live-link"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"work-published", "work-live-link", "work-orphan-link"} {
		if _, err := s.db.Exec(`INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES(?,?,?,'https://linear.app/example/issue/EX-3','now','now')`, id, "remote-"+id, map[string]string{"work-published": "EX-3", "work-live-link": "EX-4", "work-orphan-link": "EX-5"}[id]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	link, err := s.ReadLinearLinkByIssueKey(ctx, "EX-3")
	if err != nil || link.WorkID != "work-published" {
		t.Fatalf("published link = %+v, %v", link, err)
	}
	_, err = s.ResumeWorktreeLocation(ctx, "prod-alpha", "proj-web", link.WorkID)
	assertPublishedAbsence(t, err, home, commit, hash)
	if _, err := s.ReadLinearLinkByIssueKey(ctx, "EX-4"); err != nil {
		t.Fatalf("live link pruned: %v", err)
	}
	orphan, err := s.ReadLinearLinkByIssueKey(ctx, "EX-5")
	if err != nil || orphan.WorkID != "work-orphan-link" {
		t.Fatalf("unindexed work lost its issue identity: %+v %v", orphan, err)
	}
	_, err = s.ResumeWorktreeLocation(ctx, "prod-alpha", "proj-web", orphan.WorkID)
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
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	link, err = s.ReadLinearLinkByIssueKey(ctx, "EX-3")
	if err != nil || link.WorkID != "work-published" {
		t.Fatalf("missing note lost its issue identity: %+v %v", link, err)
	}
	_, err = s.ResumeWorktreeLocation(ctx, "prod-alpha", "proj-web", link.WorkID)
	assertFailureKind(t, err, KindProjectionNotFound)
	if !strings.Contains(err.Error(), link.WorkID) {
		t.Fatalf("missing note absence lost the work identity: %v", err)
	}
	if _, err := s.ReadLinearLinkByIssueKey(ctx, "EX-4"); err != nil {
		t.Fatalf("live link pruned after outcome removal: %v", err)
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
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO work_items(id,kind,title,lifecycle,version,priority,created_at,updated_at) VALUES('work-published','task','Live','needed',1,0,'now','now'); DELETE FROM fold_guard`); err != nil {
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
	writeKnowledgeFile(t, repo, ".concord/docs/work/other.md", canonicalWorkNote("work-published", "2026-08-07T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "second source")
	home := KnowledgeHome{HomeProjectID: "proj-other", HomeLocatorID: "repo-other", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
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
	commitKnowledgeRepo(t, repo, "second source")
	home := KnowledgeHome{HomeProjectID: "proj-other", HomeLocatorID: "repo-other", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
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
