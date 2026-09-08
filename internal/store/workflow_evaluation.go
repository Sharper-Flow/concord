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

func (s *Store) WorkflowEvaluationAuthority(ctx context.Context, workID, actorRef string) (WorkflowEvaluationAuthority, error) {
	return workflowEvaluationAuthority(ctx, s.db, workID, actorRef)
}

func workflowEvaluationAuthority(ctx context.Context, q queryer, workID, actorRef string) (WorkflowEvaluationAuthority, error) {
	var worker, executed bool
	err := q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM domain_events WHERE kind='worker.dispatched' AND json_extract(payload,'$.lane_actor_ref')=?),
		EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=? AND execution_actor_ref=?) OR
		EXISTS(SELECT 1 FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND ((kind='workflow.action_started' AND json_extract(payload,'$.actor_ref')=?) OR (kind='workflow.action_completed' AND actor=? AND json_extract(payload,'$.action_id')='accept_worker_result')))`, actorRef, workID, actorRef, workID, actorRef, actorRef).Scan(&worker, &executed)
	if err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_evaluation", "cannot read workflow evaluation authority", true, "retry once workflow execution history is readable", err)
	}
	if worker {
		return WorkflowWorkerEvaluator, nil
	}
	if executed {
		return WorkflowOperatorRequired, nil
	}
	return WorkflowIndependentEvaluator, nil
}
