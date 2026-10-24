package store

import (
	"context"
	"encoding/json"
)

func workflowEventDeliveryAssertion(event Event) (string, bool, error) {
	if event.SubjectType != SubjectWorkItem || event.Kind != WorkflowActionCompleted {
		return "", false, nil
	}
	var fields struct {
		Artifact string `json:"delivery_artifact"`
		State    string `json:"delivery_state"`
	}
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return "", false, err
	}
	return fields.Artifact, fields.Artifact != "" && fields.State == "asserted", nil
}

func backfillWorkflowDeliveryAssertionReceipts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT event_id,subject_type,kind,payload FROM domain_events WHERE subject_type='work_item' AND kind='workflow.action_completed' ORDER BY seq`)
	if err != nil {
		return err
	}
	var assertions []string
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.EventID, &event.SubjectType, &event.Kind, &event.Payload); err != nil {
			rows.Close()
			return err
		}
		_, asserted, err := workflowEventDeliveryAssertion(event)
		if err != nil {
			rows.Close()
			return wrapFailure(KindInvariantViolation, "migrate", "recorded delivery assertion payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap", err)
		}
		if asserted {
			assertions = append(assertions, event.EventID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range assertions {
		if _, err := q.ExecContext(ctx, `UPDATE effect_receipts SET delivery_assertion=1 WHERE event_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}
