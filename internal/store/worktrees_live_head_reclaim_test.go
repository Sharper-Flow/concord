package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CD-0212 regression coverage. The reclaim, audit, and destroy surfaces read
// one immutable live-HEAD content observation — the branch the live worktree
// has checked out (or its detached tip), that tip's SHA, and the clean-tree
// fact — and gate the content that observation names, never the stored claim
// branch alone. Directory-removal proof stays separate from stored-ref
// deletion: a stored ref is deleted only when its own durability and pinned
// tip prove it, retained otherwise, and the retention is recorded durably.

// con829Fixture drives one real Git repository plus one store, the shape the
// CON-829 report came from. Every probe below runs against real git, so the
// regression coverage cannot drift from git's own refusal edges.
type con829Fixture struct {
	t        *testing.T
	s        *Store
	repoRoot string
	workRoot string
}

func con829FixtureNew(t *testing.T) *con829Fixture {
	t.Helper()
	f := &con829Fixture{t: t, s: openTemp(t)}
	ctx := context.Background()
	if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	f.git(repoRoot, "init", "-b", "main")
	f.git(repoRoot, "config", "user.email", "concord@example.invalid")
	f.git(repoRoot, "config", "user.name", "Concord Con829 Test")
	if err := writeFile(filepath.Join(repoRoot, "tracked.txt"), "base\n"); err != nil {
		t.Fatal(err)
	}
	f.git(repoRoot, "add", "tracked.txt")
	f.git(repoRoot, "commit", "-m", "base")
	origin := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"-origin.git")
	f.git(filepath.Dir(repoRoot), "init", "-q", "--bare", "-b", "main", origin)
	f.git(repoRoot, "remote", "add", "origin", origin)
	f.git(repoRoot, "push", "-q", "origin", "main")
	f.git(repoRoot, "fetch", "-q", "origin")
	f.git(repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	f.repoRoot = repoRoot
	if err := f.s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	f.workRoot = filepath.Join(filepath.Dir(f.s.Path()), "worktrees")
	return f
}

func (f *con829Fixture) git(dir string, args ...string) {
	f.t.Helper()
	gitRunStore(f.t, dir, args...)
}

