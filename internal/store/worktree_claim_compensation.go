package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

type claimNativeGeneration struct{ path, branch int64 }

func readClaimNativeGeneration(ctx context.Context, q queryer, location WorktreeLocation) (claimNativeGeneration, error) {
	var generation claimNativeGeneration
	err := q.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT generation FROM worktree_claim_native_fences WHERE resource_kind='path' AND repository_id='' AND resource_ref=?),0),
		COALESCE((SELECT generation FROM worktree_claim_native_fences WHERE resource_kind='branch' AND repository_id=? AND resource_ref=?),0)`,
		location.Path, location.Repo, location.Branch).Scan(&generation.path, &generation.branch)
	if err != nil {
		return generation, wrapFailure(KindUnavailable, "worktree_claim", "cannot read native claim generations", true, "retry once the native fences are readable", err)
	}
	return generation, nil
}

func checkClaimNativeGeneration(ctx context.Context, q queryer, native WorktreeClaimNative) error {
	current, err := readClaimNativeGeneration(ctx, q, native.Location)
	if err != nil {
		return err
	}
	if current != native.generation {
		return newFailure(KindStaleContext, "worktree_claim", "native claim preparation predates compensation", false, "prepare the native claim again before retrying")
	}
	return nil
}

func snapshotClaimNative(ctx context.Context, q queryer, req WorktreeClaimRequest, native *WorktreeClaimNative) error {
	if err := refuseForeignNativeClaim(ctx, q, req, native.Location); err != nil {
		return err
	}
	var err error
	native.generation, err = readClaimNativeGeneration(ctx, q, native.Location)
	return err
}

func refuseForeignNativeClaim(ctx context.Context, q queryer, req WorktreeClaimRequest, location WorktreeLocation) error {
	memberID, err := worktreeClaimMemberScope(ctx, q, req)
	if err != nil {
		return err
	}
	var held bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_claims WHERE (op_id<>? OR principal_ref='worktree_claim_compensation') AND state IN ('pending','verified') AND
		(pinned_path=? OR (repository_id=? AND pinned_branch=?) OR (work_id=? AND project_id=? AND member_id=?))) OR
		EXISTS(SELECT 1 FROM worktree_entries WHERE claim_op_id<>? AND state='active' AND (path=? OR (repository_id=? AND branch=?)))`,
		req.OpID, location.Path, location.Repo, location.Branch, req.WorkID, req.ProjectID, memberID,
		req.OpID, location.Path, location.Repo, location.Branch).Scan(&held)
	if err != nil {
		return wrapFailure(KindUnavailable, "worktree_claim", "cannot read native claim ownership", true, "retry once claim ownership is readable", err)
	}
	if held {
		return newFailure(KindProjectionConflict, "worktree_claim", "another active claim owns this native path or repository branch", false, "reconcile the holding claim before preparing again")
	}
	return nil
}

func compensationClaimID(created WorktreeClaimCreation) string {
	raw, _ := json.Marshal([]string{created.WorkID, created.ProjectID, created.ClaimOpID, created.RepoRoot, created.Path, created.Branch, created.Base})
	return fmt.Sprintf("claim-compensation:%x", sha256.Sum256(raw))
}

type claimCompensationExecutor struct {
	pid   int
	start uint64
}

func incompleteClaimCompensation(cause, reason error) error {
	var failure *Failure
	if errors.As(cause, &failure) {
		failure.EffectPossible = true
		failure.Detail += "; claim compensation incomplete: " + reason.Error()
		return cause
	}
	failure = wrapFailure(KindUnavailable, "worktree_claim", "claim compensation incomplete: "+reason.Error(), true, "retry the same operation with the same op id", cause)
	failure.EffectPossible = true
	return failure
}

// CompensateWorktreeClaimCreation removes only an unowned failed creation.
// The reservation and generation increments commit together before Git. A
// committed winner, unreadable owner, or stale creation never grants deletion.
// Incomplete native cleanup keeps its exact reservation for recovery.
func (s *Store) CompensateWorktreeClaimCreation(ctx context.Context, runner GitRunner, created WorktreeClaimCreation, cause error) error {
	if s == nil || s.db == nil || created.WorkID == "" || created.ProjectID == "" || created.ClaimOpID == "" {
		return incompleteClaimCompensation(cause, errors.New("native creation lacks its store or original claim identity"))
	}
	ctx = context.WithoutCancel(ctx)
	return s.compensateClaimCreation(ctx, runner, created, cause)
}

var errClaimCreationOwned = errors.New("native creation has a current durable owner")

