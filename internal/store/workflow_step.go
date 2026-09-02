package store

import (
	"context"
	"database/sql"
	"time"
)

// The pinned workflow definition owns an instance's step vocabulary. Every
// write of workflow_instances.current_step happens in this file and resolves
// its step from the definition, so an instance can never name a step its own
// definition does not declare.
//
// A step outside that vocabulary is not a cosmetic error. definitionStepAllows
// refuses every action whose step it cannot find, so an instance holding an
// undeclared step accepts no action at all, including any that would repair it.

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
		if !containsString(step.Actions, "approve_contract") {
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

// workflowDeclaredStep confirms that stepID belongs to definition before it
// reaches storage.
func workflowDeclaredStep(definition WorkflowDefinition, stepID string) (string, error) {
	if workflowStep(definition, stepID) == nil {
		return "", newFailure(KindIllegalLifecycleTransition, "workflow_step", "step is not declared by the pinned workflow definition", false, "use a step the pinned definition declares")
	}
	return stepID, nil
}

// pinnedWorkflowDefinitionTx reads the definition an instance is pinned to and
// verifies the pin against the registry.
func pinnedWorkflowDefinitionTx(ctx context.Context, tx *sql.Tx, workID string) (RegisteredDefinition, error) {
	var pin WorkflowDefinitionPin
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, newFailure(KindProjectionNotFound, "workflow_step", "workflow instance does not exist", false, "select a workflow definition first")
		}
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_step", "cannot read the pinned workflow definition", true, "retry once the database is readable", err)
	}
	return VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), pin)
}

// pinWorkflowInstanceTx records the definition an instance follows and places
// it on that definition's start step. Re-pinning before execution starts is
// re-initialization: the instance takes the new definition's start step, so
// the pair stays coherent rather than stranding a step the new definition
// does not declare.
func pinWorkflowInstanceTx(ctx context.Context, tx *sql.Tx, workID string, definition RegisteredDefinition) error {
	start, err := workflowDefinitionStartStep(definition.Definition)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES(?,?,?,?,?,'planned') ON CONFLICT(work_id) DO UPDATE SET definition_ref=excluded.definition_ref,definition_version=excluded.definition_version,definition_digest=excluded.definition_digest,current_step=excluded.current_step`, workID, definition.Definition.Ref, definition.Definition.Version, definition.Digest, start)
	if err != nil {
		return workflowProjectionError(err, "cannot record workflow definition")
	}
	return nil
}

// startWorkflowInstanceStepTx moves an instance onto the step an action
// started. An instance sitting on its contract step is planned rather than
// running, and that step is derived from the definition rather than named by
// a literal that only two of the seven families would match.
func startWorkflowInstanceStepTx(ctx context.Context, tx *sql.Tx, workID, stepID, actorRef, executionModel string, at time.Time) error {
	definition, err := pinnedWorkflowDefinitionTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	step, err := workflowDeclaredStep(definition.Definition, stepID)
	if err != nil {
		return err
	}
	contractStep, err := workflowDefinitionContractStep(definition.Definition)
	if err != nil {
		return err
	}
	instanceState := "running"
	if step == contractStep {
		instanceState = "planned"
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=?,instance_state=?,execution_actor_ref=?,execution_model=?,started_at=coalesce(started_at,?) WHERE work_id=?`, step, instanceState, actorRef, executionModel, at.UTC().Format(time.RFC3339Nano), workID)
	if err != nil {
		return workflowProjectionError(err, "cannot start workflow action")
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "workflow instance does not exist", false, "select a workflow definition before starting an action")
	}
	return nil
}

// advanceWorkflowInstanceStepTx moves an instance to the next step on its
// definition's forward edge.
func advanceWorkflowInstanceStepTx(ctx context.Context, tx *sql.Tx, workID, stepID string, definition WorkflowDefinition) error {
	step, err := workflowDeclaredStep(definition, stepID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, step, workID); err != nil {
		return workflowProjectionError(err, "cannot advance the workflow step")
	}
	return nil
}

// resetWorkflowInstanceToContractStepTx returns an instance to its
// definition's contract step after a contract is superseded without a
// successor.
func resetWorkflowInstanceToContractStepTx(ctx context.Context, tx *sql.Tx, workID string) error {
	definition, err := pinnedWorkflowDefinitionTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	step, err := workflowDefinitionContractStep(definition.Definition)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='planned',current_step=? WHERE work_id=?`, step, workID); err != nil {
		return workflowProjectionError(err, "cannot return workflow to its contract step after supersession")
	}
	return nil
}
