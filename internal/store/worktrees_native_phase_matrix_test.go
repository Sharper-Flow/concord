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

// CD-0212 D4 native-phase coverage from the CON-829 retry-6 rejection. The
// native removal is a bounded protocol — pre-delete checkout observation,
// pinned argv no-deref delete, post-delete checkout observation, create-only
// restoration at the pinned tip — and every interruption of that protocol
// must leave durable, audit-visible state a healthy replay converges without
// erasing restoration debt. A malformed or incomplete successful worktree
// inventory is an unknown observation, never proof a branch is checked out
// nowhere, and a valid reftable live identity is never narrowed to a loose
// pathname bound.

// con829NativePhaseRunner runs real git but interrupts the native removal
// protocol at one named boundary: immediately before the pinned argv delete,
// at the worktree inventory (before or after the delete), or at the
// create-only restoration. refuseStdin rejects stdin transactions, so one
// matrix case can pin that the protocol is served entirely by the pinned
// argv primitives; every other case serves stdin to keep the interruption
// under test the only failure.
type con829NativePhaseRunner struct {
	beforeDelete  func() // fires immediately before the pinned argv delete
	failDelete    bool   // the pinned argv delete itself fails
	failCreate    bool   // the create-only argv restoration fails
	onCreate      func() // fires when the create-only restoration is attempted
	refuseStdin   bool   // stdin transactions fail outright
	failInventory string // "before" or "after" the pinned delete, or never
	// inventory overrides the worktree inventory output once the directory
	// phase completed, so a test can feed a malformed successful inventory.
	inventory func() ([]byte, error)
	deleted   bool
	removed   bool
}

func (r *con829NativePhaseRunner) isPinnedDelete(args []string) bool {
	if len(args) == 0 || args[0] != "update-ref" {
		return false
	}
	for _, arg := range args {
		if arg == "-d" {
			return true
		}
	}
	return false
}

func (r *con829NativePhaseRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		if r.inventory != nil && r.removed {
			return r.inventory()
		}
		if (r.failInventory == "before" && !r.deleted) || (r.failInventory == "after" && r.deleted) {
			return nil, errors.New("synthetic worktree inventory unavailable")
		}
	}
	if len(args) > 0 && args[0] == "update-ref" {
		if r.isPinnedDelete(args) {
			if r.failDelete {
				return nil, errors.New("synthetic pinned delete unavailable")
			}
			if !r.deleted && r.beforeDelete != nil {
				hook := r.beforeDelete
				r.beforeDelete = nil
				hook()
			}
			out, err := (ExecGitRunner{}).Run(ctx, dir, args...)
			if err == nil {
				r.deleted = true
			}
			return out, err
		}
		if r.onCreate != nil {
			hook := r.onCreate
			r.onCreate = nil
			hook()
		}
		if r.failCreate {
			return nil, errors.New("synthetic create-only restoration unavailable")
		}
	}
	out, err := (ExecGitRunner{}).Run(ctx, dir, args...)
	if err == nil && len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
		r.removed = true
	}
	return out, err
}

func (r *con829NativePhaseRunner) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	if r.refuseStdin {
		return nil, errors.New("the native removal must use the pinned argv primitives, not a stdin transaction")
	}
	// The interruption boundaries track the stdin primitives too, so the
	// matrix stays red against an implementation that regressed off argv.
	if len(args) > 0 && args[0] == "update-ref" {
		if strings.HasPrefix(string(stdin), "delete ") {
			if !r.deleted && r.beforeDelete != nil {
				hook := r.beforeDelete
				r.beforeDelete = nil
				hook()
			}
			out, err := (ExecGitRunner{}).RunStdin(ctx, dir, stdin, args...)
			if err == nil {
				r.deleted = true
			}
			return out, err
		}
		if strings.HasPrefix(string(stdin), "create ") {
			if r.onCreate != nil {
				hook := r.onCreate
				r.onCreate = nil
				hook()
			}
			if r.failCreate {
				return nil, errors.New("synthetic create-only restoration unavailable")
			}
		}
	}
	return (ExecGitRunner{}).RunStdin(ctx, dir, stdin, args...)
}

// refOutcomeKind reads the durable per-ref phase row one branch holds.
func (f *con829Fixture) refOutcomeKind(t *testing.T, branch string) (string, string) {
	t.Helper()
	var kind, reason string
	err := f.s.db.QueryRow(`SELECT phase,reason FROM worktree_ref_outcomes WHERE branch=? ORDER BY recorded_at DESC, branch LIMIT 1`, branch).Scan(&kind, &reason)
	if err != nil {
		t.Fatalf("no durable ref outcome row for %s: %v", branch, err)
	}
	return kind, reason
}

// assertRacedCheckoutWhole verifies the raced worktree still holds the branch
// checked out at the pinned tip after a restoration.
func assertRacedCheckoutWhole(t *testing.T, f *con829Fixture, other, branch, pinned string) {
	t.Helper()
	if tip := f.gitOut(other, "rev-parse", "HEAD"); tip != pinned {
		t.Fatalf("the raced worktree's HEAD must be whole at %s, got %s", pinned, tip)
	}
	if out := f.gitOut(other, "status", "--porcelain=v1", "-b"); !strings.HasPrefix(out, "## "+branch) {
		t.Fatalf("the raced worktree must hold %s checked out, got %q", branch, out)
	}
}

