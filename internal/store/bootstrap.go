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

// BootstrapLaunch is the durable launch handoff for one bootstrap operation.
// A nil SessionID means that the host prepared the launch but the child did not
// return a session identity.
type BootstrapLaunch struct {
	OperationID       string
	AttemptID         string
	SessionID         *string
	SpawnPermitted    bool
	RollbackPermitted bool
	Agent             string
	Directory         string
	Model             string
	State             string
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

// BootstrapWorktree commits the capture and pinned native intent before it
// invokes git. A pending journal row is safe to replay after any later error.
func (s *Store) BootstrapWorktree(ctx context.Context, req BootstrapRequest, phaseHook BootstrapPhaseHook) (BootstrapResult, error) {
	return s.bootstrapWorktree(ctx, req, phaseHook, ExecGitRunner{})
}

func (s *Store) bootstrapWorktree(ctx context.Context, req BootstrapRequest, phaseHook BootstrapPhaseHook, runner GitRunner) (BootstrapResult, error) {
	if s == nil || s.db == nil {
		return BootstrapResult{}, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if err := validateBootstrapRequest(req); err != nil {
		return BootstrapResult{}, err
	}
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive bootstrap identity", false, "supply JSON-safe input", err)
	}
	location, existingState, existing, err := s.pinnedBootstrapLocation(ctx, req.IdempotencyKey, digest, operationID)
	if err != nil {
		return BootstrapResult{}, err
	}
	if existingState == "rolling_back" {
		if err := s.rollbackBootstrap(ctx, operationID, workID, location, ExecGitRunner{}, true, errors.New("resume interrupted bootstrap rollback")); err != nil {
			return BootstrapResult{}, err
		}
		return BootstrapResult{}, newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap operation was rolled back", false, "use a new idempotency key")
	}
	if !existing {
		location, err = s.LocateWorktree(ctx, req.ProjectID, workID, req.Ref)
		if err != nil {
			return BootstrapResult{}, err
		}
	}
	if err := validateBootstrapDefaultBranch(ctx, runner, location.Repo); err != nil {
		return BootstrapResult{}, err
	}
	prepared, err := s.prepareBootstrap(ctx, req, operationID, workID, digest, location, runner)
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
	file, err := os.Open(lockPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
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
	defer os.RemoveAll(hookDir)
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
	var state, storedWork, sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT state,work_id,COALESCE(launch_session_id,'') FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state, &storedWork, &sessionID); err != nil {
		return err
	}
	if storedWork != workID {
		return newFailure(KindInvariantViolation, "work_bootstrap", "rollback work identity differs from the bootstrap operation", false, "use the exact bootstrap operation")
	}
	if sessionID != "" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "a recorded child session requires exact replay", false, "resume the recorded session")
	}
	if state == "rolled_back" {
		*done = true
		return nil
	}
	if state == "rolling_back" {
		return nil
	}
	if state != "completed" {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap operation is not ready for launch rollback", false, "reconcile the bootstrap operation")
	}
	result, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='rolling_back',failure_reason=?,updated_at=? WHERE operation_id=? AND state='completed' AND launch_session_id IS NULL`, reason, now.Format(time.RFC3339Nano), operationID)
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
	var state, sessionID string
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT state,expected_version,COALESCE(launch_session_id,'') FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state, &expected, &sessionID); err != nil {
		return err
	}
	if state == "rolled_back" {
		return nil
	}
	if state != "rolling_back" || sessionID != "" {
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

func (s *Store) prepareBootstrap(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, location WorktreeLocation, runner GitRunner) (bootstrapPrepared, error) {
	var out bootstrapPrepared
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap journal", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	var storedDigest, state string
	replayed := false
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
		if err := validateBootstrapNativeAbsent(ctx, runner, location); err != nil {
			return out, err
		}
		if err := validateBootstrapScopeTx(ctx, tx, req.ProductID, req.ProjectID, req.GoverningRequirements); err != nil {
			return out, err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, workID).Scan(&exists); err != nil {
			return out, err
		}
		if exists {
			return out, newFailure(KindProjectionConflict, "work_bootstrap", "derived work ID already exists without its journal", false, "use a new idempotency key")
		}
		now := s.now()
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
		expectedVersion := int64(2)
		if req.WorkflowTypeRef != "" {
			definition, definitionErr := BuiltinWorkflowDefinitionForRef(req.WorkflowTypeRef)
			if definitionErr != nil {
				return out, definitionErr
			}
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + operationID, ActorClass: ActorOperator}
			if err := InitializeWorkflowTx(ctx, &Transaction{tx: tx, clock: s.Clock}, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
				return out, err
			}
			expectedVersion = 4
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, req.IdempotencyKey, operationID, digest, bootstrapJSON(req), req.ProductID, req.ProjectID, workID, location.Repo, expectedVersion, "pending", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
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
	defaultOut, defaultErr := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if headErr != nil || defaultErr != nil {
		return newFailure(KindGitUnreachable, "work_bootstrap", "cannot prove the Project default branch checkout", false, "check out the default branch and set origin/HEAD")
	}
	head := strings.TrimSpace(string(headOut))
	defaultRef := strings.TrimSpace(string(defaultOut))
	const remotePrefix = "refs/remotes/origin/"
	if !strings.HasPrefix(defaultRef, remotePrefix) || head != "refs/heads/"+strings.TrimPrefix(defaultRef, remotePrefix) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "Project main worktree is not on its default branch", false, "check out the branch named by origin/HEAD")
	}
	return nil
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

// PrepareBootstrapLaunch records the launch intent before the host starts the
// child. Repeated calls return the same attempt and session identity.
func (s *Store) PrepareBootstrapLaunch(ctx context.Context, productID, workID, agent, directory string, ownerPID int64, ownerStart string) (BootstrapLaunch, error) {
	if s == nil || s.db == nil {
		return BootstrapLaunch{}, newFailure(KindUnavailable, "session_prepare", "store is not open", false, "open the authority database")
	}
	if productID == "" || workID == "" || agent == "" || directory == "" || ownerPID <= 1 || ownerStart == "" {
		return BootstrapLaunch{}, newFailure(KindInvalidOperation, "session_prepare", "launch identity fields are required", false, "supply Product, work, agent, and directory identity")
	}
	if len(agent) > 128 || len(directory) > 4096 || !bootstrapIDPattern.MatchString(agent) {
		return BootstrapLaunch{}, newFailure(KindInvalidOperation, "session_prepare", "launch identity fields exceed their bounds", false, "supply bounded launch identity fields")
	}
	if !launchOwnerAlive(ownerPID, ownerStart) {
		return BootstrapLaunch{}, newFailure(KindInvalidOperation, "session_prepare", "launch owner process identity is not live", false, "supply the current host process identity")
	}
	var launch BootstrapLaunch
	now := s.now()
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return prepareBootstrapLaunchTx(ctx, transaction, productID, workID, agent, directory, ownerPID, ownerStart, now, &launch)
	})
	if err != nil {
		return BootstrapLaunch{}, err
	}
	if err := s.SyncDurable(ctx); err != nil {
		return BootstrapLaunch{}, err
	}
	return launch, nil
}

func prepareBootstrapLaunchTx(ctx context.Context, transaction *Transaction, productID, workID, agent, directory string, ownerPID int64, ownerStart string, now time.Time, launch *BootstrapLaunch) error {
	tx, err := transactionSQL(transaction, "session_prepare")
	if err != nil {
		return err
	}
	var state, launchState, attemptID, sessionID, storedAgent, storedDirectory, model, launchError, storedOwnerStart string
	var storedOwnerPID int64
	var operationID string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,state,launch_state,COALESCE(launch_attempt_id,''),COALESCE(launch_session_id,''),COALESCE(launch_owner_pid,0),COALESCE(launch_owner_start,''),COALESCE(launch_agent,''),COALESCE(launch_directory,''),COALESCE(launch_model,''),COALESCE(launch_error,'') FROM bootstrap_operations WHERE product_id=? AND work_id=?`, productID, workID).Scan(&operationID, &state, &launchState, &attemptID, &sessionID, &storedOwnerPID, &storedOwnerStart, &storedAgent, &storedDirectory, &model, &launchError); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "session_prepare", "work has no bootstrap operation", false, "run work-bootstrap before session-prepare")
		}
		return wrapFailure(KindUnavailable, "session_prepare", "cannot read bootstrap launch state", true, "retry once the database is readable", err)
	}
	if state != "completed" {
		return newFailure(KindInvalidOperation, "session_prepare", "bootstrap operation is not complete", true, "retry after work-bootstrap completes")
	}
	if storedAgent != "" && (storedAgent != agent || storedDirectory != directory) {
		return newFailure(KindInvariantViolation, "session_prepare", "launch identity differs from the recorded operation", false, "use the recorded worktree and agent")
	}
	ownerAlive := launchOwnerAlive(storedOwnerPID, storedOwnerStart)
	if launchState == "failed" && sessionID == "" {
		return newFailure(KindInvalidOperation, "session_prepare", "the prior launch failed without a recoverable session identity", false, "inspect the recorded launch error before starting another session: "+launchError)
	}
	if launchState == "running" && sessionID == "" {
		return newFailure(KindInvalidOperation, "session_prepare", "the child launch started without a recoverable session identity", false, "reconcile the OpenCode session before starting another launch")
	}
	spawnPermitted := false
	rollbackPermitted := false
	if attemptID == "" {
		attemptID = operationID + ":launch"
		if len(attemptID) > 128 {
			return newFailure(KindInvariantViolation, "session_prepare", "derived launch attempt ID exceeds its bound", false, "contact_operator")
		}
		launchState = "prepared"
		if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET launch_state=?,launch_attempt_id=?,launch_owner_pid=?,launch_owner_start=?,launch_agent=?,launch_directory=?,launch_started_at=?,updated_at=? WHERE operation_id=? AND launch_state='not_started'`, launchState, attemptID, ownerPID, ownerStart, agent, directory, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), operationID); err != nil {
			return err
		}
		spawnPermitted = true
	} else if launchState == "completed" || launchState == "failed" && sessionID != "" || !ownerAlive && sessionID != "" {
		launchState = "prepared"
		if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET launch_state='prepared',launch_owner_pid=?,launch_owner_start=?,launch_error=NULL,launch_finished_at=NULL,updated_at=? WHERE operation_id=?`, ownerPID, ownerStart, now.Format(time.RFC3339Nano), operationID); err != nil {
			return err
		}
		spawnPermitted = true
	} else if !ownerAlive && launchState == "prepared" {
		if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET launch_owner_pid=?,launch_owner_start=?,updated_at=? WHERE operation_id=?`, ownerPID, ownerStart, now.Format(time.RFC3339Nano), operationID); err != nil {
			return err
		}
		rollbackPermitted = true
	}
	*launch = BootstrapLaunch{OperationID: operationID, AttemptID: attemptID, SpawnPermitted: spawnPermitted, RollbackPermitted: rollbackPermitted, Agent: agent, Directory: directory, Model: model, State: launchState}
	if sessionID != "" {
		launch.SessionID = &sessionID
	}
	return nil
}

// RecordBootstrapLaunch stores the launch fence, session identity, and child
// result. The attempt and session identities are immutable on replay.
func (s *Store) RecordBootstrapLaunch(ctx context.Context, operationID, attemptID, productID, workID, sessionID, agent, directory, model, state, failureReason string, ownerPID int64, ownerStart string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "session_record", "store is not open", false, "open the authority database")
	}
	if operationID == "" || attemptID == "" || productID == "" || workID == "" || agent == "" || directory == "" || ownerPID <= 1 || ownerStart == "" || (state != "completed" && state != "failed" && state != "running") {
		return newFailure(KindInvalidOperation, "session_record", "launch record fields are invalid", false, "supply the prepared attempt and a declared state")
	}
	if len(sessionID) > 128 || (sessionID != "" && !bootstrapIDPattern.MatchString(sessionID)) || len(model) > 256 || len(failureReason) > 8192 {
		return newFailure(KindInvalidOperation, "session_record", "launch record exceeds its bounds", false, "supply bounded launch result fields")
	}
	if state == "completed" && (sessionID == "" || model == "") || state == "failed" && failureReason == "" || state != "failed" && failureReason != "" {
		return newFailure(KindInvalidOperation, "session_record", "launch state lacks its required session, model, or failure evidence", false, "supply the evidence required by the declared launch state")
	}
	if !launchOwnerAlive(ownerPID, ownerStart) {
		return newFailure(KindInvalidOperation, "session_record", "launch owner process identity is not live", false, "record from the host process that owns the launch")
	}
	now := s.now()
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return recordBootstrapLaunchTx(ctx, transaction, operationID, attemptID, productID, workID, sessionID, agent, directory, model, state, failureReason, ownerPID, ownerStart, now)
	})
	if err != nil {
		return err
	}
	return s.SyncDurable(ctx)
}

// RollbackBootstrapOperation compensates a prepared launch that has no known
// child session. A recorded session always remains available for exact replay.
func (s *Store) RollbackBootstrapOperation(ctx context.Context, productID, workID, operationID, directory, reason string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if !bootstrapIDPattern.MatchString(productID) || !bootstrapIDPattern.MatchString(workID) || !bootstrapIDPattern.MatchString(operationID) || directory == "" || len(directory) > 4096 || len(reason) > 8192 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "rollback identity or reason is invalid", false, "supply the exact bootstrap identity and a bounded reason")
	}
	var location WorktreeLocation
	var state, launchState, sessionID string
	if err := s.db.QueryRowContext(ctx, `SELECT b.state,b.launch_state,COALESCE(b.launch_session_id,''),b.repo_path,c.pinned_branch,c.pinned_base_sha,c.pinned_path FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.product_id=? AND b.work_id=? AND b.operation_id=?`, productID, workID, operationID).Scan(&state, &launchState, &sessionID, &location.Repo, &location.Branch, &location.BaseSHA, &location.Path); err != nil {
		return wrapFailure(KindProjectionNotFound, "work_bootstrap", "bootstrap operation does not exist", false, "use the exact bootstrap operation", err)
	}
	requestedDirectory, err := normalizePath(directory)
	if err != nil {
		return err
	}
	launchDirectory, err := normalizePath(location.Path)
	if err != nil {
		return err
	}
	if requestedDirectory != launchDirectory {
		return newFailure(KindInvariantViolation, "work_bootstrap", "rollback directory differs from the recorded worktree", false, "invoke rollback from the exact prepared worktree")
	}
	if sessionID != "" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "a recorded child session requires exact replay", false, "resume the recorded session")
	}
	if launchState != "not_started" && launchState != "prepared" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "a child launch might exist without a recorded session identity", false, "reconcile the OpenCode session before rollback")
	}
	if state == "rolled_back" {
		return nil
	}
	if reason == "" {
		reason = "launch failed before session identity"
	}
	if err := s.rollbackBootstrap(ctx, operationID, workID, location, ExecGitRunner{}, true, errors.New(reason)); err != nil {
		return err
	}
	return nil
}

func recordBootstrapLaunchTx(ctx context.Context, transaction *Transaction, operationID, attemptID, productID, workID, sessionID, agent, directory, model, state, failureReason string, ownerPID int64, ownerStart string, nowValue time.Time) error {
	tx, err := transactionSQL(transaction, "session_record")
	if err != nil {
		return err
	}
	var storedAttempt, storedSession, storedAgent, storedDirectory, storedModel string
	var storedOwnerStart string
	var storedOwnerPID int64
	var storedState, bootstrapState string
	var storedProduct, storedWork string
	if err := tx.QueryRowContext(ctx, `SELECT product_id,work_id,COALESCE(launch_attempt_id,''),COALESCE(launch_session_id,''),COALESCE(launch_owner_pid,0),COALESCE(launch_owner_start,''),COALESCE(launch_agent,''),COALESCE(launch_directory,''),COALESCE(launch_model,''),launch_state,state FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&storedProduct, &storedWork, &storedAttempt, &storedSession, &storedOwnerPID, &storedOwnerStart, &storedAgent, &storedDirectory, &storedModel, &storedState, &bootstrapState); err != nil {
		return wrapFailure(KindProjectionNotFound, "session_record", "bootstrap operation does not exist", false, "run session-prepare before session-record", err)
	}
	if storedProduct != productID || storedWork != workID || storedAttempt != attemptID || storedOwnerPID != ownerPID || storedOwnerStart != ownerStart || storedAgent != agent || storedDirectory != directory || storedState == "not_started" || bootstrapState != "completed" {
		return newFailure(KindInvariantViolation, "session_record", "launch record does not match the prepared attempt", false, "use the exact prepared launch identity")
	}
	if storedSession != "" && sessionID != storedSession {
		return newFailure(KindInvalidOperation, "session_record", "launch attempt is bound to a different session", false, "resume the recorded session")
	}
	if storedModel != "" && model != "" && model != storedModel {
		return newFailure(KindInvalidOperation, "session_record", "launch attempt is bound to a different model", false, "record the model read back from the existing session")
	}
	allowedTransitions := map[string]map[string]bool{
		"prepared":  {"running": true, "failed": true},
		"running":   {"running": true, "completed": true, "failed": true},
		"failed":    {"running": true, "failed": true},
		"completed": {"running": true, "completed": true},
	}
	if !allowedTransitions[storedState][state] {
		return newFailure(KindInvalidOperation, "session_record", "launch state transition is not allowed", false, "resume the recorded session before recording another terminal state")
	}
	if storedState == "failed" && storedSession == "" && state == "running" {
		return newFailure(KindInvalidOperation, "session_record", "failed launch has no session identity to resume", false, "inspect the recorded launch error before starting another session")
	}
	if sessionID == "" {
		sessionID = storedSession
	}
	now := nowValue.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `UPDATE bootstrap_operations SET launch_state=?,launch_session_id=COALESCE(NULLIF(?,''),launch_session_id),launch_model=COALESCE(NULLIF(?,''),launch_model),launch_error=CASE WHEN ?='failed' THEN ? ELSE NULL END,launch_finished_at=CASE WHEN ? IN ('completed','failed') THEN ? ELSE NULL END,updated_at=? WHERE operation_id=?`, state, sessionID, model, state, failureReason, state, now, now, operationID)
	return err
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
