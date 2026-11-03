package store

import (
	"context"
	"database/sql"
	"slices"
	"time"
)

// workflowDefinitionStartStep returns the step an instance occupies before it
// takes its first action.
func workflowDefinitionStartStep(definition WorkflowDefinition) (string, error) {
	start := definition.StepGraph.StartStep
	if workflowStep(definition, start) == nil {
		return "", newFailure(KindInvariantViolation, "workflow_step", "pinned workflow definition declares no start step", false, "repair the workflow definition registry")
	}
	return start, nil
}

// workflowDefinitionContractStep returns the step that declares
// approve_contract. A superseded contract returns the instance here, and every
// registered family declares that action on exactly one step.
func workflowDefinitionContractStep(definition WorkflowDefinition) (string, error) {
	found := ""
	for _, step := range definition.StepGraph.Steps {
		if !slices.Contains(step.Actions, "approve_contract") {
			continue
		}
		if found != "" {
			return "", newFailure(KindInvariantViolation, "workflow_step", "pinned workflow definition declares approve_contract on more than one step", false, "repair the workflow definition registry")
		}
		found = step.ID
	}
	if found == "" {
		return "", newFailure(KindInvariantViolation, "workflow_step", "pinned workflow definition declares no contract step", false, "repair the workflow definition registry")
	}
	return found, nil
}

// startWorkflowInstanceStepTx records the start of a frontier step without
// changing the plan.
func startWorkflowInstanceStepTx(ctx context.Context, tx *sql.Tx, _ DefinitionRegistry, workID, stepID, actorRef, executionModel string, at time.Time) error {
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
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',execution_actor_ref=?,execution_model=?,started_at=coalesce(started_at,?),execution_started_at=CASE WHEN ? THEN coalesce(execution_started_at,?) ELSE execution_started_at END WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, actorRef, executionModel, now, step.ID == "implement" || step.Reviewed, now, workID)
	if err != nil {
		return workflowProjectionError(err, "cannot start workflow action")
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "workflow is terminal or missing", false, "start an active workflow")
	}
	return nil
}

// workflowCompletedInstanceActionImmutable reports terminal workflow instances.
func workflowCompletedInstanceActionImmutable(state string) bool {
	return state == "completed" || state == "cancelled" || state == "superseded"
}
