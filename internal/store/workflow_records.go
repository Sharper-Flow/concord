package store

import "context"

// Record actions write their own current subject. Control actions and native
// starts are not record edits; their step and held-attempt rules still apply.
func workflowRecordAction(action string) bool {
	switch action {
	case "record_intake", "record_clarify", "record_plan", "record_diagnosis",
		"record_delivery", "record_review", "revise_candidates":
		return true
	}
	return false
}

func workflowCurrentRecords(ctx context.Context, q queryer, work string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT action_id FROM workflow_completed_exits WHERE work_id=? AND action_id<>'record_clarify' UNION SELECT 'record_delivery' FROM workflow_delivery_state WHERE work_id=? UNION SELECT 'record_plan' FROM workflow_plan_records WHERE work_id=? UNION SELECT 'record_intake' FROM workflow_instances WHERE work_id=? AND prelude_state IS NOT NULL UNION SELECT 'record_clarify' FROM workflow_clarify_records WHERE work_id=?`, work, work, work, work, work)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read current workflow records")
	}
	defer rows.Close()
	records := map[string]bool{}
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			return nil, err
		}
		if workflowRecordAction(action) {
			records[action] = true
		}
	}
	return records, rows.Err()
}
