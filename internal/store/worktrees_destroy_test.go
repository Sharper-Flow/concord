package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// CD-0096 D3/D5 store coverage: the Destroy tier (terminal gate, unchanged
// git gates, destructive removal under approval), and the session-continuity
// re-pin of held verify leases.

// seedWorktreeLifecycle moves a fixture work item to a terminal lifecycle.
func seedWorktreeLifecycle(t *testing.T, s *Store, workID, to string, expected int64) {
	t.Helper()
	payload := `{"from":"needed","to":"` + to + `","reason":"fixture terminal","evidence_refs":["fixture"],"expected_version":` + strconv.FormatInt(expected, 10) + `,"resulting_version":` + strconv.FormatInt(expected+1, 10) + `}`
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: workID + "-wt-" + to, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDestroyMergedTerminalWorkReclaims(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)

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
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
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
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
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
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
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
	seedWorktreeLifecycle(t, s, "work-w", "completed", 3)
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

func TestContinuityRePinsHeldLease(t *testing.T) {
	s, _, _ := worktreeFixture(t)
	seedWorktreeContinuityWorkflow(t, s, "work-w")
	owner := SessionWorktreeOwner{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1"}
	stamp := time.Unix(25, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		"lease-1", "work-w", "project-w", "/worktrees/project-w/work-w", "held", owner.ClientRef, owner.AgentRef, owner.SessionRef, "principal-1", `["go","test","./..."]`, stamp, "running"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5, Owner: &owner})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ActiveVerifyLeases) != 1 || snapshot.ActiveVerifyLeases[0].LeaseID != "lease-1" || len(snapshot.ActiveVerifyLeases[0].Command) != 3 {
		t.Fatalf("active_verify_leases=%+v", snapshot.ActiveVerifyLeases)
	}

	// Without a session identity the projection stays work-keyed: the
	// session-boot bytes carry no lease.
	plain, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "work-w", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.ActiveVerifyLeases) != 0 {
		t.Fatalf("plain snapshot leases=%d, want the work-keyed projection", len(plain.ActiveVerifyLeases))
	}
}
