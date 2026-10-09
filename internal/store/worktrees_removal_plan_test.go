package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CD-0212 D4 replay coverage. One durable bounded plan, decoded from the
// committed facts, drives the first native execution and every replay: the
// retry never repins the live HEAD, never rebuilds force or deletion
// authority from its own arguments, and the durable per-ref outcomes keep
// retentions, protections, and deletion debt visible after directory removal
// and after a later claim. Every case runs against real git.

// con829CommitRemoval commits one reclaim through the agent-shaped
// transaction path and returns the native removal its event recorded, with
// no native effect run yet.
func con829CommitRemoval(t *testing.T, f *con829Fixture, req WorktreeReclaimRequest) *WorktreeNativeRemoval {
	t.Helper()
	probe, err := f.s.PrepareWorktreeReclaim(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var removal *WorktreeNativeRemoval
	if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
		result, err := ReclaimWorktreeTx(context.Background(), tx, req, probe)
		removal = result.Removal
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return removal
}

// auditDrift reads one fresh audit pass over the fixture's Product.
func (f *con829Fixture) auditDrift(t *testing.T) []WorktreeDrift {
	t.Helper()
	audit, err := f.s.WorktreeAudit(context.Background(), WorktreeAuditRequest{ProductID: "product-w", DefaultRef: "origin/main", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return audit.Drift
}

// retainedRefRow finds the retained_ref drift row naming one branch.
func retainedRefRow(drift []WorktreeDrift, branch string) *WorktreeDrift {
	for i := range drift {
		if drift[i].Class == WorktreeDriftRetainedRef && drift[i].RetainedBranch == branch {
			return &drift[i]
		}
	}
	return nil
}

// containsAll reports whether s contains every part.
func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

// recordDriftedEntry writes one hand-made worktree_created event whose claim
// names a drifted branch, so a test can pin protection for a stored claim
// the real claim surface would never record.
func (f *con829Fixture) recordDriftedEntry(t *testing.T, workID, path, branch, base string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"drifted ` + workID + `","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"expected_version":2,"resulting_version":3,"set_id":%q,"project_id":"project-w","claim_op_id":"op-%s","branch":%q,"base_sha":%q,"path":%q,"repository_id":%q,"git_facts":{}}`, WorktreeSetID(workID), workID, branch, base, path, f.repoRoot)
	if err := ApplyOperation(ctx, f.s, Operation{Events: []Event{{EventID: workID + "-created", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
}

// TestReclaimReplaysRecordedRemovalPlan pins the original-plan replay and
// the durable ref outcomes: a retry derives its authority from the
// committed plan, refuses content that arrived after it, protects changed
// and symbolic refs without deleting through them, keeps the default ref
// protected under every spelling, replays recorded force, and leaves every
// retention, protection, and debt visible to the audit.
func TestReclaimReplaysRecordedRemovalPlan(t *testing.T) {
	// Review case 1: a retry must not remove a surviving directory whose
	// live HEAD moved to unproven detached content after the committed plan.
	t.Run("retry refuses newly unproven detached content", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-detached")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		removal := con829CommitRemoval(t, f, f.reclaimRequest("work-plan-detached", "plan-detached"))
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
			t.Fatal("expected the injected directory failure to surface")
		}
		f.git(path, "checkout", "-q", "--detach")
		if err := writeFile(filepath.Join(path, "unique.txt"), "unique detached content\n"); err != nil {
			t.Fatal(err)
		}
		f.git(path, "add", "unique.txt")
		f.git(path, "commit", "-m", "unique detached content after the committed plan")
		tip := f.gitOut(path, "rev-parse", "HEAD")
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-detached", "plan-detached-retry"))
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict || !containsAll(failure.Detail, "recorded removal plan", "detached") {
			t.Fatalf("the retry must refuse the changed directory naming the plan and the head, got %v", err)
		}
		if f.worktreeGone(path) {
			t.Fatalf("the retry removed the surviving worktree holding unproven detached HEAD %s", tip)
		}
		if !f.refExists(branch) {
			t.Fatalf("the refused retry must leave the durable branch %s in place", branch)
		}
	})

	// The same boundary for a branch change: the recorded identity names one
	// branch, and the surviving checkout names another.
	t.Run("retry refuses a changed branch after a failed directory phase", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-branch-change")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		removal := con829CommitRemoval(t, f, f.reclaimRequest("work-plan-branch-change", "plan-branch"))
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
			t.Fatal("expected the injected directory failure to surface")
		}
		f.git(path, "checkout", "-q", "-b", "moved/after-plan")
		if err := writeFile(filepath.Join(path, "moved.txt"), "moved content\n"); err != nil {
			t.Fatal(err)
		}
		f.git(path, "add", "moved.txt")
		f.git(path, "commit", "-m", "moved after the committed plan")
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-branch-change", "plan-branch-retry")); err == nil {
			t.Fatal("the retry must refuse a worktree whose live branch changed after the recorded plan")
		}
		if f.worktreeGone(path) {
			t.Fatal("the refused retry must leave the worktree in place")
		}
	})

	// Review case 2: a claim ref that became symbolic must never redirect
	// the pinned deletion onto its foreign target.
	t.Run("symbolic claim ref cannot delete a foreign target", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-symbolic")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		tip := f.refTip(branch)
		removal := con829CommitRemoval(t, f, f.reclaimRequest("work-plan-symbolic", "plan-symbolic"))
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "update-ref"}, removal); err == nil {
			t.Fatal("expected the injected deletion failure to surface")
		}
		if !f.worktreeGone(path) {
			t.Fatal("the directory phase must have completed")
		}
		f.git(f.repoRoot, "branch", "foreign/shared", tip)
		f.git(f.repoRoot, "symbolic-ref", "refs/heads/"+branch, "refs/heads/foreign/shared")
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-symbolic", "plan-symbolic-retry"))
		if !f.refExists("foreign/shared") {
			t.Fatalf("the pinned deletion followed the symbolic claim ref and deleted foreign/shared; err=%v", err)
		}
		if !f.refExists(branch) {
			t.Fatalf("the symbolic claim ref %s itself must survive the protection", branch)
		}
		row := retainedRefRow(f.auditDrift(t), branch)
		if row == nil || !containsAll(row.Risk, "symbolic") {
			t.Fatalf("the protection must stay visible to the audit: %+v", row)
		}
	})

	// Review case 3: the default ref stays protected under the full
	// refs/heads spelling of the merge target, with the primary checkout
	// detached so no other default source answers for the caller.
	t.Run("default ref is protected under every accepted spelling", func(t *testing.T) {
		f := con829FixtureNew(t)
		workID := "work-plan-default-spelling"
		path := f.worktreePath(workID)
		f.git(f.repoRoot, "worktree", "add", "--detach", path, "main")
		f.git(f.repoRoot, "checkout", "-q", "--detach", "main")
		base := f.gitOut(f.repoRoot, "rev-parse", "HEAD")
		f.recordDriftedEntry(t, workID, path, "main", base)
		req := f.reclaimRequest(workID, "plan-default-spelling")
		req.ExpectedVersion = 3
		req.DefaultRef = "refs/heads/main"
		if _, err := f.s.ReclaimWorktree(context.Background(), req); err != nil {
			t.Fatalf("a durable live checkout must reclaim, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the worktree must be removed")
		}
		if !f.refExists("main") {
			t.Fatal("the default ref must never be deleted under its full spelling")
		}
		facts := f.entryFacts(workID)
		var retainedDefault bool
		for _, ref := range facts.RetainedRefs {
			if ref.Branch == "main" && ref.Reason != "" {
				retainedDefault = true
			}
		}
		if !retainedDefault {
			t.Fatalf("the default-ref retention must be recorded: %+v", facts.RetainedRefs)
		}
	})

	// Fail-closed ownership: a merge target no branch name can hold leaves
	// the protected default unestablished, so the stored ref is retained
	// while the approved destructive directory removal still proceeds.
	t.Run("unestablished default retains the stored ref without blocking", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-unknown-default")
		f.completeWork("work-plan-unknown-default")
		if err := writeFile(filepath.Join(path, "tracked.txt"), "approved dirty content\n"); err != nil {
			t.Fatal(err)
		}
		req := f.reclaimRequest("work-plan-unknown-default", "plan-unknown-default")
		req.RequireTerminal, req.Destructive, req.OperatorApprovalRef, req.DefaultRef = true, true, "concord_approval:plan-unknown", "not a valid ref"
		if _, err := f.s.ReclaimWorktree(context.Background(), req); err != nil {
			t.Fatalf("the approved destructive removal must proceed, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the unestablished default must not block the approved directory removal")
		}
		if !f.refExists(branch) {
			t.Fatalf("the stored ref %s must be retained when the protected default cannot be established", branch)
		}
		facts := f.entryFacts("work-plan-unknown-default")
		var unestablished bool
		for _, ref := range facts.RetainedRefs {
			if ref.Branch == branch && containsAll(ref.Reason, "cannot establish") {
				unestablished = true
			}
		}
		if !unestablished {
			t.Fatalf("the retention must record the unestablished default: %+v", facts.RetainedRefs)
		}
	})

	// A tag that shadows the branch name cannot bend the pinned deletion:
	// the plan resolves the branch by its exact refs/heads path and deletes
	// the branch tip, never the tag's commit.
	t.Run("ambiguous tag cannot bend the pinned deletion", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-tag")
		if err := writeFile(filepath.Join(path, "beyond.txt"), "beyond the default ref\n"); err != nil {
			t.Fatal(err)
		}
		f.git(path, "add", "beyond.txt")
		f.git(path, "commit", "-m", "beyond")
		branchTip := f.gitOut(path, "rev-parse", "HEAD")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		// The tag shadows the branch name and points elsewhere.
		f.git(f.repoRoot, "tag", branch, "main")
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-tag", "plan-tag")); err != nil {
			t.Fatalf("the pushed branch must reclaim with the live head detached at the default tip, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the worktree must be removed")
		}
		if f.refExists(branch) {
			t.Fatalf("the durable branch %s must be deleted at its own tip %s", branch, branchTip)
		}
		facts := f.entryFacts("work-plan-tag")
		if len(facts.BranchDeletions) != 1 || facts.BranchDeletions[0].ExpectedTip != branchTip {
			t.Fatalf("the recorded plan must pin the branch tip %s, not the tag's commit: %+v", branchTip, facts.BranchDeletions)
		}
	})

	// Review case 4: an approved forced removal replays its recorded force
	// and converges.
	t.Run("approved forced removal replays and converges", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, _ := f.claimWork("work-plan-force")
		f.completeWork("work-plan-force")
		if err := writeFile(filepath.Join(path, "tracked.txt"), "approved dirty content\n"); err != nil {
			t.Fatal(err)
		}
		req := f.reclaimRequest("work-plan-force", "plan-force")
		req.RequireTerminal, req.Destructive, req.OperatorApprovalRef = true, true, "concord_approval:plan-force"
		removal := con829CommitRemoval(t, f, req)
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
			t.Fatal("expected the injected directory failure to surface")
		}
		retry := f.reclaimRequest("work-plan-force", "plan-force-retry")
		retry.RequireTerminal, retry.Destructive, retry.OperatorApprovalRef = true, true, "concord_approval:plan-force"
		if _, err := f.s.ReclaimWorktree(context.Background(), retry); err != nil {
			t.Fatalf("the recorded approved forced removal must converge, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the approved directory removal is still pending")
		}
	})

	// The replay grants nothing the plan did not record: a retry declaring
	// destructive authority over a safe plan stays unforced.
	t.Run("replay cannot escalate a safe plan into a forced one", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-noescalate")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		removal := con829CommitRemoval(t, f, f.reclaimRequest("work-plan-noescalate", "plan-noescalate"))
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
			t.Fatal("expected the injected directory failure to surface")
		}
		if err := writeFile(filepath.Join(path, "tracked.txt"), "unapproved dirty content\n"); err != nil {
			t.Fatal(err)
		}
		retry := f.reclaimRequest("work-plan-noescalate", "plan-noescalate-retry")
		retry.RequireTerminal, retry.Destructive, retry.OperatorApprovalRef = true, true, "concord_approval:late"
		if _, err := f.s.ReclaimWorktree(context.Background(), retry); err == nil {
			t.Fatal("a safe recorded plan must not replay as a forced removal")
		}
		if f.worktreeGone(path) {
			t.Fatal("the unforced replay must leave the dirty worktree in place")
		}
		if !f.refExists(branch) {
			t.Fatalf("the durable branch %s must survive the refused replay", branch)
		}
	})

	// Review case 5: a changed-tip protection and an unsettled deletion both
	// stay visible to a fresh audit, before and after the durable
	// settlement.
	t.Run("changed-tip debt stays visible on a fresh audit", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-debt")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		removal := con829CommitRemoval(t, f, f.reclaimRequest("work-plan-debt", "plan-debt"))
		if err := writeFile(filepath.Join(f.repoRoot, "moved.txt"), "unique moved content\n"); err != nil {
			t.Fatal(err)
		}
		f.git(f.repoRoot, "add", "moved.txt")
		f.git(f.repoRoot, "commit", "-m", "unique moved content")
		f.git(f.repoRoot, "branch", "-f", branch, "main")
		if _, err := RunWorktreeNativeRemoval(context.Background(), nil, removal); err == nil {
			t.Fatal("expected the changed-tip protection to surface")
		}
		if !f.worktreeGone(path) {
			t.Fatal("the directory phase must have completed")
		}
		owed := retainedRefRow(f.auditDrift(t), branch)
		if owed == nil || !containsAll(owed.Risk, "owes") {
			t.Fatalf("a fresh audit must report the deletion the plan still owes: %+v", owed)
		}
		// The store retry settles the protection durably, and the audit
		// keeps reporting it with the durable-protection reason.
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-debt", "plan-debt-retry"))
		var failure *Failure
		if !errors.As(err, &failure) || !containsAll(failure.Detail, branch) {
			t.Fatalf("the retry must report the protection naming the branch, got %v", err)
		}
		protected := retainedRefRow(f.auditDrift(t), branch)
		if protected == nil || !containsAll(protected.Risk, "protection is durable") {
			t.Fatalf("the settled protection must stay visible to the audit: %+v", protected)
		}
		if !f.refExists(branch) {
			t.Fatal("the protected ref must survive")
		}
	})

	// A protected ref must not mask an independently safe pending directory:
	// the retry removes the directory the original plan proved durable and
	// reports the protected ref beside it. The stored claim names a pushed
	// branch the checkout never held, so its deletion is planned
	// independently and the ref is free to move while the directory phase
	// keeps failing.
	t.Run("protected ref does not mask a pending directory", func(t *testing.T) {
		f := con829FixtureNew(t)
		workID := "work-plan-mask"
		path := f.worktreePath(workID)
		f.git(f.repoRoot, "worktree", "add", "--detach", path, "main")
		base := f.gitOut(f.repoRoot, "rev-parse", "HEAD")
		stored := "extra/" + workID
		f.git(f.repoRoot, "branch", stored, base)
		f.git(f.repoRoot, "push", "-q", "origin", stored)
		f.recordDriftedEntry(t, workID, path, stored, base)
		removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "plan-mask"))
		if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
			t.Fatal("expected the injected directory failure to surface")
		}
		if err := writeFile(filepath.Join(f.repoRoot, "moved.txt"), "moved content\n"); err != nil {
			t.Fatal(err)
		}
		f.git(f.repoRoot, "add", "moved.txt")
		f.git(f.repoRoot, "commit", "-m", "move the stored ref while the directory fails")
		f.git(f.repoRoot, "branch", "-f", stored, "main")
		_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "plan-mask-retry"))
		var failure *Failure
		if !errors.As(err, &failure) || !containsAll(failure.Detail, stored) {
			t.Fatalf("the retry must report the protected ref naming it, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the protected ref masked the independently safe pending directory")
		}
		if !f.refExists(stored) {
			t.Fatal("the protected ref must survive the directory convergence")
		}
	})

	// Identity protections are sticky: a ref that moved away and returned to
	// its pinned tip stays protected, because the replay never rebuilds
	// deletion authority over the changed identity.
	t.Run("moved-away-and-back tip stays protected", func(t *testing.T) {
		f := con829FixtureNew(t)
		path, branch := f.claimWork("work-plan-back")
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		pinned := f.refTip(branch)
		f.git(path, "checkout", "-q", "--detach", "origin/main")
		con829CommitRemoval(t, f, f.reclaimRequest("work-plan-back", "plan-back"))
		if err := writeFile(filepath.Join(f.repoRoot, "moved.txt"), "moved away\n"); err != nil {
			t.Fatal(err)
		}
		f.git(f.repoRoot, "add", "moved.txt")
		f.git(f.repoRoot, "commit", "-m", "move away")
		f.git(f.repoRoot, "branch", "-f", branch, "main")
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-back", "plan-back-retry")); err == nil {
			t.Fatal("the first retry must report the moved ref")
		}
		if !f.worktreeGone(path) {
			t.Fatal("the first retry must converge the recorded directory")
		}
		// The ref returns to its pinned tip; the protection stays.
		f.git(f.repoRoot, "branch", "-f", branch, pinned)
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-plan-back", "plan-back-again")); err != nil {
			t.Fatalf("the sticky protection must let the reclaim converge, got %v", err)
		}
		if !f.refExists(branch) {
			t.Fatal("a moved-away-and-back ref must stay protected, not deleted at its returned tip")
		}
	})

	// A later claim replaces the entry row, and the earlier generation's
	// retention stays visible: the outcome rows key on the claim that
	// recorded them, not on the entry the fold last wrote.
	t.Run("retention stays visible after a later claim", func(t *testing.T) {
		f := con829FixtureNew(t)
		workID := "work-plan-generation"
		_, claimBranch, correctionBranch, claimTip := f.correctionScenario(workID)
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "plan-generation")); err != nil {
			t.Fatalf("the correction checkout must reclaim, got %v", err)
		}
		if !f.refExists(correctionBranch) || !f.refExists(claimBranch) || f.refTip(claimBranch) != claimTip {
			t.Fatalf("both retained refs must survive: %s %s", claimBranch, correctionBranch)
		}
		// The operator disposes the unproven claim ref by hand, then a later
		// claim replaces the reclaimed entry row.
		f.git(f.repoRoot, "branch", "-D", claimBranch)
		version, err := currentWorkVersion(context.Background(), f.s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		base := f.gitOut(f.repoRoot, "rev-parse", "HEAD")
		if _, err := f.s.ClaimWorktree(context.Background(), WorktreeClaimRequest{
			OpID: "op2-" + workID, WorkID: workID, ProjectID: "project-w", BaseSHA: base,
			PrincipalRef: "principal-1", RequestID: "req2-" + workID, ExpectedVersion: version, Now: time.Unix(60, 0).UTC(),
		}); err != nil {
			t.Fatalf("the later claim must take the freed slot, got %v", err)
		}
		row := retainedRefRow(f.auditDrift(t), correctionBranch)
		if row == nil || row.WorkID != workID || row.RetainedTip == "" || row.Risk == "" {
			t.Fatalf("the earlier generation's retained ref must stay visible after the later claim: %+v", row)
		}
	})
}

