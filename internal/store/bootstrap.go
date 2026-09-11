package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var bootstrapIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// BootstrapRequest is the bounded host request for one captured work item and
// its one canonical Project worktree.
type BootstrapRequest struct {
	ProductID             string   `json:"product_id"`
	ProjectID             string   `json:"project_id"`
	Title                 string   `json:"title"`
	ValueStatement        string   `json:"value_statement"`
	Kind                  string   `json:"kind"`
	Task                  string   `json:"task"`
	IdempotencyKey        string   `json:"idempotency_key"`
	Priority              int64    `json:"priority"`
	Urgency               string   `json:"urgency"`
	Tags                  []string `json:"tags"`
	WorkflowTypeRef       string   `json:"workflow_type_ref"`
	ExternalRef           string   `json:"external_ref"`
	GoverningRequirements []string `json:"governing_requirements"`
	Ref                   string   `json:"ref"`
}

// BootstrapResult is the durable result of a bootstrap operation.
type BootstrapResult struct {
	OperationID string
	Replayed    bool
	ProductID   string
	ProjectID   string
	WorkID      string
	WorkVersion int64
	Entry       WorktreeEntry
}

// BootstrapOrigin is the verified linked worktree that a work_start call is
// leaving. The origin is read-only input to bootstrap and is never reclaimed
// by the chained start.
type BootstrapOrigin struct {
	ProjectID string
	WorkID    string
	Branch    string
	Path      string
	Lifecycle string
}

// ExistingBootstrapRequest identifies an existing live work item that needs
// its first canonical worktree. The work identity stays the operation identity.
type ExistingBootstrapRequest struct {
	ProductID string `json:"product_id"`
	ProjectID string `json:"project_id"`
	WorkID    string `json:"work_id"`
	Ref       string `json:"ref"`
}

// ValidateBootstrapOrigin validates a clean Concord worktree with no
// active verify lease or worker attempt. It runs before bootstrap records new
// work, so every refusal has no effect.
func (s *Store) ValidateBootstrapOrigin(ctx context.Context, projectID, path string, runner GitRunner) (BootstrapOrigin, error) {
	var origin BootstrapOrigin
	if s == nil || s.db == nil {
		return origin, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if projectID == "" || path == "" {
		return origin, newFailure(KindInvalidOperation, "work_bootstrap", "linked bootstrap origin is missing Project or path identity", false, "run work_start from a Project worktree")
	}
	if runner == nil {
		runner = ExecGitRunner{}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return origin, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot resolve the linked bootstrap origin path", false, "run work_start from a reachable worktree", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the linked bootstrap origin", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT e.project_id,e.branch,e.path,w.id,w.lifecycle FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id JOIN work_items w ON w.id=c.work_id WHERE e.project_id=? AND e.path=? AND e.state='active'`, projectID, filepath.Clean(path)).Scan(&origin.ProjectID, &origin.Branch, &origin.Path, &origin.WorkID, &origin.Lifecycle)
	if err == sql.ErrNoRows {
		return origin, newFailure(KindInvalidOperation, "work_bootstrap", "linked bootstrap origin is not an active Concord worktree", false, "run work_start from the active worktree")
	}
	if err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the linked bootstrap origin", true, "retry once the database is readable", err)
	}
	status, err := runner.Run(ctx, filepath.Clean(path), "status", "--porcelain")
	if err != nil {
		return origin, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect linked bootstrap origin "+origin.WorkID, true, "restore access to the origin worktree and retry", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return origin, newFailure(KindInvalidOperation, "work_bootstrap", "cannot chain from dirty worktree of "+origin.WorkID, false, "commit or discard the origin changes before starting new work")
	}
	var leased bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_verify_leases WHERE path=? AND state='held')`, filepath.Clean(path)).Scan(&leased); err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot inspect origin verify leases", true, "retry once the database is readable", err)
	}
	if leased {
		return origin, newFailure(KindResourceBusy, "work_bootstrap", "cannot chain from worktree "+origin.WorkID+" while a verify lease is active", true, "release the origin verify lease and retry")
	}
	var dispatched bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND lifecycle_state='dispatched')`, origin.WorkID).Scan(&dispatched); err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot inspect origin worker attempts", true, "retry once the database is readable", err)
	}
	openWindow, err := bootstrapOriginHasOpenDispatchWindow(ctx, tx, origin.WorkID)
	if err != nil {
		return origin, err
	}
	if dispatched || openWindow {
		return origin, newFailure(KindResourceBusy, "work_bootstrap", "cannot chain from worktree "+origin.WorkID+" while a worker attempt is dispatched or open", true, "complete or close the origin worker attempt and retry")
	}
	if err := tx.Commit(); err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot finish reading the linked bootstrap origin", true, "retry the same operation", err)
	}
	return origin, nil
}

// ResumeWorktreeLocation resolves the active worktree a running session
// enters when it resumes an existing work item by work identity (issue #891).
// The read refuses typed before any effect: unknown work, terminal work, a
// Project the item does not hold, a Project outside the requested Product
// scope, and an item with no active worktree in the Project each refuse. The
// read records nothing (CD-0104 D1); the origin gate stays with the caller,
// exactly as work-bootstrap runs it for a capture.
func (s *Store) ResumeWorktreeLocation(ctx context.Context, productID, projectID, workID string) (WorktreeEntry, error) {
	var entry WorktreeEntry
	if s == nil || s.db == nil {
		return entry, newFailure(KindUnavailable, "work_resume", "store is not open", false, "open the authority database")
	}
	if productID == "" || projectID == "" || workID == "" {
		return entry, newFailure(KindInvalidOperation, "work_resume", "resume requires the Product, Project, and work identity", false, "supply all three identities")
	}
	var lifecycle string
	if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return entry, newFailure(KindUnknownScope, "work_resume", "work item does not exist", false, "check the work identity before resuming")
		}
		return entry, wrapFailure(KindUnavailable, "work_resume", "cannot read the work item", true, "retry once the database is readable", err)
	}
	if isTerminalLifecycle(lifecycle) {
		return entry, newFailure(KindInvalidOperation, "work_resume", "cannot resume terminal work item "+workID+" ("+lifecycle+")", false, "resume live work or capture new work instead")
	}
	projects, err := s.ProjectsForWork(ctx, workID)
	if err != nil {
		return entry, err
	}
	member := false
	for _, project := range projects {
		if project.ID == projectID {
			member = true
			break
		}
	}
	if !member {
		return entry, newFailure(KindUnknownScope, "work_resume", "work item "+workID+" does not hold Project "+projectID, false, "resume from a Project the work item belongs to")
	}
	_, products, err := s.ScopeVersion(ctx, projectID)
	if err != nil || len(products) != 1 || products[0] != productID {
		return entry, newFailure(KindUnknownScope, "work_resume", "claimed Project is not in the requested Product scope", false, "resume from the Project's own Product")
	}
	return activeWorktreeEntryForProject(ctx, s.db, "work_resume", workID, projectID)
}

// BootstrapExistingWorktree claims the first canonical worktree for an
// existing live item. An active entry is returned as a read-only resume.
func (s *Store) BootstrapExistingWorktree(ctx context.Context, req ExistingBootstrapRequest, phaseHook BootstrapPhaseHook) (BootstrapResult, error) {
	if s == nil || s.db == nil {
		return BootstrapResult{}, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if err := validateExistingBootstrapRequest(req); err != nil {
		return BootstrapResult{}, err
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	entry, err := s.ResumeWorktreeLocation(ctx, req.ProductID, req.ProjectID, req.WorkID)
	if err == nil {
		var version int64
		if versionErr := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&version); versionErr != nil {
			return BootstrapResult{}, wrapFailure(KindUnavailable, "work_resume", "cannot read the existing work version", true, "retry once the database is readable", versionErr)
		}
		return BootstrapResult{Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: req.WorkID, WorkVersion: version, Entry: entry}, nil
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionNotFound {
		return BootstrapResult{}, err
	}
	operationID, workID, digest, err := CanonicalExistingBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive existing bootstrap identity", false, "supply bounded work identity", err)
	}
	return s.bootstrapWorktreeMode(ctx, BootstrapRequest{ProductID: req.ProductID, ProjectID: req.ProjectID, IdempotencyKey: "bootstrap-existing-" + digest[7:55], Ref: req.Ref}, operationID, workID, digest, true, req, phaseHook, ExecGitRunner{})
}

func bootstrapOriginHasOpenDispatchWindow(ctx context.Context, tx *sql.Tx, workID string) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM domain_events started
		JOIN domain_events completed ON completed.subject_type=started.subject_type AND completed.subject_id=started.subject_id AND completed.kind=? AND json_extract(completed.payload,'$.action_id')='dispatch_worker' AND completed.seq>started.seq
		WHERE started.subject_type=? AND started.subject_id=? AND started.kind=? AND json_extract(started.payload,'$.action_id')='dispatch_worker'
		AND NOT EXISTS (SELECT 1 FROM domain_events dispatched WHERE dispatched.subject_type=started.subject_type AND dispatched.subject_id=started.subject_id AND dispatched.kind=? AND dispatched.seq>completed.seq AND json_extract(dispatched.payload,'$.attempt_id')=json_extract(completed.payload,'$.worker_attempt_id'))
	)`, WorkerDispatched, string(SubjectWorkItem), workID, WorkflowActionStarted, WorkerDispatched).Scan(&open)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "work_bootstrap", "cannot inspect origin worker attempt windows", true, "retry once the database is readable", err)
	}
	return open, nil
}

