package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type branchProbeFailureRunner struct{}

func (branchProbeFailureRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "show-ref" {
		return nil, errors.New("injected branch probe failure")
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

type branchMoveBeforeDeleteRunner struct {
	branch string
	sha    string
}

func (r branchMoveBeforeDeleteRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "branch -d -- ") {
		if _, err := (ExecGitRunner{}).Run(ctx, dir, "update-ref", "refs/heads/"+r.branch, r.sha); err != nil {
			return nil, err
		}
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

type branchMoveBeforeWorktreeRemovalRunner struct {
	branch    string
	sha       string
	attempted bool
	blocked   bool
}

func (r *branchMoveBeforeWorktreeRemovalRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
		r.attempted = true
		_, err := (ExecGitRunner{}).Run(ctx, dir, "update-ref", "refs/heads/"+r.branch, r.sha)
		r.blocked = err != nil
		if err == nil {
			return nil, errors.New("concurrent branch movement bypassed rollback locks")
		}
	}
	return (ExecGitRunner{}).Run(ctx, dir, args...)
}

func initBootstrapStoreRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runBootstrapGit(t, repo, "init", "-q", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("bootstrap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runBootstrapGit(t, repo, "add", "README.md")
	runBootstrapGit(t, repo, "commit", "-q", "-m", "base")
	runBootstrapGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	runBootstrapGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}

func runBootstrapGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func deadBootstrapOwner(t *testing.T) (int64, string) {
	t.Helper()
	owner := exec.Command("sleep", "60")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerPID := int64(owner.Process.Pid)
	ownerStart, err := processStartIdentity(ownerPID)
	if err != nil {
		_ = owner.Process.Kill()
		t.Fatal(err)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("killed lock owner exited successfully")
	}
	return ownerPID, ownerStart
}

func seedBootstrapStoreAuthority(t *testing.T, s *Store, repo string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "bootstrap-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product-bootstrap", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"bootstrap","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "bootstrap-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project-bootstrap", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"bootstrap"}`)},
		{EventID: "bootstrap-membership", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-bootstrap", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-bootstrap","project_id":"project-bootstrap","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-bootstrap"): 0, VersionRef(SubjectProject, "project-bootstrap"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-bootstrap", ProjectLocator{ID: "bootstrap-path", Kind: LocatorCanonicalPath, Value: repo}, 1); err != nil {
		t.Fatal(err)
	}
}

func bootstrapStoreRequest() BootstrapRequest {
	return BootstrapRequest{ProductID: "product-bootstrap", ProjectID: "project-bootstrap", Title: "Bootstrap", ValueStatement: "Work starts in one worktree", Kind: "task", Task: "run", IdempotencyKey: "bootstrap-store", Priority: 1, Urgency: "standard", Ref: "HEAD"}
}

func bootstrapTestOwner(t *testing.T) (int64, string) {
	t.Helper()
	pid := int64(os.Getpid())
	start, err := processStartIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	return pid, start
}

func TestValidateBootstrapTaskUsesUTF8ByteLimit(t *testing.T) {
	base := BootstrapRequest{ProductID: "product", ProjectID: "project", Title: "title", ValueStatement: "value", Kind: "task", IdempotencyKey: "key"}
	base.Task = strings.Repeat("✓", 2730)
	if len(base.Task) != 8190 {
		t.Fatalf("task bytes=%d", len(base.Task))
	}
	if err := validateBootstrapRequest(base); err != nil {
		t.Fatalf("8190-byte UTF-8 task refused: %v", err)
	}
	base.Task += "✓"
	if len(base.Task) != 8193 {
		t.Fatalf("boundary task bytes=%d", len(base.Task))
	}
	if err := validateBootstrapRequest(base); err == nil {
		t.Fatal("8193-byte UTF-8 task accepted")
	}
}

func TestPrepareBootstrapReplayUsesTransactionPinnedLocation(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.Ref = "moving"
	runBootstrapGit(t, repo, "branch", "moving", "HEAD")
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	firstLocation, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.prepareBootstrap(context.Background(), req, operationID, workID, digest, firstLocation, ExecGitRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("bootstrap moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runBootstrapGit(t, repo, "add", "README.md")
	runBootstrapGit(t, repo, "commit", "-q", "-m", "move ref")
	runBootstrapGit(t, repo, "branch", "-f", "moving", "HEAD")
	secondLocation, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if firstLocation.BaseSHA == secondLocation.BaseSHA {
		t.Fatal("moving ref did not change")
	}
	second, err := s.prepareBootstrap(context.Background(), req, operationID, workID, digest, secondLocation, ExecGitRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Result.Replayed || second.Location != first.Location || second.Location.BaseSHA != firstLocation.BaseSHA {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestBootstrapBranchProbeFailureCreatesNoReplayAuthority(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-probe-failure"
	_, workID, _, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.bootstrapWorktree(context.Background(), req, nil, branchProbeFailureRunner{}); err == nil {
		t.Fatal("branch probe failure was accepted")
	}
	var journals int
	if err := s.db.QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("branch probe failure journal count=%d err=%v", journals, err)
	}
	runBootstrapGit(t, repo, "branch", location.Branch, location.BaseSHA)
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("later request adopted the branch hidden by the failed probe")
	}
	if err := s.db.QueryRow("SELECT count(*) FROM bootstrap_operations").Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("planted branch journal count=%d err=%v", journals, err)
	}
}

func TestBootstrapLaunchReplayPreservesSessionIdentity(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerPID, ownerStart := bootstrapTestOwner(t)
	launch, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, "concord-implement", result.Entry.Path, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if launch.State != "prepared" || launch.SessionID != nil || !launch.SpawnPermitted {
		t.Fatalf("initial launch=%+v", launch)
	}
	contender, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, "concord-implement", result.Entry.Path, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if contender.SpawnPermitted {
		t.Fatalf("concurrent launch was permitted: %+v", contender)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-1", launch.Agent, launch.Directory, "openai/model-1", "completed", "", ownerPID, ownerStart); err == nil {
		t.Fatal("prepared launch moved directly to completed")
	}
	if err := s.StartBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, launch.Agent, launch.Directory, launch.Title, ownerPID, ownerStart, int64(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackBootstrapOperation(context.Background(), result.ProductID, result.WorkID, result.OperationID, result.Entry.Path, "child transport failed", true); err == nil {
		t.Fatal("rollback removed a worktree after the child launch fence")
	}
	active, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if active.SpawnPermitted || active.RollbackPermitted || active.RecoveryLookup {
		t.Fatalf("running launch allowed another action: %+v", active)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-1", launch.Agent, launch.Directory, "", "running", "", ownerPID, ownerStart); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackBootstrapOperation(context.Background(), result.ProductID, result.WorkID, result.OperationID, result.Entry.Path, "session preparation failed", false); err == nil {
		t.Fatal("rollback removed a worktree with a recorded session")
	}
	replay, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if replay.State != "running" || replay.SessionID == nil || *replay.SessionID != "session-1" || replay.SpawnPermitted {
		t.Fatalf("running replay=%+v", replay)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-1", launch.Agent, launch.Directory, "openai/model-1", "completed", "", ownerPID, ownerStart); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-1", launch.Agent, launch.Directory, "openai/model-1", "failed", "late failure", ownerPID, ownerStart); err == nil {
		t.Fatal("completed launch moved to failed")
	}
	completed, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "prepared" || completed.SessionID == nil || *completed.SessionID != "session-1" || completed.Model != "openai/model-1" || !completed.SpawnPermitted {
		t.Fatalf("completed replay=%+v", completed)
	}
	terminalContender, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if terminalContender.SpawnPermitted {
		t.Fatalf("concurrent terminal replay was permitted: %+v", terminalContender)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-2", launch.Agent, launch.Directory, "openai/model-1", "running", "", ownerPID, ownerStart); err == nil {
		t.Fatal("launch accepted a different session identity")
	}
}

func TestBootstrapLaunchFailureWithoutSessionRefusesDuplicateLaunch(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-launch-failure"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerPID, ownerStart := bootstrapTestOwner(t)
	launch, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, "concord-implement", result.Entry.Path, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "", launch.Agent, launch.Directory, "", "failed", "child process did not report a session", ownerPID, ownerStart); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-new", launch.Agent, launch.Directory, "", "running", "", ownerPID, ownerStart); err == nil {
		t.Fatal("failed launch without a session accepted a new running session")
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-new", launch.Agent, launch.Directory, "openai/model-1", "completed", "", ownerPID, ownerStart); err == nil {
		t.Fatal("failed launch moved directly to completed")
	}
	if _, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart); err == nil {
		t.Fatal("failed launch without a session allowed a duplicate launch")
	}
}

func TestBootstrapLaunchOwnerRecoveryIsExclusive(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("sleep", "60")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerPID := int64(owner.Process.Pid)
	ownerStart, err := processStartIdentity(ownerPID)
	if err != nil {
		_ = owner.Process.Kill()
		t.Fatal(err)
	}
	launch, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, "concord-implement", result.Entry.Path, ownerPID, ownerStart)
	if err != nil {
		_ = owner.Process.Kill()
		t.Fatal(err)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-recovery", launch.Agent, launch.Directory, "", "running", "", ownerPID, ownerStart); err != nil {
		_ = owner.Process.Kill()
		t.Fatal(err)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("killed launch owner exited successfully")
	}
	currentPID, currentStart := bootstrapTestOwner(t)
	recovery, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, currentPID, currentStart)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.SpawnPermitted || recovery.RollbackPermitted || recovery.RecoveryLookup || recovery.SessionID == nil || *recovery.SessionID != "session-recovery" {
		t.Fatalf("recovery=%+v", recovery)
	}
	contender, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, currentPID, currentStart)
	if err != nil {
		t.Fatal(err)
	}
	if contender.SpawnPermitted || contender.RollbackPermitted {
		t.Fatalf("recovery contender=%+v", contender)
	}

	staleReq := bootstrapStoreRequest()
	staleReq.IdempotencyKey = "bootstrap-stale-owner"
	stale, err := s.BootstrapWorktree(context.Background(), staleReq, nil)
	if err != nil {
		t.Fatal(err)
	}
	staleOwner := exec.Command("sleep", "60")
	if err := staleOwner.Start(); err != nil {
		t.Fatal(err)
	}
	stalePID := int64(staleOwner.Process.Pid)
	staleStart, err := processStartIdentity(stalePID)
	if err != nil {
		_ = staleOwner.Process.Kill()
		t.Fatal(err)
	}
	if _, err := s.PrepareBootstrapLaunch(context.Background(), stale.ProductID, stale.WorkID, "concord-implement", stale.Entry.Path, stalePID, staleStart); err != nil {
		_ = staleOwner.Process.Kill()
		t.Fatal(err)
	}
	if err := staleOwner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := staleOwner.Wait(); err == nil {
		t.Fatal("killed stale owner exited successfully")
	}
	staleRecovery, err := s.PrepareBootstrapLaunch(context.Background(), stale.ProductID, stale.WorkID, "concord-implement", stale.Entry.Path, currentPID, currentStart)
	if err != nil {
		t.Fatal(err)
	}
	if staleRecovery.SpawnPermitted || !staleRecovery.RollbackPermitted || staleRecovery.SessionID != nil {
		t.Fatalf("stale recovery=%+v", staleRecovery)
	}
}

