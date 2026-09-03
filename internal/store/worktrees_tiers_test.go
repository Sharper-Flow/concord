package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// CD-0096 D3 tier coverage (part 3a of #689): Inspect reads one active
// same-Project worktree without a lease and without touching the persistent
// target; Verify runs a command under an exclusive lease and refuses typed
// completion when tracked files changed. The fake git runner drives the
// coordination paths; one real-git test drives the native command path.

func claimFixtureWorktree(t *testing.T, s *Store, git *fakeWorktreeGit) WorktreeEntry {
	t.Helper()
	result, err := s.ClaimWorktree(context.Background(), baseClaim(git))
	if err != nil {
		t.Fatal(err)
	}
	return result.Entry
}

func inspectRequest(git *fakeWorktreeGit, mode, path string) WorktreeInspectRequest {
	return WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: mode, Path: path, Runner: git}
}

func TestInspectWorktreeReadsStatusDiffWithoutLeaseOrTarget(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	clean, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, ""))
	if err != nil {
		t.Fatal(err)
	}
	if clean.Content != "" || clean.Truncated || clean.Branch != entry.Branch || clean.Path != entry.Path {
		t.Fatalf("clean status=%+v", clean)
	}
	git.dirty[entry.Path] = true
	dirty, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, ""))
	if err != nil {
		t.Fatal(err)
	}
	if dirty.Content != "M file\n" {
		t.Fatalf("dirty status=%q", dirty.Content)
	}
	diff, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeDiff, ""))
	if err != nil {
		t.Fatal(err)
	}
	if diff.Content != "M file\n" {
		t.Fatalf("dirty diff=%q", diff.Content)
	}
	// The Inspect tier takes no lease.
	var leases int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("leases=%d, want a pure read", leases)
	}
}

func TestInspectWorktreeRequiresActiveEntryInSessionProject(t *testing.T) {
	s, git, _ := worktreeFixture(t)

	_, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, ""))
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found before any claim", err)
	}
	claimFixtureWorktree(t, s, git)

	// The entry lives in project-w; a session anchored in another Project
	// cannot see it, which is the same-Project tier boundary.
	_, err = s.InspectWorktree(context.Background(), WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-other", Mode: WorktreeInspectModeStatus, Runner: git})
	if failureKind(err) != KindProjectionNotFound {
		t.Fatalf("err=%v, want projection_not_found across Projects", err)
	}
}

func TestInspectWorktreeValidatesModeAndSelector(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)

	if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, "log", "")); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want invalid mode refusal", err)
	}
	if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeStatus, "README.md")); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want path refusal outside file mode", err)
	}
	if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeFile, "")); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want empty selector refusal", err)
	}
	for _, selector := range []string{"/etc/passwd", "../escape", "a/../../escape"} {
		if _, err := s.InspectWorktree(context.Background(), inspectRequest(git, WorktreeInspectModeFile, selector)); failureKind(err) != KindInvalidOperation {
			t.Fatalf("selector %q err=%v, want traversal refusal", selector, err)
		}
	}
}