// TestReclaimPostDeleteRecoveryDebtPersistsAcrossReplay pins the retry-6
// recovery findings: when the pinned deletion ran but its post-delete
// checkout observation or its create-only restoration could not complete,
// the uncertainty persists as a durable recovery debt. A healthy replay must
// resolve it — restoring the deleted ref at its immutable pinned tip for the
// worktree that holds it — and must never treat the unobserved absence as
// convergence.
func TestReclaimPostDeleteRecoveryDebtPersistsAcrossReplay(t *testing.T) {
	interrupt := func(failInventory string, failCreate bool) *con829NativePhaseRunner {
		return &con829NativePhaseRunner{failInventory: failInventory, failCreate: failCreate}
	}
	run := func(t *testing.T, runner *con829NativePhaseRunner) {
		t.Helper()
		f := con829FixtureNew(t)
		workID := "work-native-recovery"
		path, branch := f.claimWork(workID)
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		pinned := f.refTip(branch)
		removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "native-recovery"))
		other := filepath.Join(t.TempDir(), "raced-checkout")
		runner.beforeDelete = func() { f.git(f.repoRoot, "worktree", "add", other, branch) }
		err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal)
		if err == nil {
			t.Fatal("the interrupted native phase must surface its failure")
		} else {
			t.Logf("first native failure: %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the independently safe directory phase did not complete")
		}
		if kind, _ := f.refOutcomeKind(t, branch); kind != WorktreeRefPhaseRestorationOwed {
			t.Fatalf("the interrupted recovery must be durable as %s, got %s", WorktreeRefPhaseRestorationOwed, kind)
		}
		if row := retainedRefRow(f.auditDrift(t), branch); row == nil {
			t.Fatal("the recovery debt must be audit-visible after directory removal")
		}
		// A healthy replay resolves the debt: the raced checkout still names
		// the deleted branch, so the ref is restored at the immutable pinned
		// tip and protected, never treated as converged absence.
		_, retryErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "native-recovery-replay"))
		if !f.refExists(branch) {
			t.Fatalf("replay erased restoration debt: the checked-out branch stays absent; retry=%v", retryErr)
		}
		if tip := f.refTip(branch); tip != pinned {
			t.Fatalf("the restoration must land at the pinned tip %s, got %s", pinned, tip)
		}
		assertRacedCheckoutWhole(t, f, other, branch, pinned)
		if kind, _ := f.refOutcomeKind(t, branch); kind != WorktreeRefPhaseRestored {
			t.Fatalf("the resolved restoration must settle as %s, got %s", WorktreeRefPhaseRestored, kind)
		}
		if row := retainedRefRow(f.auditDrift(t), branch); row == nil || !strings.Contains(row.Risk, "checked out") {
			t.Fatalf("the settled protection must stay audit-visible: %+v", row)
		}
	}
	t.Run("failed post-delete checkout observation", func(t *testing.T) {
		run(t, interrupt("after", false))
	})
	t.Run("failed create-only restoration", func(t *testing.T) {
		run(t, interrupt("", true))
	})
}

// TestReclaimIncompleteInventoryNeverAuthorizesDeletion pins the retry-6
// inventory finding: a malformed or incomplete successful worktree inventory
// is an unknown observation. It never proves a branch is checked out nowhere,
// so it never authorizes the pinned deletion, and the unproven ref keeps a
// durable audit-visible outcome.
func TestReclaimIncompleteInventoryNeverAuthorizesDeletion(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-native-incomplete"
	path, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "native-incomplete"))
	runner := &con829NativePhaseRunner{inventory: func() ([]byte, error) {
		return []byte("worktree\nHEAD unknown\n"), nil
	}}
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal)
	if !f.worktreeGone(path) {
		t.Fatal("the independently safe directory phase did not complete")
	}
	if !f.refExists(branch) {
		t.Fatalf("an incomplete successful inventory authorized the deletion; native result=%v", err)
	}
	if row := retainedRefRow(f.auditDrift(t), branch); row == nil {
		t.Fatal("incomplete checkout evidence produced no durable unproven outcome")
	}
	// A healthy replay converges the still-owed deletion.
	if _, retryErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "native-incomplete-replay")); retryErr != nil {
		t.Fatalf("a healthy replay must converge the owed deletion, got %v", retryErr)
	}
	if f.refExists(branch) {
		t.Fatal("the converged deletion must remove the ref")
	}
}

// TestWorktreeInventoryParsingIsCompleteAndSymbolic drives the inventory
// parser table: every listed HEAD's symbolic identity is observed from a
// complete parse of the record grammar real git emits — blank-separated,
// blank-terminated records — and each malformed or incomplete shape
// refuses instead of reporting an unknown checkout state as unheld.
func TestWorktreeInventoryParsingIsCompleteAndSymbolic(t *testing.T) {
	valid := "worktree /repo/main\nHEAD " + strings.Repeat("0", 40) + "\nbranch refs/heads/main\n\n" +
		"worktree /repo/linked\nHEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/feature\nlocked why not\n\n" +
		"worktree /repo/detached\nHEAD " + strings.Repeat("b", 40) + "\ndetached\n\n" +
		"worktree /repo/bare.git\nbare\n\n"
	// holdsEntry reports whether the parsed inventory carries a non-bare
	// record whose attached branch is the full ref named.
	holdsEntry := func(inv *worktreeInventory, full string) bool {
		for _, entry := range inv.entries {
			if !entry.bare && entry.branch == full {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name    string
		out     string
		wantErr bool
		held    string // a branch the inventory must report held
	}{
		{name: "complete mixed inventory", out: valid, held: "feature"},
		{name: "attribute before any worktree", out: "HEAD " + strings.Repeat("a", 40) + "\n\n", wantErr: true},
		{name: "worktree without a path", out: "worktree\nHEAD unknown\n\n", wantErr: true},
		{name: "entry without a head disposition", out: "worktree /repo/main\nHEAD " + strings.Repeat("a", 40) + "\n\n", wantErr: true},
		{name: "head is not an object name", out: "worktree /repo/main\nHEAD unknown\nbranch refs/heads/main\n\n", wantErr: true},
		{name: "unknown attribute", out: "worktree /repo/main\nmystery x\nbranch refs/heads/main\n\n", wantErr: true},
		{name: "branch and detached disagree", out: "worktree /repo/main\nHEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/main\ndetached\n\n", wantErr: true},
		{name: "bare entry claiming a branch", out: "worktree /repo/bare\nbare\nbranch refs/heads/main\n\n", wantErr: true},
		{name: "empty inventory", out: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseWorktreeInventory([]byte(tc.out))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the malformed inventory must refuse, parsed %+v", inv.entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("the valid inventory must parse: %v", err)
			}
			if tc.held != "" && !holdsEntry(inv, "refs/heads/"+tc.held) {
				t.Fatalf("the inventory must observe %s checked out, held %+v", tc.held, inv.entries)
			}
			if holdsEntry(inv, "refs/heads/absent-branch") {
				t.Fatal("the inventory must not report a branch no entry holds")
			}
		})
	}
}

