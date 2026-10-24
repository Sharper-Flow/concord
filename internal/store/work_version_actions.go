package store

import (
	"context"
	"database/sql"
)

func recordWorkVersionActionTx(ctx context.Context, tx *sql.Tx, event Event, actionID string, resultingVersion int64) error {
	if actionID != "" {
		_, err := tx.ExecContext(ctx, `INSERT INTO work_version_actions(work_id,resulting_version,action_id,session_ref,effect_seq)
		 SELECT ?,?,?,session_ref,? FROM workflow_actors WHERE actor_ref=?`, event.SubjectID, resultingVersion, actionID, event.Seq, event.Actor)
		if err != nil {
			return workflowProjectionError(err, "cannot record the version conflict action")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET version_actions_floor=max(version_actions_floor,version-256,0) WHERE id=?`, event.SubjectID); err != nil {
		return workflowProjectionError(err, "cannot advance the version conflict retention floor")
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM work_version_actions WHERE work_id=? AND resulting_version<=(SELECT version_actions_floor FROM work_items WHERE id=?)`, event.SubjectID, event.SubjectID)
	return workflowProjectionError(err, "cannot prune the version conflict actions")
}
