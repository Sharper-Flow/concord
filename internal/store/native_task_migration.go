package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// The upgrade owner supplies its verified-snapshot transaction and rolls it
// back on any error, including a failure after destructive DDL has begun.
func applyNativeTaskCutover(ctx context.Context, tx *sql.Tx, retirementSQL string) error {
	if err := validateNativeTaskCutover(ctx, tx); err != nil {
		return err
	}
	var guarded bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM write_guard WHERE active=1)`).Scan(&guarded); err != nil {
		return err
	}
	if !guarded {
		if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			return err
		}
	}
	if err := normalizeNativeTaskCorrections(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, retirementSQL); err != nil {
		return wrapFailure(KindUnavailable, "migrate", "native Task retirement failed", false, "roll back the upgrade transaction and correct the migration definition", err)
	}
	if !guarded {
		_, err := tx.ExecContext(ctx, `DELETE FROM write_guard WHERE active=1`)
		return err
	}
	return nil
}

func normalizeNativeTaskCorrections(ctx context.Context, tx *sql.Tx) error {
	// Authorization consumes an unbound direction only after that same attempt
	// materialized. Its authorization sequence, not its later dispatch sequence,
	// bounds the directions it could have received.
	rows, err := tx.QueryContext(ctx, `SELECT c.work_id,c.state,COALESCE((
	 SELECT MAX(a.seq) FROM worker_authorizations a JOIN worker_attempts t
	 ON t.work_id=a.work_id AND t.attempt_id=a.attempt_id
	 WHERE a.work_id=c.work_id AND t.dispatch_payload IS NOT NULL),0)
	 FROM workflow_correction_state c ORDER BY c.work_id`)
	if err != nil {
		return workflowProjectionError(err, "cannot inspect legacy correction consumption")
	}
	type correctionUpdate struct{ work, state string }
	var updates []correctionUpdate
	for rows.Next() {
		var work, raw string
		var consumedThrough int64
		if err := rows.Scan(&work, &raw, &consumedThrough); err != nil {
			rows.Close()
			return err
		}
		state, err := normalizeNativeTaskCorrection(raw, consumedThrough)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, correctionUpdate{work: work, state: workflowJSON(state)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_correction_state SET state=? WHERE work_id=?`, update.state, update.work); err != nil {
			return workflowProjectionError(err, "cannot normalize legacy correction consumption")
		}
	}
	return nil
}

func normalizeNativeTaskCorrection(raw string, consumedThrough int64) (correctionStateFact, error) {
	var legacy correctionStateFact
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return correctionStateFact{}, newFailure(KindInvariantViolation, "migrate", "legacy correction state is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	state := correctionStateFact{Boundary: max(legacy.Boundary, consumedThrough)}
	for _, record := range legacy.Records {
		if record.Fields.ActionID != "request_correction" {
			continue
		}
		if record.Seq <= 0 || record.SourceID == "" {
			return correctionStateFact{}, newFailure(KindInvariantViolation, "migrate", "legacy correction direction has no source identity", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if record.Seq >= consumedThrough && (len(state.Records) == 0 || record.Seq > state.Records[0].Seq) {
			state.Records = []correctionRecordFact{record}
		}
	}
	if len(state.Records) != 0 {
		// The old acceptance boundary did not consume an unbound request.
		state.Boundary = 0
	}
	return state, nil
}

// Member execution metadata can retire only after its native owners settle.
// The upgrade must never collapse independently claimed trees into one claim.
func validateNativeTaskCutover(ctx context.Context, q queryer) error {
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT
	 EXISTS(SELECT 1 FROM worktree_entries WHERE member_id<>'' AND state='active') OR
	 EXISTS(SELECT 1 FROM worktree_claims WHERE member_id<>'' AND state IN ('pending','verified')) OR
	 EXISTS(SELECT 1 FROM worktree_entries e JOIN worktree_occupancy o ON o.worktree_id=e.set_id||':'||e.project_id||':'||e.claim_op_id WHERE e.member_id<>'') OR
	 EXISTS(SELECT 1 FROM worktree_entries e JOIN worktree_verify_leases l ON l.work_id IN (SELECT work_id FROM worktree_claims WHERE set_id=e.set_id) AND l.project_id=e.project_id AND l.path=e.path WHERE e.member_id<>'' AND (l.state='held' OR l.outcome='running')) OR
	 EXISTS(SELECT 1 FROM worktree_entries e JOIN worktree_ref_outcomes r ON r.set_id=e.set_id AND r.path=e.path AND r.branch=e.branch WHERE e.member_id<>'' AND r.phase NOT IN ('deleted','settled'))`).Scan(&blocked)
	if err != nil {
		return wrapFailure(KindUnavailable, "migrate", "cannot inspect retained member worktrees", false, "restore readable native claim state before upgrading", err)
	}
	if blocked {
		return newFailure(KindResourceBusy, "migrate", "native Task cutover retains an active or unsettled member worktree", false, "reconcile and reclaim every member worktree with the installed release before upgrading; preserve unmerged branches")
	}
	return nil
}
