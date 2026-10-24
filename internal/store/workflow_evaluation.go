package store

import "context"

// WorkflowEvaluationAuthority separates an independent evaluator, a coordinator
// that needs the operator's approval, and a worker whose authority ends at its
// report. Lease rotation does not erase recorded execution.
type WorkflowEvaluationAuthority string

const (
	WorkflowIndependentEvaluator WorkflowEvaluationAuthority = "independent"
	WorkflowOperatorRequired     WorkflowEvaluationAuthority = "operator_required"
	WorkflowWorkerEvaluator      WorkflowEvaluationAuthority = "worker"
)

func workflowEvaluationAuthority(ctx context.Context, q queryer, workID, actorRef string) (WorkflowEvaluationAuthority, error) {
	worker, err := workflowRecordedEvaluationFacts(ctx, q, workID, actorRef, int64(1)<<62)
	if err != nil {
		return "", err
	}
	if worker {
		return WorkflowWorkerEvaluator, nil
	}
	return WorkflowIndependentEvaluator, nil
}

// Participation belongs to the dispatched lane actor, not the coordinator
// who recorded the evidence. Receipt order preserves the evaluation prefix.
func workflowRecordedEvaluationFacts(ctx context.Context, q queryer, _ string, actorRef string, prefix int64) (worker bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM effect_receipts r JOIN worker_attempts a ON a.work_id=r.subject_id AND a.dispatch_event_id=r.event_id
		WHERE r.kind='worker.dispatched' AND r.subject_type='work_item' AND r.effect_seq<=? AND json_extract(a.dispatch_payload,'$.lane_actor_ref')=?)`, prefix, actorRef).Scan(&worker)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_evaluation", "cannot read workflow evaluation authority", true, "retry once workflow execution history is readable", err)
	}
	return worker, nil
}
