package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// CD-0212 D3-D4 ref-boundary coverage from the CON-829 independent review.
// The three destructive findings share one owner — the native ref-deletion
// boundary — so these tests pin the boundary itself, not the plan-time
// observations: the protected-default ownership is re-derived at the deletion,
// an unestablished default set fails closed instead of granting deletion, a
// checkout that appears at the mutation boundary is detected and restored at
// its pinned tip, and the retained-ref projection admits any branch name a
// live Linux checkout can hold, so a valid long live branch never blocks a
// proven-durable removal.

// con829UpdateRefHookRunner runs real git but fires hook immediately before
// the pinned argv delete at the native ref-deletion boundary: the exact
// boundary a concurrent actor (a runner-level wrapper, a hook, another
// agent) sits on between the plan's observations and git's own mutation.
type con829UpdateRefHookRunner struct {
	fired bool
	hook  func()
}

func (r *con829UpdateRefHookRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if !r.fired && len(args) > 0 && args[0] == "update-ref" {
		for _, arg := range args {
			if arg == "-d" {
				r.fired = true
				r.hook()
				break
			}
		}
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func (r *con829UpdateRefHookRunner) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	return (ExecGitRunner{}).RunStdin(ctx, dir, stdin, args...)
}

// TestReclaimRevalidatesDefaultOwnershipAtNativeDeletion pins review finding
// 1: a plan that proved a branch deletable stays honest when the repository's
// default moves onto that branch after the plan committed. The directory phase
// completes independently, the ref is retained with a durable protection the
// audit reports, and a replay converges the pinned deletion once the default
// no longer names the branch — the protection is re-derived, never sticky.
func TestReclaimRevalidatesDefaultOwnershipAtNativeDeletion(t *testing.T) {
	f := con829FixtureNew(t)
	path, branch := f.claimWork("work-boundary-default")
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest("work-boundary-default", "boundary-default"))
	// The default moves onto the planned branch after the plan committed: the
	// tip is unchanged, so only ownership revalidation can catch it.
	f.git(f.repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), nil, removal)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("the deletion of the repository's default ref must be refused as a protection, got %v", err)
	}
	if !f.worktreeGone(path) {
		t.Fatal("the durable directory phase must complete independently of the ref protection")
	}
	if !f.refExists(branch) {
		t.Fatal("the repository default ref was deleted after origin/HEAD moved onto it")
	}
	row := retainedRefRow(f.auditDrift(t), branch)
	if row == nil {
		t.Fatal("the default protection must stay visible as a retained_ref drift row after directory removal")
	}
	if !strings.Contains(row.Risk, "default") {
		t.Fatalf("the protection must name the default ownership it enforced, got %q", row.Risk)
	}
	// The protection is re-derived, not sticky: once the default no longer
	// names the branch, the recorded plan's pinned deletion converges.
	f.git(f.repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-boundary-default", "boundary-default-retry")); err != nil {
		t.Fatalf("the retry must converge the pinned deletion once the default moved away: %v", err)
	}
	if f.refExists(branch) {
		t.Fatal("the pinned deletion must converge once the default no longer protects the branch")
	}
	if retainedRefRow(f.auditDrift(t), branch) != nil {
		t.Fatal("a converged deletion leaves no retained_ref drift row")
	}
}

