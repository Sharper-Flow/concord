package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CD-0096 D3/D5 store coverage: Destroy's safe git gates and destructive removal.

// seedWorktreeLifecycle moves a fixture work item to a terminal lifecycle.
func seedWorktreeLifecycle(t *testing.T, s *Store) {
	t.Helper()
	payload := `{"from":"needed","to":"completed","reason":"fixture terminal","evidence_refs":["fixture"],"expected_version":3,"resulting_version":4}`
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "work-w-wt-completed", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 3}})
	if err != nil {
		t.Fatal(err)
	}
}

// destroyWorktreeForTest drives the Destroy tier through the live
// transaction-scoped twin exactly as the store's callers do: the host leases
// and git probe run before the transaction opens, the reclamation event
// commits inside it, and the native removal follows the commit (CD-0195 D2).
func destroyWorktreeForTest(ctx context.Context, s *Store, req WorktreeDestroyRequest) (WorktreeEntry, error) {
	reclaimReq := WorktreeReclaimRequest{
		WorkID: req.WorkID, ProjectID: req.ProjectID, DefaultRef: req.DefaultRef,
		PrincipalRef: req.PrincipalRef, RequestID: req.RequestID,
		ExpectedVersion: req.ExpectedVersion, Now: req.Now, Runner: req.Runner,
		RequireTerminal: true, Destructive: req.Destructive,
	}
	reclaimReq.HostLeases = s.ReadHostLeases()
	runner := reclaimReq.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	probe, err := s.PrepareWorktreeReclaim(ctx, reclaimReq)
	if err != nil {
		return WorktreeEntry{}, err
	}
	var result WorktreeReclaimResult
	if err := s.Transact(ctx, func(tx *Transaction) error {
		var txErr error
		result, txErr = DestroyWorktreeTx(ctx, tx, reclaimReq, probe)
		return txErr
	}); err != nil {
		return WorktreeEntry{}, err
	}
	if err := s.FinishWorktreeNativeRemoval(ctx, runner, result.Removal); err != nil {
		return result.Entry, err
	}
	return result.Entry, nil
}

func TestDestroyMergedTerminalWorkReclaims(t *testing.T) {
	t.Parallel()
	s, worktreePath := realGitTiersFixture(t)
	seedWorktreeLifecycle(t, s)

	entry, err := destroyWorktreeForTest(context.Background(), s, WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-1", Now: time.Unix(30, 0).UTC()})
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

func TestDestroyNonTerminalWithoutApproval(t *testing.T) {
	t.Parallel()
	s, _ := realGitTiersFixture(t)
	_, err := destroyWorktreeForTest(context.Background(), s, WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 3, PrincipalRef: "principal-1", RequestID: "destroy-nt", Now: time.Unix(30, 0).UTC()})
	// CD-0243 D3/D7 retire nonterminal consent; content gates still apply.
	if err != nil {
		t.Fatalf("destroy err=%v, want clean merged nonterminal removal", err)
	}
	entries, entriesErr := s.WorktreeEntries(context.Background(), "work-w")
	if entriesErr != nil || len(entries) != 1 || entries[0].State != worktreeEntryReclaimed {
		t.Fatalf("entries=%+v err=%v, want the worktree reclaimed", entries, entriesErr)
	}
}

func TestDestroyRefusesDirtyTreeAndNamesDestructiveRoute(t *testing.T) {
	t.Parallel()
	s, worktreePath := realGitTiersFixture(t)
	seedWorktreeLifecycle(t, s)
	if err := writeFile(filepath.Join(worktreePath, "tracked.txt"), "dirty\n"); err != nil {
		t.Fatal(err)
	}
	_, err := destroyWorktreeForTest(context.Background(), s, WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-dirty", Now: time.Unix(30, 0).UTC()})
	if err == nil || err.(*Failure).Kind != KindInvalidOperation {
		t.Fatalf("destroy err=%v, want invalid_operation", err)
	}
	if !strings.Contains(err.(*Failure).Detail, "dirty") || !strings.Contains(err.(*Failure).RecoveryAction, "worktree_destroy with destructive=true") {
		t.Fatalf("refusal=%q recovery=%q, want the dirty refusal naming the destructive route", err.(*Failure).Detail, err.(*Failure).RecoveryAction)
	}
}

func TestDestroyDestructiveWithoutApprovalForcesRemoval(t *testing.T) {
	t.Parallel()
	s, worktreePath := realGitTiersFixture(t)
	seedWorktreeLifecycle(t, s)
	if err := writeFile(filepath.Join(worktreePath, "tracked.txt"), "dirty\n"); err != nil {
		t.Fatal(err)
	}
	entry, err := destroyWorktreeForTest(context.Background(), s, WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, Destructive: true,
		PrincipalRef: "principal-1", RequestID: "destroy-force", Now: time.Unix(30, 0).UTC()})
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

func TestDestroyRefusesLocalOnlyCommits(t *testing.T) {
	t.Parallel()
	s, worktreePath := realGitTiersFixture(t)
	seedWorktreeLifecycle(t, s)
	// A clean tree with a commit that no remote ref retains.
	if err := writeFile(filepath.Join(worktreePath, "tracked.txt"), "local-only change\n"); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, worktreePath, "add", "tracked.txt")
	gitRunStore(t, worktreePath, "commit", "-m", "unmerged")
	_, err := destroyWorktreeForTest(context.Background(), s, WorktreeDestroyRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "main",
		ExpectedVersion: 4, PrincipalRef: "principal-1", RequestID: "destroy-local-only", Now: time.Unix(30, 0).UTC()})
	if err == nil || !strings.Contains(err.(*Failure).Detail, "not reachable from remote refs") {
		t.Fatalf("destroy err=%v, want the local-only refusal", err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
