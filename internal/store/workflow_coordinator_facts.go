package store

import (
	"context"
	"database/sql"
	"time"
)

// writeWorkflowCoordinatorActionTx retains each action actor's current display
// fact after the action fold validates it. Timestamp text is the published
// LastActedAt ordering; effect sequence breaks ties, not regressing timestamps.
// Actor registration can follow the action, so class filtering belongs to reads.
func writeWorkflowCoordinatorActionTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if event.SubjectType != SubjectWorkItem {
		return nil
	}
	switch event.Kind {
	case WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed:
	default:
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO workflow_coordinator_actions(work_id,actor_ref,last_effect_seq,last_action_id,last_acted_at)
		VALUES(?,?,?,COALESCE(json_extract(?,'$.action_id'),?),?)
		ON CONFLICT(work_id,actor_ref) DO UPDATE SET
			last_effect_seq=excluded.last_effect_seq,
			last_action_id=excluded.last_action_id,
			last_acted_at=excluded.last_acted_at
		WHERE excluded.last_acted_at>workflow_coordinator_actions.last_acted_at
		   OR (excluded.last_acted_at=workflow_coordinator_actions.last_acted_at
		       AND excluded.last_effect_seq>workflow_coordinator_actions.last_effect_seq)`,
		event.SubjectID, event.Actor, event.Seq, string(event.Payload), event.Kind, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record the current coordinator action")
}