// TestReclaimNativePhaseSurvivesInterruptionMatrix drives the complete
// interruption and concurrent-restoration matrix across the native removal
// protocol. Every interrupted boundary leaves durable state a healthy replay
// converges, a concurrently rebuilt ref is preserved rather than overwritten,
// and the pinned primitives are argv commands a stdin-only runner cannot
// serve.
func TestReclaimNativePhaseSurvivesInterruptionMatrix(t *testing.T) {
	commitRemoval := func(t *testing.T, f *con829Fixture, workID string) (*WorktreeNativeRemoval, string, string) {
		t.Helper()
		path, branch := f.claimWork(workID)
		f.git(f.repoRoot, "push", "-q", "origin", branch)
		return con829CommitRemoval(t, f, f.reclaimRequest(workID, "matrix-"+workID)), path, branch
	}
	t.Run("pre-delete inventory unavailable retains and converges", func(t *testing.T) {
		f := con829FixtureNew(t)
		removal, path, branch := commitRemoval(t, f, "work-matrix-pre-inventory")
		runner := &con829NativePhaseRunner{failInventory: "before"}
		if err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal); err == nil {
			t.Fatal("the unavailable pre-delete observation must surface")
		}
		if !f.worktreeGone(path) || !f.refExists(branch) {
			t.Fatal("the directory converges while the unobserved deletion is refused")
		}
		// The failed observation leaves its actual uncertainty durably — a
		// recovery phase with the failed-observation reason, never a silent
		// planned row still carrying its original durability reason — and
		// it stays audit-visible after the directory removal.
		kind, reason := f.refOutcomeKind(t, branch)
		if kind != WorktreeRefPhaseRestorationOwed && kind != WorktreeRefPhaseRetainedUnproven {
			t.Fatalf("the failed pre-delete observation must record durable uncertainty, got %s (%s)", kind, reason)
		}
		if !strings.Contains(reason, "cannot inspect") {
			t.Fatalf("the recorded uncertainty must name the failed observation, got %q", reason)
		}
		if row := retainedRefRow(f.auditDrift(t), branch); row == nil || !strings.Contains(row.Risk, "inspect") {
			t.Fatalf("the uncertainty must stay audit-visible after directory removal: %+v", row)
		}
		// The recorded uncertainty is a replayable recovery debt, not a
		// terminal protection: a healthy replay re-derives the ownership
		// and converges the deletion the plan still owes.
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-matrix-pre-inventory", "matrix-pre-replay")); err != nil {
			t.Fatalf("the healthy replay must converge the deletion, got %v", err)
		}
		if f.refExists(branch) {
			t.Fatal("the converged deletion must remove the ref")
		}
	})
	t.Run("pinned delete unavailable retains and converges", func(t *testing.T) {
		f := con829FixtureNew(t)
		removal, path, branch := commitRemoval(t, f, "work-matrix-fail-delete")
		runner := &con829NativePhaseRunner{failDelete: true}
		if err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal); err == nil {
			t.Fatal("the failed pinned delete must surface")
		}
		if !f.worktreeGone(path) || !f.refExists(branch) {
			t.Fatal("the directory converges while the failed deletion leaves the ref")
		}
		if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-matrix-fail-delete", "matrix-fail-replay")); err != nil {
			t.Fatalf("the healthy replay must converge the deletion, got %v", err)
		}
		if f.refExists(branch) {
			t.Fatal("the converged deletion must remove the ref")
		}
	})
	t.Run("concurrently rebuilt ref is preserved", func(t *testing.T) {
		f := con829FixtureNew(t)
		removal, path, branch := commitRemoval(t, f, "work-matrix-rebuilt")
		pinned := f.refTip(branch)
		other := filepath.Join(t.TempDir(), "raced-checkout")
		runner := &con829NativePhaseRunner{}
		runner.beforeDelete = func() { f.git(f.repoRoot, "worktree", "add", other, branch) }
		runner.onCreate = func() {
			// A concurrent actor rebuilds the ref at a foreign tip while the
			// restoration is starting: the create-only restoration must
			// refuse to overwrite it.
			if err := writeFile(filepath.Join(f.repoRoot, "foreign.txt"), "foreign content\n"); err != nil {
				t.Log(err)
			}
			f.git(f.repoRoot, "add", "foreign.txt")
			f.git(f.repoRoot, "commit", "-m", "foreign rebuild")
			f.git(f.repoRoot, "branch", "-f", branch, "main")
		}
		err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("the concurrently rebuilt ref must surface as a checkout protection, got %v", err)
		}
		if !f.worktreeGone(path) {
			t.Fatal("the directory phase must complete")
		}
		if !f.refExists(branch) || f.refTip(branch) == pinned || f.refTip(branch) != f.refTip("main") {
			t.Fatal("the concurrently rebuilt ref must survive at its foreign tip")
		}
		if kind, _ := f.refOutcomeKind(t, branch); kind != WorktreeRefPhaseRetainedUnproven {
			t.Fatalf("the rebuilt ref must record the durable retention, got %s", kind)
		}
		// The foreign identity is protected on replay: retained_unproven is
		// terminal, so the replay never rebuilds deletion authority over the
		// rebuilt ref. It converges with nothing owed, the ref survives at
		// its foreign tip, and the durable retention row keeps naming it.
		if _, retryErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-matrix-rebuilt", "matrix-rebuilt-replay")); retryErr != nil {
			t.Fatalf("the replay over a terminal protection must converge with nothing owed, got %v", retryErr)
		}
		if !f.refExists(branch) || f.refTip(branch) != f.refTip("main") {
			t.Fatal("the protected ref must survive the replay at its foreign tip")
		}
		if kind, _ := f.refOutcomeKind(t, branch); kind != WorktreeRefPhaseRetainedUnproven {
			t.Fatalf("the replay must keep the moved-tip retention terminal, got %s", kind)
		}
	})
	t.Run("pinned argv primitives carry the whole protocol", func(t *testing.T) {
		f := con829FixtureNew(t)
		removal, path, branch := commitRemoval(t, f, "work-matrix-argv")
		runner := &con829NativePhaseRunner{refuseStdin: true}
		if err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal); err != nil {
			t.Fatalf("a stdin-refusing runner must serve the whole native protocol, got %v", err)
		}
		if !f.worktreeGone(path) || f.refExists(branch) {
			t.Fatal("the pinned argv protocol must finish the removal and the deletion")
		}
	})
}

