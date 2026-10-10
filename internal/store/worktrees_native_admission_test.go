package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// CD-0212 D4 native-admission and observation coverage. The four review
// findings share the durable
// settlement owner and the checkout-observation boundary in
// internal/store/worktrees.go: a historical settlement event must never
// authorize a native effect against a different current durable predecessor,
// every listed worktree's symbolic HEAD is observed live around the pinned
// deletion, a malformed successful inventory refuses, and a failed
// pre-deletion observation leaves durable, audit-visible uncertainty. Every
// case runs against real git.

// con829NativeBoundaryRunner runs real git but observes the native removal
// boundary: hook fires immediately before each command and after fires only
// after a successful one, so a test can mutate the repository between the
// post-deletion inventory and the observation that consumes it.
type con829NativeBoundaryRunner struct {
	hook  func(dir string, args []string)
	after func(dir string, args []string, out []byte)
}

func (r *con829NativeBoundaryRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if r.hook != nil {
		r.hook(dir, args)
	}
	out, err := (ExecGitRunner{}).Run(ctx, dir, args...)
	if err == nil && r.after != nil {
		r.after(dir, args, out)
	}
	return out, err
}

// con829IsPinnedDelete reports whether argv is the pinned no-deref deletion.
func con829IsPinnedDelete(args []string) bool {
	return len(args) > 2 && args[0] == "update-ref" && args[1] == "--no-deref" && args[2] == "-d"
}

