package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type WorkflowReadDeliveryAssertion struct {
	EventID    string `json:"event_id"`
	Seq        int64  `json:"seq"`
	Artifact   string `json:"artifact"`
	State      string `json:"state"`
	ActorRef   string `json:"actor_ref"`
	AssertedAt string `json:"asserted_at"`
}

func workflowDeliveryAssertionRead(ctx context.Context, q queryer, workID string) (*WorkflowReadDeliveryAssertion, error) {
	var assertion WorkflowReadDeliveryAssertion
	var payload string
	err := q.QueryRowContext(ctx, `SELECT assertion_seq,assertion_id,actor,occurred_at,payload FROM workflow_delivery_state WHERE work_id=?`, workID).Scan(&assertion.Seq, &assertion.EventID, &assertion.ActorRef, &assertion.AssertedAt, &payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read the delivery assertion")
	}
	var fields struct {
		Artifact string `json:"delivery_artifact"`
		State    string `json:"delivery_state"`
	}
	if err := json.Unmarshal([]byte(payload), &fields); err != nil || !ValidReference(fields.Artifact) || fields.State != "asserted" {
		return nil, newFailure(KindInvariantViolation, "workflow_read", "delivery assertion payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	assertion.Artifact, assertion.State = fields.Artifact, fields.State
	return &assertion, nil
}