func processStartIdentity(pid int64) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/stat")
	if err != nil {
		return "", err
	}
	closeParen := strings.LastIndex(string(data), ")")
	if closeParen < 0 {
		return "", errors.New("process stat has no command boundary")
	}
	fields := strings.Fields(string(data)[closeParen+1:])
	if len(fields) < 20 {
		return "", errors.New("process stat lacks a start identity")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", errors.New("process stat start identity is invalid")
	}
	return fields[19], nil
}

func launchOwnerAlive(pid int64, start string) bool {
	current, err := processStartIdentity(pid)
	return err == nil && current == start
}

type bootstrapPrepared struct {
	Result   BootstrapResult
	State    string
	Location WorktreeLocation
}

// BootstrapPhaseHook is a test seam for failures between durable phases.
type BootstrapPhaseHook func(string) error

// CanonicalBootstrapIdentity returns the stable operation and work IDs for a
// normalized request.
func CanonicalBootstrapIdentity(req BootstrapRequest) (string, string, string, error) {
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if req.GoverningRequirements == nil {
		req.GoverningRequirements = []string{}
	}
	data, err := json.Marshal(req)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return "bootstrap-" + digest[:48], "work-" + digest[:24], "sha256:" + digest, nil
}

// CanonicalExistingBootstrapIdentity returns the stable operation identity for
// the first worktree of one existing work item. The ref is not part of it:
// replay must use the pinned ref recorded by the first operation.
func CanonicalExistingBootstrapIdentity(req ExistingBootstrapRequest) (string, string, string, error) {
	identity := struct {
		Mode      string `json:"mode"`
		ProductID string `json:"product_id"`
		ProjectID string `json:"project_id"`
		WorkID    string `json:"work_id"`
	}{Mode: "existing", ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: req.WorkID}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return "bootstrap-" + digest[:48], req.WorkID, "sha256:" + digest, nil
}

// BootstrapWorktree commits the capture and pinned native intent before it
// invokes git. A pending journal row is safe to replay after any later error.
func (s *Store) BootstrapWorktree(ctx context.Context, req BootstrapRequest, phaseHook BootstrapPhaseHook) (BootstrapResult, error) {
	return s.bootstrapWorktree(ctx, req, phaseHook, ExecGitRunner{})
}

func (s *Store) bootstrapWorktree(ctx context.Context, req BootstrapRequest, phaseHook BootstrapPhaseHook, runner GitRunner) (BootstrapResult, error) {
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive bootstrap identity", false, "supply JSON-safe input", err)
	}
	return s.bootstrapWorktreeMode(ctx, req, operationID, workID, digest, false, req, phaseHook, runner)
}

