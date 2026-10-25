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

// Worker classification belongs to the actor identity, not to the mutable
// execution lease or the lifetime of an individual work item.
func workflowRecordedEvaluationFacts(ctx context.Context, q queryer, _ string, actorRef string, prefix int64) (worker bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_worker_actors WHERE actor_ref=? AND first_dispatch_seq<=?)`, actorRef, prefix).Scan(&worker)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_evaluation", "cannot read workflow evaluation authority", true, "retry once workflow execution history is readable", err)
	}
	return worker, nil
}
