package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// workflowSplitLeafProgressBasis is the convergence basis CD-0225 D3 adds at
// escalation: the latest result disposition accepts an under-cap required
// split leaf with no unresolved blocking findings and no later failed or
// rejected attempt, and that acceptance funds exactly one first dispatch of a
// never-dispatched required sibling of the same split lineage. It retains all
// counters and predecessor debt, and the dispatch consumes it.
const workflowSplitLeafProgressBasis = "split_leaf_progress"

const workerJobSplitProgressSubject = "worker_job_split_progress"

// workerSplitLeafProgressFunding is the funding one recorded acceptance
// supplies: the accepted leaf's split lineage beside the never-dispatched
// unsatisfied sibling leaves the acceptance may fund, in leaf order.
type workerSplitLeafProgressFunding struct {
	acceptanceSeq int64
	splitID       string
	accepted      WorkerJobRevisionView
	targets       []WorkerJobRevisionView
}

// workflowSplitLeafProgressConvergenceTx derives the split_leaf_progress
// basis when the recorded acceptance still funds a first sibling dispatch.
func workflowSplitLeafProgressConvergenceTx(ctx context.Context, q queryer, workID string, lastDispatch int64) (WorkflowRetryConvergence, error) {
	funding, err := workerSplitLeafProgressFundingTx(ctx, q, workID, lastDispatch, 0)
	if err != nil || funding == nil {
		return WorkflowRetryConvergence{}, err
	}
	return WorkflowRetryConvergence{Basis: workflowSplitLeafProgressBasis, LatestRecordSeq: funding.acceptanceSeq}, nil
}

// workerSplitLeafProgressGateTx reports whether the work's pinned definition
// declares the production-size members: the split-progress behavior exists
// only where the split itself does (CD-0225 D3).
func workerSplitLeafProgressGateTx(ctx context.Context, q queryer, workID string) (bool, error) {
	var pin WorkflowDefinitionPin
	switch err := q.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Version, &pin.Digest); {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, wrapFailure(KindUnavailable, workerJobSplitProgressSubject, "cannot read the workflow definition pin for convergence", true, "retry once the workflow projection is readable", err)
	}
	registered, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), pin)
	if err != nil {
		return false, err
	}
	return workflowJobSizeActive(registered.Definition), nil
}