// TestReclaimFailsClosedWhenNoDefaultIsEstablished pins review finding 2: an
// empty protected set that no observation established is unknown, not
// established-empty. With no caller default, a detached primary HEAD, and a
// missing origin/HEAD, deletion permission fails closed — even under a
// consumed operator approval — while the approved directory removal proceeds
// and the retained ref stays visible to the audit.
func TestReclaimFailsClosedWhenNoDefaultIsEstablished(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-boundary-no-default"
	path := f.worktreePath(workID)
	f.git(f.repoRoot, "worktree", "add", "--detach", path, "main")
	f.git(f.repoRoot, "checkout", "-q", "--detach", "main")
	f.git(f.repoRoot, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	tip := f.gitOut(f.repoRoot, "rev-parse", "HEAD")
	f.recordDriftedEntry(t, workID, path, "main", tip)
	req := f.reclaimRequest(workID, "boundary-no-default")
	req.DefaultRef = ""
	req.RequireTerminal = true
	req.Destructive = true
	req.OperatorApprovalRef = "concord_approval:synthetic-boundary-no-default"
	entry, err := f.s.ReclaimWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("the approved directory removal must not be blocked by the unknown default: %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("the reclamation must commit, got state %q", entry.State)
	}
	if !f.worktreeGone(path) {
		t.Fatal("the approved directory removal must proceed")
	}
	if !f.refExists("main") {
		t.Fatal("main was deleted although no protected default could be established")
	}
	facts := f.entryFacts(workID)
	retained := false
	for _, ref := range facts.RetainedRefs {
		if ref.Branch == "main" && ref.Tip == tip {
			retained = true
		}
	}
	if !retained {
		t.Fatalf("the retained default must be recorded with its branch and tip, got %+v", facts.RetainedRefs)
	}
	row := retainedRefRow(f.auditDrift(t), "main")
	if row == nil || !strings.Contains(row.Risk, "default") {
		t.Fatalf("the retained default must stay visible to the audit naming the unestablished default, got %+v", row)
	}
}

// TestReclaimProtectsCheckoutRaceAtDeletionBoundary pins review finding 3: a
// branch checked out nowhere when the plan committed can gain a checkout at
// the mutation boundary itself. The pinned transaction still deletes the ref
// (update-ref enforces no checkout guard), so the boundary must verify
// ownership after the transaction and restore the ref at its pinned tip,
// leaving the other worktree whole, the protection durable and visible, and
// the pinned deletion convergent once the other worktree moves off.
func TestReclaimProtectsCheckoutRaceAtDeletionBoundary(t *testing.T) {
	f := con829FixtureNew(t)
	path, branch := f.claimWork("work-boundary-checkout")
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest("work-boundary-checkout", "boundary-checkout"))
	other := filepath.Join(t.TempDir(), "other-checkout")
	runner := &con829UpdateRefHookRunner{hook: func() {
		f.git(f.repoRoot, "worktree", "add", other, branch)
	}}
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("the deletion of a branch a second worktree just checked out must be refused as a protection, got %v", err)
	}
	if !f.worktreeGone(path) {
		t.Fatal("the first directory removal must complete")
	}
	if !f.refExists(branch) {
		t.Fatal("the branch was deleted while a second worktree held it at the deletion boundary")
	}
	pinned := removal.BranchDeletions[0].ExpectedTip
	if tip := f.refTip(branch); tip != pinned {
		t.Fatalf("the raced branch must be restored at its pinned tip: got %s want %s", tip, pinned)
	}
	if tip := f.gitOut(other, "rev-parse", "HEAD"); tip != pinned {
		t.Fatalf("the second worktree's HEAD must be whole again after the restore, got %s", tip)
	}
	if out := f.gitOut(other, "status", "--porcelain=v1", "-b"); !strings.HasPrefix(out, "## "+branch) {
		t.Fatalf("the second worktree must hold the branch checked out after the restore, got %q", out)
	}
	row := retainedRefRow(f.auditDrift(t), branch)
	if row == nil || !strings.Contains(row.Risk, "checked out") {
		t.Fatalf("the checkout protection must stay visible to the audit, got %+v", row)
	}
	// The checkout protection is re-derived: once the other worktree moves
	// off the branch, the recorded plan's pinned deletion converges.
	f.git(other, "checkout", "-q", "--detach")
	if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-boundary-checkout", "boundary-checkout-retry")); err != nil {
		t.Fatalf("the retry must converge the pinned deletion once no worktree holds the branch: %v", err)
	}
	if f.refExists(branch) {
		t.Fatal("the pinned deletion must converge once no worktree holds the branch")
	}
}

// TestReclaimRecordsLongLiveBranchRetention pins review finding 4: a live
// checkout Git itself accepts — a correction branch whose name outruns the
// claim-branch bound — must not fail the reclamation fold. The full retained
// identity is recorded durably and reported to the audit.
func TestReclaimRecordsLongLiveBranchRetention(t *testing.T) {
	f := con829FixtureNew(t)
	path, _ := f.claimWork("work-boundary-long-live")
	longBranch := "correction/" + strings.Repeat("a", 130)
	f.git(path, "checkout", "-q", "-b", longBranch)
	tip := f.gitOut(path, "rev-parse", "HEAD")
	entry, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-boundary-long-live", "boundary-long-live"))
	if err != nil {
		t.Fatalf("a durable checkout holding a valid long live branch must reclaim: %v", err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("the reclamation must commit, got state %q", entry.State)
	}
	if !f.worktreeGone(path) {
		t.Fatal("the proven-durable directory removal must complete")
	}
	if !f.refExists(longBranch) {
		t.Fatal("the divergent live branch must survive the reclaim")
	}
	facts := f.entryFacts("work-boundary-long-live")
	retained := false
	for _, ref := range facts.RetainedRefs {
		if ref.Branch == longBranch && ref.Tip == tip {
			retained = true
		}
	}
	if !retained {
		t.Fatalf("the full long live branch identity must be recorded in the reclamation facts, got %+v", facts.RetainedRefs)
	}
	row := retainedRefRow(f.auditDrift(t), longBranch)
	if row == nil || row.RetainedBranch != longBranch || row.RetainedTip != tip {
		t.Fatalf("the full long live branch identity must stay visible to the audit, got %+v", row)
	}
}
