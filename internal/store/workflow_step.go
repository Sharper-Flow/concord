package store

import (
	"context"
	"database/sql"
	"time"
)

// startWorkflowInstanceStepTx records the start of a frontier step without
// changing the plan.
func startWorkflowInstanceStepTx(ctx context.Context, tx *sql.Tx, workID, stepID, actorRef, executionModel string, at time.Time) error {
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	var step WorkflowPlanStep
	found := false
	if plan != nil {
		for _, candidate := range WorkflowStepPlanFrontier(*plan) {
			if candidate.ID == stepID {
				step, found = candidate, true
				break
			}
		}
	}
	if !found {
		return newFailure(KindIllegalLifecycleTransition, "workflow_step", "step is not on the workflow plan frontier", false, "start a frontier step")
	}
	now := at.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',execution_actor_ref=?,execution_model=?,started_at=coalesce(started_at,?),execution_started_at=CASE WHEN ? THEN coalesce(execution_started_at,?) ELSE execution_started_at END WHERE work_id=? AND instance_state NOT IN ('completed','cancelled')`, actorRef, executionModel, now, step.ID == "implement" || step.Reviewed, now, workID)
	if err != nil {
		return workflowProjectionError(err, "cannot start workflow action")
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "workflow is terminal or missing", false, "start an active workflow")
	}
	return nil
}

func workflowCompletedInstanceActionImmutable(state string) bool {
	return state == "completed" || state == "cancelled"
}