// TestReclaimValidReftableIdentityIsNotNarrowed pins the retry-6 identity
// finding: a reftable-backed repository holds valid branch identities of any
// length the writer's block size admits, so neither the reclamation fold nor
// the retention projection may narrow a live identity to a loose-pathname
// bound. The full identity stays recorded and audit-visible.
func TestReclaimValidReftableIdentityIsNotNarrowed(t *testing.T) {
	for _, length := range []int{5000, 6000} {
		// The subtest name carries the length: a name go test would
		// disambiguate as "#01" puts a '#' in the temp-dir path, and the
		// SQLite URI DSN truncates at that fragment marker.
		t.Run(fmt.Sprintf("identity of %d bytes", length), func(t *testing.T) {
			f := con829FixtureNew(t)
			f.git(f.repoRoot, "refs", "migrate", "--ref-format=reftable")
			workID := "work-native-reftable"
			path, _ := f.claimWork(workID)
			branch := "correction/" + strings.Repeat("a", length)
			// Git documents reftable.blockSize as a writer choice that must
			// exceed the longest ref name, so this is a valid native
			// checkout no loose pathname could hold.
			f.git(path, "-c", "reftable.blockSize=8192", "checkout", "-q", "-b", branch)
			if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "native-reftable")); err != nil {
				t.Fatalf("a valid reftable live branch must reclaim without narrowing its identity: %v", err)
			}
			if !f.worktreeGone(path) {
				t.Fatal("the proven-durable directory removal must complete")
			}
			if !f.refExists(branch) {
				t.Fatal("the divergent live branch must survive the reclaim")
			}
			facts := f.entryFacts(workID)
			var retained bool
			for _, ref := range facts.RetainedRefs {
				if ref.Branch == branch {
					retained = true
				}
			}
			if !retained {
				t.Fatalf("the complete valid reftable identity must be retained in the facts, got %+v", facts.RetainedRefs)
			}
			row := retainedRefRow(f.auditDrift(t), branch)
			if row == nil || row.RetainedBranch != branch {
				t.Fatalf("the complete valid reftable identity must stay audit-visible, got %+v", row)
			}
		})
	}
}