func (f *con829Fixture) gitOut(dir string, args ...string) string {
	f.t.Helper()
	out, err := (ExecGitRunner{}).Run(context.Background(), dir, args...)
	if err != nil {
		f.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// claimWork creates the work item, claims its canonical worktree, and returns
// the worktree path and the stored claim branch.
func (f *con829Fixture) claimWork(workID string) (string, string) {
	f.t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Con829 ` + workID + `","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		f.t.Fatal(err)
	}
	base := f.gitOut(f.repoRoot, "rev-parse", "HEAD")
	result, err := f.s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "op-" + workID, WorkID: workID, ProjectID: "project-w",
		BaseSHA:      base,
		PrincipalRef: "principal-1", RequestID: "req-" + workID,
		ExpectedVersion: 2, Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return result.Entry.Path, result.Entry.Branch
}

func (f *con829Fixture) completeWork(workID string) {
	f.t.Helper()
	payload := `{"from":"needed","to":"completed","reason":"fixture terminal","evidence_refs":["fixture"],"expected_version":3,"resulting_version":4}`
	err := ApplyOperation(context.Background(), f.s, Operation{Events: []Event{
		{EventID: workID + "-wt-completed", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 3}})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *con829Fixture) reclaimRequest(workID, requestID string) WorktreeReclaimRequest {
	version, err := currentWorkVersion(context.Background(), f.s.db, workID)
	if err != nil {
		f.t.Fatal(err)
	}
	return WorktreeReclaimRequest{
		WorkID: workID, ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: requestID,
		ExpectedVersion: version, Now: time.Unix(40, 0).UTC(),
	}
}

// refTip resolves a branch ref's current tip, failing the test when the ref
// no longer exists.
func (f *con829Fixture) refTip(branch string) string {
	f.t.Helper()
	return f.gitOut(f.repoRoot, "rev-parse", "--verify", "refs/heads/"+branch)
}

func (f *con829Fixture) refExists(branch string) bool {
	f.t.Helper()
	_, err := (ExecGitRunner{}).Run(context.Background(), f.repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func (f *con829Fixture) worktreeGone(path string) bool {
	f.t.Helper()
	_, err := (ExecGitRunner{}).Run(context.Background(), path, "rev-parse", "--abbrev-ref", "HEAD")
	return err != nil
}

// worktreePath returns the canonical worktree path of an already-claimed
// work item, the same convention LocateWorktree owns.
func (f *con829Fixture) worktreePath(workID string) string {
	f.t.Helper()
	return filepath.Join(f.workRoot, "project-w", workID)
}

// con829RetainedRef is the decoded retained_refs entry of reclaim facts.
type con829RetainedRef struct {
	Branch string `json:"branch"`
	Tip    string `json:"tip"`
	Reason string `json:"reason"`
}

// con829BranchDeletion is the decoded branch_deletions entry of reclaim
// facts: the recorded native-removal plan.
type con829BranchDeletion struct {
	Branch      string `json:"branch"`
	ExpectedTip string `json:"expected_tip"`
	Reason      string `json:"reason"`
}

type con829LiveHeadFacts struct {
	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	Tip      string `json:"tip"`
}

type con829ReclaimFacts struct {
	LiveHead        *con829LiveHeadFacts   `json:"live_head"`
	RetainedRefs    []con829RetainedRef    `json:"retained_refs"`
	BranchDeletions []con829BranchDeletion `json:"branch_deletions"`
	StoredBranch    string                 `json:"stored_branch"`
	StoredRefAbsent bool                   `json:"stored_ref_absent"`
}

// entryFacts decodes the recorded git facts of the work item's worktree entry.
func (f *con829Fixture) entryFacts(workID string) con829ReclaimFacts {
	f.t.Helper()
	entries, err := f.s.WorktreeEntries(context.Background(), workID)
	if err != nil || len(entries) != 1 {
		f.t.Fatalf("entries for %s: %+v err=%v", workID, entries, err)
	}
	var facts con829ReclaimFacts
	if err := json.Unmarshal(entries[0].GitFacts, &facts); err != nil {
		f.t.Fatalf("facts decode: %v", err)
	}
	return facts
}

// correctionScenario models the CON-829 report: round-1 commits stay on the
// stored claim branch unmerged, the correction round moved the live checkout
// onto a correction branch whose net diff the default ref holds as one squash
// commit, and the correction branch's remote head was deleted after the
// merge. The clean terminal worktree must reclaim without destructive
// approval, the stored claim ref must survive with its retention recorded,
// and the divergent live branch ref must survive.
func (f *con829Fixture) correctionScenario(workID string) (path, claimBranch, correctionBranch, claimTip string) {
	f.t.Helper()
	path, claimBranch = f.claimWork(workID)
	// Round 1: unique commits only the stored claim branch holds.
	if err := writeFile(filepath.Join(path, "round1.txt"), "round one\n"); err != nil {
		f.t.Fatal(err)
	}
	f.git(path, "add", "round1.txt")
	f.git(path, "commit", "-m", "round one")
	claimTip = f.gitOut(path, "rev-parse", "HEAD")
	// Correction round: a new branch from the default endpoint carrying the
	// correction, squash-merged into the default ref and remote-deleted.
	f.git(path, "checkout", "-q", "-b", "correction/"+workID, "origin/main")
	if err := writeFile(filepath.Join(path, "correction.txt"), "correction\n"); err != nil {
		f.t.Fatal(err)
	}
	f.git(path, "add", "correction.txt")
	f.git(path, "commit", "-m", "correction")
	f.git(f.repoRoot, "merge", "--squash", "correction/"+workID)
	f.git(f.repoRoot, "commit", "-m", "squash correction "+workID)
	f.git(f.repoRoot, "push", "-q", "origin", "main")
	f.git(f.repoRoot, "push", "-q", "origin", "correction/"+workID)
	f.git(f.repoRoot, "push", "-q", "origin", "--delete", "correction/"+workID)
	f.git(f.repoRoot, "fetch", "--prune", "-q", "origin")
	return path, claimBranch, "correction/" + workID, claimTip
}

// assertCorrectionReclaimed checks the shared outcome of every reclaim path
// over the correction scenario: the directory is gone, the stored claim ref
// survives at its recorded tip, and the facts carry the live head.
func (f *con829Fixture) assertCorrectionReclaimed(workID, path, claimBranch, correctionBranch, claimTip string) {
	f.t.Helper()
	if !f.worktreeGone(path) {
		f.t.Fatalf("worktree at %s was not removed", path)
	}
	if !f.refExists(claimBranch) || f.refTip(claimBranch) != claimTip {
		f.t.Fatalf("unproven stored claim ref %s must survive at %s", claimBranch, claimTip)
	}
	if !f.refExists(correctionBranch) {
		f.t.Fatalf("divergent live branch %s must survive", correctionBranch)
	}
	facts := f.entryFacts(workID)
	if facts.LiveHead == nil || facts.LiveHead.Branch != correctionBranch || facts.LiveHead.Detached || facts.LiveHead.Tip != f.refTip(correctionBranch) {
		f.t.Fatalf("facts must record the live head: %+v", facts.LiveHead)
	}
	if facts.StoredBranch != claimBranch {
		f.t.Fatalf("facts must name the stored claim branch %s: %+v", claimBranch, facts)
	}
	if len(facts.RetainedRefs) == 0 {
		f.t.Fatalf("facts must record the retained refs: %+v", facts)
	}
	retained := map[string]con829RetainedRef{}
	for _, ref := range facts.RetainedRefs {
		retained[ref.Branch] = ref
	}
	if r, ok := retained[claimBranch]; !ok || r.Tip != claimTip || r.Reason == "" {
		f.t.Fatalf("stored claim ref retention must record branch, tip, and reason: %+v", r)
	}
	if _, ok := retained[correctionBranch]; !ok {
		f.t.Fatalf("divergent live branch retention must be recorded: %+v", facts.RetainedRefs)
	}
}

// TestCorrectionCheckoutReclaimsAcrossAllPaths pins CD-0212 D1: a clean
// terminal worktree checked out on a squash-contained correction branch
// reclaims without destructive approval after the remote branch is deleted,
// across the direct reclaim, the audit classification, the audit reclaim
// pass, and the non-destructive destroy. The unstarted correction reclaims
// under its own gate, and the dirty tree, unpublished live lesson, and live
// occupant keep their refusals.
func TestCorrectionCheckoutReclaimsAcrossAllPaths(t *testing.T) {
	t.Run("direct reclaim", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch, correctionBranch, claimTip := f.correctionScenario("work-direct")
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-direct", "con829-direct")); err != nil {
			t.Fatalf("correction checkout must reclaim without destructive approval, got %v", err)
		}
		f.assertCorrectionReclaimed("work-direct", path, claimBranch, correctionBranch, claimTip)
	})

	t.Run("audit classification", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, _, correctionBranch, _ := f.correctionScenario("work-classified")
		f.completeWork("work-classified")
		audit, err := f.s.WorktreeAudit(context.Background(), WorktreeAuditRequest{ProductID: "product-w", Limit: 100, DefaultRef: "origin/main"})
		if err != nil {
			t.Fatal(err)
		}
		var terminal, contentRisk, lesson bool
		for _, row := range audit.Drift {
			if row.Path != path {
				continue
			}
			switch row.Class {
			case WorktreeDriftTerminalPresent:
				terminal = true
				if row.HeadBranch != correctionBranch {
					t.Fatalf("terminal-present row must name the live head branch %s: %+v", correctionBranch, row)
				}
			case WorktreeDriftUnpushedContent, WorktreeDriftUncommittedContent:
				contentRisk = true
			case WorktreeDriftUnpublishedLesson:
				lesson = true
			}
		}
		if contentRisk {
			t.Fatalf("squash-contained live head must carry no content risk: %+v", audit.Drift)
		}
		if lesson {
			t.Fatalf("the correction branch adds no lesson record, so no lesson row may appear: %+v", audit.Drift)
		}
		if !terminal {
			t.Fatalf("terminal worktree on a contained correction branch must be classified terminal_present: %+v", audit.Drift)
		}
	})

	t.Run("audit reclaim", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch, correctionBranch, claimTip := f.correctionScenario("work-pass")
		f.completeWork("work-pass")
		result, err := f.s.WorktreeAuditReclaim(context.Background(), WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "con829-pass", Now: time.Unix(40, 0).UTC(), Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var row *WorktreeAuditReclaimRow
		for i := range result.Rows {
			if result.Rows[i].WorkID == "work-pass" {
				row = &result.Rows[i]
			}
		}
		if row == nil || row.Outcome != WorktreeAuditReclaimed {
			t.Fatalf("audit pass must reclaim the correction checkout: %+v", row)
		}
		f.assertCorrectionReclaimed("work-pass", path, claimBranch, correctionBranch, claimTip)
		var retainedClaim bool
		for _, ref := range row.RetainedRefs {
			if ref.Branch == claimBranch && ref.Tip == claimTip && ref.Reason != "" {
				retainedClaim = true
			}
		}
		if !retainedClaim {
			t.Fatalf("reclaimed row must report the retained stored ref visibly: %+v", row.RetainedRefs)
		}
	})

	t.Run("non-destructive destroy", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch, correctionBranch, claimTip := f.correctionScenario("work-destroy")
		f.completeWork("work-destroy")
		entry, err := f.s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
			WorkID: "work-destroy", ProjectID: "project-w", DefaultRef: "origin/main",
			ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "con829-destroy", Now: time.Unix(40, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("non-destructive destroy must reclaim the correction checkout, got %v", err)
		}
		if entry.State != worktreeEntryReclaimed {
			t.Fatalf("entry=%+v, want reclaimed", entry)
		}
		f.assertCorrectionReclaimed("work-destroy", path, claimBranch, correctionBranch, claimTip)
	})

	t.Run("unstarted correction", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-unstarted")
		f.git(path, "checkout", "-q", "-b", "correction/work-unstarted", "origin/main")
		req := f.reclaimRequest("work-unstarted", "con829-unstarted")
		req.RequireUnstarted = true
		if _, err := f.s.ReclaimWorktree(context.Background(), req); err != nil {
			t.Fatalf("unstarted correction checkout must reclaim under the CD-0118 gate, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		if f.refExists(claimBranch) {
			t.Fatalf("durable stored claim ref %s must be deleted after removal", claimBranch)
		}
		if !f.refExists("correction/work-unstarted") {
			f.t.Fatal("divergent live branch correction/work-unstarted must survive")
		}
	})

	t.Run("unpublished live lesson refuses", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, _, correctionBranch, _ := f.correctionScenario("work-lesson")
		lessonPath := filepath.Join(path, knowledgeRecordTreeDir, "lesson-con829.json")
		if err := os.MkdirAll(filepath.Dir(lessonPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(lessonPath, `{"id":"lesson-con829","kind":"lesson"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		f.git(path, "add", knowledgeRecordTreeDir)
		f.git(path, "commit", "-m", "lesson")
		// Pushed, not merged: the live head is durable by remote
		// reachability, so the lesson gate is the refusal that fires.
		f.git(f.repoRoot, "push", "-q", "origin", correctionBranch)
		req := f.reclaimRequest("work-lesson", "con829-lesson")
		_, err := f.s.ReclaimWorktree(context.Background(), req)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindWorktreeUnpublishedLesson {
			t.Fatalf("live unpublished lesson must refuse typed, got %v", err)
		}
		if !strings.Contains(failure.Detail, correctionBranch) || !strings.Contains(failure.Detail, "lesson-con829.json") {
			t.Fatalf("refusal must name the live branch and the record: %q", failure.Detail)
		}
		if f.worktreeGone(path) {
			t.Fatal("refused worktree must remain")
		}
	})

	t.Run("dirty correction refuses", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, _, _, _ := f.correctionScenario("work-dirty")
		if err := writeFile(filepath.Join(path, "tracked.txt"), "dirty\n"); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-dirty", "con829-dirty"))
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "dirty") {
			t.Fatalf("dirty correction checkout must refuse typed, got %v", err)
		}
		if f.worktreeGone(path) {
			t.Fatal("dirty worktree must remain")
		}
	})

	t.Run("live occupant refuses", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, _, _, _ := f.correctionScenario("work-occupied")
		f.completeWork("work-occupied")
		writeProtectingHostLease(f.t, f.s)
		setWorktreeOccupant(f.t, f.s, "work-occupied", "ses-con829")
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-occupied", "con829-occupied"))
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindWorktreeOwnershipConflict {
			t.Fatalf("occupied correction checkout must refuse typed, got %v", err)
		}
		if f.worktreeGone(path) {
			t.Fatal("occupied worktree must remain")
		}
	})
}

// TestReclaimProtectsUnprovenLiveHead pins CD-0212 D2: a durable stored
// branch is not proof that a different or detached live HEAD is durable. The
// gates read the live head, so an unsafe live branch and an unproven detached
// tip refuse while the stored claim branch is fully durable, and a detached
// tip the default ref holds reclaims.
func TestReclaimProtectsUnprovenLiveHead(t *testing.T) {
	assertRefusedNamingLiveHead := func(t *testing.T, f *con829Fixture, workID, path, wantBranch string) {
		t.Helper()
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "con829-"+workID))
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("unproven live head must refuse typed, got %v", err)
		}
		if !strings.Contains(failure.Detail, wantBranch) {
			t.Fatalf("refusal must name the live head %s: %q", wantBranch, failure.Detail)
		}
		if !strings.Contains(failure.Detail, "not reachable from remote refs") {
			t.Fatalf("refusal must keep the durable-count reason: %q", failure.Detail)
		}
		if f.worktreeGone(path) {
			t.Fatal("refused worktree must remain")
		}
	}

	t.Run("unsafe live branch", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-unsafe-live")
		// The stored claim branch is fully durable on the remote.
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		// The live checkout moved to a branch holding unique unmerged commits.
		f.git(path, "checkout", "-q", "-b", "unproven/work-unsafe-live", "origin/main")
		if err := writeFile(filepath.Join(path, "unique.txt"), "unique\n"); err != nil {
			t.Fatal(err)
		}
		f.git(path, "add", "unique.txt")
		f.git(path, "commit", "-m", "unique")
		assertRefusedNamingLiveHead(t, f, "work-unsafe-live", path, "unproven/work-unsafe-live")
		if !f.refExists(claimBranch) {
			t.Fatalf("durable stored branch %s must survive the refusal", claimBranch)
		}
	})

	t.Run("detached unproven head", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-detached-unproven")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		if err := writeFile(filepath.Join(path, "detached.txt"), "detached\n"); err != nil {
			t.Fatal(err)
		}
		f.git(path, "add", "detached.txt")
		f.git(path, "commit", "-m", "detached unique")
		assertRefusedNamingLiveHead(t, f, "work-detached-unproven", path, "detached")
	})

	t.Run("detached durable head reclaims", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-detached-durable")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-detached-durable", "con829-detached-durable")); err != nil {
			t.Fatalf("detached head the default ref holds must reclaim, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		facts := f.entryFacts("work-detached-durable")
		if facts.LiveHead == nil || !facts.LiveHead.Detached || facts.LiveHead.Tip != f.gitOut(f.repoRoot, "rev-parse", "origin/main") {
			t.Fatalf("facts must record the detached live head and tip: %+v", facts.LiveHead)
		}
		if f.refExists(claimBranch) {
			t.Fatalf("durable stored branch %s must be deleted with the removal", claimBranch)
		}
	})
}

// TestReclaimRetainsAndReportsUnprovenStoredRef pins CD-0212 D3: an unproven
// stored claim ref survives the reclaim of a proven-durable live checkout,
// with branch, tip, and reason recorded durably and visible to the audit
// after directory removal, and a missing stored ref neither blocks the
// removal nor owes a deletion.
func TestReclaimRetainsAndReportsUnprovenStoredRef(t *testing.T) {
	t.Run("unproven stored ref survives and is reported", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch, correctionBranch, claimTip := f.correctionScenario("work-retain")
		f.completeWork("work-retain")
		result, err := f.s.WorktreeAuditReclaim(context.Background(), WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "con829-retain", Now: time.Unix(40, 0).UTC(), Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var row *WorktreeAuditReclaimRow
		for i := range result.Rows {
			if result.Rows[i].WorkID == "work-retain" {
				row = &result.Rows[i]
			}
		}
		if row == nil || row.Outcome != WorktreeAuditReclaimed {
			t.Fatalf("retention must not block the durable live removal: %+v", row)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		if !f.refExists(claimBranch) || f.refTip(claimBranch) != claimTip {
			t.Fatalf("unproven stored ref %s must survive at %s", claimBranch, claimTip)
		}
		if !f.refExists(correctionBranch) {
			t.Fatalf("divergent live branch %s must survive", correctionBranch)
		}
		// Visible after directory removal: the durable entry facts keep the
		// retention, and the pass row reported it at reclaim time.
		facts := f.entryFacts("work-retain")
		var retained *con829RetainedRef
		for i := range facts.RetainedRefs {
			if facts.RetainedRefs[i].Branch == claimBranch {
				retained = &facts.RetainedRefs[i]
			}
		}
		if retained == nil || retained.Tip != claimTip || !strings.Contains(retained.Reason, "durable") {
			t.Fatalf("retention must record branch, tip, and durability reason: %+v", retained)
		}
		if row.RetainedRefs == nil {
			t.Fatalf("reclaimed row must report retained refs: %+v", row)
		}
		for _, ref := range row.RetainedRefs {
			if ref.Branch == claimBranch && ref.Tip == claimTip && ref.Reason != "" {
				return
			}
		}
		t.Fatalf("reclaimed row must report the retained stored ref: %+v", row.RetainedRefs)
	})

	t.Run("missing stored ref neither blocks nor owes", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch, correctionBranch, _ := f.correctionScenario("work-missing")
		f.git(f.repoRoot, "branch", "-D", claimBranch)
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-missing", "con829-missing")); err != nil {
			t.Fatalf("a missing stored ref must not block the durable live removal, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		facts := f.entryFacts("work-missing")
		if !facts.StoredRefAbsent || facts.StoredBranch != claimBranch {
			t.Fatalf("facts must record the absent stored ref: %+v", facts)
		}
		if !f.refExists(correctionBranch) {
			t.Fatalf("divergent live branch %s must survive", correctionBranch)
		}
	})

	// The coordinator's fresh-audit probe, kept as a permanent regression:
	// reporting a retention on the reclaim response is not visibility. A
	// later audit read of the reclaimed entry must still report the retained
	// ref, so the operator who arrives after the directory is gone sees the
	// branch that survived and why.
	t.Run("retention stays visible on a fresh audit", func(t *testing.T) {
		f := con829FixtureNew(t)
		_, claimBranch, _, claimTip := f.correctionScenario("work-audit-retention")
		f.completeWork("work-audit-retention")
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-audit-retention", "con829-audit-retention")); err != nil {
			t.Fatal(err)
		}
		audit, err := f.s.WorktreeAudit(context.Background(), WorktreeAuditRequest{ProductID: "product-w", DefaultRef: "origin/main", Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var row *WorktreeDrift
		for i := range audit.Drift {
			if audit.Drift[i].Class == WorktreeDriftRetainedRef && audit.Drift[i].RetainedBranch == claimBranch {
				row = &audit.Drift[i]
			}
		}
		if row == nil {
			t.Fatalf("fresh audit must report the retained ref %s: %+v", claimBranch, audit.Drift)
		}
		if row.RetainedTip != claimTip || row.Risk == "" || row.RecoveryAction != WorktreeRecoveryInspect || row.WorkID != "work-audit-retention" {
			t.Fatalf("retained-ref row must carry tip, reason, and the inspect action: %+v", row)
		}
		// A reclaim pass reports the retention and never attempts it.
		pass, err := f.s.WorktreeAuditReclaim(context.Background(), WorktreeAuditReclaimRequest{ProductID: "product-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "con829-audit-again", Now: time.Unix(50, 0).UTC(), Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var reported bool
		for _, report := range pass.ReportOnly {
			if report.Class == WorktreeDriftRetainedRef && report.RetainedBranch == claimBranch {
				reported = true
			}
		}
		if !reported {
			t.Fatalf("reclaim pass must carry the retained ref report-only: %+v", pass.ReportOnly)
		}
		for _, r := range pass.Rows {
			if r.Outcome != WorktreeAuditRefused {
				t.Fatalf("a retained ref must not be reclaimed: %+v", r)
			}
		}
	})
}

// con829FailingGitRunner fails exactly the git verbs whose joined arguments
// start with failPrefix, so a test can interrupt one native removal step.
type con829FailingGitRunner struct {
	failPrefix string
}

func (r con829FailingGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if strings.HasPrefix(strings.Join(args, " "), r.failPrefix) {
		return nil, fmt.Errorf("injected failure: %s", r.failPrefix)
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func (r con829FailingGitRunner) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	if strings.HasPrefix(strings.Join(args, " "), r.failPrefix) {
		return nil, fmt.Errorf("injected failure: %s", r.failPrefix)
	}
	return (ExecGitRunner{}).RunStdin(ctx, dir, stdin, args...)
}

// con829LateMoveRunner moves a branch between the plan's probe and the
// pinned argv deletion boundary: the instant the boundary runs, the ref is
// already elsewhere. The pinned old-value check must refuse it.
type con829LateMoveRunner struct {
	branch string
	fired  bool
}

func (r *con829LateMoveRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if !r.fired && len(args) > 0 && args[0] == "update-ref" {
		for _, arg := range args {
			if arg == "-d" {
				r.fired = true
				if _, err := (ExecGitRunner{}).Run(ctx, dir, "branch", "-f", r.branch, "main"); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func (r *con829LateMoveRunner) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	return (ExecGitRunner{}).RunStdin(ctx, dir, stdin, args...)
}

// TestReclaimNativePlanProtectsChangedTipsAndConverges pins CD-0212 D4: the
// bounded native-removal plan pins the tip of every branch deletion it owes,
// never deletes a ref whose tip changed or the default ref, and a pending
// deletion survives directory removal so a retry converges it.
func TestReclaimNativePlanProtectsChangedTipsAndConverges(t *testing.T) {
	t.Run("changed tip is protected", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-moved-tip")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		pinnedTip := f.refTip(claimBranch)
		// Detach the live head so the stored branch is free to move.
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		req := f.reclaimRequest("work-moved-tip", "con829-moved-tip")
		probe, err := f.s.PrepareWorktreeReclaim(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var removal *WorktreeNativeRemoval
		if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
			result, err := ReclaimWorktreeTx(context.Background(), tx, req, probe)
			if err != nil {
				return err
			}
			removal = result.Removal
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// The stored branch moves between the committed probe and the native
		// removal: the plan must refuse to delete it.
		if err := writeFile(filepath.Join(f.repoRoot, "mover.txt"), "moved\n"); err != nil {
			t.Fatal(err)
		}
		f.git(f.repoRoot, "add", "mover.txt")
		f.git(f.repoRoot, "commit", "-m", "move the stored branch")
		f.git(f.repoRoot, "branch", "-f", claimBranch, "main")
		if _, err := RunWorktreeNativeRemoval(context.Background(), nil, removal); err == nil {
			t.Fatal("a moved branch tip must refuse deletion")
		} else {
			var failure *Failure
			if !errors.As(err, &failure) || !strings.Contains(failure.Detail, claimBranch) {
				t.Fatalf("protection error must name the retained branch: %v", err)
			}
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		if !f.refExists(claimBranch) || f.refTip(claimBranch) == pinnedTip {
			t.Fatalf("moved ref %s must survive at its new tip", claimBranch)
		}
		// A retry reproduces the protection instead of silently converging.
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-moved-tip", "con829-moved-tip-retry")); err == nil {
			t.Fatal("retry must keep protecting the moved ref")
		} else if !strings.Contains(err.Error(), claimBranch) {
			t.Fatalf("retry protection must name the retained branch: %v", err)
		}
		if !f.refExists(claimBranch) {
			t.Fatalf("moved ref %s must still survive the retry", claimBranch)
		}
	})

	t.Run("default ref is never deleted", func(t *testing.T) {
		f := con829FixtureNew(t)
		workID := "work-default-ref"
		ctx := context.Background()
		if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{
			{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Con829 default ref","priority":1}`)},
			{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
			t.Fatal(err)
		}
		// A drifted stored claim whose branch is the Project's default ref.
		path := filepath.Join(f.workRoot, "project-w", workID)
		f.git(f.repoRoot, "worktree", "add", "--detach", path, "main")
		base := f.gitOut(f.repoRoot, "rev-parse", "HEAD")
		payload := fmt.Sprintf(`{"expected_version":2,"resulting_version":3,"set_id":%q,"project_id":"project-w","claim_op_id":"op-default-ref","branch":"main","base_sha":%q,"path":%q,"repository_id":%q,"git_facts":{}}`, WorktreeSetID(workID), base, path, f.repoRoot)
		if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{{EventID: workID + "-created", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
			t.Fatal(err)
		}
		req := f.reclaimRequest(workID, "con829-default-ref")
		req.ExpectedVersion = 3
		if _, err := f.s.ReclaimWorktree(context.Background(), req); err != nil {
			t.Fatalf("durable live checkout must reclaim, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		if !f.refExists("main") {
			t.Fatal("the default ref must never be deleted")
		}
		facts := f.entryFacts(workID)
		var retainedDefault bool
		for _, ref := range facts.RetainedRefs {
			if ref.Branch == "main" && ref.Reason != "" {
				retainedDefault = true
			}
		}
		if !retainedDefault {
			t.Fatalf("default-ref retention must be recorded: %+v", facts.RetainedRefs)
		}
	})

	t.Run("pending deletion converges after directory removal", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-pending")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		pinnedTip := f.refTip(claimBranch)
		req := f.reclaimRequest("work-pending", "con829-pending")
		probe, err := f.s.PrepareWorktreeReclaim(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var removal *WorktreeNativeRemoval
		if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
			result, err := ReclaimWorktreeTx(context.Background(), tx, req, probe)
			if err != nil {
				return err
			}
			removal = result.Removal
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// The directory removal lands, the branch deletion fails: the retry
		// must still finish the pending deletion instead of losing it.
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "update-ref"}, removal); err == nil {
			t.Fatal("injected branch deletion failure must surface")
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		if !f.refExists(claimBranch) {
			t.Fatalf("branch %s must survive the failed deletion", claimBranch)
		}
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-pending", "con829-pending-retry")); err != nil {
			t.Fatalf("retry must converge the pending branch deletion, got %v", err)
		}
		if f.refExists(claimBranch) {
			t.Fatalf("pending deletion of %s at %s must converge", claimBranch, pinnedTip)
		}
	})

	// The coordinator's atomicity probe, kept as a permanent regression: a
	// ref that moves at the deletion boundary itself — after every check,
	// inside the runner that executes the mutation — is refused by the
	// pinned transaction, because the old-value check and the deletion are
	// one git-internal transaction no wrapper can split.
	t.Run("late move at the deletion boundary is refused", func(t *testing.T) {
		f := con829FixtureNew(t)
		_, claimBranch := f.claimWork("work-late-move")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		req := f.reclaimRequest("work-late-move", "con829-late-move")
		probe, err := f.s.PrepareWorktreeReclaim(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var removal *WorktreeNativeRemoval
		if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
			result, err := ReclaimWorktreeTx(context.Background(), tx, req, probe)
			if err != nil {
				return err
			}
			removal = result.Removal
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// main advances after the commit, so the runner's move lands the ref
		// on a tip the plan never pinned.
		if err := writeFile(filepath.Join(f.repoRoot, "late-move.txt"), "unproven content\n"); err != nil {
			t.Fatal(err)
		}
		f.git(f.repoRoot, "add", "late-move.txt")
		f.git(f.repoRoot, "commit", "-m", "late ref update")
		_, err = RunWorktreeNativeRemoval(context.Background(), &con829LateMoveRunner{branch: claimBranch}, removal)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict || !strings.Contains(failure.Detail, claimBranch) {
			t.Fatalf("the pinned transaction must refuse the moved ref by name, got %v", err)
		}
		if !f.refExists(claimBranch) {
			t.Fatalf("ref %s moved at the deletion boundary and was deleted anyway", claimBranch)
		}
		if f.refTip(claimBranch) != f.refTip("main") {
			t.Fatalf("the moved ref must survive at its new tip, not be reset: %s", f.refTip(claimBranch))
		}
	})

	// git's own ownership protection for checked-out refs survives the move
	// from `branch -D` to the pinned transaction: a branch another worktree
	// holds checked out is retained, never stranded by a dangling HEAD.
	t.Run("checked-out branch is never deleted", func(t *testing.T) {
		f := con829FixtureNew(t)
		_, claimBranch := f.claimWork("work-checkedout")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		// The live head detaches first, so the plan owes the stored ref's
		// deletion and the branch is free for a hand-made second worktree to
		// check out.
		path := f.worktreePath("work-checkedout")
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		other := filepath.Join(f.t.TempDir(), "other-worktree")
		f.git(f.repoRoot, "worktree", "add", "--checkout", other, claimBranch)
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-checkedout", "con829-checkedout"))
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "checked out") {
			t.Fatalf("a checked-out branch must refuse deletion naming the checkout, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed", path)
		}
		if !f.refExists(claimBranch) {
			t.Fatalf("checked-out branch %s must survive the removal", claimBranch)
		}
		f.git(f.repoRoot, "worktree", "remove", other)
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-checkedout", "con829-checkedout-retry")); err != nil {
			t.Fatalf("retry after the checkout releases must converge the deletion, got %v", err)
		}
		if f.refExists(claimBranch) {
			t.Fatalf("branch %s must converge once no worktree holds it", claimBranch)
		}
	})

	// A committed reclaim whose directory phase failed retries under its
	// recorded plan: the retry re-owes only the removal, keeps the original
	// pinned deletions instead of re-planning, and appends no new event.
	t.Run("directory phase failure retries under the recorded plan", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, claimBranch := f.claimWork("work-dirfail")
		f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
		req := f.reclaimRequest("work-dirfail", "con829-dirfail")
		probe, err := f.s.PrepareWorktreeReclaim(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var removal *WorktreeNativeRemoval
		if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
			result, err := ReclaimWorktreeTx(context.Background(), tx, req, probe)
			if err != nil {
				return err
			}
			removal = result.Removal
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// The directory removal itself fails: the worktree stays, the
		// deletions never run, and the plan stays recorded.
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
			t.Fatal("injected directory removal failure must surface")
		}
		if f.worktreeGone(path) {
			t.Fatal("the failed directory phase must leave the worktree in place")
		}
		if !f.refExists(claimBranch) {
			t.Fatalf("branch %s must survive the failed removal", claimBranch)
		}
		version, err := currentWorkVersion(context.Background(), f.s.db, "work-dirfail")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-dirfail", "con829-dirfail-retry")); err != nil {
			t.Fatalf("retry must finish the recorded removal plan, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatalf("worktree at %s was not removed by the retry", path)
		}
		if f.refExists(claimBranch) {
			t.Fatalf("recorded deletion of %s must converge on the retry", claimBranch)
		}
		after, err := currentWorkVersion(context.Background(), f.s.db, "work-dirfail")
		if err != nil {
			t.Fatal(err)
		}
		if after != version {
			t.Fatalf("the converged retry appends no new event: version %d -> %d", version, after)
		}
		facts := f.entryFacts("work-dirfail")
		if len(facts.BranchDeletions) == 0 || facts.BranchDeletions[0].Branch != claimBranch {
			t.Fatalf("the recorded plan must keep its pinned deletions: %+v", facts.BranchDeletions)
		}
	})

	// The agent surface probes before the approval tail supplies the
	// consumed reference; the committed forced facts must still name that
	// reference, because the store composes them at the append boundary.
	t.Run("committed forced facts carry the consumed approval", func(t *testing.T) {
		f := con829FixtureNew(t)
		f.claimWork("work-approval-facts")
		f.completeWork("work-approval-facts")
		req := f.reclaimRequest("work-approval-facts", "con829-approval")
		req.RequireTerminal = true
		req.Destructive = true
		preflight := req
		probe, err := f.s.PrepareWorktreeReclaim(context.Background(), preflight)
		if err != nil {
			t.Fatal(err)
		}
		req.OperatorApprovalRef = "concord_approval:con829"
		var result WorktreeReclaimResult
		if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
			var err error
			result, err = DestroyWorktreeTx(context.Background(), tx, req, probe)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := RunWorktreeNativeRemoval(context.Background(), nil, result.Removal); err != nil {
			t.Fatal(err)
		}
		var facts struct {
			Forced           bool   `json:"forced"`
			OperatorOverride string `json:"operator_override"`
			LiveHead         *struct {
				Tip string `json:"tip"`
			} `json:"live_head"`
		}
		if err := json.Unmarshal(result.Entry.GitFacts, &facts); err != nil {
			t.Fatal(err)
		}
		if !facts.Forced || facts.OperatorOverride != "concord_approval:con829" {
			t.Fatalf("committed forced facts must name the consumed approval: %+v", facts)
		}
		if facts.LiveHead == nil || facts.LiveHead.Tip == "" {
			t.Fatalf("forced facts must keep the live-head observation: %+v", facts)
		}
		if !f.worktreeGone(f.worktreePath("work-approval-facts")) {
			t.Fatal("the destructive removal must remove the worktree")
		}
	})
}