func TestRollbackBootstrapOperationRemovesOnlyCleanUnlaunchedState(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-clean-rollback"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackBootstrapOperation(context.Background(), result.ProductID, result.WorkID, result.OperationID, repo, "session preparation failed", false); err == nil {
		t.Fatal("rollback accepted the default checkout")
	}
	if err := s.RollbackBootstrapOperation(context.Background(), result.ProductID, result.WorkID, result.OperationID, result.Entry.Path, "session preparation failed", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Entry.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back worktree still exists: %v", err)
	}
	if _, exists, err := bootstrapBranchHead(context.Background(), ExecGitRunner{}, repo, result.Entry.Branch); err != nil || exists {
		t.Fatalf("rolled-back branch exists=%v err=%v", exists, err)
	}
	var operationState, claimState, lifecycle string
	if err := s.db.QueryRow(`SELECT b.state,c.state,w.lifecycle FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id JOIN work_items w ON w.id=b.work_id WHERE b.operation_id=?`, result.OperationID).Scan(&operationState, &claimState, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if operationState != "rolled_back" || claimState != "reclaimed" || lifecycle != "cancelled" {
		t.Fatalf("operation=%s claim=%s lifecycle=%s", operationState, claimState, lifecycle)
	}
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("rolled-back idempotency key was replayed")
	}

	dirtyReq := bootstrapStoreRequest()
	dirtyReq.IdempotencyKey = "bootstrap-dirty-rollback"
	dirty, err := s.BootstrapWorktree(context.Background(), dirtyReq, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty.Entry.Path, "operator.txt"), []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackBootstrapOperation(context.Background(), dirty.ProductID, dirty.WorkID, dirty.OperationID, dirty.Entry.Path, "session preparation failed", false); err == nil {
		t.Fatal("dirty worktree was removed")
	}
	if _, err := os.Stat(filepath.Join(dirty.Entry.Path, "operator.txt")); err != nil {
		t.Fatalf("dirty worktree was not preserved: %v", err)
	}
	if err := s.db.QueryRow(`SELECT state FROM bootstrap_operations WHERE operation_id=?`, dirty.OperationID).Scan(&operationState); err != nil || operationState != "rolling_back" {
		t.Fatalf("dirty operation state=%s err=%v", operationState, err)
	}
}