func reserveClaimCompensationTx(ctx context.Context, tx *sql.Tx, created WorktreeClaimCreation, executor claimCompensationExecutor, now time.Time) error {
	reservation := compensationClaimID(created)
	if err := requireActionableWork(ctx, tx, created.WorkID, "worktree_claim"); err != nil {
		return err
	}
	var owned bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_claims WHERE op_id<>? AND state IN ('pending','verified') AND (pinned_path=? OR (repository_id=? AND pinned_branch=?) OR (work_id=? AND project_id=? AND member_id=?))) OR EXISTS(SELECT 1 FROM worktree_entries WHERE state='active' AND (path=? OR (repository_id=? AND branch=?)))`,
		reservation, created.Path, created.RepoRoot, created.Branch, created.WorkID, created.ProjectID, created.MemberID, created.Path, created.RepoRoot, created.Branch).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return errClaimCreationOwned
	}
	location := WorktreeLocation{Repo: created.RepoRoot, Path: created.Path, Branch: created.Branch}
	generation, err := readClaimNativeGeneration(ctx, tx, location)
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_claims WHERE op_id=?)`, reservation).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return reacquireClaimCompensationTx(ctx, tx, created, executor, now, generation)
	}
	if generation != created.generation {
		return newFailure(KindStaleContext, "worktree_claim", "creation predates a native compensation generation", false, "retry the original claim with fresh native preparation")
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,executor_pid,executor_pid_start,member_id) VALUES(?,?,?,?,?,?,?,?,'pending','worktree_claim_compensation',?,?,?,?,?,?)`,
		reservation, created.WorkID, created.ProjectID, WorktreeSetID(created.WorkID), created.RepoRoot, created.Branch, created.Base, created.Path, created.ClaimOpID, stamp, stamp, executor.pid, executor.start, created.MemberID); err != nil {
		return err
	}
	for _, resource := range []struct{ kind, repo, ref string }{{"path", "", created.Path}, {"branch", created.RepoRoot, created.Branch}} {
		result, err := tx.ExecContext(ctx, `INSERT INTO worktree_claim_native_fences(resource_kind,repository_id,resource_ref,set_id,generation) VALUES(?,?,?,?,1)
				ON CONFLICT(resource_kind,repository_id,resource_ref) DO UPDATE SET generation=generation+1 WHERE set_id=excluded.set_id`,
			resource.kind, resource.repo, resource.ref, WorktreeSetID(created.WorkID))
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.Join(errors.New("cannot advance an independently owned native fence"), err)
		}
	}
	return nil
}

func reacquireClaimCompensationTx(ctx context.Context, tx *sql.Tx, created WorktreeClaimCreation, executor claimCompensationExecutor, now time.Time, generation claimNativeGeneration) error {
	reservation := compensationClaimID(created)
	var exact bool
	var previous claimCompensationExecutor
	if err := tx.QueryRowContext(ctx, `SELECT work_id=? AND project_id=? AND set_id=? AND repository_id=? AND pinned_path=? AND pinned_branch=? AND pinned_base_sha=? AND state='pending' AND request_id=? AND principal_ref='worktree_claim_compensation', executor_pid, executor_pid_start FROM worktree_claims WHERE op_id=?`,
		created.WorkID, created.ProjectID, WorktreeSetID(created.WorkID), created.RepoRoot, created.Path, created.Branch, created.Base, created.ClaimOpID, reservation).Scan(&exact, &previous.pid, &previous.start); err != nil {
		return err
	}
	if !exact || generation.path != created.generation.path+1 || generation.branch != created.generation.branch+1 {
		return newFailure(KindStaleContext, "worktree_claim", "compensation reservation no longer matches this creation", false, "retry the original claim to reconcile compensation")
	}
	// A row CAS cannot stop Git already running in the old executor. Only
	// proven process death permits takeover; unreadable liveness preserves
	// the reservation. Zero means the executor returned without finishing.
	if previous.pid != 0 {
		start, err := hostlease.ProcessStart(previous.pid)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return wrapFailure(KindUnavailable, "worktree_claim", "cannot read native compensation executor liveness", true, "retry when the recorded executor is observable", err)
		}
		if err == nil && start == previous.start {
			return newFailure(KindProjectionConflict, "worktree_claim", "native claim compensation is still executing", true, "retry the original claim after compensation finishes")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET executor_pid=?,executor_pid_start=?,updated_at=? WHERE op_id=? AND executor_pid=? AND executor_pid_start=?`,
		executor.pid, executor.start, now.Format(time.RFC3339Nano), reservation, previous.pid, previous.start)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.Join(errors.New("cannot acquire the native compensation executor"), err)
	}
	return nil
}

func (s *Store) compensateClaimCreation(ctx context.Context, runner GitRunner, created WorktreeClaimCreation, cause error) error {
	executor, err := s.acquireClaimCompensation(ctx, created)
	if err == errClaimCreationOwned {
		return cause
	}
	if err != nil {
		return incompleteClaimCompensation(cause, err)
	}
	return s.executeClaimCompensation(ctx, runner, created, executor, cause)
}

