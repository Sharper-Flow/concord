package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CD-0096 D3/D5 store coverage, part 3b of issue #689: the Take over tier
// (release, transfer with operator override, live-launch refusal), the
// Destroy tier (terminal gate, unchanged git gates, destructive removal under
// approval), and the session-continuity re-pin.

func takeoverRequest(git *fakeWorktreeGit, owner SessionWorktreeOwner, workID string, expectedWork, expectedTarget int64, override string) WorktreeTakeoverRequest {
	return WorktreeTakeoverRequest{
		Owner: owner, WorkID: workID,
		ExpectedWorkVersion:   expectedWork,
		ExpectedTargetVersion: expectedTarget,
		OperatorOverrideRef:   override,
		PrincipalRef:          "principal-1", RequestID: "req-tk-" + workID, OpID: "tk-op-" + workID,
		Now: time.Unix(20, 0).UTC(), Runner: git,
	}
}

func TestReleaseSessionWorktreeTransitionsAndFailsClosed(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	owner := retargetOwner("client-1", "agent-1", "session-1")
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, owner, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}

	// A stale pin fails closed before any state moves (CD-0096 D5).
	if _, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: owner, WorkID: "work-w", ExpectedTargetVersion: 5, PrincipalRef: "principal-1", RequestID: "rel-stale", Now: time.Unix(21, 0).UTC()}); err == nil || err.(*Failure).Kind != KindVersionConflict {
		t.Fatalf("stale release err=%v, want version_conflict", err)
	}
	// The binding names its own work item.
	if _, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: owner, WorkID: "work-other", ExpectedTargetVersion: 1, PrincipalRef: "principal-1", RequestID: "rel-wrong", Now: time.Unix(21, 0).UTC()}); err == nil || err.(*Failure).Kind != KindInvalidOperation {
		t.Fatalf("wrong-work release err=%v, want invalid_operation", err)
	}

	released, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: owner, WorkID: "work-w", ExpectedTargetVersion: 1, PrincipalRef: "principal-1", RequestID: "rel-1", Now: time.Unix(21, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if released.State != sessionTargetReleased || released.TargetVersion != 2 {
		t.Fatalf("released=%+v, want released state and bumped version", released)
	}

	// A second release is a refused transition, not an idempotent no-op.
	if _, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: owner, WorkID: "work-w", ExpectedTargetVersion: 2, PrincipalRef: "principal-1", RequestID: "rel-2", Now: time.Unix(22, 0).UTC()}); err == nil || err.(*Failure).Kind != KindInvalidTransition {
		t.Fatalf("second release err=%v, want invalid_transition", err)
	}
	// A session with no binding has nothing to release.
	other := retargetOwner("client-2", "agent-2", "session-2")
	if _, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: other, WorkID: "work-w", ExpectedTargetVersion: 1, PrincipalRef: "principal-1", RequestID: "rel-none", Now: time.Unix(22, 0).UTC()}); err == nil || err.(*Failure).Kind != KindProjectionNotFound {
		t.Fatalf("no-binding release err=%v, want projection_not_found", err)
	}
}

func TestTakeoverRefusesActiveHolderWithoutOverrideNamingOwner(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	first := retargetOwner("client-1", "agent-1", "session-1")
	second := retargetOwner("client-2", "agent-2", "session-2")
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, first, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}

	_, err := s.TakeoverSessionWorktree(context.Background(), takeoverRequest(git, second, "work-w", 2, 0, ""))
	if err == nil || err.(*Failure).Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("takeover err=%v, want worktree_ownership_conflict", err)
	}
	failure := err.(*Failure)
	if !strings.Contains(failure.Detail, sessionOwnerLabel(first)) {
		t.Fatalf("refusal %q must name the owner %s", failure.Detail, sessionOwnerLabel(first))
	}
	if !strings.Contains(failure.RecoveryAction, "release") || !strings.Contains(failure.RecoveryAction, "operator") {
		t.Fatalf("recovery %q must name both recovery routes", failure.RecoveryAction)
	}
	// The refusal recorded nothing: the holder is unchanged.
	holder, held, _, _, err := s.WorktreeTakeoverBlockers(context.Background(), "work-w", second)
	if err != nil || !held || holder.ClientRef != first.ClientRef {
		t.Fatalf("holder=%+v held=%v err=%v, want the original holder", holder, held, err)
	}
}