func (s *Store) bootstrapWorktreeMode(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, existing bool, journalRequest any, phaseHook BootstrapPhaseHook, runner GitRunner) (BootstrapResult, error) {
	if s == nil || s.db == nil {
		return BootstrapResult{}, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if !existing {
		if err := validateBootstrapRequest(req); err != nil {
			return BootstrapResult{}, err
		}
	} else if err := validateExistingBootstrapRequest(ExistingBootstrapRequest{ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, Ref: req.Ref}); err != nil {
		return BootstrapResult{}, err
	}
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	location, existingState, dbExisting, err := s.pinnedBootstrapLocation(ctx, req.IdempotencyKey, digest, operationID)
	if err != nil {
		return BootstrapResult{}, err
	}
	if existingState == "rolling_back" {
		if err := s.rollbackBootstrap(ctx, operationID, workID, location, ExecGitRunner{}, true, errors.New("resume interrupted bootstrap rollback")); err != nil {
			return BootstrapResult{}, err
		}
		return BootstrapResult{}, newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap operation was rolled back", false, "use a new idempotency key")
	}
	if !dbExisting {
		location, err = s.LocateWorktree(ctx, req.ProjectID, workID, req.Ref)
		if err != nil {
			return BootstrapResult{}, err
		}
	}
	if err := validateBootstrapDefaultBranch(ctx, runner, location.Repo); err != nil {
		return BootstrapResult{}, err
	}
	prepared, err := s.prepareBootstrapMode(ctx, req, operationID, workID, digest, existing, journalRequest, location, runner)
	if err != nil {
		return BootstrapResult{}, err
	}
	location = prepared.Location
	if phaseHook != nil {
		if err := phaseHook("after_prepare"); err != nil {
			return BootstrapResult{}, err
		}
	}
	if prepared.Result.Replayed && prepared.Result.Entry.State == worktreeEntryActive {
		return prepared.Result, nil
	}
	if prepared.State == "pending" {
		if err := s.setBootstrapState(ctx, operationID, "pending", "creating"); err != nil {
			return BootstrapResult{}, err
		}
	}

	facts, err := reconcileBootstrapNative(ctx, runner, location.Repo, location, prepared.Result.Replayed)
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := s.setBootstrapState(ctx, operationID, "creating", "native_ready"); err != nil {
		return BootstrapResult{}, err
	}
	if phaseHook != nil {
		if err := phaseHook("after_native_create"); err != nil {
			return BootstrapResult{}, err
		}
	}
	result, err := s.finalizeBootstrap(ctx, req, operationID, workID, location, facts)
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := s.SyncDurable(ctx); err != nil {
		return BootstrapResult{}, err
	}
	result.Replayed = prepared.Result.Replayed
	return result, nil
}

// rollbackBootstrap removes only native state that still matches the pinned
// intent, then closes the durable claim and operation in one transaction.
func (s *Store) rollbackBootstrap(ctx context.Context, operationID, workID string, location WorktreeLocation, runner GitRunner, removeNative bool, cause error) error {
	now := s.now()
	done := false
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return beginBootstrapRollbackTx(ctx, transaction, operationID, workID, now, cause.Error(), &done)
	})
	if err != nil || done {
		return err
	}
	if err := s.SyncDurable(ctx); err != nil {
		return err
	}
	if removeNative {
		if err := removeBootstrapWorktree(ctx, runner, location); err != nil {
			return err
		}
		if branchSHA, exists, err := bootstrapBranchHead(ctx, runner, location.Repo, location.Branch); err != nil {
			return err
		} else if exists {
			if branchSHA != location.BaseSHA {
				return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap branch contains changes and cannot be removed", false, "preserve the branch and contact_operator")
			}
			if attached, err := branchAttachedElsewhere(ctx, runner, location.Repo, location.Branch); err != nil {
				return err
			} else if attached {
				return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap branch is attached to another worktree", false, "detach the exact pinned branch before retrying")
			}
			if err := deleteBootstrapBranch(ctx, runner, location.Repo, location.Branch, location.BaseSHA); err != nil {
				return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot remove the failed bootstrap branch", false, "remove the exact pinned branch before retrying", err)
			}
		}
	}
	err = s.Transact(ctx, func(transaction *Transaction) error {
		return rollbackBootstrapTx(ctx, transaction, operationID, workID, now, cause.Error())
	})
	if err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot record bootstrap rollback", false, "restore the authority database before retrying", err)
	}
	return s.SyncDurable(ctx)
}

func removeBootstrapWorktree(ctx context.Context, runner GitRunner, location WorktreeLocation) error {
	if _, err := os.Lstat(location.Path); errors.Is(err, os.ErrNotExist) {
		ownerPID := int64(os.Getpid())
		ownerStart, ownerErr := processStartIdentity(ownerPID)
		if ownerErr != nil {
			return ownerErr
		}
		lockPath, lockErr := acquireBootstrapGitPath(ctx, runner, location.Repo, "refs/heads/"+location.Branch, ownerPID, ownerStart)
		if lockErr != nil {
			return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap branch has an active Git lock", false, "retry after the other Git operation completes")
		}
		_ = os.Remove(lockPath)
		return nil
	} else if err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the failed bootstrap worktree path", false, "restore repository access before retrying", err)
	}
	unlock, err := lockBootstrapWorktreeRefs(ctx, runner, location)
	if err != nil {
		return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap worktree changed while rollback acquired its Git locks", false, "preserve the worktree and retry after the other Git operation completes")
	}
	defer unlock()
	ok, facts, err := probeWorktree(ctx, runner, location.Repo, location.Path, location.Branch, location.BaseSHA)
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot compensate the failed bootstrap", false, "restore repository access before retrying", err)
	}
	if !ok {
		return nil
	}
	if facts.headSHA != location.BaseSHA {
		return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap worktree contains changes and cannot be removed", false, "preserve the worktree and contact_operator")
	}
	status, err := runner.Run(ctx, location.Path, "status", "--porcelain")
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the failed bootstrap worktree", false, "restore repository access before retrying", err)
	}
	if len(status) != 0 {
		return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap worktree is dirty and cannot be removed", false, "preserve the worktree and contact_operator")
	}
	if _, err := runner.Run(ctx, location.Repo, "worktree", "remove", location.Path); err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot remove the failed bootstrap worktree", false, "remove the exact pinned worktree before retrying", err)
	}
	return nil
}