func TestBootstrapLaunchRecoversAfterFencedProcessDiesWithoutSession(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-dead-launch-process"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerPID, ownerStart := bootstrapTestOwner(t)
	launch, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, "concord-implement", result.Entry.Path, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	process := exec.Command("sleep", "60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.StartBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, launch.Agent, launch.Directory, launch.Title, ownerPID, ownerStart, int64(process.Process.Pid)); err != nil {
		_ = process.Process.Kill()
		t.Fatal(err)
	}
	active, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart)
	if err != nil {
		_ = process.Process.Kill()
		t.Fatal(err)
	}
	if active.SpawnPermitted || active.RollbackPermitted || active.RecoveryLookup {
		_ = process.Process.Kill()
		t.Fatalf("active process recovery=%+v", active)
	}
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("killed launch process exited successfully")
	}
	recovery, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, launch.Agent, launch.Directory, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.SpawnPermitted || recovery.RollbackPermitted || !recovery.RecoveryLookup || recovery.SessionID != nil {
		t.Fatalf("dead process recovery=%+v", recovery)
	}
	if err := s.RollbackBootstrapOperation(context.Background(), result.ProductID, result.WorkID, result.OperationID, result.Entry.Path, "session lookup found no matching session", true); err != nil {
		t.Fatal(err)
	}
}