func TestTakeoverWithOperatorOverrideTransfersAndForceReleasesHolder(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	first := retargetOwner("client-1", "agent-1", "session-1")
	second := retargetOwner("client-2", "agent-2", "session-2")
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, first, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}

	target, err := s.TakeoverSessionWorktree(context.Background(), takeoverRequest(git, second, "work-w", 2, 0, "approval:override-1"))
	if err != nil {
		t.Fatal(err)
	}
	if target.State != sessionTargetActive || target.TargetVersion != 1 || target.WorkID != "work-w" {
		t.Fatalf("target=%+v, want the taker active on the work", target)
	}
	// The prior holder's binding is force-released with a bumped version, so
	// its next pinned operation fails closed (CD-0096 D5).
	prior, found, err := s.SessionWorktreeTarget(context.Background(), first)
	if err != nil || !found {
		t.Fatalf("prior read found=%v err=%v", found, err)
	}
	if prior.State != sessionTargetReleased || prior.TargetVersion != 2 {
		t.Fatalf("prior=%+v, want released with bumped version", prior)
	}
	// One active holder per work item survives the transfer (CD-0096 D6).
	var active int64
	if err := s.db.QueryRow(`SELECT count(*) FROM session_worktree_targets WHERE work_id='work-w' AND state='active'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active holders=%d err=%v, want exactly one", active, err)
	}
	// The prior holder's stale pin fails closed.
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, first, "work-w", 3, 1)); err == nil || err.(*Failure).Kind != KindVersionConflict {
		t.Fatalf("stale re-assert err=%v, want version_conflict", err)
	}
}

func TestTakeoverAfterReleaseNeedsNoOverride(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	first := retargetOwner("client-1", "agent-1", "session-1")
	second := retargetOwner("client-2", "agent-2", "session-2")
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, first, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: first, WorkID: "work-w", ExpectedTargetVersion: 1, PrincipalRef: "principal-1", RequestID: "rel-1", Now: time.Unix(21, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	target, err := s.TakeoverSessionWorktree(context.Background(), takeoverRequest(git, second, "work-w", 2, 0, ""))
	if err != nil {
		t.Fatalf("takeover after release err=%v", err)
	}
	if target.State != sessionTargetActive || target.WorkID != "work-w" {
		t.Fatalf("target=%+v", target)
	}
}

func TestTakeoverMovesCallersOwnBinding(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	seedRetargetWork(t, s, "work-x")
	first := retargetOwner("client-1", "agent-1", "session-1")
	second := retargetOwner("client-2", "agent-2", "session-2")
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, first, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseSessionWorktree(context.Background(), WorktreeReleaseRequest{Owner: first, WorkID: "work-w", ExpectedTargetVersion: 1, PrincipalRef: "principal-1", RequestID: "rel-1", Now: time.Unix(21, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, second, "work-x", 2, 0)); err != nil {
		t.Fatal(err)
	}

	// The takeover is the caller's own binding move: work-x is left unheld
	// and the caller lands on work-w with a bumped version.
	target, err := s.TakeoverSessionWorktree(context.Background(), takeoverRequest(git, second, "work-w", 3, 1, ""))
	if err != nil {
		t.Fatal(err)
	}
	if target.WorkID != "work-w" || target.TargetVersion != 2 {
		t.Fatalf("target=%+v, want the moved binding at version 2", target)
	}
	var xActive int64
	if err := s.db.QueryRow(`SELECT count(*) FROM session_worktree_targets WHERE work_id='work-x' AND state='active'`).Scan(&xActive); err != nil || xActive != 0 {
		t.Fatalf("work-x active holders=%d err=%v, want none after the move", xActive, err)
	}
}

func TestTakeoverRefusesLiveForeignLaunchEvenWithOverride(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	second := retargetOwner("client-2", "agent-2", "session-2")
	seedBootstrapLaunch(t, s, "work-w", "session-launched", "running")

	_, err := s.TakeoverSessionWorktree(context.Background(), takeoverRequest(git, second, "work-w", 2, 0, "approval:override-launch"))
	if err == nil || err.(*Failure).Kind != KindWorktreeOwnershipConflict {
		t.Fatalf("takeover err=%v, want worktree_ownership_conflict", err)
	}
	failure := err.(*Failure)
	if !strings.Contains(failure.Detail, "session-launched") {
		t.Fatalf("refusal %q must name the launched session", failure.Detail)
	}
	if !strings.Contains(failure.RecoveryAction, "end the launched session") {
		t.Fatalf("recovery %q must name the only recovery route", failure.RecoveryAction)
	}
}

func TestTakeoverReassertsSelfBinding(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	owner := retargetOwner("client-1", "agent-1", "session-1")
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, owner, "work-w", 2, 0)); err != nil {
		t.Fatal(err)
	}
	target, err := s.TakeoverSessionWorktree(context.Background(), takeoverRequest(git, owner, "work-w", 2, 1, ""))
	if err != nil {
		t.Fatal(err)
	}
	if target.WorkID != "work-w" || target.TargetVersion != 2 || target.State != sessionTargetActive {
		t.Fatalf("self re-assert target=%+v", target)
	}
}

func TestDestroyMergedTerminalWorkReclaims(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	seedRetargetLifecycle(t, s, "work-w", "completed", 3)

	entry, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-1", Now: time.Unix(30, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v, want the reclaimed state", entry)
	}
	if _, probeErr := (ExecGitRunner{}).Run(context.Background(), worktreePath, "rev-parse", "--abbrev-ref", "HEAD"); probeErr == nil {
		t.Fatalf("worktree still reachable at %s", worktreePath)
	}
}

func TestDestroyRefusesNonTerminalWithoutApproval(t *testing.T) {
	s, _ := realGitTiersFixture(t)
	_, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 3, PrincipalRef: "principal-1", RequestID: "destroy-nt", Now: time.Unix(30, 0).UTC(),
	})
	if err == nil || err.(*Failure).Kind != KindInvalidTransition {
		t.Fatalf("destroy err=%v, want invalid_transition", err)
	}
	if !strings.Contains(err.(*Failure).Detail, "not merged terminal work") {
		t.Fatalf("refusal %q must name the terminal gate", err.(*Failure).Detail)
	}
	entries, entriesErr := s.WorktreeEntries(context.Background(), "work-w")
	if entriesErr != nil || len(entries) != 1 || entries[0].State != worktreeEntryActive {
		t.Fatalf("entries=%+v err=%v, want the worktree untouched", entries, entriesErr)
	}
}

func TestDestroyNonTerminalWithApprovalKeepsGitGates(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	// The approval satisfies the terminal gate; the git gates still run, and
	// a clean merged tree passes them.
	entry, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 3, OperatorApprovalRef: "approval:destroy-nt", PrincipalRef: "principal-1", RequestID: "destroy-nt-2", Now: time.Unix(30, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, probeErr := (ExecGitRunner{}).Run(context.Background(), worktreePath, "rev-parse", "--abbrev-ref", "HEAD"); probeErr == nil {
		t.Fatal("worktree still reachable after destroy")
	}
}

func TestDestroyRefusesDirtyTreeAndNamesDestructiveRoute(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	seedRetargetLifecycle(t, s, "work-w", "completed", 3)
	if err := writeFile(filepath.Join(worktreePath, "tracked.txt"), "dirty\n"); err != nil {
		t.Fatal(err)
	}
	_, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-dirty", Now: time.Unix(30, 0).UTC(),
	})
	if err == nil || err.(*Failure).Kind != KindInvalidOperation {
		t.Fatalf("destroy err=%v, want invalid_operation", err)
	}
	if !strings.Contains(err.(*Failure).Detail, "dirty") || !strings.Contains(err.(*Failure).RecoveryAction, "destructive destroy") {
		t.Fatalf("refusal=%q recovery=%q, want the dirty refusal naming the destructive route", err.(*Failure).Detail, err.(*Failure).RecoveryAction)
	}
}

func TestDestroyDestructiveWithApprovalForcesRemoval(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	seedRetargetLifecycle(t, s, "work-w", "completed", 3)
	if err := writeFile(filepath.Join(worktreePath, "tracked.txt"), "dirty\n"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, OperatorApprovalRef: "approval:destroy-force", Destructive: true,
		PrincipalRef: "principal-1", RequestID: "destroy-force", Now: time.Unix(30, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != worktreeEntryReclaimed {
		t.Fatalf("entry=%+v", entry)
	}
	if _, probeErr := (ExecGitRunner{}).Run(context.Background(), worktreePath, "rev-parse", "--abbrev-ref", "HEAD"); probeErr == nil {
		t.Fatal("dirty worktree still reachable after forced removal")
	}
}

func TestDestroyDestructiveWithoutApprovalRefuses(t *testing.T) {
	s, _ := realGitTiersFixture(t)
	seedRetargetLifecycle(t, s, "work-w", "completed", 3)
	_, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, Destructive: true,
		PrincipalRef: "principal-1", RequestID: "destroy-unapproved", Now: time.Unix(30, 0).UTC(),
	})
	if err == nil || err.(*Failure).Kind != KindInvalidOperation {
		t.Fatalf("destroy err=%v, want the unapproved-destructive refusal", err)
	}
}

func TestDestroyRefusesUnmergedBranch(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	seedRetargetLifecycle(t, s, "work-w", "completed", 3)
	// A clean tree whose content the default branch does not hold.
	if err := writeFile(filepath.Join(worktreePath, "tracked.txt"), "unmerged change\n"); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, worktreePath, "add", "tracked.txt")
	gitRunStore(t, worktreePath, "commit", "-m", "unmerged")
	_, err := s.DestroyWorktree(context.Background(), WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-unmerged", Now: time.Unix(30, 0).UTC(),
	})
	if err == nil || !strings.Contains(err.(*Failure).Detail, "not merged into main") {
		t.Fatalf("destroy err=%v, want the unmerged refusal", err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// seedWorktreeContinuityWorkflow gives the worktree fixture's work item a
// workflow instance, so ReadWorkflowContinuity answers for it.
func seedWorktreeContinuityWorkflow(t *testing.T, s *Store, workID string) {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Unix(5, 0).UTC()}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestContinuityRePinsEffectiveTargetAndHeldLease(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	seedWorktreeContinuityWorkflow(t, s, "work-w")
	owner := retargetOwner("client-1", "agent-1", "session-1")
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	target, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, owner, "work-w", version, 0))
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(25, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		"lease-1", "work-w", "project-w", target.Path, "held", owner.ClientRef, owner.AgentRef, owner.SessionRef, "principal-1", `["go","test","./..."]`, stamp, "running"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5, Owner: &owner})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EffectiveTarget == nil || snapshot.EffectiveTarget.WorkID != "work-w" || snapshot.EffectiveTarget.TargetVersion != 1 || snapshot.EffectiveTarget.State != sessionTargetActive {
		t.Fatalf("effective_target=%+v", snapshot.EffectiveTarget)
	}
	if len(snapshot.ActiveVerifyLeases) != 1 || snapshot.ActiveVerifyLeases[0].LeaseID != "lease-1" || len(snapshot.ActiveVerifyLeases[0].Command) != 3 {
		t.Fatalf("active_verify_leases=%+v", snapshot.ActiveVerifyLeases)
	}

	// Without a session identity the projection stays work-keyed: the
	// session-boot bytes carry neither field.
	plain, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if plain.EffectiveTarget != nil || len(plain.ActiveVerifyLeases) != 0 {
		t.Fatalf("plain snapshot target=%+v leases=%d, want the work-keyed projection", plain.EffectiveTarget, len(plain.ActiveVerifyLeases))
	}
}

func TestContinuityStaleTargetPinFailsClosed(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	seedWorktreeContinuityWorkflow(t, s, "work-w")
	owner := retargetOwner("client-1", "agent-1", "session-1")
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetargetSessionWorktree(context.Background(), retargetRequest(git, owner, "work-w", version, 0)); err != nil {
		t.Fatal(err)
	}

	stale := int64(2)
	if _, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5, Owner: &owner, ExpectedTargetVersion: &stale}); err == nil || err.(*Failure).Kind != KindVersionConflict {
		t.Fatalf("stale pin err=%v, want version_conflict", err)
	}
	current := int64(1)
	if _, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5, Owner: &owner, ExpectedTargetVersion: &current}); err != nil {
		t.Fatalf("current pin err=%v", err)
	}
	// A pin without a session identity is malformed, not ignorable.
	if _, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5, ExpectedTargetVersion: &current}); err == nil || err.(*Failure).Kind != KindInvalidOperation {
		t.Fatalf("anonymous pin err=%v, want invalid_operation", err)
	}
}
