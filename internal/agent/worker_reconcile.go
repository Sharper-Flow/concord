package agent

import (
	"context"
	"encoding/json"

	"github.com/sharper-flow/concord/internal/store"
)

type workerReconcileInput struct {
	WorkID         string         `json:"work_id"`
	AttemptID      string         `json:"attempt_id"`
	TaskPartID     string         `json:"task_part_id"`
	IdempotencyKey string         `json:"idempotency_key"`
	Approval       *approvalInput `json:"approval"`
}

// Preparation and the post-settlement receipt use the same scoped read of the
// existing attempt. Neither opens a dispatch window or accepts worker evidence.
func (r runtime) planWorkerReconcile(_ context.Context, base Envelope, raw []byte, plan *mutationPlan) (Envelope, error, bool) {
	var in workerReconcileInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	if in.Approval != nil {
		plan.approval = in.Approval.ApprovalRef
	}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []ChangedRef, error) {
		recovery, err := store.WorkerRecoveryContextTx(ctx, tx, in.WorkID, in.AttemptID, r.Envelope.AmbientProjectID, r.Envelope.Worktree, "client:"+grant.ClientRef+":"+grant.PrincipalRef)
		if err != nil {
			return nil, nil, nil, err
		}
		payload, err := json.Marshal(map[string]any{"changed_refs": []any{}, "next_valid_intents": []any{}, "worker_recovery": recovery})
		return payload, nil, nil, err
	}
	return Envelope{}, nil, false
}
