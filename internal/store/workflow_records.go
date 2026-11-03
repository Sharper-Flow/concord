package store

import "context"

func workflowReviewReportSequence(ctx context.Context, q queryer, workID, reportRef string) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT effect_seq FROM effect_receipts WHERE event_id=? AND subject_type='work_item' AND subject_id=?`, reportRef, workID).Scan(&seq)
	return seq, err
}

// Record actions write their own current subject. Control actions and native
// starts are not record edits; their step and held-attempt rules still apply.
func workflowRecordAction(action string) bool {
	switch action {
	case "record_proposal", "record_design", "record_diagnosis", "record_alignment",
		"record_delivery", "record_review", "record_verdict", "revise_candidates":
		return true
	}
	return false
}

func workflowCurrentRecords(ctx context.Context, q queryer, work string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT action_id FROM workflow_completed_exits WHERE work_id=? UNION SELECT 'record_delivery' FROM workflow_delivery_state WHERE work_id=? UNION SELECT 'record_review' FROM workflow_review_state WHERE work_id=? AND json_type(state,'$.accepted')='object'`, work, work, work)
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