// TestRetainedOutcomeKeepsItsReasonWhenThePhasePersistFails pins the same
// contract restorationOwedRun already carries: a retained_unproven record
// whose inline persist fails keeps the bounded outcome — the post-run
// settlement pass re-records it — and carries the persist failure in the
// reason instead of dropping the retention from the run's report behind the
// persist error. The protection signal stays the typed refusal; the persist
// failure stays visible in the outcome the run collected.
func TestRetainedOutcomeKeepsItsReasonWhenThePhasePersistFails(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-retain-persist-fails"
	path, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	pinned := f.refTip(branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "retain-persist-fails"))
	// The claim checkout releases the ref, and the branch drifts off the tip
	// the plan pinned, so the deletion boundary takes the retain path.
	f.git(path, "checkout", "-q", "--detach")
	moved := f.gitOut(f.repoRoot, "commit-tree", pinned+"^{tree}", "-m", "move the pinned tip")
	f.git(f.repoRoot, "update-ref", "refs/heads/"+branch, moved)
	removal.persistPhase = func(outcome WorktreeNativeOutcome) error {
		return errors.New("synthetic phase record unavailable")
	}
	outcomes, runErr := RunWorktreeNativeRemoval(context.Background(), ExecGitRunner{}, removal)
	var failure *Failure
	if !errors.As(runErr, &failure) || failure.Kind != KindProjectionConflict || !strings.Contains(failure.Detail, "moved from the tip the reclaim pinned") {
		t.Fatalf("the moved-tip protection must stay the run's reported error, got %v", runErr)
	}
	if len(outcomes) != 1 {
		t.Fatalf("the run must collect the retained outcome for the post-run settlement, got %+v", outcomes)
	}
	outcome := outcomes[0]
	if outcome.Branch != branch || outcome.Phase != WorktreeRefPhaseRetainedUnproven || outcome.Refusal != WorktreeRefRefusalMovedTip {
		t.Fatalf("the collected outcome must be the moved-tip retention: %+v", outcome)
	}
	if !strings.Contains(outcome.Reason, "the phase record could not be persisted") {
		t.Fatalf("the collected outcome must carry the persist failure in its reason: %q", outcome.Reason)
	}
	if !f.refExists(branch) || f.refTip(branch) != moved {
		t.Fatalf("the retained ref must survive at its moved tip %s", moved)
	}
}