// workerSplitLeafProgressFundingTx derives the funding the recorded log
// supplies, or nil when it supplies none. atSeq anchors the read at one exact
// acceptance for the dispatch guard; zero reads the latest result
// acceptance. The count is the completion's core-recorded production size,
// never a second observation.
func workerSplitLeafProgressFundingTx(ctx context.Context, q queryer, workID string, lastDispatch, atSeq int64) (*workerSplitLeafProgressFunding, error) {
	active, err := workerSplitLeafProgressGateTx(ctx, q, workID)
	if err != nil || !active {
		return nil, err
	}
	query := `SELECT seq, COALESCE(json_extract(payload,'$.worker_job.job_id'),''), COALESCE(json_extract(payload,'$.worker_job.revision'),0), COALESCE(json_extract(payload,'$.worker_job.digest'),''), json_extract(payload,'$.worker_production_size.count') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result'`
	args := []any{string(SubjectWorkItem), workID, WorkflowActionCompleted}
	if atSeq > 0 {
		query += ` AND seq=?`
		args = append(args, atSeq)
	}
	query += ` ORDER BY seq DESC LIMIT 1`
	var acceptanceSeq int64
	var jobID, digest string
	var revision int64
	var count *int64
	switch err := q.QueryRowContext(ctx, query, args...).Scan(&acceptanceSeq, &jobID, &revision, &digest, &count); {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, workflowProjectionError(err, "cannot read the latest result acceptance for convergence")
	}
	if jobID == "" {
		// The latest result disposition accepted no worker-job leaf: a
		// jobless acceptance displaces the leaf acceptance and funds
		// nothing.
		return nil, nil
	}
	// Any later failure, rejection, correction request, terminal failure, or
	// no_ship completion stops the forward-progress route.
	stopped, err := workerSplitProgressLaterStopTx(ctx, q, workID, acceptanceSeq)
	if err != nil || stopped {
		return nil, err
	}
	// One acceptance funds exactly one dispatch: a recorded dispatch after
	// it consumes the basis, including an authorization whose worker never
	// materialized.
	if acceptanceSeq <= lastDispatch {
		return nil, nil
	}
	states, err := readWorkerJobSplitStatesTx(ctx, q, workID)
	if err != nil || len(states) == 0 {
		return nil, err
	}
	// The accepted leaf belongs to the outermost undischarged lineage that
	// holds it — recorded log order is ancestor first — so required outer
	// siblings stay fundable beside an exhausted nested lineage.
	var state *workerJobSplitState
	var leaf *WorkerJobRevisionView
	for index := range states {
		recorded := &states[index]
		if recorded.DischargeID != "" {
			continue
		}
		for position := range recorded.Leaves {
			if recorded.Leaves[position].Binding.JobID == jobID && recorded.Leaves[position].Binding.Revision == revision && recorded.Leaves[position].Binding.Digest == digest {
				state, leaf = recorded, &recorded.Leaves[position]
				break
			}
		}
		if state != nil {
			break
		}
	}
	if state == nil || leaf == nil {
		// The accepted job is no required leaf of an undischarged split —
		// a replaced predecessor, an unrelated job, a work without a split
		// — so the acceptance funds nothing.
		return nil, nil
	}
	if leaf.State != "satisfied" || leaf.SatisfiedResultRef == "" ||
		slices.Contains(state.Unsatisfied, workerJobKey(jobID, revision)) {
		// The acceptance no longer satisfies the leaf's current revision
		// under the active parent authority — satisfaction voided, a
		// superseded authority, or a stale revision — so it funds nothing.
		return nil, nil
	}
	if leaf.ProductionSizeMax == nil || count == nil || *count >= *leaf.ProductionSizeMax {
		// Only an under-cap leaf funds forward progress.
		return nil, nil
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if len(lineage.openFindingIDs()) != 0 {
		// Unresolved blocking findings stop the forward-progress route.
		return nil, nil
	}
	funding := &workerSplitLeafProgressFunding{acceptanceSeq: acceptanceSeq, splitID: state.SplitID, accepted: *leaf}
	for _, sibling := range state.Leaves {
		if !sibling.Ready {
			// A leaf that is no dispatchable current revision — already
			// satisfied, superseded authority, superseded revision, or not
			// coordinator-ready — never becomes a funding target.
			continue
		}
		dispatched, err := workerSplitProgressJobDispatchedTx(ctx, q, workID, sibling.Binding.JobID)
		if err != nil {
			return nil, err
		}
		if dispatched {
			// A previously dispatched sibling cannot be funded again, and
			// it must not discard the untouched siblings beside it.
			continue
		}
		funding.targets = append(funding.targets, sibling)
	}
	if len(funding.targets) == 0 {
		return nil, nil
	}
	return funding, nil
}

// workerSplitProgressLaterStopTx reports any recorded disposition or terminal
// fact after the acceptance that stops the forward-progress route.
func workerSplitProgressLaterStopTx(ctx context.Context, q queryer, workID string, acceptanceSeq int64) (bool, error) {
	var stopped bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.action_id') IN ('record_worker_failure','reject_worker_result','request_correction')
	) OR EXISTS(
		SELECT 1 FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>?
	) OR EXISTS(
		SELECT 1 FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.review.verdict')='no_ship'
	)`,
		string(SubjectWorkItem), workID, WorkflowActionCompleted, acceptanceSeq,
		string(SubjectWorkItem), workID, WorkerFailed, acceptanceSeq,
		string(SubjectWorkItem), workID, WorkerCompleted, acceptanceSeq).Scan(&stopped)
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the dispositions after the acceptance for convergence")
	}
	return stopped, nil
}

// workerSplitProgressJobDispatchedTx reports whether any recorded dispatch of
// the named job — under any revision — exists: an authorization completion or
// the worker's dispatched evidence.
func workerSplitProgressJobDispatchedTx(ctx context.Context, q queryer, workID, jobID string) (bool, error) {
	var dispatched bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_job.job_id')=?
	) OR EXISTS(
		SELECT 1 FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.worker_job.job_id')=?
	)`,
		string(SubjectWorkItem), workID, WorkflowActionCompleted, jobID,
		string(SubjectWorkItem), workID, WorkerDispatched, jobID).Scan(&dispatched)
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the recorded dispatches of a split-progress funding target")
	}
	return dispatched, nil
}

// admitWorkerJobSplitProgressDispatchTx is the dispatch target guard for the
// split_leaf_progress basis: the packet's job must be one funded,
// never-dispatched required sibling of the exact acceptance the basis names,
// and that funding must still stand at assembly. Any other basis returns nil;
// the existing retry approval and fencing rules own those dispatches.
func admitWorkerJobSplitProgressDispatchTx(ctx context.Context, tx *sql.Tx, workID string, convergence WorkflowRetryConvergence, target *WorkerJobBinding) error {
	if convergence.Basis != workflowSplitLeafProgressBasis || !convergence.valid() {
		return nil
	}
	var lastDispatch int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker'`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&lastDispatch); err != nil {
		return workflowProjectionError(err, "cannot read the latest dispatch for the split-progress guard")
	}
	funding, err := workerSplitLeafProgressFundingTx(ctx, tx, workID, lastDispatch, convergence.LatestRecordSeq)
	if err != nil {
		return err
	}
	if funding == nil {
		return newFailure(KindMissingEvidence, "worker_dispatch",
			"the dispatch's split_leaf_progress basis names an acceptance that no longer funds a first sibling dispatch",
			false, "derive a fresh convergence basis from the recorded log, or use the failed-retry approval and recovery routes the wall names")
	}
	if target == nil {
		return newFailure(KindInvalidOperation, "worker_dispatch",
			"the split_leaf_progress basis funds only a job-bound dispatch of a never-dispatched required sibling",
			false, "dispatch the recorded sibling revision the acceptance funds")
	}
	for _, sibling := range funding.targets {
		if sibling.Binding == *target {
			return nil
		}
	}
	names := make([]string, 0, len(funding.targets))
	for _, sibling := range funding.targets {
		names = append(names, sibling.Binding.JobID)
	}
	return newFailure(KindUnauthorizedDispatch, "worker_dispatch",
		fmt.Sprintf("the split_leaf_progress acceptance of job %s funds only the never-dispatched required sibling(s) %s of split %s; the packet binds job %s",
			funding.accepted.Binding.JobID, strings.Join(names, ", "), funding.splitID, target.JobID),
		false, "dispatch one funded sibling, or derive the convergence basis the packet's job actually owes")
}