// TestReclaimReplayRecordsAuthorizationBeforeNativeDeletion pins review
// finding 1's restored-replay half: a replay of a reclamation whose ref a
// first attempt restored must not execute the pinned deletion while the
// durable row still says restored. The replay re-records the
// checkout_proven_absent authorization — moving the durable row — before the
// native effect runs, and converges to settled only through that admission.
func TestReclaimReplayRecordsAuthorizationBeforeNativeDeletion(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-admission-restored"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "admission-restored"))
	other := filepath.Join(t.TempDir(), "holder")
	// The first attempt races a holder onto the branch at the deletion
	// boundary: the ref is restored and stays protected-but-replayable.
	first := &con829UpdateRefHookRunner{hook: func() { f.git(f.repoRoot, "worktree", "add", other, branch) }}
	if err := f.s.FinishWorktreeNativeRemoval(context.Background(), first, removal); err == nil {
		t.Fatal("expected restored protection")
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRestored {
		t.Fatalf("first phase=%s", phase)
	}
	f.git(other, "checkout", "-q", "--detach")
	// The durable phase at the instant the pinned deletion runs must be the
	// recorded authorization, not the replay's stale carried predecessor.
	seen := ""
	runner := &con829NativeBoundaryRunner{hook: func(_ string, args []string) {
		if con829IsPinnedDelete(args) {
			seen, _ = f.refOutcomeKind(t, branch)
		}
	}}
	req := f.reclaimRequest(workID, "admission-restored-replay")
	req.Runner = runner
	_, err := f.s.ReclaimWorktree(context.Background(), req)
	if seen != WorktreeRefPhaseCheckoutProvenAbsent {
		t.Errorf("delete ran without recorded predecessor: durable=%s, want checkout_proven_absent; result=%v", seen, err)
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseSettled {
		t.Errorf("final phase=%s; result=%v", phase, err)
	}
}

// TestReclaimDelayedPreparedReplayCannotDeleteStickyProtectedRef pins review
// finding 1's sticky-identity half: a request prepared while the row still
// said checkout_proven_absent, committed after a first attempt recorded
// retained_unproven, must not read its stale phase or the historical
// authorization event as permission. The durable owner refuses the phase
// record before the native effect, so the sticky protection holds even after
// the tip returns to the pinned value.
func TestReclaimDelayedPreparedReplayCannotDeleteStickyProtectedRef(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-admission-sticky"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "admission-sticky"))
	pinned := removal.BranchDeletions[0].ExpectedTip
	other := filepath.Join(t.TempDir(), "unique")
	f.git(f.repoRoot, "worktree", "add", "--detach", other, pinned)
	f.git(other, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-m", "unique")
	changed := f.gitOut(other, "rev-parse", "HEAD")
	delayedReq := f.reclaimRequest(workID, "admission-sticky-delayed")
	var delayedProbe WorktreeReclaimProbe
	first := &con829UpdateRefHookRunner{hook: func() {
		// The delayed request prepares while the row still holds its stale
		// phase, then the boundary moves the tip so the first attempt
		// records the sticky retained_unproven protection.
		var probeErr error
		delayedProbe, probeErr = f.s.PrepareWorktreeReclaim(context.Background(), delayedReq)
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		f.git(f.repoRoot, "update-ref", "refs/heads/"+branch, changed)
	}}
	if err := f.s.FinishWorktreeNativeRemoval(context.Background(), first, removal); err == nil {
		t.Fatal("expected moved-tip protection")
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRetainedUnproven {
		t.Fatalf("protection phase=%s", phase)
	}
	f.git(f.repoRoot, "update-ref", "refs/heads/"+branch, pinned)
	// The delayed request commits its original-plan replay under the stale
	// prepared phase: the durable owner, not the historical event, must
	// refuse the authorization before the deletion runs.
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, delayedRemoval, err := reclaimWorktreeStoreTx(context.Background(), tx, delayedReq, delayedProbe)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	err = f.s.FinishWorktreeNativeRemoval(context.Background(), nil, delayedRemoval)
	if !f.refExists(branch) {
		t.Errorf("stale finalizer deleted sticky retained_unproven ref after tip returned; result=%v", err)
	}
}

// TestReclaimReadsListedSymbolicHeadsAroundNativeDeletion pins review
// finding 2: after the post-deletion inventory subprocess produced its
// complete genuine output, a listed checkout whose HEAD comes to name the
// deleted branch must be observed, leaving restoration debt and a create-only
// restore instead of a settled report over an unborn checkout.
func TestReclaimReadsListedSymbolicHeadsAroundNativeDeletion(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-admission-symbolic"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "admission-symbolic"))
	other := filepath.Join(t.TempDir(), "existing-checkout")
	f.git(f.repoRoot, "worktree", "add", "--detach", other, "main")
	deleted, changed := false, false
	symbolicReads := 0
	runner := &con829NativeBoundaryRunner{}
	runner.hook = func(dir string, args []string) {
		if dir == other && len(args) > 0 && args[0] == "symbolic-ref" {
			symbolicReads++
		}
	}
	runner.after = func(_ string, args []string, _ []byte) {
		if con829IsPinnedDelete(args) {
			deleted = true
		}
		if deleted && !changed && len(args) > 1 && args[0] == "worktree" && args[1] == "list" {
			changed = true
			f.git(other, "symbolic-ref", "HEAD", "refs/heads/"+branch)
		}
	}
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal)
	if symbolicReads == 0 {
		t.Error("no symbolic HEAD observation of the listed existing checkout before or after deletion")
	}
	if !f.refExists(branch) {
		t.Errorf("post-list symbolic HEAD change left existing checkout unborn and was reported settled; result=%v", err)
	}
	phase, _ := f.refOutcomeKind(t, branch)
	t.Logf("phase=%s, result=%v, symbolicReads=%d", phase, err, symbolicReads)
}

// TestWorktreeInventoryParserRefusesIncompleteRecords pins review finding 3:
// an attached record without its HEAD attribute, duplicate detached markers,
// a bare record claiming a HEAD, records without their blank separators or
// the terminating boundary, repeated locked/prunable attributes, a repeated
// worktree path, and a whitespace-only line standing in for a boundary are
// incomplete, contradictory, or unrecognized records — never complete
// recognized Git entries — so the owning parser refuses them.
func TestWorktreeInventoryParserRefusesIncompleteRecords(t *testing.T) {
	head := func(char byte) string { return strings.Repeat(string(char), 40) }
	for _, text := range []string{
		"worktree /synthetic\nbranch refs/heads/main\n\n",
		"worktree /synthetic\nHEAD " + head('a') + "\ndetached\ndetached\n\n",
		"worktree /synthetic\nbare\nHEAD " + head('a') + "\n\n",
		"worktree /a\nHEAD " + head('a') + "\ndetached\nworktree /b\nHEAD " + head('b') + "\ndetached\n\n",
		"worktree /a\nHEAD " + head('a') + "\ndetached\n",
		"worktree /a\nHEAD " + head('a') + "\ndetached\nlocked one\nlocked two\n\n",
		"worktree /a\nHEAD " + head('a') + "\ndetached\nprunable one\nprunable two\n\n",
		"worktree /a\nHEAD " + head('a') + "\ndetached\n\nworktree /a\nHEAD " + head('b') + "\ndetached\n\n",
		"worktree /a\nHEAD " + head('a') + "\ndetached\n   \n\n",
	} {
		if _, err := parseWorktreeInventory([]byte(text)); err == nil {
			t.Errorf("malformed inventory admitted: %q", text)
		}
	}
}

// TestReclaimFailedPreObservationLeavesDurableUncertainty pins review
// finding 4: an unavailable pre-deletion checkout observation leaves the
// durable uncertainty the recorded contract requires — a recovery phase with
// the actual failed-observation reason — instead of a planned row that still
// carries its original durability reason, and the uncertainty stays visible
// to a fresh audit after the safe directory removal.
func TestReclaimFailedPreObservationLeavesDurableUncertainty(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-admission-unknown"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "admission-unknown"))
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), &con829NativePhaseRunner{failInventory: "before"}, removal)
	if err == nil {
		t.Fatal("expected unavailable pre-observation")
	}
	phase, reason := f.refOutcomeKind(t, branch)
	if phase != WorktreeRefPhaseRetainedUnproven && phase != WorktreeRefPhaseRestorationOwed {
		t.Errorf("failed observation has no durable uncertainty phase: phase=%s reason=%s", phase, reason)
	}
	row := retainedRefRow(f.auditDrift(t), branch)
	if row == nil || (!strings.Contains(row.Risk, "unobserv") && !strings.Contains(row.Risk, "inventory") && !strings.Contains(row.Risk, "inspect")) {
		t.Errorf("fresh audit lost failed observation: %+v", row)
	}
}
