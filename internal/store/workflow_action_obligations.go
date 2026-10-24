package store

import (
	"context"
	"database/sql"
)

// advanceWorkflowActionObligationsTx retains only unresolved starts and
// failures. A terminal disposes its exact step/epoch, never a newer or older
// obligation on the same work.
func advanceWorkflowActionObligationsTx(ctx context.Context, tx *sql.Tx, event Event, step string, epoch int64, actor string) error {
	if event.Kind == WorkflowActionCompleted || event.Kind == WorkflowActionFailed {
		query := `DELETE FROM workflow_action_obligations WHERE work_id=? AND step_id=? AND attempt_epoch=? AND effect_seq<?`
		if _, err := tx.ExecContext(ctx, query, event.SubjectID, step, epoch, event.Seq); err != nil {
			return workflowProjectionError(err, "cannot dispose the matching workflow action obligation")
		}
	}
	if event.Kind == WorkflowActionCompleted {
		_, err := tx.ExecContext(ctx, `INSERT INTO workflow_step_completions(work_id,step_id,completion_seq) VALUES(?,?,?) ON CONFLICT(work_id,step_id) DO UPDATE SET completion_seq=excluded.completion_seq`, event.SubjectID, step, event.Seq)
		return workflowProjectionError(err, "cannot record the step completion frontier")
	}
	state := "started"
	if event.Kind == WorkflowActionFailed {
		state = "failed"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_action_obligations(work_id,step_id,attempt_epoch,state,actor_ref,effect_seq,payload) VALUES(?,?,?,?,?,?,?)`, event.SubjectID, step, epoch, state, actor, event.Seq, string(event.Payload))
	return workflowProjectionError(err, "cannot retain the unresolved workflow action obligation")
}