func TestMigration59PreservesPopulatedBootstrapOperation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-v58.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:len(migrations)-1] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-30T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, path: path}
	repo := initBootstrapStoreRepo(t)
	seedBootstrapStoreAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(ctx, bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var operationID, state, launchState string
	var attemptID, sessionID, ownerStart, processStart sql.NullString
	var ownerPID, processPID sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT operation_id,state,launch_state,launch_attempt_id,launch_session_id,launch_owner_pid,launch_owner_start,launch_process_pid,launch_process_start FROM bootstrap_operations WHERE work_id=?`, result.WorkID).Scan(&operationID, &state, &launchState, &attemptID, &sessionID, &ownerPID, &ownerStart, &processPID, &processStart); err != nil {
		t.Fatal(err)
	}
	if operationID != result.OperationID || state != "completed" || launchState != "not_started" || attemptID.Valid || sessionID.Valid || ownerPID.Valid || ownerStart.Valid || processPID.Valid || processStart.Valid {
		t.Fatalf("operation=%s state=%s launch=%s attempt=%v session=%v owner_pid=%v owner_start=%v process_pid=%v process_start=%v", operationID, state, launchState, attemptID, sessionID, ownerPID, ownerStart, processPID, processStart)
	}
}

func TestRollbackBootstrapDoesNotDeleteConcurrentlyMovedBranch(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-branch-race"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("concurrent branch target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runBootstrapGit(t, repo, "add", "README.md")
	runBootstrapGit(t, repo, "commit", "-q", "-m", "concurrent target")
	newSHA := runBootstrapGit(t, repo, "rev-parse", "HEAD")
	location := WorktreeLocation{Repo: repo, Path: result.Entry.Path, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA}
	err = s.rollbackBootstrap(context.Background(), result.OperationID, result.WorkID, location, branchMoveBeforeDeleteRunner{branch: result.Entry.Branch, sha: newSHA}, true, errors.New("session preparation failed"))
	if err == nil {
		t.Fatal("concurrently moved branch was deleted")
	}
	branchSHA, exists, err := bootstrapBranchHead(context.Background(), ExecGitRunner{}, repo, result.Entry.Branch)
	if err != nil || !exists || branchSHA != newSHA {
		t.Fatalf("branch sha=%s exists=%v err=%v", branchSHA, exists, err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM bootstrap_operations WHERE operation_id=?`, result.OperationID).Scan(&state); err != nil || state != "rolling_back" {
		t.Fatalf("operation state=%s err=%v", state, err)
	}
}

