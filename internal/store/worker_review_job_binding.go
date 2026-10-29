package store

import (
	"context"
	"database/sql"
)

// finalReviewWorkerJobTx binds final Review to accepted candidate provenance
// and the exact revision's current satisfaction. A repairing Review can install
// the candidate without replacing an earlier producer's satisfaction. Neither
// fact substitutes for the other; no ready revision or older anchor is invented.
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
	anchor, err := newestAcceptedCandidateAttemptTx(ctx, q, workID, *review.Candidate)
	if err != nil || anchor == nil {
		return nil, err
	}
	binding := anchor.job
	var satisfied bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_job_revisions j
JOIN effect_receipts r ON r.effect_seq=j.satisfied_seq AND r.event_id=j.satisfied_result_ref
 AND r.subject_type='work_item' AND r.subject_id=j.work_id AND r.kind='workflow.action_completed'
JOIN worker_attempt_dispositions e ON e.work_id=j.work_id AND e.action_id='accept_worker_result'
 AND (e.seq=j.satisfied_seq AND e.event_id=j.satisfied_result_ref OR e.first_acceptance_seq=j.satisfied_seq)
JOIN worker_authorizations a ON a.work_id=e.work_id AND a.attempt_id=e.attempt_id
 AND json_extract(a.payload,'$.worker_job.job_id')=j.job_id
 AND json_extract(a.payload,'$.worker_job.revision')=j.revision AND json_extract(a.payload,'$.worker_job.digest')=j.digest
JOIN worker_attempts t ON t.work_id=e.work_id AND t.attempt_id=e.attempt_id
 AND t.lifecycle_state='completed' AND t.terminal_kind='worker.completed'
 AND json_extract(t.terminal_payload,'$.worker_job.job_id')=j.job_id
 AND json_extract(t.terminal_payload,'$.worker_job.revision')=j.revision AND json_extract(t.terminal_payload,'$.worker_job.digest')=j.digest
WHERE j.work_id=? AND j.job_id=? AND j.revision=? AND j.digest=? AND j.state='satisfied' AND j.satisfied_seq>0
 AND j.satisfied_seq<=?
 AND NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions conflict WHERE conflict.work_id=e.work_id
  AND conflict.attempt_id=e.attempt_id AND conflict.action_id IN ('reject_worker_result','record_worker_failure') AND conflict.seq>=j.satisfied_seq)
 AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id)
 AND (`+workerJobDependenciesCurrentPredicate+`)
 AND j.contract_version=(SELECT c.contract_version FROM workflow_contracts c WHERE c.work_id=j.work_id AND c.superseded_by IS NULL))`,
		workID, binding.JobID, binding.Revision, binding.Digest, anchor.seq).Scan(&satisfied); err != nil {
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

type acceptedCandidateAttempt struct {
	seq int64
	job WorkerJobBinding
}

// The newest claimed-worktree producer must qualify; an invalid newest result
// never falls back to an older acceptance. An unchanged continue Review only
// grants execution permission and supplies no new candidate provenance.
func newestAcceptedCandidateAttemptTx(ctx context.Context, q queryer, workID string, current WorkerReviewCandidate) (*acceptedCandidateAttempt, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.attempt_id,e.seq,
 NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions conflict WHERE conflict.work_id=e.work_id AND conflict.attempt_id=e.attempt_id
  AND conflict.action_id IN ('reject_worker_result','record_worker_failure') AND conflict.seq>=e.first_acceptance_seq)
 FROM worker_attempt_dispositions e WHERE e.work_id=? AND e.action_id='accept_worker_result' ORDER BY e.seq DESC`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read accepted worker attempts", true, "retry once the worker attempt projection is readable", err)
	}
	// Close the cursor before attempt reads on the single pooled connection.
	type acceptance struct {
		attempt string
		seq     int64
		current bool
	}
	var attempts []acceptance
	for rows.Next() {
		var attempt acceptance
		if err := rows.Scan(&attempt.attempt, &attempt.seq, &attempt.current); err != nil {
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
		auth, result, err := readReviewAttempt(ctx, q, workID, attempt.attempt)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if auth.WorkerJob == nil {
			continue
		}
		if base, err := workflowMemberResultBase(ctx, q, workID, auth); err != nil || base != nil {
			if err != nil {
				return nil, err
			}
			continue
		}
		if workerReviewContinuesJob(auth, result) && result.Review.InputCandidate == result.Review.OutputCandidate {
			continue
		}
		if !attempt.current {
			return nil, nil
		}
		matches, err := acceptedAttemptMatchesCandidateTx(ctx, q, workID, auth, result, current)
		if err != nil || !matches {
			return nil, err
		}
		return &acceptedCandidateAttempt{seq: attempt.seq, job: *auth.WorkerJob}, nil
	}
	return nil, nil
}

func acceptedAttemptMatchesCandidateTx(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload, result WorkerCompletedPayload, current WorkerReviewCandidate) (bool, error) {
	if result.ObservedDelivery == nil || result.ObservedDelivery.State != "clean" {
		return false, nil
	}
	switch auth.WorkerCapabilityClass {
	case "implementation", "review":
		if auth.WorkerCapabilityClass == "review" && result.Review == nil {
			return false, nil
		}
	default:
		lane, err := LookupLane(auth.WorkerLaneID, auth.WorkerLaneDigest)
		if err != nil || lane.CapabilityClass != auth.WorkerCapabilityClass || workerLaneEdits(lane.ID, lane.Digest) || auth.WorkerSubjectCommit != current.Commit {
			return false, nil
		}
	}
	candidate, err := workflowAcceptedResultCandidate(ctx, q, workID, auth, result)
	if err != nil || candidate == nil || *candidate != current {
		return false, err
	}
	return true, nil
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
