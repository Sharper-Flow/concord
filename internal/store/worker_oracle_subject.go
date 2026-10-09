package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// oracleGitSubjectSnapshot is a native observation, never a caller identity.
// It is captured outside both verify transactions. A dirty tree can still
// complete a legacy verify, but cannot identify that run as a commit subject.
type oracleGitSubjectSnapshot struct {
	head  string
	clean bool
}

func snapshotOracleGitSubject(ctx context.Context, runner GitRunner, path string) (oracleGitSubjectSnapshot, error) {
	status, err := runner.Run(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return oracleGitSubjectSnapshot{}, wrapFailure(KindGitUnreachable, "worktree_verify", "cannot observe the oracle subject's clean status", true, "retry once the worktree is reachable", err)
	}
	head, err := runner.Run(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return oracleGitSubjectSnapshot{}, wrapFailure(KindGitUnreachable, "worktree_verify", "cannot observe the oracle subject's commit", true, "retry once the worktree is reachable", err)
	}
	return oracleGitSubjectSnapshot{head: strings.TrimSpace(string(head)), clean: len(status) == 0}, nil
}

func oracleVerifySubject(before, after oracleGitSubjectSnapshot, changed bool) string {
	if changed || !before.clean || !after.clean || before.head != after.head || !worktreeSHAPattern.MatchString(before.head) {
		return ""
	}
	return "commit:" + before.head
}

// oracleVerifyReceiptSQL joins the existing lease outcome to its green durable
// producer. Receipt text, claim git_facts, and model reports cannot supply this
// subject. Both existing records must retain the same bounded native result.
const oracleVerifyReceiptSQL = `SELECT l.result_json FROM worktree_verify_leases l
	JOIN durable_operations d ON d.work_id=l.work_id AND d.op_id=(
		CASE WHEN instr(l.lease_id,':worktree-verify:')>0
		THEN 'worktree_verify:' || substr(l.lease_id,1,instr(l.lease_id,':worktree-verify:')-1)
		ELSE 'worktree_verify:' || l.lease_id END)
	WHERE l.work_id=? AND l.state='released' AND l.outcome='completed' AND l.exit_code=0
	AND d.workflow_type_ref='worktree.verify' AND d.attempt_epoch=1 AND d.result_kind='completed'
	AND d.principal_ref=l.principal_ref
	AND d.result_payload=l.result_json AND json_valid(l.result_json)
	AND json_extract(l.result_json,'$.work_id')=l.work_id
	AND json_extract(l.result_json,'$.project_id')=l.project_id
	AND json_extract(l.result_json,'$.path')=l.path
	AND json_extract(l.result_json,'$.lease_id')=l.lease_id
	AND json_extract(l.result_json,'$.operation_ref')=d.op_id
	AND json_extract(l.result_json,'$.exit_code')=l.exit_code
	AND json_extract(l.result_json,'$.command')=l.command_json
	AND json_extract(l.result_json,'$.tracked_files_changed')=0
	AND json_extract(l.result_json,'$.subject_ref') LIKE 'commit:%'`

func decodeOracleVerifyReceipt(raw, workID string) (*WorktreeVerifyResult, error) {
	var result WorktreeVerifyResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, wrapFailure(KindInvariantViolation, "oracle_subject_read", "the stored verify subject receipt is malformed", false, "reconcile_operation", err)
	}
	oid, ok := strings.CutPrefix(result.SubjectRef, "commit:")
	if !ok || !worktreeSHAPattern.MatchString(oid) || result.WorkID != workID || result.ExitCode != 0 || result.TrackedFilesChanged || result.LeaseID == "" || result.OperationRef != worktreeVerifyOperationRef(result.LeaseID) {
		return nil, newFailure(KindInvariantViolation, "oracle_subject_read", "the stored verify subject receipt does not match its producer", false, "reconcile_operation")
	}
	return &result, nil
}

// readOracleVerifySubjectReceipt resolves one actual producer receipt for the
// terminal receipt owner. A missing, historical subject-less, failed, mutated,
// wrong-work, or unjoined run returns nil; it never qualifies a reported claim.
// The caller's queryer may be its transaction: this function performs SQL only.
func readOracleVerifySubjectReceipt(ctx context.Context, q queryer, workID, runRef string) (*WorktreeVerifyResult, error) {
	var raw string
	err := q.QueryRowContext(ctx, oracleVerifyReceiptSQL+` AND d.op_id=? LIMIT 1`, workID, runRef).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "oracle_subject_read", "cannot read the verify subject receipt", true, "retry once the database is readable", err)
	}
	return decodeOracleVerifyReceipt(raw, workID)
}

// readCurrentOracleSubject selects the newest qualified verify receipt for an
// active worktree. It does not claim that Git is still there: the
// adapter must compare this pinned subject with its fresh clean HEAD before
// authorization. A later dirty/failed run contributes no qualified subject.
func readCurrentOracleSubject(ctx context.Context, q queryer, workID string) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, oracleVerifyReceiptSQL+`
		AND EXISTS (SELECT 1 FROM worktree_entries e
			WHERE e.set_id=? AND e.project_id=l.project_id AND e.path=l.path
			AND e.branch=json_extract(l.result_json,'$.branch')
			AND e.state='active')
		ORDER BY d.rowid DESC LIMIT 1`, workID, WorktreeSetID(workID)).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "oracle_subject_read", "cannot read the current verify subject", true, "retry once the database is readable", err)
	}
	result, err := decodeOracleVerifyReceipt(raw, workID)
	if err != nil {
		return "", err
	}
	return result.SubjectRef, nil
}