func lockBootstrapWorktreeRefs(ctx context.Context, runner GitRunner, location WorktreeLocation) (func(), error) {
	ownerPID := int64(os.Getpid())
	ownerStart, err := processStartIdentity(ownerPID)
	if err != nil {
		return nil, err
	}
	targets := []struct {
		directory string
		gitPath   string
	}{
		{directory: location.Repo, gitPath: "refs/heads/" + location.Branch},
		{directory: location.Path, gitPath: "HEAD"},
	}
	locked := make([]string, 0, len(targets))
	unlock := func() {
		for index := len(locked) - 1; index >= 0; index-- {
			_ = os.Remove(locked[index])
		}
	}
	for _, target := range targets {
		lockPath, err := acquireBootstrapGitPath(ctx, runner, target.directory, target.gitPath, ownerPID, ownerStart)
		if err != nil {
			unlock()
			return nil, err
		}
		locked = append(locked, lockPath)
	}
	return unlock, nil
}

func acquireBootstrapGitPath(ctx context.Context, runner GitRunner, directory, gitPath string, ownerPID int64, ownerStart string) (string, error) {
	output, err := runner.Run(ctx, directory, "rev-parse", "--git-path", gitPath)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		path = filepath.Join(directory, path)
	}
	lockPath := filepath.Clean(path) + ".lock"
	if err := acquireBootstrapGitLock(lockPath, ownerPID, ownerStart); err != nil {
		return "", err
	}
	return lockPath, nil
}

func acquireBootstrapGitLock(lockPath string, ownerPID int64, ownerStart string) error {
	marker := []byte("concord-bootstrap-lock-v1\n" + strconv.FormatInt(ownerPID, 10) + "\n" + ownerStart + "\n")
	for attempt := 0; attempt < 4; attempt++ {
		temporary, err := os.CreateTemp(filepath.Dir(lockPath), filepath.Base(lockPath)+".concord-*")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		if _, err = temporary.Write(marker); err == nil {
			err = temporary.Sync()
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
		err = os.Link(temporaryPath, lockPath)
		_ = os.Remove(temporaryPath)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		reclaimed, reclaimErr := reclaimStaleBootstrapGitLock(lockPath)
		if reclaimErr != nil {
			if errors.Is(reclaimErr, os.ErrNotExist) {
				continue
			}
			return reclaimErr
		}
		if !reclaimed {
			return err
		}
	}
	return os.ErrExist
}

func reclaimStaleBootstrapGitLock(lockPath string) (bool, error) {
	// #nosec G304 -- lockPath is a Git metadata path derived by the bootstrap lock owner.
	file, err := os.Open(lockPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	openedInfo, err := file.Stat()
	if err != nil {
		return false, err
	}
	pathInfo, err := os.Stat(lockPath)
	if err != nil {
		return false, err
	}
	if !os.SameFile(openedInfo, pathInfo) {
		return false, os.ErrNotExist
	}
	content, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return false, err
	}
	if len(content) > 256 {
		return false, nil
	}
	parts := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(parts) != 3 || parts[0] != "concord-bootstrap-lock-v1" {
		return false, nil
	}
	ownerPID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || launchOwnerAlive(ownerPID, parts[2]) {
		return false, nil
	}
	currentInfo, err := os.Stat(lockPath)
	if err != nil {
		return false, err
	}
	if !os.SameFile(openedInfo, currentInfo) {
		return false, os.ErrNotExist
	}
	if err := os.Remove(lockPath); err != nil {
		return false, err
	}
	return true, nil
}

func deleteBootstrapBranch(ctx context.Context, runner GitRunner, repo, branch, expectedSHA string) error {
	hookDir, err := os.MkdirTemp("", "concord-bootstrap-ref-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(hookDir) }()
	if err := os.WriteFile(filepath.Join(hookDir, "expected-sha"), []byte(expectedSHA+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(hookDir, "expected-ref"), []byte("refs/heads/"+branch+"\n"), 0o600); err != nil {
		return err
	}
	hook := `#!/bin/sh
set -eu
[ "$1" = "prepared" ] || exit 0
dir=${0%/*}
IFS= read -r expected_sha < "$dir/expected-sha"
IFS= read -r expected_ref < "$dir/expected-ref"
observed_sha=$(git rev-parse --verify "$expected_ref^{commit}" 2>/dev/null || true)
[ "$observed_sha" = "$expected_sha" ] || exit 1
git worktree list --porcelain > "$dir/worktrees"
while IFS= read -r line; do
  if [ "$line" = "branch $expected_ref" ]; then
    exit 1
  fi
done < "$dir/worktrees"
`
	hookPath := filepath.Join(hookDir, "reference-transaction")
	// #nosec G306 -- Git hooks must be executable by the repository owner.
	if err := os.WriteFile(hookPath, []byte(hook), 0o700); err != nil {
		return err
	}
	output, err := runner.Run(ctx, repo, "-c", "core.hooksPath="+hookDir, "branch", "-d", "--", branch)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func beginBootstrapRollbackTx(ctx context.Context, transaction *Transaction, operationID, workID string, now time.Time, reason string, done *bool) error {
	*done = false
	tx, err := transactionSQL(transaction, "work_bootstrap")
	if err != nil {
		return err
	}
	var state, storedWork string
	if err := tx.QueryRowContext(ctx, `SELECT state,work_id FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state, &storedWork); err != nil {
		return err
	}
	if storedWork != workID {
		return newFailure(KindInvariantViolation, "work_bootstrap", "rollback work identity differs from the bootstrap operation", false, "use the exact bootstrap operation")
	}
	if state == "rolled_back" {
		*done = true
		return nil
	}
	if state == "rolling_back" {
		return nil
	}
	if state != "completed" {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap operation is not ready for rollback", false, "reconcile the bootstrap operation")
	}
	result, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='rolling_back',failure_reason=?,updated_at=? WHERE operation_id=? AND state='completed'`, reason, now.Format(time.RFC3339Nano), operationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap rollback could not acquire exclusive state", false, "reconcile the bootstrap operation")
	}
	return nil
}

func rollbackBootstrapTx(ctx context.Context, transaction *Transaction, operationID, workID string, now time.Time, reason string) error {
	tx, err := transactionSQL(transaction, "work_bootstrap")
	if err != nil {
		return err
	}
	var state string
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT state,expected_version FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state, &expected); err != nil {
		return err
	}
	if state == "rolled_back" {
		return nil
	}
	if state != "rolling_back" {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap rollback lost its exclusive state", false, "reconcile the bootstrap operation")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state='reclaimed',updated_at=? WHERE op_id=? AND state IN ('pending','verified')`, now.Format(time.RFC3339Nano), operationID); err != nil {
		return err
	}
	var lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle,version FROM work_items WHERE id=?`, workID).Scan(&lifecycle, &version); err != nil {
		return err
	}
	if lifecycle == "needed" || lifecycle == "in_progress" {
		payload, marshalErr := json.Marshal(workTransitionPayload{From: lifecycle, To: "cancelled", Reason: "bootstrap rolled back: " + reason, ExpectedVersion: version, ResultingVersion: version + 1})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{EventID: operationID + ":rolled-back", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}, true, false); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='rolled_back',failure_reason=?,updated_at=? WHERE operation_id=?`, reason, now.Format(time.RFC3339Nano), operationID)
	return err
}

func (s *Store) pinnedBootstrapLocation(ctx context.Context, idempotencyKey, digest, operationID string) (WorktreeLocation, string, bool, error) {
	var out WorktreeLocation
	var storedDigest, repo, state string
	err := s.db.QueryRowContext(ctx, `SELECT request_digest,repo_path,state FROM bootstrap_operations WHERE idempotency_key=?`, idempotencyKey).Scan(&storedDigest, &repo, &state)
	if err == sql.ErrNoRows {
		return out, "", false, nil
	}
	if err != nil {
		return out, "", false, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}
	if storedDigest != digest {
		return out, "", false, newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
	}
	if state == "rolled_back" {
		return out, state, false, newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap operation was rolled back", false, "use a new idempotency key")
	}
	var branch, base, path string
	if err := s.db.QueryRowContext(ctx, `SELECT pinned_branch,pinned_base_sha,pinned_path FROM worktree_claims WHERE op_id=?`, operationID).Scan(&branch, &base, &path); err != nil {
		return out, "", false, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
	}
	return WorktreeLocation{Branch: branch, BaseSHA: base, Path: path, Repo: repo}, state, true, nil
}

func validateBootstrapRequest(req BootstrapRequest) error {
	for name, value := range map[string]string{"product_id": req.ProductID, "project_id": req.ProjectID, "idempotency_key": req.IdempotencyKey, "kind": req.Kind} {
		if !bootstrapIDPattern.MatchString(value) || len(value) > 128 {
			return newFailure(KindInvalidOperation, "work_bootstrap", name+" is not a valid bounded identifier", false, "supply an identifier with letters, digits, and _ . : -")
		}
	}
	for name, value := range map[string]string{"title": req.Title, "value_statement": req.ValueStatement, "external_ref": req.ExternalRef} {
		if value == "" && name != "external_ref" || len(value) > 256 || strings.ContainsRune(value, '\x00') || !utf8.ValidString(value) {
			return newFailure(KindInvalidOperation, "work_bootstrap", name+" is empty, too long, or contains NUL", false, "supply bounded prose")
		}
	}
	if req.Task == "" || len(req.Task) > 8192 || strings.ContainsRune(req.Task, '\x00') || !utf8.ValidString(req.Task) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "task is empty, too long, or contains NUL", false, "supply bounded UTF-8 task text")
	}
	if req.Kind != "task" && req.Kind != "bug" && req.Kind != "decision" && req.Kind != "research" && req.Kind != "other" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "kind is not a declared work kind", false, "use task, bug, decision, research, or other")
	}
	if req.Urgency != "" && req.Urgency != "standard" && req.Urgency != "expedite" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "urgency is not recognized", false, "use standard or expedite")
	}
	if req.Priority < -100 || req.Priority > 100 || len(req.Tags) > 32 || len(req.GoverningRequirements) > 32 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "priority or list field exceeds its bound", false, "use the declared field bounds")
	}
	for _, values := range [][]string{req.Tags, req.GoverningRequirements} {
		seen := map[string]bool{}
		for _, tag := range values {
			if !bootstrapIDPattern.MatchString(tag) || seen[tag] {
				return newFailure(KindInvalidOperation, "work_bootstrap", "tag or governing requirement is invalid or duplicated", false, "supply unique bounded identifiers")
			}
			seen[tag] = true
		}
	}
	if req.WorkflowTypeRef != "" && !bootstrapIDPattern.MatchString(req.WorkflowTypeRef) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "workflow_type_ref is not a valid identifier", false, "supply a bounded workflow reference")
	}
	if req.Ref != "" && (len(req.Ref) > 128 || strings.HasPrefix(req.Ref, "-") || strings.ContainsAny(req.Ref, " \t\n\r\x00")) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "ref is not a bounded rev-syntax value", false, "supply one safe repository ref")
	}
	return nil
}

