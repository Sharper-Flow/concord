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
		if err := applyMigration(ctx, db, migration); err != nil {
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
	var operationID, state string
	if err := db.QueryRowContext(ctx, `SELECT operation_id,state FROM bootstrap_operations WHERE work_id=?`, result.WorkID).Scan(&operationID, &state); err != nil {
		t.Fatal(err)
	}
	if operationID != result.OperationID || state != "completed" {
		t.Fatalf("operation=%s state=%s", operationID, state)
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
	location := WorktreeLocation{Repo: repo, Path: result.Entry.Path, Branch: result.Entry.Branch, BaseSHA: result.Entry.BaseSHA}
	if err := s.rollbackBootstrap(context.Background(), result.OperationID, result.WorkID, location, ExecGitRunner{}, true, errors.New("bootstrap interrupted")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale Git lock still exists: %v", err)
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
