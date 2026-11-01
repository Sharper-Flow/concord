package store

import "context"

// Step execution ends with delivery. Current terminal identity, the reported
// delivery, law attribution, evidence producers and mutation receipts survive.
func discardTerminalWorkflowStepsTx(ctx context.Context, q migrationRunner, workID string) error {
	for _, table := range []string{
		"workflow_step_epochs", "workflow_step_completions", "workflow_phase_exits",
		"workflow_completed_exits", "workflow_checkpoints", "workflow_context_checkpoints",
		"workflow_context_boundaries", "workflow_artifact_actions", "workflow_coordinator_actions",
		"work_context_findings", "work_context_state",
	} {
		if _, err := q.ExecContext(ctx, "DELETE FROM "+table+" WHERE work_id=? AND EXISTS(SELECT 1 FROM work_items WHERE id=? AND lifecycle IN ('completed','cancelled','superseded'))", workID, workID); err != nil {
			return workflowProjectionError(err, "cannot discard terminal workflow steps")
		}
	}
	return nil
}