func validateExistingBootstrapRequest(req ExistingBootstrapRequest) error {
	for name, value := range map[string]string{"product_id": req.ProductID, "project_id": req.ProjectID, "work_id": req.WorkID} {
		if !bootstrapIDPattern.MatchString(value) || len(value) > 128 {
			return newFailure(KindInvalidOperation, "work_bootstrap", name+" is not a valid bounded identifier", false, "supply an identifier with letters, digits, and _ . : -")
		}
	}
	if req.Ref != "" && (len(req.Ref) > 128 || strings.HasPrefix(req.Ref, "-") || strings.ContainsAny(req.Ref, " \t\n\r\x00")) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "ref is not a bounded rev-syntax value", false, "supply one safe repository ref")
	}
	return nil
}

func (s *Store) prepareBootstrap(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, location WorktreeLocation, runner GitRunner) (bootstrapPrepared, error) {
	return s.prepareBootstrapMode(ctx, req, operationID, workID, digest, false, req, location, runner)
}

func (s *Store) prepareBootstrapMode(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, existing bool, journalRequest any, location WorktreeLocation, runner GitRunner) (bootstrapPrepared, error) {
	var out bootstrapPrepared
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap journal", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	var storedDigest, state string
	replayed := false
	expectedVersion := int64(0)
	err = tx.QueryRowContext(ctx, `SELECT request_digest,state FROM bootstrap_operations WHERE idempotency_key=?`, req.IdempotencyKey).Scan(&storedDigest, &state)
	if err == nil {
		replayed = true
		if storedDigest != digest {
			return out, newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
		}
		if err := tx.QueryRowContext(ctx, `SELECT b.repo_path,c.pinned_branch,c.pinned_base_sha,c.pinned_path FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.operation_id=?`, operationID).Scan(&location.Repo, &location.Branch, &location.BaseSHA, &location.Path); err != nil {
			return out, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
		}
		if state == "completed" {
			entry, entryErr := worktreeEntryByClaim(ctx, tx, operationID)
			if entryErr != nil {
				return out, entryErr
			}
			version, versionErr := workVersionTx(ctx, tx, workID)
			if versionErr != nil {
				return out, versionErr
			}
			if err := tx.Commit(); err != nil {
				return out, err
			}
			return bootstrapPrepared{Result: BootstrapResult{OperationID: operationID, Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, State: state, Location: location}, nil
		}
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}

	if err == sql.ErrNoRows {
		state = "pending"
		if existing {
			entry, activeErr := activeWorktreeEntryForProject(ctx, tx, "work_bootstrap", workID, req.ProjectID)
			if activeErr == nil {
				version, versionErr := workVersionTx(ctx, tx, workID)
				if versionErr != nil {
					return out, versionErr
				}
				if err := tx.Commit(); err != nil {
					return out, err
				}
				return bootstrapPrepared{Result: BootstrapResult{Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, State: "completed", Location: WorktreeLocation{Branch: entry.Branch, BaseSHA: entry.BaseSHA, Path: entry.Path}}, nil
			}
			var activeFailure *Failure
			if !errors.As(activeErr, &activeFailure) || activeFailure.Kind != KindProjectionNotFound {
				return out, activeErr
			}
			var activeElsewhere bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=? AND e.state='active')`, workID).Scan(&activeElsewhere); err != nil {
				return out, err
			}
			if activeElsewhere {
				return out, newFailure(KindUnknownScope, "work_bootstrap", "work item has an active worktree in another Project", false, "resume from the Project that owns the active worktree")
			}
		}
		if err := validateBootstrapNativeAbsent(ctx, runner, location); err != nil {
			return out, err
		}
		if existing {
			var priorOperation bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bootstrap_operations WHERE work_id=?)`, workID).Scan(&priorOperation); err != nil {
				return out, err
			}
			if priorOperation {
				return out, newFailure(KindProjectionConflict, "work_bootstrap", "existing work item has a bootstrap operation for another Project or Product", false, "resume with the original Project and Product")
			}
		}
		if err := validateBootstrapScopeTx(ctx, tx, req.ProductID, req.ProjectID, req.GoverningRequirements); err != nil {
			return out, err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, workID).Scan(&exists); err != nil {
			return out, err
		}
		if existing {
			if !exists {
				return out, newFailure(KindUnknownScope, "work_bootstrap", "existing work item does not exist", false, "check the work identity before starting")
			}
			var lifecycle string
			if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
				return out, err
			}
			if isTerminalLifecycle(lifecycle) {
				return out, newFailure(KindInvalidOperation, "work_bootstrap", "cannot bootstrap terminal work item "+workID+" ("+lifecycle+")", false, "start live work or capture new work instead")
			}
			var member bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_projects WHERE work_id=? AND project_id=?)`, workID, req.ProjectID).Scan(&member); err != nil {
				return out, err
			}
			if !member {
				return out, newFailure(KindUnknownScope, "work_bootstrap", "work item does not hold Project "+req.ProjectID, false, "start from a Project the work item belongs to")
			}
			if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&expectedVersion); err != nil {
				return out, err
			}
		} else if exists {
			return out, newFailure(KindProjectionConflict, "work_bootstrap", "derived work ID already exists without its journal", false, "use a new idempotency key")
		}
		now := s.now()
		if !existing {
			priority := req.Priority
			workPayload, _ := json.Marshal(workCreatedPayload{WorkID: workID, WorkKind: req.Kind, Title: req.Title, ValueStatement: req.ValueStatement, Priority: &priority, Urgency: req.Urgency, Tags: req.Tags, WorkflowTypeRef: req.WorkflowTypeRef, ExternalRef: req.ExternalRef})
			membershipPayload, _ := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: req.ProjectID, Role: "primary"}}, ExpectedVersion: 1, ResultingVersion: 2})
			events := []Event{
				{EventID: operationID + ":work-created", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: workPayload},
				{EventID: operationID + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
			}
			if _, err := applyOperationTx(ctx, tx, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}, true, false); err != nil {
				return out, err
			}
			// C19 continuity is unconditional: session-prepare reads a workflow
			// instance for every captured work item. A capture that names no
			// workflow_type_ref (the common case, CD-0035) pins the kind-driven
			// default instead of skipping initialization (#650).
			workflowRef := req.WorkflowTypeRef
			if workflowRef == "" {
				workflowRef = DefaultWorkflowRefForKind(req.Kind)
			}
			definition, definitionErr := BuiltinWorkflowDefinitionForRef(workflowRef)
			if definitionErr != nil {
				return out, definitionErr
			}
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + operationID, ActorClass: ActorOperator}
			if err := InitializeWorkflowTx(ctx, &Transaction{tx: tx, clock: s.Clock}, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
				return out, err
			}
			expectedVersion = 4
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, req.IdempotencyKey, operationID, digest, bootstrapJSON(journalRequest), req.ProductID, req.ProjectID, workID, location.Repo, expectedVersion, "pending", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return out, err
		}
		if err := pinBootstrapClaimTx(ctx, tx, operationID, workID, req.ProjectID, location, expectedVersion, now); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot commit bootstrap journal", true, "retry the same idempotency key", err)
	}
	if err := s.SyncDurable(ctx); err != nil {
		return out, err
	}
	return bootstrapPrepared{Result: BootstrapResult{OperationID: operationID, Replayed: replayed, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID}, State: state, Location: location}, nil
}

func (s *Store) setBootstrapState(ctx context.Context, operationID, from, to string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap phase record", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state); err != nil {
		return err
	}
	if state == to || state == "native_ready" || state == "completed" {
		return nil
	}
	if state != from {
		return newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap journal phase conflicts with the requested transition", true, "retry the exact operation")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state=?,updated_at=? WHERE operation_id=? AND state=?`, to, s.now().Format(time.RFC3339Nano), operationID, from); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot commit bootstrap phase record", true, "retry the same operation", err)
	}
	return s.SyncDurable(ctx)
}

