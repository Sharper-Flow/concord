package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// A member's dispatch binds its roster job and caller; its result is not the
// integration candidate. The roster's qualified base remains its acceptance base.
func workflowMemberResultBase(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload) (*workflowMemberBase, error) {
	if auth.WorkerCapabilityClass != "implementation" || auth.WorkerJob == nil {
		return nil, nil
	}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT g.base_subject FROM workflow_group_members m
	 JOIN workflow_member_groups g USING(work_id,group_id)
	 WHERE m.work_id=? AND m.job_id=? AND m.job_revision=? AND m.job_digest=? AND m.actor_ref=?`,
		workID, auth.WorkerJob.JobID, auth.WorkerJob.Revision, auth.WorkerJob.Digest, auth.ActorRef).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var base workflowMemberBase
	if err := json.Unmarshal([]byte(raw), &base); err != nil {
		return nil, err
	}
	return &base, nil
}

// Only the current roster's accepted clean deliveries constrain its join.
// The caller samples these rows before probing Git, under the work-version pin.
type workflowMemberCandidateState struct {
	Ancestors           []string
	Base                workflowMemberBase
	LatestAcceptanceSeq int64
}

func workflowMemberCandidate(ctx context.Context, q queryer, workID string) (workflowMemberCandidateState, error) {
	var state workflowMemberCandidateState
	rows, err := q.QueryContext(ctx, `SELECT json_extract(t.terminal_payload,'$.observed_delivery.commit'), d.seq, g.base_subject
	 FROM workflow_group_members m JOIN workflow_member_groups g USING(work_id,group_id) JOIN worker_authorizations a ON a.work_id=m.work_id
	 AND json_extract(a.payload,'$.worker_job.job_id')=m.job_id
	 AND json_extract(a.payload,'$.worker_job.revision')=m.job_revision
	 AND json_extract(a.payload,'$.worker_job.digest')=m.job_digest
	 AND json_extract(a.payload,'$.actor_ref')=m.actor_ref
	 JOIN worker_attempts t ON t.work_id=a.work_id AND t.attempt_id=a.attempt_id
	 JOIN worker_attempt_dispositions d ON d.work_id=a.work_id AND d.attempt_id=a.attempt_id AND d.action_id='accept_worker_result'
	 WHERE m.work_id=? AND m.group_id=(SELECT group_id FROM workflow_member_groups WHERE work_id=? ORDER BY recorded_seq DESC LIMIT 1)
	 AND json_extract(a.payload,'$.worker_capability_class')='implementation'
	 AND json_extract(t.terminal_payload,'$.observed_delivery.state')='clean'
	 AND NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions r WHERE r.work_id=d.work_id AND r.attempt_id=d.attempt_id AND r.seq>d.seq)
	 ORDER BY 1`, workID, workID)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var commit, raw string
		var seq int64
		if err := rows.Scan(&commit, &seq, &raw); err != nil {
			rows.Close()
			return state, err
		}
		if err := json.Unmarshal([]byte(raw), &state.Base); err != nil {
			rows.Close()
			return state, err
		}
		state.Ancestors = append(state.Ancestors, commit)
		state.LatestAcceptanceSeq = max(state.LatestAcceptanceSeq, seq)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(state.Ancestors) == 0 {
		return state, err
	}
	review, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return state, err
	}
	if review.Candidate != nil {
		state.Ancestors = append(state.Ancestors, review.Candidate.Commit)
	}
	return state, nil
}

func workflowPreparedMemberCandidate(ctx context.Context, subject string) bool {
	prepared := workerDispatchPreparationFrom(ctx)
	return prepared != nil && prepared.proof.memberCandidate && prepared.observationMatches(subject)
}

// Dispatch already proved ancestry. Acceptance can reuse that proof while its
// qualified input, roster base and accepted member population stay unchanged.
func workflowMemberReviewCurrent(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload, current, input WorkerReviewCandidate) (bool, error) {
	join, err := workflowMemberCandidate(ctx, q, workID)
	if err != nil || len(join.Ancestors) == 0 || current.Commit != join.Base.Commit {
		return false, err
	}
	var dispatchSeq int64
	if err := q.QueryRowContext(ctx, `SELECT seq FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, auth.WorkerAttemptID).Scan(&dispatchSeq); err != nil {
		return false, err
	}
	receipt, err := readCurrentOracleSubjectReceipt(ctx, q, workID)
	return err == nil && receipt != nil && receipt.SubjectRef == "commit:"+input.Commit && receipt.ProjectID == input.ProjectID && join.LatestAcceptanceSeq < dispatchSeq, err
}

// Both input exceptions reuse dispatch's proof; neither promotes terminal bytes
// into a candidate before Main accepts the fresh Review.
func workflowReviewInputCurrent(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload, current, input WorkerReviewCandidate) (bool, error) {
	if proof := auth.WorkerRetainedReview; proof != nil {
		if proof.State.Candidate != current || proof.Input != input || auth.WorkerJob == nil || *auth.WorkerJob != proof.State.Job || auth.WorkerSubjectCommit != input.Commit {
			return false, nil
		}
		return retainedReviewCurrent(ctx, q, workID, proof, auth.WorkerAttemptID, auth.WorkerWorktreeIdentity)
	}
	return workflowMemberReviewCurrent(ctx, q, workID, auth, current, input)
}