func verifyRequest(git *fakeWorktreeGit, leaseID string, command []string, run func(ctx context.Context, dir string, command []string, maxOutput int) (int, []byte, bool, error)) WorktreeVerifyRequest {
	return WorktreeVerifyRequest{
		Owner: SessionWorktreeOwner{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1"}, WorkID: "work-w", ProjectID: "project-w",
		Command: command, LeaseID: leaseID, PrincipalRef: "principal-1", RequestID: "req-v-" + leaseID,
		Now: time.Unix(20, 0).UTC(), Runner: git, RunCommand: run,
	}
}

func TestVerifyWorktreeRunsUnderLeaseAndReleases(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	var commands [][]string
	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-1", []string{"go", "test", "./..."}, func(_ context.Context, _ string, command []string, _ int) (int, []byte, bool, error) {
		commands = append(commands, command)
		return 0, []byte("all good"), false, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Output != "all good" || result.TrackedFilesChanged {
		t.Fatalf("result=%+v", result)
	}
	if len(commands) != 1 {
		t.Fatalf("command runs=%d, want one leased run", len(commands))
	}
	var state, outcome string
	if err := s.db.QueryRow(`SELECT state,outcome FROM worktree_verify_leases WHERE lease_id='lease-1'`).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "released" || outcome != "completed" {
		t.Fatalf("lease state=%q outcome=%q, want released/completed", state, outcome)
	}
	if result.Path != entry.Path {
		t.Fatalf("result path=%q entry path=%q", result.Path, entry.Path)
	}
}

func TestVerifyWorktreeRefusesWhenTrackedFilesChange(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-1", []string{"make", "check"}, func(_ context.Context, dir string, _ []string, _ int) (int, []byte, bool, error) {
		git.dirty[dir] = true
		return 1, []byte("edited the subject"), false, nil
	}))
	if failureKind(err) != KindWorktreeVerifyMutated {
		t.Fatalf("err=%v, want worktree_verify_mutated", err)
	}
	if !result.TrackedFilesChanged || result.ExitCode != 1 {
		t.Fatalf("result=%+v, want the recorded refused outcome", result)
	}
	var state, outcome string
	if err := s.db.QueryRow(`SELECT state,outcome FROM worktree_verify_leases WHERE lease_id='lease-1'`).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "released" || outcome != "refused_mutated" {
		t.Fatalf("lease state=%q outcome=%q, want released/refused_mutated", state, outcome)
	}
	if !strings.Contains(err.Error(), entry.Path) {
		t.Fatalf("message=%q, want the worktree named", err.Error())
	}
}

func TestVerifyWorktreeConcurrentLeaseRefusesTypedNamingHolder(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	var wg sync.WaitGroup
	ready := make(chan struct{})
	release := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-holder", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
			close(ready)
			<-release
			return 0, nil, false, nil
		}))
		if err != nil {
			t.Errorf("holder verify: %v", err)
		}
	}()
	<-ready
	refused, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-contender", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		t.Error("contender command must not run")
		return 0, nil, false, nil
	}))
	if failureKind(err) != KindWorktreeLeaseHeld {
		t.Fatalf("err=%v, want worktree_lease_held", err)
	}
	if !strings.Contains(err.Error(), "session-1") || !strings.Contains(err.Error(), entry.Path) {
		t.Fatalf("message=%q, want holder session and worktree named", err.Error())
	}
	_ = refused
	close(release)
	wg.Wait()
	var held int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("held leases=%d after release, want zero", held)
	}
}

func TestVerifyWorktreeSameLeaseResumesOnlyPinnedCommand(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	// A held lease from an interrupted run: same owner, pinned command.
	commandJSON, _ := json.Marshal([]string{"go", "test", "./..."})
	stamp := time.Unix(1, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('lease-resume','work-w','project-w',?, 'held','client-1','agent-1','session-1','principal-1',?,?, 'running')`, entry.Path, string(commandJSON), stamp); err != nil {
		t.Fatal(err)
	}

	// Same lease id, different command: the pinned intent wins.
	if _, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-resume", []string{"make", "check"}, nil)); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want pinned-command refusal", err)
	}

	// Same lease id, same command: the run resumes and releases.
	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-resume", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		return 0, []byte("resumed"), false, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "resumed" {
		t.Fatalf("result=%+v, want the resumed run", result)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM worktree_verify_leases WHERE lease_id='lease-resume'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "released" {
		t.Fatalf("state=%q, want released", state)
	}
}

func TestVerifyWorktreeReleasedLeaseReportsRecordedOutcome(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	recorded := WorktreeVerifyResult{WorkID: "work-w", ProjectID: "project-w", Branch: entry.Branch, Path: entry.Path, LeaseID: "lease-done", Command: []string{"go", "test", "./..."}, ExitCode: 2, Output: "prior run"}
	recordedJSON, _ := json.Marshal(recorded)
	stamp := time.Unix(1, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json) VALUES('lease-done','work-w','project-w',?, 'released','client-1','agent-1','session-1','principal-1','["go"]',?,?,2,'completed',?)`, entry.Path, stamp, stamp, string(recordedJSON)); err != nil {
		t.Fatal(err)
	}

	replayed, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-done", []string{"go"}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		t.Error("released lease must not run the command again")
		return 0, nil, false, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ExitCode != 2 || replayed.Output != "prior run" {
		t.Fatalf("replayed=%+v, want the recorded outcome", replayed)
	}
}

