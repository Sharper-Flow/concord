package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadWorkerDiffHostFoundationCoversCoordinatorChange(t *testing.T) {
	f := seedWorkContextFixture(t, "diff-coordinator-foundation")
	t.Cleanup(func() { f.store.Close() })
	repo, base := reviewSubjectClaim(t, f.store, f.workID)
	gitRunStore(t, repo, "branch", "-m", "work/"+f.workID)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	dir := filepath.Join(repo, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("coordinator change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := j.commit(t, "coordinator@example.invalid", "coordinator-change")
	j.observeRun(t, "coordinator-change")
	req := workerDiffDispatch(t, j, "coordinator-review")
	before := j.effects(t)
	page, err := j.s.ReadWorkerDiff(t.Context(), req)
	if err != nil || page.BaseCommit != base || page.SubjectCommit != head || !bytes.Contains(page.Data, []byte("+coordinator change\n")) {
		t.Fatalf("host foundation did not expose coordinator delta: %+v err=%v; want base=%s subject=%s", page, err, base, head)
	}
	if after := j.effects(t); after != before {
		t.Fatal("diff read changed workflow state")
	}
}

func TestHostReviewFoundationSelection(t *testing.T) {
	for _, tc := range []struct {
		name, project, tree, claim, class, editorSubject string
		seq                                              int64
		wantRefusal                                      bool
	}{
		{name: "coordinator only", seq: 1},
		{name: "coordinator before later editor", seq: 1, class: "implementation", editorSubject: strings.Repeat("b", 40)},
		{name: "another project", seq: 1, project: "other", wantRefusal: true},
		{name: "another worktree", seq: 1, tree: "other", wantRefusal: true},
		{name: "recreated claim at same path", seq: 1, claim: "recreated", wantRefusal: true},
		{name: "captured at review", seq: 10, wantRefusal: true},
		{name: "captured after review", seq: 11, wantRefusal: true},
		{name: "unclassifiable authorization", seq: 1, class: "unknown", wantRefusal: true},
		{name: "editing authorization without subject", seq: 1, class: "implementation", wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerFoundationCurrentDB(t)
			if _, err := db.Exec(`CREATE TABLE worktree_entries(set_id TEXT,project_id TEXT,claim_op_id TEXT,state TEXT)`); err != nil {
				t.Fatal(err)
			}
			project, tree, claim := tc.project, tc.tree, tc.claim
			if project == "" {
				project = "project"
			}
			if tree == "" {
				tree = "claim"
			}
			if claim == "" {
				claim = "initial"
			}
			base := strings.Repeat("a", 40)
			if _, err := db.Exec(`INSERT INTO worktree_review_foundations VALUES('work',?,?,?,?,'initial')`, project, tree, base, tc.seq); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO worktree_entries VALUES(?,'project',?,'active')`, WorktreeSetID("work"), claim); err != nil {
				t.Fatal(err)
			}
			if tc.class != "" {
				foundationAuthorizationForTest(t, db, 2, 1, "attempt:editor", "implement", tc.editorSubject, "claim")
				foundationAttemptForTest(t, db, "attempt:editor", "implement", tc.class)
			}
			got, err := firstWorkerEditingSubject(t.Context(), db, "work", "project", "claim", 10)
			if tc.wantRefusal {
				if got != "" || !hasFailureKind(err, KindUnauthorizedDispatch) {
					t.Fatalf("got=%q err=%v, want refusal", got, err)
				}
			} else if err != nil || got != base {
				t.Fatalf("got=%q err=%v, want host start %q", got, err, base)
			}
		})
	}
}

func TestHostReviewFoundationClaimEligibility(t *testing.T) {
	for _, state := range []string{"fresh", "pending without native", "adopted branch", "recovered native"} {
		t.Run(state, func(t *testing.T) {
			s, git, _ := worktreeFixture(t)
			req := baseClaim(git)
			if state == "pending without native" || state == "recovered native" {
				if err := s.insertPendingClaim(req, git.repoRoot); err != nil {
					t.Fatal(err)
				}
			}
			if state == "adopted branch" {
				git.branches[claimBranch()] = req.BaseSHA
			}
			if state == "recovered native" {
				if _, err := git.Run(t.Context(), git.repoRoot, "worktree", "add", claimPath(s), "-b", claimBranch(), req.BaseSHA); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.ClaimWorktree(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if state == "fresh" || state == "pending without native" {
				want = 1
				var subject, claim, identity string
				if err := s.db.QueryRow(`SELECT subject_commit,claim_op_id,worktree_identity FROM worktree_review_foundations WHERE work_id=?`, req.WorkID).Scan(&subject, &claim, &identity); err != nil {
					t.Fatal(err)
				}
				if subject != req.BaseSHA || claim != req.OpID || identity != workerWorktreeIdentity(result.Entry.Path) {
					t.Fatalf("incorrect creation observation: %s %s %s", subject, claim, identity)
				}
			}
			if got := countRows(t, s, "worktree_review_foundations"); got != want {
				t.Fatalf("foundation count=%d want=%d", got, want)
			}
			if _, err := s.ClaimWorktree(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if got := countRows(t, s, "worktree_review_foundations"); got != want {
				t.Fatalf("claim replay minted a foundation: count=%d want=%d", got, want)
			}
		})
	}
}

func TestHostReviewFoundationImmutableAndWorkRemoval(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if _, err := s.ClaimWorktree(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE worktree_review_foundations SET subject_commit=?`, strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("foundation update did not refuse: %v", err)
	}
	for _, sql := range []string{
		`INSERT INTO worktree_review_foundations SELECT * FROM worktree_review_foundations`,
		`DELETE FROM worktree_review_foundations`,
	} {
		if _, err := s.db.Exec(sql); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
			t.Fatalf("unguarded foundation write did not refuse: %s: %v", sql, err)
		}
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE worktree_review_foundations SET subject_commit=?`, strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("guarded foundation update did not refuse: %v", err)
	}
	if err := deleteWorkOwnedProjections(t.Context(), tx, req.WorkID); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 0 {
		t.Fatalf("removal retained %d foundations", got)
	}
}

func TestHostReviewFoundationBootstrapReplayAndTerminalCleanup(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	first, err := s.BootstrapWorktree(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	var subject, claim string
	var seq int64
	if err := s.db.QueryRow(`SELECT subject_commit,claim_op_id,captured_seq FROM worktree_review_foundations WHERE work_id=?`, first.WorkID).Scan(&subject, &claim, &seq); err != nil {
		t.Fatal(err)
	}
	if subject != first.Entry.BaseSHA || claim != first.Entry.ClaimOpID || seq <= 0 {
		t.Fatalf("bootstrap foundation=%s %s %d; entry=%+v", subject, claim, seq, first.Entry)
	}
	if _, err := s.BootstrapWorktree(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 1 {
		t.Fatalf("bootstrap replay foundation count=%d", got)
	}
	terminalBootstrapOrigin(t, s, req.IdempotencyKey)
	if got := countRows(t, s, "worktree_review_foundations"); got != 1 {
		t.Fatalf("cancellation before publication lost its retained foundation: %d", got)
	}
	if err := s.TransactDurable(t.Context(), func(transaction *Transaction) error {
		tx := transaction.tx
		if err := enterWrite(t.Context(), tx); err != nil {
			return err
		}
		if err := deleteTerminalExecutionTx(t.Context(), tx, first.WorkID); err != nil {
			return err
		}
		return leaveWrite(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 0 {
		t.Fatalf("terminal cleanup retained %d foundations", got)
	}
}

func TestHostReviewFoundationCreationRollsBackWithClaim(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	native, _, err := s.PrepareWorktreeClaimNative(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := claimWorktreeStoreTx(t.Context(), tx, req, native); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM worktree_review_foundations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("foundation inside claim=%d err=%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 0 {
		t.Fatalf("failed claim left %d foundations", got)
	}
}

func TestHostReviewFoundationGenericWorktreeEventCannotMintStart(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	if err := s.insertPendingClaim(req, git.repoRoot); err != nil {
		t.Fatal(err)
	}
	native, _, err := s.PrepareWorktreeClaimNative(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	payload := marshalWorktreeCreated(req.ExpectedVersion, WorktreeSetID(req.WorkID), req.ProjectID, req.OpID, native.Location, native.Facts, "", 0, 0)
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{{
		EventID: "generic-created", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: req.WorkID,
		Actor: req.PrincipalRef, OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 0 {
		t.Fatalf("generic event minted %d host foundations", got)
	}
}

func TestHostReviewFoundationUpgradeDoesNotBackfill(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	if _, err := s.ClaimWorktree(t.Context(), baseClaim(git)); err != nil {
		t.Fatal(err)
	}
	var foundationMigration migration
	for _, m := range migrations {
		if m.Name == "host_review_foundations" {
			foundationMigration = m
		}
	}
	if foundationMigration.Version == 0 {
		t.Fatal("host foundation migration missing")
	}
	// Undo only the additive foundation migration to leave a populated prior
	// schema. Its claims must survive upgrade without acquiring invented history.
	if _, err := s.db.Exec(`DROP TABLE worktree_review_foundations`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version=?`, foundationMigration.Version); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 0 {
		t.Fatalf("upgrade invented %d foundations", got)
	}
	if got := countRows(t, s, "worktree_claims"); got != 1 {
		t.Fatalf("upgrade changed prior claims: %d", got)
	}
}

func TestHostReviewFoundationMissingBeforeReopenStaysMissing(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	first := liveBootstrapOrigin(t, s, "host-foundation-old-reopen")
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM worktree_review_foundations; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorktree(t.Context(), WorktreeReclaimRequest{
		WorkID: first.WorkID, ProjectID: first.ProjectID, DefaultRef: "origin/main",
		PrincipalRef: "principal/operator", RequestID: "reclaim-old-foundation",
		ExpectedVersion: first.WorkVersion, Runner: ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapExistingWorktree(t.Context(), ExistingBootstrapRequest{
		ProductID: first.ProductID, ProjectID: first.ProjectID, WorkID: first.WorkID,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s, "worktree_review_foundations"); got != 0 {
		t.Fatalf("reopen invented %d foundations", got)
	}
}
