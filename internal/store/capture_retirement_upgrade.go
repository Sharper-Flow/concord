package store

import (
	"context"
	"database/sql"
)

func retireCapture(ctx context.Context, q migrationRunner, migrationSQL string) error {
	if err := requireCaptureRetirementSnapshot(ctx, q); err != nil {
		return err
	}
	// UUIDs were globally unique before this migration, but human keys were
	// not. Refuse conflicting live identities rather than choose a survivor.
	var key string
	err := q.QueryRowContext(ctx, `SELECT l.human_key FROM linear_issue_links l
 JOIN work_items w ON w.id=l.work_id WHERE w.lifecycle IN ('needed','in_progress')
 GROUP BY l.human_key HAVING count(*)>1 ORDER BY l.human_key LIMIT 1`).Scan(&key)
	if err == nil {
		return newFailure(KindUpgradeBlocked, "migrate", "multiple live work items hold Linear issue "+key, false, "resolve the live issue collision through the current lifecycle operations before upgrade")
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = q.ExecContext(ctx, migrationSQL)
	return err
}

// Capture retirement rebuilds work and relation projections, so any existing
// work needs the snapshot verified against the pre-migration transaction.
func requireCaptureRetirementSnapshot(ctx context.Context, q migrationRunner) error {
	var populated bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items)
 OR EXISTS(SELECT 1 FROM relations)
 OR EXISTS(SELECT 1 FROM workflow_overlap_resolutions)
 OR EXISTS(SELECT 1 FROM linear_issue_links)
 OR EXISTS(SELECT 1 FROM project_governing_requirements)
 OR EXISTS(SELECT 1 FROM work_context_state)
 OR EXISTS(SELECT 1 FROM work_context_findings)`).Scan(&populated); err != nil {
		return err
	}
	if !populated {
		return nil
	}
	if proof, ok := ctx.Value(outcomeRetirementProofKey{}).(*outcomeRetirementProof); ok {
		if proof.verified {
			return nil
		}
		if proof.refusal != nil {
			return proof.refusal
		}
	}
	return newFailure(KindUpgradeBlocked, "migrate", "capture retirement requires a verified snapshot", false, "run concord upgrade with a verified snapshot")
}
