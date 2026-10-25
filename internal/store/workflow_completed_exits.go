package store

import (
	"context"
	"database/sql"
)

// A completion witnesses a current admission class, not a replay position.
// Repeating the same family's step/action replaces that class's coordinate.
func writeWorkflowCompletedExitTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, definitionRef string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_completed_exits(work_id,definition_ref,step_id,action_id,exit_seq)
VALUES(?,?,?,?,?) ON CONFLICT(work_id,definition_ref,step_id,action_id) DO UPDATE SET exit_seq=max(exit_seq,excluded.exit_seq)`, event.SubjectID, definitionRef, p.StepID, p.ActionID, event.Seq)
	return workflowProjectionError(err, "cannot record the current completed exit")
}