func reconcileBootstrapNative(ctx context.Context, runner GitRunner, repo string, location WorktreeLocation, allowExisting bool) (worktreeFacts, error) {
	if ok, facts, err := probeWorktree(ctx, runner, repo, location.Path, location.Branch, location.BaseSHA); err != nil {
		return worktreeFacts{}, err
	} else if ok {
		if !allowExisting {
			return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "fresh bootstrap found a pre-existing canonical worktree", false, "resolve the conflicting native state and use a new idempotency key")
		}
		if facts.headSHA != location.BaseSHA {
			return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "existing canonical worktree has commits after the pinned base", false, "resolve the conflicting native state without adopting it")
		}
		return facts, nil
	}
	if _, err := os.Lstat(location.Path); err == nil {
		return worktreeFacts{}, newFailure(KindInvalidOperation, "work_bootstrap", "expected worktree path conflicts with the pinned intent", false, "resolve the conflicting path without removing native resources")
	} else if !os.IsNotExist(err) {
		return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the expected worktree path", true, "restore access to the worktree path", err)
	}
	branchSHA, branchExists, err := bootstrapBranchHead(ctx, runner, repo, location.Branch)
	if err != nil {
		return worktreeFacts{}, err
	}
	if !branchExists {
		if _, err := runner.Run(ctx, repo, "worktree", "add", location.Path, "-b", location.Branch, location.BaseSHA); err != nil {
			if allowExisting {
				return retryBootstrapProbe(ctx, runner, repo, location, err)
			}
			retrySafe := gitFailureRetrySafe(err)
			return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "native worktree creation failed; the recorded operation is safe to replay", retrySafe, "retry the same idempotency key", err)
		}
		return verifyBootstrapNative(ctx, runner, repo, location)
	}
	if !allowExisting {
		return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "fresh bootstrap found a pre-existing canonical branch", false, "resolve the conflicting native state and use a new idempotency key")
	}
	if branchSHA != location.BaseSHA {
		return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "existing branch does not match the pinned base", false, "resolve the conflicting native state without adopting it")
	}
	attached, err := branchAttachedElsewhere(ctx, runner, repo, location.Branch)
	if err != nil {
		return worktreeFacts{}, err
	}
	if attached {
		return worktreeFacts{}, newFailure(KindInvalidOperation, "work_bootstrap", "existing branch is attached to another worktree", false, "resolve the native branch attachment without removing native resources")
	}
	if _, err := runner.Run(ctx, repo, "worktree", "add", location.Path, location.Branch); err != nil {
		return retryBootstrapProbe(ctx, runner, repo, location, err)
	}
	return verifyBootstrapNative(ctx, runner, repo, location)
}