func TestRollbackBootstrapLocksWorktreeHeadBeforeRemoval(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-worktree-race"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	baseTree := runBootstrapGit(t, repo, "rev-parse", result.Entry.BaseSHA+"^{tree}")
	newSHA := runBootstrapGit(t, repo, "commit-tree", baseTree, "-p", result.Entry.BaseSHA, "-m", "concurrent target")
	runner := &branchMoveBeforeWorktreeRemovalRunner{branch: result.Entry.Branch, sha: newSHA}
	location := WorktreeLocation{Repo: repo, Path: result.Entry.Path, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA}
	if err := s.rollbackBootstrap(context.Background(), result.OperationID, result.WorkID, location, runner, true, errors.New("session preparation failed")); err != nil {
		t.Fatal(err)
	}
	if !runner.attempted || !runner.blocked {
		t.Fatalf("concurrent movement attempted=%v blocked=%v", runner.attempted, runner.blocked)
	}
	if _, err := os.Stat(result.Entry.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back worktree still exists: %v", err)
	}
}

func TestRollbackBootstrapRecoversItsStaleGitLock(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	result, err := s.BootstrapWorktree(context.Background(), bootstrapStoreRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("sleep", "60")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerPID := int64(owner.Process.Pid)
	ownerStart, err := processStartIdentity(ownerPID)
	if err != nil {
		_ = owner.Process.Kill()
		t.Fatal(err)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("killed Git lock owner exited successfully")
	}
	refPath := runBootstrapGit(t, repo, "rev-parse", "--git-path", "refs/heads/"+result.Entry.Branch)
	if !filepath.IsAbs(refPath) {
		refPath = filepath.Join(repo, refPath)
	}
	lockPath := filepath.Clean(refPath) + ".lock"
	marker := "concord-bootstrap-lock-v1\n" + strconv.FormatInt(ownerPID, 10) + "\n" + ownerStart + "\n"
	if err := os.WriteFile(lockPath, []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackBootstrapOperation(context.Background(), result.ProductID, result.WorkID, result.OperationID, result.Entry.Path, "session preparation failed", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale Git lock still exists: %v", err)
	}
}

func TestRollbackBootstrapExcludesConcurrentSessionRecord(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-rollback-session-race"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerPID, ownerStart := bootstrapTestOwner(t)
	launch, err := s.PrepareBootstrapLaunch(context.Background(), result.ProductID, result.WorkID, "concord-implement", result.Entry.Path, ownerPID, ownerStart)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		done := false
		return beginBootstrapRollbackTx(context.Background(), transaction, result.OperationID, result.WorkID, now, "session preparation failed", &done)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBootstrapLaunch(context.Background(), launch.OperationID, launch.AttemptID, result.ProductID, result.WorkID, "session-race", launch.Agent, launch.Directory, "", "running", "", ownerPID, ownerStart); err == nil {
		t.Fatal("session record entered a rollback-owned operation")
	}
	location := WorktreeLocation{Repo: repo, Path: result.Entry.Path, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA}
	if err := s.rollbackBootstrap(context.Background(), result.OperationID, result.WorkID, location, ExecGitRunner{}, true, errors.New("session preparation failed")); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapReplayFinishesInterruptedNativeRollback(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.IdempotencyKey = "bootstrap-interrupted-rollback"
	result, err := s.BootstrapWorktree(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		done := false
		return beginBootstrapRollbackTx(context.Background(), transaction, result.OperationID, result.WorkID, s.now(), "session preparation failed", &done)
	}); err != nil {
		t.Fatal(err)
	}
	runBootstrapGit(t, repo, "worktree", "remove", result.Entry.Path)
	ownerPID, ownerStart := deadBootstrapOwner(t)
	refPath := runBootstrapGit(t, repo, "rev-parse", "--git-path", "refs/heads/"+result.Entry.Branch)
	if !filepath.IsAbs(refPath) {
		refPath = filepath.Join(repo, refPath)
	}
	lockPath := filepath.Clean(refPath) + ".lock"
	marker := "concord-bootstrap-lock-v1\n" + strconv.FormatInt(ownerPID, 10) + "\n" + ownerStart + "\n"
	if err := os.WriteFile(lockPath, []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapWorktree(context.Background(), req, nil); err == nil {
		t.Fatal("replay accepted an interrupted rollback")
	}
	var state, claimState string
	if err := s.db.QueryRow(`SELECT b.state,c.state FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.operation_id=?`, result.OperationID).Scan(&state, &claimState); err != nil {
		t.Fatal(err)
	}
	if state != "rolled_back" || claimState != "reclaimed" {
		t.Fatalf("operation=%s claim=%s", state, claimState)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted rollback lock still exists: %v", err)
	}
}

func TestBootstrapGitLockRecoveryExcludesConcurrentReclaimers(t *testing.T) {
	directory := t.TempDir()
	lockPath := filepath.Join(directory, "branch.lock")
	stalePID, staleStart := deadBootstrapOwner(t)
	marker := "concord-bootstrap-lock-v1\n" + strconv.FormatInt(stalePID, 10) + "\n" + staleStart + "\n"
	if err := os.WriteFile(lockPath, []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	ownerPID, ownerStart := bootstrapTestOwner(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- acquireBootstrapGitLock(lockPath, ownerPID, ownerStart)
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful lock reclaimers=%d", successes)
	}
}

// #650: session-prepare reads C19 continuity unconditionally, so every
// captured work item needs a workflow instance. Captures usually carry no
// workflow_type_ref (CD-0035), so the bootstrap derives a kind-driven default
// instead of skipping initialization.
func TestBootstrapWithoutWorkflowTypeRefStillInitializesContinuity(t *testing.T) {
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)
	req := bootstrapStoreRequest()
	req.WorkflowTypeRef = ""
	req.IdempotencyKey = "bootstrap-continuity"
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(context.Background(), req.ProjectID, workID, req.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepareBootstrap(context.Background(), req, operationID, workID, digest, location, ExecGitRunner{}); err != nil {
		t.Fatal(err)
	}
	continuity, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID, Limit: 1})
	if err != nil {
		t.Fatalf("a captured work item must carry workflow continuity: %v", err)
	}
	if continuity.WorkflowStep == "" {
		t.Fatalf("continuity carries no workflow step: %+v", continuity)
	}
}