func (s *Store) acquireClaimCompensation(ctx context.Context, created WorktreeClaimCreation) (claimCompensationExecutor, error) {
	executor := claimCompensationExecutor{pid: os.Getpid()}
	var err error
	executor.start, err = hostlease.ProcessStart(executor.pid)
	if err != nil {
		return executor, err
	}
	now := s.now()
	err = s.TransactDurable(ctx, func(transaction *Transaction) error {
		return reserveClaimCompensationTx(ctx, transaction.tx, created, executor, now)
	})
	return executor, err
}

func (s *Store) executeClaimCompensation(ctx context.Context, runner GitRunner, created WorktreeClaimCreation, executor claimCompensationExecutor, cause error) error {
	if runner == nil {
		runner = ExecGitRunner{}
	}
	if err := removeClaimCreation(ctx, runner, created); err != nil {
		return s.yieldClaimCompensation(ctx, created, executor, cause, err)
	}
	err := s.TransactDurable(ctx, func(transaction *Transaction) error {
		return releaseClaimCompensationTx(ctx, transaction.tx, created, executor)
	})
	if err != nil {
		return s.yieldClaimCompensation(ctx, created, executor, cause, err)
	}
	return cause
}

// Yield only after Git has returned. The pending reservation continues to
// exclude claim publication, while an exact retry can acquire its idle executor.
func (s *Store) yieldClaimCompensation(ctx context.Context, created WorktreeClaimCreation, executor claimCompensationExecutor, cause, reason error) error {
	err := s.TransactDurable(ctx, func(transaction *Transaction) error {
		return yieldClaimCompensationTx(ctx, transaction.tx, created, executor)
	})
	return incompleteClaimCompensation(cause, errors.Join(reason, err))
}

func yieldClaimCompensationTx(ctx context.Context, tx *sql.Tx, created WorktreeClaimCreation, executor claimCompensationExecutor) error {
	result, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET executor_pid=0,executor_pid_start=0 WHERE op_id=? AND executor_pid=? AND executor_pid_start=? AND state='pending' AND principal_ref='worktree_claim_compensation'`,
		compensationClaimID(created), executor.pid, executor.start)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.Join(errors.New("cannot yield the exact native compensation executor"), err)
	}
	return nil
}

func releaseClaimCompensationTx(ctx context.Context, tx *sql.Tx, created WorktreeClaimCreation, executor claimCompensationExecutor) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM worktree_claims WHERE op_id=? AND work_id=? AND project_id=? AND repository_id=? AND pinned_path=? AND pinned_branch=? AND pinned_base_sha=? AND request_id=? AND state='pending' AND principal_ref='worktree_claim_compensation' AND executor_pid=? AND executor_pid_start=?`,
		compensationClaimID(created), created.WorkID, created.ProjectID, created.RepoRoot, created.Path, created.Branch, created.Base, created.ClaimOpID, executor.pid, executor.start)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.Join(errors.New("cannot release the exact native compensation reservation"), err)
	}
	return nil
}

// Recovery knows the exact request and pins, but a restarted process has no
// proof that it created the branch. It may remove its clean reserved tree,
// preserve the branch, release the reservation, and prepare afresh to adopt it.
func (s *Store) recoverClaimCompensation(ctx context.Context, runner GitRunner, req WorktreeClaimRequest, location WorktreeLocation) error {
	created := WorktreeClaimCreation{WorkID: req.WorkID, ProjectID: req.ProjectID, ClaimOpID: req.OpID, RepoRoot: location.Repo, Path: location.Path, Branch: location.Branch, Base: req.BaseSHA}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_claims WHERE op_id=?)`, compensationClaimID(created)).Scan(&exists); err != nil {
		return wrapFailure(KindUnavailable, "worktree_claim", "cannot read compensation recovery", true, "retry the exact original claim", err)
	}
	if !exists {
		return nil
	}
	generation, err := readClaimNativeGeneration(ctx, s.db, location)
	if err != nil {
		return err
	}
	if generation.path < 1 || generation.branch < 1 {
		return newFailure(KindStaleContext, "worktree_claim", "compensation reservation lacks native generations", false, "reconcile the exact native reservation")
	}
	created.generation = claimNativeGeneration{generation.path - 1, generation.branch - 1}
	executor, err := s.acquireClaimCompensation(ctx, created)
	if err != nil {
		return err
	}
	cause := newFailure(KindUnavailable, "worktree_claim", "resuming interrupted native compensation", true, "retry the exact original claim")
	result := s.executeClaimCompensation(ctx, runner, created, executor, cause)
	if result != cause || cause.EffectPossible {
		return result
	}
	return nil
}