// TestReclaimRefusalReasonMatrix drives every gate refusal reason the reclaim
// and destroy surfaces own: each refuses typed with its reason, leaves the
// worktree in place, and records no reclamation. The matrix is table-derived
// so a new refusal reason fails to find a row and must be added here.
func TestReclaimRefusalReasonMatrix(t *testing.T) {
	type refusalCase struct {
		name       string
		setup      func(t *testing.T) (f *con829Fixture, path string, invoke func() error)
		wantKind   FailureKind
		wantDetail []string
	}
	cases := []refusalCase{
		{
			name: "dirty tree",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _, _, _ := f.correctionScenario("work-refuse-dirty")
				if err := writeFile(filepath.Join(path, "tracked.txt"), "dirty\n"); err != nil {
					t.Fatal(err)
				}
				return f, path, func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-dirty", "refuse-dirty"))
					return err
				}
			},
			wantKind:   KindInvalidOperation,
			wantDetail: []string{"dirty"},
		},
		{
			name: "unproven live branch",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, claimBranch := f.claimWork("work-refuse-live")
				f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
				f.git(path, "checkout", "-q", "-b", "unproven/work-refuse-live", "origin/main")
				if err := writeFile(filepath.Join(path, "unique.txt"), "unique\n"); err != nil {
					t.Fatal(err)
				}
				f.git(path, "add", "unique.txt")
				f.git(path, "commit", "-m", "unique")
				return f, path, func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-live", "refuse-live"))
					return err
				}
			},
			wantKind:   KindInvalidOperation,
			wantDetail: []string{"unproven/work-refuse-live", "not reachable from remote refs"},
		},
		{
			name: "unproven detached head",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, claimBranch := f.claimWork("work-refuse-detached")
				f.git(f.repoRoot, "push", "-q", "origin", claimBranch)
				f.git(path, "checkout", "-q", "--detach", "origin/main")
				if err := writeFile(filepath.Join(path, "detached.txt"), "detached\n"); err != nil {
					t.Fatal(err)
				}
				f.git(path, "add", "detached.txt")
				f.git(path, "commit", "-m", "detached unique")
				return f, path, func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-detached", "refuse-detached"))
					return err
				}
			},
			wantKind:   KindInvalidOperation,
			wantDetail: []string{"detached", "not reachable from remote refs"},
		},
		{
			name: "unpublished live lesson",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _, correctionBranch, _ := f.correctionScenario("work-refuse-lesson")
				lessonPath := filepath.Join(path, knowledgeRecordTreeDir, "lesson-refuse.json")
				if err := os.MkdirAll(filepath.Dir(lessonPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := writeFile(lessonPath, `{"id":"lesson-refuse","kind":"lesson"}`+"\n"); err != nil {
					t.Fatal(err)
				}
				f.git(path, "add", knowledgeRecordTreeDir)
				f.git(path, "commit", "-m", "lesson")
				f.git(f.repoRoot, "push", "-q", "origin", correctionBranch)
				return f, path, func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-lesson", "refuse-lesson"))
					return err
				}
			},
			wantKind:   KindWorktreeUnpublishedLesson,
			wantDetail: []string{"lesson-refuse.json"},
		},
		{
			name: "live occupant",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _, _, _ := f.correctionScenario("work-refuse-occupied")
				f.completeWork("work-refuse-occupied")
				writeProtectingHostLease(t, f.s)
				setWorktreeOccupant(t, f.s, "work-refuse-occupied", "ses-refuse")
				return f, path, func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-occupied", "refuse-occupied"))
					return err
				}
			},
			wantKind: KindWorktreeOwnershipConflict,
		},
		{
			name: "non-terminal destroy without approval",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _ := f.claimWork("work-refuse-nonterminal")
				return f, path, func() error {
					_, err := destroyWorktreeForTest(context.Background(), f.s, WorktreeDestroyRequest{
						WorkID: "work-refuse-nonterminal", ProjectID: "project-w", DefaultRef: "origin/main",
						PrincipalRef: "principal-1", RequestID: "refuse-nonterminal",
					})
					return err
				}
			},
			wantKind:   KindInvalidTransition,
			wantDetail: []string{"not merged terminal work"},
		},
		{
			name: "destructive removal without approval",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _ := f.claimWork("work-refuse-destructive")
				f.completeWork("work-refuse-destructive")
				if err := writeFile(filepath.Join(path, "tracked.txt"), "dirty\n"); err != nil {
					t.Fatal(err)
				}
				return f, path, func() error {
					_, err := destroyWorktreeForTest(context.Background(), f.s, WorktreeDestroyRequest{
						WorkID: "work-refuse-destructive", ProjectID: "project-w", DefaultRef: "origin/main",
						Destructive: true, PrincipalRef: "principal-1", RequestID: "refuse-destructive",
					})
					return err
				}
			},
			wantKind:   KindInvalidOperation,
			wantDetail: []string{"operator approval"},
		},
		{
			name: "started work outside the unstarted gate",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _ := f.claimWork("work-refuse-unstarted")
				f.completeWork("work-refuse-unstarted")
				return f, path, func() error {
					req := f.reclaimRequest("work-refuse-unstarted", "refuse-unstarted")
					req.RequireUnstarted = true
					_, err := f.s.ReclaimWorktree(context.Background(), req)
					return err
				}
			},
			wantKind:   KindInvalidTransition,
			wantDetail: []string{"not unstarted work"},
		},
		{
			name: "no active worktree",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				workID := "work-refuse-none"
				if err := ApplyOperation(context.Background(), f.s, Operation{Events: []Event{
					{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"no worktree","priority":1}`)},
					{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
				}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
					t.Fatal(err)
				}
				return f, "", func() error {
					req := f.reclaimRequest(workID, "refuse-none")
					_, err := f.s.ReclaimWorktree(context.Background(), req)
					return err
				}
			},
			wantKind:   KindProjectionNotFound,
			wantDetail: []string{"no active worktree"},
		},
		{
			name: "unresolvable default ref",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _ := f.claimWork("work-refuse-default")
				f.git(f.repoRoot, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
				return f, path, func() error {
					req := f.reclaimRequest("work-refuse-default", "refuse-default")
					req.DefaultRef = ""
					_, err := f.s.ReclaimWorktree(context.Background(), req)
					return err
				}
			},
			wantKind:   KindGitUnreachable,
			wantDetail: []string{"cannot resolve the default branch"},
		},
		{
			name: "unreachable worktree status probe",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, _, _, _ := f.correctionScenario("work-refuse-status")
				return f, path, func() error {
					req := f.reclaimRequest("work-refuse-status", "refuse-status")
					req.Runner = con829FailingGitRunner{failPrefix: "status"}
					_, err := f.s.ReclaimWorktree(context.Background(), req)
					return err
				}
			},
			wantKind:   KindGitUnreachable,
			wantDetail: []string{"cannot read worktree status"},
		},
		{
			name: "surviving directory changed after the recorded plan",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				path, branch := f.claimWork("work-refuse-changed")
				f.git(f.repoRoot, "push", "-q", "origin", branch)
				removal := con829CommitRemoval(t, f, f.reclaimRequest("work-refuse-changed", "refuse-changed"))
				if _, err := RunWorktreeNativeRemoval(context.Background(), con829FailingGitRunner{failPrefix: "worktree remove"}, removal); err == nil {
					t.Fatal("expected the injected directory failure to surface")
				}
				f.git(path, "checkout", "-q", "-b", "changed/after-plan")
				if err := writeFile(filepath.Join(path, "changed.txt"), "changed\n"); err != nil {
					t.Fatal(err)
				}
				f.git(path, "add", "changed.txt")
				f.git(path, "commit", "-m", "changed after the plan")
				return f, path, func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-changed", "refuse-changed-retry"))
					return err
				}
			},
			wantKind:   KindProjectionConflict,
			wantDetail: []string{"recorded removal plan"},
		},
		{
			name: "unparsable recorded reclaim facts",
			setup: func(t *testing.T) (*con829Fixture, string, func() error) {
				f := con829FixtureNew(t)
				_, branch := f.claimWork("work-refuse-facts")
				f.git(f.repoRoot, "push", "-q", "origin", branch)
				if _, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-facts", "refuse-facts")); err != nil {
					t.Fatal(err)
				}
				if _, err := f.s.DatabaseForTesting().ExecContext(context.Background(),
					`INSERT INTO fold_guard(active) VALUES(1); UPDATE worktree_entries SET git_facts='### not json' WHERE set_id=?; DELETE FROM fold_guard`,
					WorktreeSetID("work-refuse-facts")); err != nil {
					t.Fatal(err)
				}
				return f, "", func() error {
					_, err := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest("work-refuse-facts", "refuse-facts-retry"))
					return err
				}
			},
			wantKind:   KindInvalidPayload,
			wantDetail: []string{"unparsable"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, path, invoke := tc.setup(t)
			err := invoke()
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != tc.wantKind {
				t.Fatalf("refusal must be typed %s, got %v", tc.wantKind, err)
			}
			for _, part := range tc.wantDetail {
				if !strings.Contains(failure.Detail, part) {
					t.Fatalf("refusal detail must contain %q, got %q", part, failure.Detail)
				}
			}
			if path != "" && f.worktreeGone(path) {
				t.Fatal("the refused worktree must remain")
			}
		})
	}
}

// TestWorktreeRefPhaseMatrixCoversAllOrderedPairs generates the negative
// phase matrix from the owning transition table
// (worktreeRefPhaseTransitions) and drives it through the durable
// settlement owner, not by comparing helpers derived from the same table.
// All 49 ordered pairs of the seven phases walk a real worktree_ref_outcomes
// row: a pair the table admits (or the same-phase idempotent re-record)
// folds a work.worktree_removal_settled event and moves the row; every
// other pair refuses typed inside the fold and leaves the row unchanged.
// The refusal reason for a refused pair is the enumerated structural reason
// — terminal_phase from a terminal predecessor, stale_predecessor
// otherwise — and the five live-state refusal reasons are exercised
// behaviorally by the native boundary tests in this suite.
func TestWorktreeRefPhaseMatrixCoversAllOrderedPairs(t *testing.T) {
	steps := []string{WorktreeRefStepObserveCheckouts, WorktreeRefStepDeletePinned, WorktreeRefStepObservePostDelete, WorktreeRefStepRestoreExpected}
	if got := len(worktreeRefPhases); got != 7 {
		t.Fatalf("the owner vocabulary must hold exactly seven phases, got %d", got)
	}
	f := con829FixtureNew(t)
	workID := "work-phase-matrix"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	tip := f.refTip(branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "phase-matrix"))
	setID := WorktreeSetID(removal.WorkID)
	pairs, admitted, refused := 0, 0, 0
	for _, from := range worktreeRefPhases {
		for _, to := range worktreeRefPhases {
			pairs++
			wantAdmitted := from == to || refPhaseTransitionAdmitted(from, to)
			// Seed the durable row at the predecessor phase directly: the
			// seeding is fold-guarded SQL the reclaim fold itself could
			// have written, so the settlement under test is the only
			// behavior exercised.
			seed := func(t *testing.T) {
				t.Helper()
				if _, err := f.s.db.ExecContext(context.Background(),
					`INSERT INTO fold_guard(active) VALUES(1);
					 UPDATE worktree_ref_outcomes SET phase=?, tip=?, reason='matrix seed' WHERE set_id=? AND project_id=? AND claim_op_id=? AND claim_incarnation=? AND branch=?;
					 DELETE FROM fold_guard`,
					from, tip, setID, removal.ProjectID, removal.ClaimOpID, removal.ClaimIncarnation, branch); err != nil {
					t.Fatal(err)
				}
			}
			t.Run(from+" to "+to, func(t *testing.T) {
				seed(t)
				tx, err := f.s.db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err := enterFold(context.Background(), tx); err != nil {
					t.Fatal(err)
				}
				payload, _ := json.Marshal(worktreeRemovalSettledPayload{
					SetID: setID, ProjectID: removal.ProjectID, ClaimOpID: removal.ClaimOpID, ClaimIncarnation: removal.ClaimIncarnation,
					Branch: branch, Tip: tip, Phase: to, Reason: "matrix settlement",
				})
				foldErr := foldWorktreeRemovalSettled(context.Background(), tx, Event{
					EventID: "matrix-" + from + "-" + to, Kind: "work.worktree_removal_settled",
					SubjectType: SubjectWorkItem, SubjectID: removal.WorkID,
					OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: payload,
				})
				if foldErr != nil {
					if err := leaveFold(context.Background(), tx); err != nil {
						t.Fatal(err)
					}
					if wantAdmitted {
						t.Fatalf("the owner table admits %s→%s but the durable settlement fold refused: %v", from, to, foldErr)
					}
					var failure *Failure
					if !errors.As(foldErr, &failure) || failure.Kind != KindProjectionConflict {
						t.Fatalf("a refused phase pair must refuse typed as a projection conflict, got %v", foldErr)
					}
					// A refused settlement keeps the row at its recorded
					// phase: refusal never moves durable state.
					var kept string
					if err := tx.QueryRowContext(context.Background(),
						`SELECT phase FROM worktree_ref_outcomes WHERE set_id=? AND project_id=? AND claim_op_id=? AND claim_incarnation=? AND branch=?`,
						setID, removal.ProjectID, removal.ClaimOpID, removal.ClaimIncarnation, branch).Scan(&kept); err != nil {
						t.Fatal(err)
					}
					if kept != from {
						t.Fatalf("a refused %s→%s settlement moved the row to %s", from, to, kept)
					}
					return
				}
				if err := leaveFold(context.Background(), tx); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if !wantAdmitted {
					t.Fatalf("no recorded step admits %s→%s, yet the durable settlement fold applied it", from, to)
				}
				var moved string
				if err := f.s.db.QueryRowContext(context.Background(),
					`SELECT phase FROM worktree_ref_outcomes WHERE set_id=? AND project_id=? AND claim_op_id=? AND claim_incarnation=? AND branch=?`,
					setID, removal.ProjectID, removal.ClaimOpID, removal.ClaimIncarnation, branch).Scan(&moved); err != nil {
					t.Fatal(err)
				}
				if moved != to {
					t.Fatalf("an admitted %s→%s settlement left the row at %s", from, to, moved)
				}
			})
			if wantAdmitted {
				admitted++
			} else {
				refused++
				// The structural refusal reason for the pair is enumerated:
				// terminal predecessors refuse every step as terminal_phase,
				// and every pending predecessor refuses at least one step as
				// stale_predecessor.
				if terminalRefPhase(from) {
					if worktreeRefStepRefusal(WorktreeRefStepDeletePinned, from) != WorktreeRefRefusalTerminalPhase {
						t.Fatalf("pair %s→%s from a terminal phase must refuse as %s", from, to, WorktreeRefRefusalTerminalPhase)
					}
				} else {
					staleSeen := false
					for _, step := range steps {
						if worktreeRefStepRefusal(step, from) == WorktreeRefRefusalStalePredecessor {
							staleSeen = true
						}
					}
					if !staleSeen {
						t.Fatalf("pending pair %s→%s must carry %s for a step that cannot start from %s", from, to, WorktreeRefRefusalStalePredecessor, from)
					}
				}
			}
		}
	}
	if pairs != 49 {
		t.Fatalf("the ordered matrix must hold 49 phase pairs, walked %d", pairs)
	}
	if admitted == 0 || refused == 0 {
		t.Fatalf("the matrix must exercise admitted and refused pairs, got %d admitted and %d refused", admitted, refused)
	}
	// Every step's permission is exercised through the union the fold
	// validates, and the delete step starts only from a persisted
	// checkout_proven_absent authorization.
	if worktreeRefStepRefusal(WorktreeRefStepDeletePinned, WorktreeRefPhasePlanned) != WorktreeRefRefusalStalePredecessor {
		t.Fatal("delete_pinned must not start from planned: only a persisted checkout_proven_absent authorizes the native deletion")
	}
	if !worktreeRefStepTarget(WorktreeRefStepDeletePinned, WorktreeRefPhaseCheckoutProvenAbsent, WorktreeRefPhaseDeleted) {
		t.Fatal("the owner must admit checkout_proven_absent→deleted")
	}
	if !worktreeRefStepTarget(WorktreeRefStepObservePostDelete, WorktreeRefPhaseDeleted, WorktreeRefPhaseSettled) ||
		!worktreeRefStepTarget(WorktreeRefStepObservePostDelete, WorktreeRefPhaseDeleted, WorktreeRefPhaseRestorationOwed) {
		t.Fatal("the owner must admit deleted→settled and deleted→restoration_owed")
	}
	if !worktreeRefStepTarget(WorktreeRefStepRestoreExpected, WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseRestored) {
		t.Fatal("the owner must admit restoration_owed→restored")
	}
	if !refPhaseTransitionAdmitted(WorktreeRefPhasePlanned, WorktreeRefPhaseSettled) || !refPhaseTransitionAdmitted(WorktreeRefPhaseRestorationOwed, WorktreeRefPhaseSettled) {
		t.Fatal("an absent pinned ref must converge its debt from every pending phase through a complete inventory")
	}
	if refPhaseTransitionAdmitted(WorktreeRefPhaseDeleted, WorktreeRefPhaseRestored) || refPhaseTransitionAdmitted(WorktreeRefPhasePlanned, WorktreeRefPhaseDeleted) {
		t.Fatal("no step may jump to restored without a recorded restoration debt, nor to deleted without the persisted authorization")
	}
	if refPhaseTransitionAdmitted(WorktreeRefPhaseSettled, WorktreeRefPhasePlanned) ||
		refPhaseTransitionAdmitted(WorktreeRefPhaseRetainedUnproven, WorktreeRefPhasePlanned) ||
		refPhaseTransitionAdmitted(WorktreeRefPhaseRestored, WorktreeRefPhasePlanned) {
		t.Fatal("terminal phases admit no step")
	}
	// The five live-state refusal reasons the boundary observes are
	// enumerated beside the table and exercised behaviorally by the tests
	// below and around this one: holder_present, moved_from_pinned_tip,
	// symbolic_ref, protected_default, inventory_unproven.
	structural := []string{WorktreeRefRefusalHolderPresent, WorktreeRefRefusalMovedTip, WorktreeRefRefusalSymbolicRef, WorktreeRefRefusalProtectedDefault, WorktreeRefRefusalInventoryUnknown}
	for _, reason := range structural {
		if reason == "" {
			t.Fatal("the enumerated live-state refusal vocabulary must not hold an empty reason")
		}
	}
}

// TestReclaimPreReadHolderRefusesDeletionAndKeepsPhase exercises the
// holder_present refusal through the native boundary: a branch already
// checked out in another worktree when the run starts refuses the pinned
// deletion without moving the durable row, and a replay converges once the
// holder releases the ref.
func TestReclaimPreReadHolderRefusesDeletionAndKeepsPhase(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-matrix-holder"
	path, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	// The claim checkout releases the ref, so the only holder is the second
	// worktree the run's pre-read observation must find.
	f.git(path, "checkout", "-q", "--detach")
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "matrix-holder"))
	other := filepath.Join(t.TempDir(), "holder-checkout")
	// The holder exists before the run: the pre-read observation itself
	// refuses, unlike the post-delete boundary race.
	f.git(f.repoRoot, "worktree", "add", other, branch)
	err := f.s.FinishWorktreeNativeRemoval(context.Background(), nil, removal)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "checked out") {
		t.Fatalf("a pre-read holder must refuse the deletion typed, got %v", err)
	}
	if !f.worktreeGone(path) {
		t.Fatal("the independently safe directory phase must complete")
	}
	if !f.refExists(branch) {
		t.Fatal("the held branch must survive the refused deletion")
	}
	if kind, reason := f.refOutcomeKind(t, branch); kind != WorktreeRefPhasePlanned || !strings.Contains(reason, "checked out") {
		t.Fatalf("the holder refusal must keep the row owed at %s with the holder reason, got %s (%s)", WorktreeRefPhasePlanned, kind, reason)
	}
	// The holder releases the ref; the replay converges the deletion.
	f.git(other, "checkout", "-q", "--detach")
	if _, retryErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "matrix-holder-replay")); retryErr != nil {
		t.Fatalf("the replay must converge the deletion once the holder releases the ref, got %v", retryErr)
	}
	if f.refExists(branch) {
		t.Fatal("the converged deletion must remove the ref")
	}
}

// TestReclaimSettlementCommitsNoStrandedFoldGuard ports the epoch-8
// coordinator replay diagnosis into the durable suite: a native run that
// already recorded a phase inline settles the same outcome again after the
// run, and that duplicate settlement must converge without committing an
// unclosed fold scope. A committed stray guard would wedge every later
// write behind the fold invariant, which is exactly what blocked the
// recorded replay from resolving restoration debt.
func TestReclaimSettlementCommitsNoStrandedFoldGuard(t *testing.T) {
	f := con829FixtureNew(t)
	workID := "work-settlement-guard"
	_, branch := f.claimWork(workID)
	f.git(f.repoRoot, "push", "-q", "origin", branch)
	pinned := f.refTip(branch)
	removal := con829CommitRemoval(t, f, f.reclaimRequest(workID, "settlement-guard"))
	other := filepath.Join(t.TempDir(), "raced-checkout")
	runner := &con829NativePhaseRunner{failInventory: "after"}
	runner.beforeDelete = func() { f.git(f.repoRoot, "worktree", "add", other, branch) }
	if err := f.s.FinishWorktreeNativeRemoval(context.Background(), runner, removal); err == nil {
		t.Fatal("the interrupted native run must surface its failure")
	}
	assertNoStrandedFoldGuard(t, f)
	if kind, _ := f.refOutcomeKind(t, branch); kind != WorktreeRefPhaseRestorationOwed {
		t.Fatalf("the interrupted recovery must be durable as %s, got %s", WorktreeRefPhaseRestorationOwed, kind)
	}
	// The replay resolves the debt by restoring the ref at its pinned tip
	// for the raced checkout; the restored-but-held ref reports its
	// protection beside the converged removal, so a typed report — not a
	// git failure — is the resolved shape.
	_, retryErr := f.s.ReclaimWorktree(context.Background(), f.reclaimRequest(workID, "settlement-guard-replay"))
	var failure *Failure
	if !errors.As(retryErr, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "restored at its pinned tip") {
		t.Fatalf("the replay must resolve the debt and report the restored protection, got %v", retryErr)
	}
	assertNoStrandedFoldGuard(t, f)
	if kind, _ := f.refOutcomeKind(t, branch); kind != WorktreeRefPhaseRestored {
		t.Fatalf("the resolved restoration must settle as %s, got %s", WorktreeRefPhaseRestored, kind)
	}
	if tip := f.refTip(branch); tip != pinned {
		t.Fatalf("the restoration must land at the pinned tip %s, got %s", pinned, tip)
	}
}

// assertNoStrandedFoldGuard pins the transaction-ownership invariant the
// settlement must hold: no fold_guard row survives a committed settlement
// pass (CD-0195 D2, fold.go).
func assertNoStrandedFoldGuard(t *testing.T, f *con829Fixture) {
	t.Helper()
	var guards int
	if err := f.s.db.QueryRow(`SELECT COUNT(*) FROM fold_guard`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if guards != 0 {
		t.Fatalf("the settlement committed %d fold guard row(s); the fold scope must close before commit", guards)
	}
}

// TestWorktreeRefPhaseReplayResumesRecordedPredecessor exercises the
// owner's replay entry: pendingRecordedDeletions retires exactly the
// terminal phases and carries every pending phase into the removal, so
// each native step resumes from its recorded predecessor and a terminal
// row never restarts work. A restored ref stays pending: its checkout
// protection tracks a live condition a later replay re-derives.
func TestWorktreeRefPhaseReplayResumesRecordedPredecessor(t *testing.T) {
	recorded := []WorktreeBranchDeletion{{Branch: "correction/one", ExpectedTip: "a", Reason: "pinned"}}
	cases := []struct {
		phase   string
		pending bool
	}{
		{WorktreeRefPhasePlanned, true},
		{WorktreeRefPhaseCheckoutProvenAbsent, true},
		{WorktreeRefPhaseDeleted, true},
		{WorktreeRefPhaseRestorationOwed, true},
		{WorktreeRefPhaseRestored, true},
		{WorktreeRefPhaseRetainedUnproven, false},
		{WorktreeRefPhaseSettled, false},
		{"", true},
	}
	for _, tc := range cases {
		prior := []worktreeRefOutcome{}
		if tc.phase != "" {
			prior = append(prior, worktreeRefOutcome{branch: "correction/one", phase: tc.phase, reason: "recorded"})
		}
		got := pendingRecordedDeletions(recorded, prior)
		if tc.pending && (len(got) != 1 || got[0].Phase != tc.phase) {
			t.Fatalf("phase %q must stay pending and resume with its recorded phase, got %+v", tc.phase, got)
		}
		if !tc.pending && len(got) != 0 {
			t.Fatalf("terminal phase %q must retire the deletion, got %+v", tc.phase, got)
		}
	}
}