func TestVerifyWorktreeValidatesCommand(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	for _, command := range [][]string{nil, {}, strings.Fields(strings.Repeat("x ", 20))} {
		if _, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-invalid", command, nil)); failureKind(err) != KindInvalidOperation {
			t.Fatalf("command=%v err=%v, want invalid argv refusal", command, err)
		}
	}
}

// realGitTiersFixture seeds one Project and work item against a real git
// repository, claims the canonical worktree through the real runner, and
// returns the store plus the worktree path.
func realGitTiersFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "work-w-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Tiers Work","priority":1}`)},
		{EventID: "work-w-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	gitRunStore(t, repoRoot, "init", "-b", "main")
	gitRunStore(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRunStore(t, repoRoot, "config", "user.name", "Concord Tiers Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repoRoot, "add", "tracked.txt")
	gitRunStore(t, repoRoot, "commit", "-m", "base")
	if err := s.AddProjectLocator(ctx, "project-w", ProjectLocator{ID: "path-w", Kind: LocatorCanonicalPath, Value: repoRoot}, 1); err != nil {
		t.Fatal(err)
	}
	runner := ExecGitRunner{}
	baseOut, err := runner.Run(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-w", "work-w")
	result, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "tiers-op-1", WorkID: "work-w", ProjectID: "project-w",
		Branch: "work/work-w", BaseSHA: strings.TrimSpace(string(baseOut)), Path: worktreePath,
		PrincipalRef: "principal-1", RequestID: "req-tiers-1",
		ExpectedVersion: 2, Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, result.Entry.Path
}

func gitRunStore(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := ExecGitRunner{}.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestTiersAgainstRealGitInspectFileAndVerifyCommand(t *testing.T) {
	s, worktreePath := realGitTiersFixture(t)
	ctx := context.Background()
	if !filepath.IsAbs(worktreePath) {
		t.Fatalf("worktree path=%q, want absolute", worktreePath)
	}

	file, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeFile, Path: "tracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if file.Content != "base\n" {
		t.Fatalf("file content=%q", file.Content)
	}
	missing, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeFile, Path: "absent.txt"})
	if failureKind(err) != KindProjectionNotFound || missing.Path != "" {
		t.Fatalf("err=%v, want typed missing-file refusal", err)
	}

	verify := WorktreeVerifyRequest{
		Owner: SessionWorktreeOwner{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1"}, WorkID: "work-w", ProjectID: "project-w",
		Command: []string{"true"}, LeaseID: "tiers-lease-1", PrincipalRef: "principal-1", RequestID: "req-tiers-1",
		Now: time.Unix(20, 0).UTC(),
	}
	passed, err := s.VerifyWorktree(ctx, verify)
	if err != nil {
		t.Fatal(err)
	}
	if passed.ExitCode != 0 || passed.TrackedFilesChanged {
		t.Fatalf("passed=%+v", passed)
	}

	verify.Command = []string{"sh", "-c", "echo mutated >> tracked.txt"}
	verify.LeaseID = "tiers-lease-2"
	refused, err := s.VerifyWorktree(ctx, verify)
	if failureKind(err) != KindWorktreeVerifyMutated || !refused.TrackedFilesChanged {
		t.Fatalf("err=%v refused=%+v, want real mutation refusal", err, refused)
	}
	status, err := s.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: "work-w", ProjectID: "project-w", Mode: WorktreeInspectModeStatus})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Content, "tracked.txt") {
		t.Fatalf("status=%q, want the mutated file visible", status.Content)
	}
}
