package store

import (
	"context"
	"database/sql"
)

// recordHostReviewFoundationTx is called only by the live initial-creation
// producers, before they grant the coordinator a worktree. Replays of generic
// worktree events do not establish a pre-edit observation.
func recordHostReviewFoundationTx(ctx context.Context, tx *sql.Tx, workID, projectID, claimID, path, subject string) (retErr error) {
	if !worktreeSHAPattern.MatchString(subject) {
		return workerDiffRefusal("host creation has no raw review foundation")
	}
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		return err
	}
	defer func() {
		if err := scope.close(ctx); retErr == nil {
			retErr = err
		}
	}()
	_, err = tx.ExecContext(ctx, `INSERT INTO worktree_review_foundations(work_id,project_id,worktree_identity,subject_commit,claim_op_id,captured_seq)
		SELECT ?,?,?,?,?,last FROM store_sequences WHERE name='effect'`, workID, projectID, workerWorktreeIdentity(path), subject, claimID)
	return err
}

func hostReviewFoundationTx(ctx context.Context, q queryer, workID, projectID, identity string, reviewSeq int64) (string, error) {
	var subject, claimID string
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT subject_commit,captured_seq,claim_op_id FROM worktree_review_foundations WHERE work_id=? AND project_id=? AND worktree_identity=?`, workID, projectID, identity).Scan(&subject, &seq, &claimID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !worktreeSHAPattern.MatchString(subject) || seq >= reviewSeq {
		return "", workerDiffRefusal("host review foundation does not predate this review")
	}
	var active bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_entries WHERE set_id=? AND project_id=? AND claim_op_id=? AND state='active')`, WorktreeSetID(workID), projectID, claimID).Scan(&active); err != nil {
		return "", err
	}
	if !active {
		return "", workerDiffRefusal("host review foundation belongs to a different worktree claim")
	}
	return subject, nil
}
