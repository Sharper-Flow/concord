package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// con829RefOutcomeRow is one durable per-ref outcome row snapshot.
type con829RefOutcomeRow struct {
	phase  string
	tip    string
	reason string
}

// refOutcomeSnapshot reads every durable per-ref outcome row the store
// holds, keyed by branch.
func refOutcomeSnapshot(t *testing.T, f *con829Fixture) map[string]con829RefOutcomeRow {
	t.Helper()
	rows, err := f.s.db.Query(`SELECT branch,phase,tip,reason FROM worktree_ref_outcomes ORDER BY branch`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]con829RefOutcomeRow{}
	for rows.Next() {
		var branch string
		var row con829RefOutcomeRow
		if err := rows.Scan(&branch, &row.phase, &row.tip, &row.reason); err != nil {
			t.Fatal(err)
		}
		out[branch] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReclaimRefOutcomeSettlesAfterRestoration(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-rebuild-settled"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	pinned := f.refTip(branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "rebuild-settled"))
	other := filepath.Join(t.TempDir(), "holder")
	first := &con829UpdateRefHookRunner{hook: func() { f.git(f.repoRoot, "worktree", "add", other, branch) }}
	if err := f.s.FinishWorktreeNativeRemoval(context.Background(), first, removal); err == nil {
		t.Fatal("expected restored protection")
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRestored {
		t.Fatalf("first phase=%s", phase)
	}
	f.git(other, "checkout", "-q", "--detach")
	if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "rebuild-settled-replay")); err != nil {
		t.Fatalf("the healthy replay must converge, got %v", err)
	}
	if f.refExists(branch) {
		t.Fatal("the converged deletion must remove the ref")
	}
	before := refOutcomeSnapshot(t, f)
	if row, ok := before[branch]; !ok || row.phase != WorktreeRefPhaseSettled || row.tip != pinned || row.reason == "" {
		t.Fatalf("settled row: %+v", before)
	}
}

// TestReclaimThreeRestorationCyclesLogEveryTransition pins the settlement
// identity owner across repeated restoration/deletion cycles whose failed
// observation reasons change: every cycle's transitions are fresh
// chronological settlement events — the per-ref event count rises by exactly
// the transitions each cycle records. A sequence derived from a prefix that
// matches no stored event leaves later cycles appending nothing.
func TestReclaimThreeRestorationCyclesLogEveryTransition(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-three-cycles"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	pinned := f.refTip(branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "three-cycles"))
	holder := filepath.Join(t.TempDir(), "raced-checkout")
	settlementCount := func() int {
		t.Helper()
		var count int
		if err := f.s.db.QueryRow(`SELECT COUNT(*) FROM domain_events WHERE kind='work.worktree_removal_settled' AND subject_id=?`, workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// Cycle 1: a holder raced onto the branch at the deletion boundary, so
	// the run restores the ref — checkout_proven_absent, deleted,
	// restoration_owed, restored.
	first := &con829UpdateRefHookRunner{hook: func() { f.git(f.repoRoot, "worktree", "add", holder, branch) }}
	if err := f.s.FinishWorktreeNativeRemoval(context.Background(), first, removal); err == nil {
		t.Fatal("expected restored protection")
	}
	if got := settlementCount(); got != 4 {
		t.Fatalf("cycle 1 logged %d settlement events, want 4", got)
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRestored {
		t.Fatalf("cycle 1 phase=%s", phase)
	}

	// Cycle 2: the holder releases; the replay deletes and loses the
	// post-deletion observation, leaving restoration debt under the
	// failed-observation reason — checkout_proven_absent, deleted,
	// restoration_owed.
	f.git(holder, "checkout", "-q", "--detach")
	repeat := f.reclaimRequest(workID, "three-cycles-repeat")
	repeat.Runner = &con829NativePhaseRunner{failInventory: "after"}
	if _, err := f.s.ReclaimWorktree(context.Background(), repeat); err == nil {
		t.Fatal("the interrupted repeat cycle must surface its failure")
	}
	if got := settlementCount(); got != 7 {
		t.Fatalf("cycle 2 logged %d settlement events, want 7", got)
	}
	phase, reason := f.refOutcomeKind(t, branch)
	if phase != WorktreeRefPhaseRestorationOwed || !strings.Contains(reason, "cannot inspect") {
		t.Fatalf("cycle 2 must leave failed-observation debt, got %s (%s)", phase, reason)
	}
	if f.refExists(branch) {
		t.Fatal("cycle 2's pinned deletion must have removed the ref")
	}

	// Cycle 3: a checkout names the missing branch again, so the debt
	// resolves by restoring at the pinned tip — restoration_owed under the
	// holder reason, restored.
	f.git(holder, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	_, resolveErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "three-cycles-resolve"))
	var failure *Failure
	if !errors.As(resolveErr, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "restored at its pinned tip") {
		t.Fatalf("cycle 3 must resolve the debt and report the restored protection, got %v", resolveErr)
	}
	if got := settlementCount(); got != 9 {
		t.Fatalf("cycle 3 logged %d settlement events, want 9", got)
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRestored {
		t.Fatalf("cycle 3 phase=%s", phase)
	}
	if !f.refExists(branch) || f.refTip(branch) != pinned {
		t.Fatalf("cycle 3 must restore the ref at the pinned tip %s", pinned)
	}

	// Cycle 4: the holder releases again and the healthy replay converges
	// the deletion — checkout_proven_absent, deleted, settled.
	f.git(holder, "checkout", "-q", "--detach")
	if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "three-cycles-converge")); err != nil {
		t.Fatalf("cycle 4 must converge, got %v", err)
	}
	if got := settlementCount(); got != 12 {
		t.Fatalf("cycle 4 logged %d settlement events, want 12", got)
	}
	if f.refExists(branch) {
		t.Fatal("cycle 4's converged deletion must remove the ref")
	}

	before := refOutcomeSnapshot(t, f)
	if row, ok := before[branch]; !ok || row.phase != WorktreeRefPhaseSettled || row.tip != pinned {
		t.Fatalf("settled row: %+v", before)
	}
}

func TestReclaimInterruptedRestorationDebtResolvesAtPinnedTip(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-rebuild-debt"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	pinned := f.refTip(branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "rebuild-debt"))
	other := filepath.Join(t.TempDir(), "raced-checkout")
	first := &con829UpdateRefHookRunner{hook: func() { f.git(f.repoRoot, "worktree", "add", other, branch) }}
	if err := f.s.FinishWorktreeNativeRemoval(context.Background(), first, removal); err == nil {
		t.Fatal("expected restored protection")
	}
	f.git(other, "checkout", "-q", "--detach")
	// The repeat cycle deletes the ref and loses the post-deletion
	// observation: the run must leave durable restoration debt.
	req := f.reclaimRequest(workID, "rebuild-debt-repeat")
	req.Runner = &con829NativePhaseRunner{failInventory: "after"}
	if _, err := f.s.ReclaimWorktree(context.Background(), req); err == nil {
		t.Fatal("the interrupted repeat cycle must surface its failure")
	}
	if f.refExists(branch) {
		t.Fatal("the repeat cycle's pinned deletion must have removed the ref")
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRestorationOwed {
		t.Fatalf("the repeat cycle must leave restoration debt, got %s", phase)
	}
	// A checkout that names the missing branch
	// still gets its restoration at the immutable pinned tip, reported as
	// the restored protection beside the converged removal.
	f.git(other, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	_, resolveErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "rebuild-debt-resolve"))
	var failure *Failure
	if !errors.As(resolveErr, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "restored at its pinned tip") {
		t.Fatalf("the retry must resolve the debt and report the restored protection, got %v", resolveErr)
	}
	if !f.refExists(branch) || f.refTip(branch) != pinned {
		t.Fatalf("the debt must restore the ref at the pinned tip %s", pinned)
	}
	if phase, _ := f.refOutcomeKind(t, branch); phase != WorktreeRefPhaseRestored {
		t.Fatalf("the resolved debt must settle as restored, got %s", phase)
	}
}

// con829SymbolicHeadCounterRunner counts every live symbolic HEAD read per
// worktree directory and fails the observation of one named directory, so a
// test can prove the observer read every listed HEAD — none skipped after a
// holder was found — and that a later failed observation is not hidden by an
// earlier holder.
type con829SymbolicHeadCounterRunner struct {
	failDir string
	reads   map[string]int
}

func (r *con829SymbolicHeadCounterRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "symbolic-ref" && args[len(args)-1] == "HEAD" {
		if r.reads == nil {
			r.reads = map[string]int{}
		}
		r.reads[dir]++
		if dir == r.failDir {
			return nil, errors.New("synthetic symbolic HEAD observation unavailable")
		}
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

// TestReclaimObservesEveryListedHeadBeforeConcluding pins the observation
// boundary's completeness: the pre-deletion observation reads every listed
// worktree's symbolic HEAD — accumulating holders instead of returning at
// the first one — so a failed later observation surfaces as unknown
// uncertainty rather than a holder refusal that never looked further.
func TestReclaimObservesEveryListedHeadBeforeConcluding(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-observe-all"
	path, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	// The claim checkout releases the ref, so a second worktree can hold
	// the branch while the plan still pins its deletion.
	f.git(path, "checkout", "-q", "--detach")
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "observe-all"))
	holder := filepath.Join(t.TempDir(), "holder-checkout")
	later := filepath.Join(t.TempDir(), "later-checkout")
	// The holder precedes the failing entry in the porcelain order, so an
	// observer that stops at the first holder never reaches the failure.
	f.git(f.repoRoot, "worktree", "add", holder, branch)
	f.git(f.repoRoot, "worktree", "add", "--detach", later, "main")
	runner := &con829SymbolicHeadCounterRunner{failDir: later}
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal)
	if err == nil {
		t.Fatal("the failed later observation must surface")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable {
		t.Fatalf("the incomplete observation must be unknown, got %v", err)
	}
	if !f.refExists(branch) {
		t.Fatal("the unobserved deletion must be refused")
	}
	phase, reason := f.refOutcomeKind(t, branch)
	if phase != WorktreeRefPhaseRestorationOwed && phase != WorktreeRefPhaseRetainedUnproven {
		t.Errorf("the incomplete observation must record durable uncertainty, got %s (%s)", phase, reason)
	}
	if !strings.Contains(reason, "cannot inspect") {
		t.Errorf("the recorded uncertainty must name the failed observation, got %q", reason)
	}
	for _, dir := range []string{f.repoRoot, holder, later} {
		if runner.reads[dir] == 0 {
			t.Errorf("the listed worktree %s was never symbolically observed; reads %+v", dir, runner.reads)
		}
	}
}
