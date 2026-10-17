package store

import (
	"context"
	"strings"
)

// ExternalCondition is the immutable condition projection a readiness reader
// inspects. The engine has no condition resolver (CD-0234 D1): a recorded
// condition folds, reads, and rebuilds, and an open one refuses consequential
// actions at admission.
type ExternalCondition struct {
	WorkID              string
	ConditionID         string
	AwaitType           string
	AwaitRef            string
	ResolutionAuthority string
	State               string
}

// ConditionAuthorityReader is the read seam used by consequential readiness
// checks. It allows an authority provider to report an unreadable source
// without mutating the persisted condition projection.
type ConditionAuthorityReader interface {
	Readable(context.Context, ExternalCondition) error
}

// WorkflowReadyResult is a read-only consequential-boundary projection. An
// unknown condition is reported separately from an ordinary open blocker; no
// read path rewrites or resolves it.
type WorkflowReadyResult struct {
	Ready              bool
	UnknownConditions  []string
	BlockingConditions []string
}

// DeriveWorkflowReady evaluates ordinary blocks and unreadable condition
// authorities fail closed. It performs no mutation or automatic resolution.
func DeriveWorkflowReady(ctx context.Context, s *Store, workID string) (WorkflowReadyResult, error) {
	return DeriveWorkflowReadyWithReader(ctx, s, workID, nil)
}

// DeriveWorkflowReadyWithReader is the explicit read boundary for readiness.
// A reader failure is an unknown, blocking condition; it is never treated as a
// resolved condition and never rewrites workflow state.
func DeriveWorkflowReadyWithReader(ctx context.Context, s *Store, workID string, reader ConditionAuthorityReader) (WorkflowReadyResult, error) {
	result := WorkflowReadyResult{Ready: true}
	if s == nil || s.db == nil {
		return result, newFailure(KindUnavailable, "derive_workflow_ready", "store is not open", false, "open the authority database")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT condition_id,resolution_authority FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' ORDER BY condition_id`, workID)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot inspect workflow conditions", true, "retry once the database is readable", err)
	}
	type conditionAuthority struct{ id, authority string }
	var conditions []conditionAuthority
	for rows.Next() {
		var conditionID, authority string
		if err := rows.Scan(&conditionID, &authority); err != nil {
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
		if reader != nil {
			var conditionRow ExternalCondition
			conditionRow.WorkID = workID
			conditionRow.ConditionID = conditionID
			conditionRow.ResolutionAuthority = authority
			if err := reader.Readable(ctx, conditionRow); err != nil {
				result.UnknownConditions = append(result.UnknownConditions, conditionID)
				continue
			}
		}
		const prefix = "durable_operation:"
		if !strings.HasPrefix(authority, prefix) || len(authority) == len(prefix) {
			result.UnknownConditions = append(result.UnknownConditions, conditionID)
			continue
		}
		var owner string
		if err := s.db.QueryRowContext(ctx, `SELECT work_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, strings.TrimPrefix(authority, prefix)).Scan(&owner); err != nil || owner != workID {
			result.UnknownConditions = append(result.UnknownConditions, conditionID)
			continue
		}
		result.BlockingConditions = append(result.BlockingConditions, conditionID)
	}
	return result, nil
}