func retryBootstrapProbe(ctx context.Context, runner GitRunner, repo string, location WorktreeLocation, cause error) (worktreeFacts, error) {
	if ok, facts, err := probeWorktree(ctx, runner, repo, location.Path, location.Branch, location.BaseSHA); err == nil && ok {
		if facts.headSHA != location.BaseSHA {
			return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "recovered worktree has commits after the pinned base", false, "resolve the conflicting native state without adopting it")
		}
		return facts, nil
	}
	return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "native worktree creation failed; the recorded operation is safe to replay", gitFailureRetrySafe(cause), "retry the same idempotency key", cause)
}

func gitFailureRetrySafe(err error) bool {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode() < 128
	}
	return true
}

func verifyBootstrapNative(ctx context.Context, runner GitRunner, repo string, location WorktreeLocation) (worktreeFacts, error) {
	ok, facts, err := probeWorktree(ctx, runner, repo, location.Path, location.Branch, location.BaseSHA)
	if err != nil {
		return worktreeFacts{}, err
	}
	if !ok {
		return worktreeFacts{}, newFailure(KindGitUnreachable, "work_bootstrap", "native worktree did not verify against the pinned intent", false, "contact_operator")
	}
	if facts.headSHA != location.BaseSHA {
		return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "native worktree has commits after the pinned base", false, "resolve the conflicting native state without adopting it")
	}
	return facts, nil
}

func validateBootstrapDefaultBranch(ctx context.Context, runner GitRunner, repo string) error {
	headOut, headErr := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "HEAD")
	defaultRef, defaultErr := bootstrapDefaultBranchRef(ctx, runner, repo)
	if headErr != nil || defaultErr != nil {
		return newFailure(KindGitUnreachable, "work_bootstrap", "cannot prove the Project default branch checkout", false, "check out the default branch and set origin/HEAD")
	}
	head := strings.TrimSpace(string(headOut))
	if head != "refs/heads/"+strings.TrimPrefix(defaultRef, "origin/") {
		return newFailure(KindInvalidOperation, "work_bootstrap", "Project main worktree is not on its default branch", false, "check out the branch named by origin/HEAD")
	}
	return nil
}

// DefaultBranchRef returns the Project default branch ref from origin/HEAD.
// A linked worktree uses this ref instead of its own terminal branch as the
// base for the next claimed worktree.
func DefaultBranchRef(ctx context.Context, repo string) (string, error) {
	return bootstrapDefaultBranchRef(ctx, ExecGitRunner{}, repo)
}

func bootstrapDefaultBranchRef(ctx context.Context, runner GitRunner, repo string) (string, error) {
	defaultOut, err := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", newFailure(KindGitUnreachable, "work_bootstrap", "cannot resolve the Project default branch", false, "set origin/HEAD")
	}
	const remotePrefix = "refs/remotes/origin/"
	value := strings.TrimSpace(string(defaultOut))
	if !strings.HasPrefix(value, remotePrefix) || len(strings.TrimPrefix(value, remotePrefix)) == 0 {
		return "", newFailure(KindGitUnreachable, "work_bootstrap", "origin/HEAD does not name a default branch", false, "set origin/HEAD")
	}
	return "origin/" + strings.TrimPrefix(value, remotePrefix), nil
}

