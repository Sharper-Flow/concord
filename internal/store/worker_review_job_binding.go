package store

import (
	"context"
	"database/sql"
)

// finalReviewWorkerJobTx returns the one revision a Review may bind when no
// revision is ready (CD-0205 D2): the latest revision of the job dispatched by
// the delivery anchor, the newest accepted implementation delivery, when that
// delivery is Main's current candidate, the revision is current under the
// active contract, and its satisfaction comes from that exact acceptance. It
// returns nil otherwise. It reads SQL only and authors no job or choice.
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
	anchor, err := newestAcceptedImplementationDeliveryTx(ctx, q, workID)
	if err != nil || anchor == nil || anchor.candidate != *review.Candidate {
		return nil, err
	}
	binding := anchor.job
	var satisfied bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_job_revisions j
JOIN worker_attempt_dispositions e ON e.work_id=j.work_id AND e.attempt_id=? AND e.action_id='accept_worker_result'
 AND (e.seq=j.satisfied_seq AND e.event_id=j.satisfied_result_ref OR e.first_acceptance_seq=j.satisfied_seq)
WHERE j.work_id=? AND j.job_id=? AND j.revision=? AND j.digest=? AND j.state='satisfied' AND j.satisfied_seq>0
 AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id)
 AND (`+workerJobDependenciesCurrentPredicate+`)
 AND j.contract_version=(SELECT c.contract_version FROM workflow_contracts c WHERE c.work_id=j.work_id AND c.superseded_by IS NULL))`,
		anchor.attempt, workID, binding.JobID, binding.Revision, binding.Digest).Scan(&satisfied); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the delivery anchor's worker-job revision", true, "retry once the worker-job projection is readable", err)
	}
	if !satisfied {
		return nil, nil
	}
	// A checks repair keeps its one confirming Review (CD-0205 D4); only a
	// confirmed repair becomes a final-Review binding.
	repairs, err := readWorkerJobChecksRepairsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if repairs[binding] != nil {
		watermark, err := workerChecksRepairWatermarkTx(ctx, q, workID)
		if err != nil {
			return nil, err
		}
		history, err := workerChecksRepairWalkTx(ctx, q, workID, watermark)
		if err != nil {
			return nil, err
		}
		repair := history.checksRepairs[workerJobKey(binding.JobID, binding.Revision)]
		if repair == nil || !repair.confirmed {
			return nil, nil
		}
	}
	return &binding, nil
}

type implementationDelivery struct {
	attempt   string
	job       WorkerJobBinding
	candidate WorkerReviewCandidate
}

// newestAcceptedImplementationDeliveryTx returns the delivery anchor: the
// newest accepted job-bound implementation result on the work's claimed
// worktree. A member result delivers on its member worktree and is skipped.
// The anchor must carry a clean core observation; when the newest result has
// none, there is no anchor and no older delivery stands in for it.
func newestAcceptedImplementationDeliveryTx(ctx context.Context, q queryer, workID string) (*implementationDelivery, error) {
	rows, err := q.QueryContext(ctx, `SELECT attempt_id FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' ORDER BY seq DESC`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read accepted worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	// Close the cursor before attempt reads on the single pooled connection.
	var attempts []string
	for rows.Next() {
		var attempt string
		if err := rows.Scan(&attempt); err != nil {
			rows.Close()
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		auth, result, err := readReviewAttempt(ctx, q, workID, attempt)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		if auth.WorkerCapabilityClass != "implementation" || auth.WorkerJob == nil {
			continue
		}
		if base, err := workflowMemberResultBase(ctx, q, workID, auth); err != nil || base != nil {
			if err != nil {
				return nil, err
			}
			continue
		}
		candidate, err := workflowResultCandidate(ctx, q, workID, auth, result)
		if err != nil || candidate == nil {
			return nil, err
		}
		return &implementationDelivery{attempt: attempt, job: *auth.WorkerJob, candidate: *candidate}, nil
	}
	return nil, nil
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
// execution: a Review whose authorization froze its preparations keeps the
// state-only Review rule; every other dispatch keeps the ordinary state
// readiness its admission already proved preparations for.
func nativeDispatchJobStateTx(ctx context.Context, q queryer, workID string, dispatch workflowActionCompletedPayload, binding WorkerJobBinding) error {
	if dispatch.WorkerCapabilityClass == "review" && len(dispatch.WorkerOraclePreparations) != 0 {
		return requireWorkerReviewJobRevisionStateTx(ctx, q, workID, binding)
	}
	return requireWorkerJobRevisionStateReadyTx(ctx, q, workID, binding)
}
