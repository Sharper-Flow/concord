package store

import (
	"context"
	"database/sql"
	"slices"
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
func pinnedWorkflowDefinitionTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
	var pin WorkflowDefinitionPin
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, newFailure(KindProjectionNotFound, "workflow_step", "workflow instance does not exist", false, "select a workflow definition first")
		}
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_step", "cannot read the pinned workflow definition", true, "retry once the database is readable", err)
	}
	return resolveCurrentWorkflowDefinition(registry, pin.Ref)
}

// pinWorkflowInstanceTx records the definition an instance follows and places
// it on that definition's start step. Initialization and cross-family
// re-pinning are re-initialization: the instance takes the new definition's
// start step, so the pair stays coherent rather than stranding a step the new
// definition does not declare. A same-family carry forward keeps the step the
// instance holds instead and goes through pinWorkflowInstanceToStepTx.
//
// A lane-less instance has no fenced start to record its execution identity.
// Its authenticated initialization actor supplies the current tuple that
// verdict redaction reads. A later step start or dispatch records the actual
// executing actor instead. This records identity, not an agent or model choice.
func pinWorkflowInstanceTx(ctx context.Context, tx *sql.Tx, workID string, definition RegisteredDefinition, selectingActor string) error {
	start, err := workflowDefinitionStartStep(definition.Definition)
	if err != nil {
		return err
	}
	return pinWorkflowInstanceToStepTx(ctx, tx, workID, definition, start, selectingActor)
}

// pinWorkflowInstanceToStepTx records the definition an instance follows and
// places it on the given step of that definition. stepID must be declared by
// the definition: the admitting callers resolve it from the definition's
// start step, or from the instance's current step after the fold verified the
// carried-forward definition declares it.
func pinWorkflowInstanceToStepTx(ctx context.Context, tx *sql.Tx, workID string, definition RegisteredDefinition, stepID, selectingActor string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) VALUES(?,?,?,?,'planned') ON CONFLICT(work_id) DO UPDATE SET definition_ref=excluded.definition_ref,definition_digest=excluded.definition_digest,current_step=excluded.current_step`, workID, definition.Definition.Ref, definition.Digest, stepID)
	if err != nil {
		return workflowProjectionError(err, "cannot record workflow definition")
	}
	if err := recordRuntimeStateWriter(ctx, tx, workflowStateWriter(WorkflowDefinitionPin{Ref: definition.Definition.Ref, Digest: definition.Digest})); err != nil {
		return err
	}
	if selectingActor == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET execution_actor_ref=? WHERE work_id=? AND EXISTS(SELECT 1 FROM workflow_actors WHERE actor_ref=?)`, selectingActor, workID, selectingActor); err != nil {
		return workflowProjectionError(err, "cannot record the selecting workflow actor")
	}
	return nil
}

// startWorkflowInstanceStepTx moves an instance onto the step an action
// started. An instance sitting on its contract step is planned rather than
// running, and that step is derived from the definition rather than named by
// a literal that only two of the seven families would match.
func startWorkflowInstanceStepTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID, stepID, actorRef, executionModel string, at time.Time) error {
	definition, err := pinnedWorkflowDefinitionTx(ctx, tx, registry, workID)
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
	// CD-0183: beginning an external-effect step is the durable execution
	// start the Domain-overlap peer claim reads. The first start in log order
	// wins, so replay and the migration backfill derive the same value. Only the
	// external-effect branch names the column: a start on any other step
	// writes nothing the older schemas it can run against do not hold.
	var result sql.Result
	if resolved := workflowStep(definition.Definition, step); resolved != nil && resolved.Kind == WorkflowStepExternalEffect {
		result, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=?,instance_state=?,execution_actor_ref=?,execution_model=?,started_at=coalesce(started_at,?),execution_started_at=coalesce(execution_started_at,?) WHERE work_id=?`, step, instanceState, actorRef, executionModel, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano), workID)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=?,instance_state=?,execution_actor_ref=?,execution_model=?,started_at=coalesce(started_at,?) WHERE work_id=?`, step, instanceState, actorRef, executionModel, at.UTC().Format(time.RFC3339Nano), workID)
	}
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

// workflowCompletedInstanceActionImmutable reports terminal workflow instances.
func workflowCompletedInstanceActionImmutable(state string) bool {
	return state == "completed" || state == "cancelled" || state == "superseded" || state == "outside_repair"
}