func validateBootstrapNativeAbsent(ctx context.Context, runner GitRunner, location WorktreeLocation) error {
	if _, err := os.Lstat(location.Path); err == nil {
		return newFailure(KindProjectionConflict, "work_bootstrap", "canonical worktree path exists before the bootstrap operation", false, "resolve the pre-existing native state and use a new idempotency key")
	} else if !os.IsNotExist(err) {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the canonical worktree path", true, "restore access to the worktree path", err)
	}
	if _, exists, err := bootstrapBranchHead(ctx, runner, location.Repo, location.Branch); err != nil {
		return err
	} else if exists {
		return newFailure(KindProjectionConflict, "work_bootstrap", "canonical branch exists before the bootstrap operation", false, "resolve the pre-existing native state and use a new idempotency key")
	}
	return nil
}

func bootstrapBranchHead(ctx context.Context, runner GitRunner, repo, branch string) (string, bool, error) {
	ref := "refs/heads/" + branch
	_, err := runner.Run(ctx, repo, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the canonical branch", true, "restore access to the repository and retry", err)
	}
	out, err := runner.Run(ctx, repo, "rev-parse", "--verify", ref)
	if err != nil {
		return "", false, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot resolve the canonical branch", true, "restore access to the repository and retry", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func branchAttachedElsewhere(ctx context.Context, runner GitRunner, repo, branch string) (bool, error) {
	out, err := runner.Run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect native worktree attachments", true, "restore access to the repository and retry", err)
	}
	want := "branch refs/heads/" + branch
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if line == want {
				return true, nil
			}
		}
	}
	return false, nil
}

func pinBootstrapClaimTx(ctx context.Context, tx *sql.Tx, operationID, workID, projectID string, location WorktreeLocation, expectedVersion int64, now time.Time) error {
	if err := ValidateWorktreeClaimIntent(location.Branch, location.BaseSHA, location.Path); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_claims WHERE work_id=? AND project_id=? AND state IN ('pending','verified')`, workID, projectID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return newFailure(KindProjectionConflict, "work_bootstrap", "work already has an active canonical worktree", false, "replay the original operation")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, operationID, workID, projectID, WorktreeSetID(workID), location.Branch, location.BaseSHA, location.Path, worktreeStatePending, "operator", operationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return err
}

func validateBootstrapScopeTx(ctx context.Context, tx *sql.Tx, productID, projectID string, declared []string) error {
	var member bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, productID, projectID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return newFailure(KindUnknownScope, "work_bootstrap", "Project is not a member of product_id", false, "use a Product and Project with an existing membership")
	}
	var productCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM product_projects WHERE project_id=?`, projectID).Scan(&productCount); err != nil {
		return err
	}
	if productCount != 1 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "Project has cross-Product scope", false, "use the ordinary agent approval path for cross-Product work")
	}
	applicable, err := governingRequirementsForProjectIDs(ctx, tx, []string{projectID})
	if err != nil {
		return err
	}
	if missing := MissingGoverningRequirements(applicable, declared); len(missing) != 0 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "governing requirements need ordinary agent approval", false, "use the ordinary agent approval path with all governing requirements")
	}
	return nil
}

func (s *Store) finalizeBootstrap(ctx context.Context, req BootstrapRequest, operationID, workID string, location WorktreeLocation, facts worktreeFacts) (BootstrapResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer tx.Rollback()
	var digest, state string
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT request_digest,state,expected_version FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&digest, &state, &expected); err != nil {
		return BootstrapResult{}, err
	}
	var pinnedBranch, pinnedBase, pinnedPath, pinnedRepo string
	if err := tx.QueryRowContext(ctx, `SELECT c.pinned_branch,c.pinned_base_sha,c.pinned_path,b.repo_path FROM worktree_claims c JOIN bootstrap_operations b ON b.operation_id=c.op_id WHERE c.op_id=?`, operationID).Scan(&pinnedBranch, &pinnedBase, &pinnedPath, &pinnedRepo); err != nil {
		return BootstrapResult{}, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap finalization has no pinned worktree intent", false, "contact_operator", err)
	}
	if location.Branch != pinnedBranch || location.BaseSHA != pinnedBase || location.Path != pinnedPath || location.Repo != pinnedRepo {
		return BootstrapResult{}, newFailure(KindInvariantViolation, "work_bootstrap", "bootstrap finalization differs from the pinned worktree intent", false, "contact_operator")
	}
	if state == "completed" {
		entry, err := worktreeEntryByClaim(ctx, tx, operationID)
		if err != nil {
			return BootstrapResult{}, err
		}
		version, err := workVersionTx(ctx, tx, workID)
		if err != nil {
			return BootstrapResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BootstrapResult{}, err
		}
		return BootstrapResult{OperationID: operationID, Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, nil
	}
	if digest == "" {
		return BootstrapResult{}, newFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no request digest", false, "contact_operator")
	}
	if state != "native_ready" || facts.branch != location.Branch || facts.headSHA != location.BaseSHA || facts.repositoryID == "" {
		return BootstrapResult{}, newFailure(KindInvariantViolation, "work_bootstrap", "bootstrap native facts do not match the pinned finalization state", false, "contact_operator")
	}
	payload, _ := json.Marshal(worktreeCreatedPayload{ExpectedVersion: expected, ResultingVersion: expected + 1, SetID: WorktreeSetID(workID), ProjectID: req.ProjectID, ClaimOpID: operationID, Branch: location.Branch, BaseSHA: location.BaseSHA, Path: location.Path, RepositoryID: facts.repositoryID, GitFacts: facts.raw()})
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{EventID: operationID + ":worktree-created", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}, true, false); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?,updated_at=? WHERE op_id=? AND state=?`, worktreeStateVerified, s.now().Format(time.RFC3339Nano), operationID, worktreeStatePending); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='completed',updated_at=? WHERE operation_id=? AND state='native_ready'`, s.now().Format(time.RFC3339Nano), operationID); err != nil {
		return BootstrapResult{}, err
	}
	entry, err := worktreeEntryByClaim(ctx, tx, operationID)
	if err != nil {
		return BootstrapResult{}, err
	}
	version := expected + 1
	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{OperationID: operationID, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, nil
}

func bootstrapJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func workVersionTx(ctx context.Context, tx *sql.Tx, workID string) (int64, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err == sql.ErrNoRows {
		return 0, newFailure(KindProjectionNotFound, "work_bootstrap", "work item does not exist", false, "replay the original operation")
	} else if err != nil {
		return 0, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read work item version", true, "retry once the database is readable", err)
	}
	return version, nil
}
