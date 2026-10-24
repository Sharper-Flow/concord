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

// Historical satisfaction uses the same execution facts as live evaluation,
// bounded to its event prefix. The mutable lease is a live guard only.
func workflowRecordedEvaluationFacts(ctx context.Context, q queryer, workID, actorRef string, prefix int64) (worker bool, err error) {
	var executed bool
	err = q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM domain_events WHERE kind='worker.dispatched' AND seq<=? AND json_extract(payload,'$.lane_actor_ref')=?),
		EXISTS(SELECT 1 FROM domain_events WHERE seq<=? AND subject_type='work_item' AND subject_id=? AND ((kind='workflow.action_started' AND json_extract(payload,'$.actor_ref')=?) OR (kind='workflow.action_completed' AND actor=? AND json_extract(payload,'$.action_id')='accept_worker_result')))`, prefix, actorRef, prefix, workID, actorRef, actorRef).Scan(&worker, &executed)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_evaluation", "cannot read workflow evaluation authority", true, "retry once workflow execution history is readable", err)
	}
	return worker, nil
}
