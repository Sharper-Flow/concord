package store

import (
	"context"
	"database/sql"
)

// finalReviewWorkerJobTx returns the one revision a Review may bind when no
// revision is ready (CD-0205 D2): the latest revision of the job whose
// accepted implementation attempt delivered the current integrated candidate,
// still current under the active contract and satisfied by that exact
// acceptance. It returns nil when a ready revision exists, when no candidate
// is installed, or when the delivery anchor does not resolve to exactly one
// revision. It reads SQL only and authors no job or choice.
func finalReviewWorkerJobTx(ctx context.Context, q queryer, workID string) (*WorkerJobBinding, error) {
	var ready bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_job_revisions j WHERE j.work_id=? AND `+workerJobReadyPredicate+`)`, workID).Scan(&ready); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read worker-job readiness", true, "retry once the worker-job projection is readable", err)
	}
	if ready {
		return nil, nil
	}
	review, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil || review.Candidate == nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT j.job_id,j.revision,j.digest,e.attempt_id FROM worker_job_revisions j
JOIN worker_attempt_dispositions e ON e.work_id=j.work_id AND e.action_id='accept_worker_result'
 AND (e.seq=j.satisfied_seq AND e.event_id=j.satisfied_result_ref OR e.first_acceptance_seq=j.satisfied_seq)
WHERE j.work_id=? AND j.state='satisfied' AND j.satisfied_seq>0
 AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id)
 AND (`+workerJobDependenciesCurrentPredicate+`)
 AND j.contract_version=(SELECT c.contract_version FROM workflow_contracts c WHERE c.work_id=j.work_id AND c.superseded_by IS NULL)
ORDER BY j.job_id,j.revision`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read satisfied worker-job revisions", true, "retry once the worker-job projection is readable", err)
	}
	type satisfied struct {
		binding WorkerJobBinding
		attempt string
	}
	// Close the cursor before attempt reads on the single pooled connection.
	var found []satisfied
	for rows.Next() {
		var s satisfied
		if err := rows.Scan(&s.binding.JobID, &s.binding.Revision, &s.binding.Digest, &s.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var anchored []WorkerJobBinding
	for _, s := range found {
		auth, result, err := readReviewAttempt(ctx, q, workID, s.attempt)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		if auth.WorkerCapabilityClass != "implementation" || auth.WorkerJob == nil || *auth.WorkerJob != s.binding {
			continue
		}
		candidate, err := workflowResultCandidate(ctx, q, workID, auth, result)
		if err != nil {
			return nil, err
		}
		if candidate != nil && *candidate == *review.Candidate {
			anchored = append(anchored, s.binding)
		}
	}
	if len(anchored) != 1 {
		return nil, nil
	}
	// A checks repair keeps its one confirming Review (CD-0205 D4); only a
	// confirmed repair becomes a final-Review binding.
	repairs, err := readWorkerJobChecksRepairsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if repairs[anchored[0]] != nil {
		watermark, err := workerChecksRepairWatermarkTx(ctx, q, workID)
		if err != nil {
			return nil, err
		}
		history, err := workerChecksRepairWalkTx(ctx, q, workID, watermark)
		if err != nil {
			return nil, err
		}
		repair := history.checksRepairs[workerJobKey(anchored[0].JobID, anchored[0].Revision)]
		if repair == nil || !repair.confirmed {
			return nil, nil
		}
	}
	return &anchored[0], nil
}

// requireWorkerReviewJobRevisionStateTx is Review's state-only job admission:
// an ordinary ready revision, or the final-Review binding above. Native
// preparation is bound separately in the dispatch authorization (CD-0205 D1).
func requireWorkerReviewJobRevisionStateTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) error {
	readyErr := requireWorkerJobRevisionStateReadyTx(ctx, q, workID, binding)
	if readyErr == nil {
		return nil
	}
	final, err := finalReviewWorkerJobTx(ctx, q, workID)
	if err != nil {
		return err
	}
	if final != nil && *final == binding {
		return nil
	}
	return readyErr
}

// dispatchBoundFinalReviewTx reports whether the attempt's authorization is a
// Review dispatch of exactly the final-Review binding the current state admits.
func dispatchBoundFinalReviewTx(ctx context.Context, q queryer, workID, attemptID string, binding WorkerJobBinding) (bool, error) {
	var class string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_capability_class'),'') FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&class)
	if err == sql.ErrNoRows || err == nil && class != "review" {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot read the Review dispatch authorization", true, "retry once the dispatch authorization is readable", err)
	}
	final, err := finalReviewWorkerJobTx(ctx, q, workID)
	if err != nil {
		return false, err
	}
	return final != nil && *final == binding, nil
}

// nativeDispatchJobStateTx applies the dispatch's job admission at native
// execution: a final Review whose authorization froze its preparations keeps
// the state-only final-Review rule; every other dispatch keeps the ordinary
// state readiness its admission already proved preparations for.
func nativeDispatchJobStateTx(ctx context.Context, q queryer, workID string, dispatch workflowActionCompletedPayload, binding WorkerJobBinding) error {
	if dispatch.WorkerCapabilityClass == "review" && len(dispatch.WorkerOraclePreparations) != 0 {
		return requireWorkerReviewJobRevisionStateTx(ctx, q, workID, binding)
	}
	return requireWorkerJobRevisionStateReadyTx(ctx, q, workID, binding)
}
