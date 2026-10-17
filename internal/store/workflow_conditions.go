package store

import (
	"context"
	"strings"
)

// WorkflowReadyResult is a read-only consequential-boundary projection. An
// unknown condition is reported separately from an ordinary open blocker; no
// read path rewrites or resolves it.
type WorkflowReadyResult struct {
	Ready              bool
	UnknownConditions  []string
	BlockingConditions []string
}

// deriveWorkflowReady evaluates ordinary blocks and unreadable condition
// authorities fail closed. It performs no mutation or automatic resolution.
func deriveWorkflowReady(ctx context.Context, q queryer, workID string) (WorkflowReadyResult, error) {
	result := WorkflowReadyResult{Ready: true}
	rows, err := q.QueryContext(ctx, `SELECT condition_id,resolution_authority FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' ORDER BY condition_id`, workID)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot inspect workflow conditions", true, "retry once the database is readable", err)
	}
	type conditionAuthority struct{ id, authority string }
	var conditions []conditionAuthority
	for rows.Next() {
		var conditionID, authority string
		if err := rows.Scan(&conditionID, &authority); err != nil {
			rows.Close()
			return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot read workflow condition", true, "retry once the database is readable", err)
		}
		conditions = append(conditions, conditionAuthority{id: conditionID, authority: authority})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot scan workflow conditions", true, "retry once the database is readable", err)
	}
	rows.Close()
	for _, condition := range conditions {
		conditionID, authority := condition.id, condition.authority
		result.Ready = false
		const prefix = "durable_operation:"
		if !strings.HasPrefix(authority, prefix) || len(authority) == len(prefix) {
			result.UnknownConditions = append(result.UnknownConditions, conditionID)
			continue
		}
		var owner string
		if err := q.QueryRowContext(ctx, `SELECT work_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, strings.TrimPrefix(authority, prefix)).Scan(&owner); err != nil || owner != workID {
			result.UnknownConditions = append(result.UnknownConditions, conditionID)
			continue
		}
		result.BlockingConditions = append(result.BlockingConditions, conditionID)
	}
	return result, nil
}
