package store

import (
	"context"
	"database/sql"
)

// ReplaceWorkflowOutcome is the semantic outcome-revision boundary. Outcome
// replacement is only legal while planning is still authoritative; execution
// must use contract supersession instead.
func ReplaceWorkflowOutcome(ctx context.Context, s *Store, workID string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "replace_workflow_outcome", "store is not open", false, "open the authority database")
	}
	var step string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		return wrapFailure(KindUnavailable, "replace_workflow_outcome", "cannot read workflow step", true, "retry once the workflow is readable", err)
	}
	if step != "planning" {
		return newFailure(KindInvalidTransition, "replace_workflow_outcome", "approved outcome cannot be replaced after execution begins", false, "supersede the contract during planning")
	}
	return nil
}

// ReplaceWorkflowCheckTx is the evaluator-owned check replacement boundary.
// It delegates to the authenticated verdict route, preserving actor fencing
// and evidence validation instead of inventing a second check mutation path.
func ReplaceWorkflowCheckTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	request.ActionID = "record_verdict"
	return applyWorkflowActionRawTx(ctx, tx, registry, request)
}
