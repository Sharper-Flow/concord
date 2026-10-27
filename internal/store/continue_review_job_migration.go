package store

import (
	"context"
	"encoding/json"
)

type continueReviewRestorationEvidence struct {
	jobRaw, reviewRaw, attempt, reportRef, actor string
	payloads, digests                            [5]string
}

func (e continueReviewRestorationEvidence) proves(binding WorkerJobBinding) bool {
	for index, raw := range e.payloads {
		digest, err := effectPayloadDigest([]byte(raw))
		if err != nil || digest != e.digests[index] {
			return false
		}
	}
	var job WorkerJobRecordedPayload
	var review workflowReviewFacts
	var auth, acceptance, latest workflowActionCompletedPayload
	var dispatch WorkerDispatchedPayload
	var terminal WorkerCompletedPayload
	if json.Unmarshal([]byte(e.jobRaw), &job) != nil || json.Unmarshal([]byte(e.reviewRaw), &review) != nil ||
		json.Unmarshal([]byte(e.payloads[0]), &auth) != nil || json.Unmarshal([]byte(e.payloads[1]), &dispatch) != nil ||
		json.Unmarshal([]byte(e.payloads[2]), &terminal) != nil || json.Unmarshal([]byte(e.payloads[3]), &acceptance) != nil ||
		json.Unmarshal([]byte(e.payloads[4]), &latest) != nil {
		return false
	}
	for _, accepted := range []workflowActionCompletedPayload{acceptance, latest} {
		if accepted.ActionID != "accept_worker_result" || accepted.WorkerAttemptID != e.attempt || accepted.DeliveryArtifact != "" || accepted.DeliveryState != "" ||
			accepted.WorkerJob == nil || *accepted.WorkerJob != binding {
			return false
		}
	}
	return job.JobID == binding.JobID && job.Revision == binding.Revision && job.Digest == binding.Digest && DeriveWorkerJobDigest(job) == binding.Digest &&
		auth.ActionID == "dispatch_worker" && auth.WorkerAttemptID == e.attempt && auth.WorkerCapabilityClass == "review" && auth.ChecksRepairReview == nil &&
		dispatch.AttemptID == e.attempt && dispatch.CapabilityClass == "review" && dispatch.Terminal == "" && terminal.AttemptID == e.attempt &&
		auth.WorkerJob != nil && *auth.WorkerJob == binding && dispatch.WorkerJob != nil && *dispatch.WorkerJob == binding &&
		terminal.WorkerJob != nil && *terminal.WorkerJob == binding &&
		review.Accepted != nil && review.Accepted.AttemptID == e.attempt && review.Accepted.ReportRef == e.reportRef && review.Accepted.ActorRef == e.actor &&
		terminal.Review != nil && terminal.Review.Disposition == "continue" &&
		review.Accepted.ReportDigest == reviewDigest(terminal.Review) && reviewDigest(review.Accepted.Report) == reviewDigest(terminal.Review)
}

// Restoration consumes retained current facts, never retired event history.
// The migration caller owns the transaction. Close its cursor before issuing
// updates so this also works with the store's single connection.
func restoreContinueReviewJobSatisfaction(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT j.work_id,j.job_id,j.revision,j.digest,j.satisfied_seq,j.satisfied_result_ref,
 j.recorded_payload,r.state,a.attempt_id,a.terminal_event_id,
 d.payload,a.dispatch_payload,a.terminal_payload,
 CASE WHEN e.seq=j.satisfied_seq AND e.event_id=j.satisfied_result_ref THEN e.payload ELSE e.first_acceptance_payload END,
 e.payload,dr.payload_digest,ar.payload_digest,tr.payload_digest,er.payload_digest,lr.payload_digest,er.actor
FROM worker_job_revisions j
JOIN work_items w ON w.id=j.work_id AND w.lifecycle IN ('needed','in_progress')
JOIN workflow_instances i ON i.work_id=j.work_id
 AND i.instance_state NOT IN ('completed','cancelled','superseded','outside_repair')
JOIN workflow_review_state r ON r.work_id=j.work_id
JOIN worker_attempts a ON a.work_id=j.work_id AND a.attempt_id=json_extract(r.state,'$.accepted.attempt_id')
 AND a.capability_class='review' AND a.lifecycle_state='completed' AND a.terminal_kind='worker.completed'
 AND a.terminal_event_id=json_extract(r.state,'$.accepted.report_ref')
JOIN worker_authorizations d ON d.work_id=a.work_id AND d.attempt_id=a.attempt_id
JOIN worker_attempt_dispositions e ON e.work_id=a.work_id AND e.attempt_id=a.attempt_id AND e.action_id='accept_worker_result'
JOIN effect_receipts dr ON dr.effect_seq=d.seq AND dr.event_id=d.event_id
 AND dr.subject_id=j.work_id AND dr.subject_type='work_item' AND dr.kind='workflow.action_completed'
JOIN effect_receipts ar ON ar.effect_seq=a.dispatch_seq AND ar.event_id=a.dispatch_event_id
 AND ar.subject_id=j.work_id AND ar.subject_type='work_item' AND ar.kind='worker.dispatched'
JOIN effect_receipts tr ON tr.effect_seq=a.terminal_seq AND tr.event_id=a.terminal_event_id
 AND tr.subject_id=j.work_id AND tr.subject_type='work_item' AND tr.kind='worker.completed'
JOIN effect_receipts er ON er.effect_seq=j.satisfied_seq AND er.event_id=j.satisfied_result_ref
 AND er.subject_id=j.work_id AND er.subject_type='work_item' AND er.kind='workflow.action_completed' AND er.delivery_assertion=0
JOIN effect_receipts lr ON lr.effect_seq=e.seq AND lr.event_id=e.event_id
 AND lr.subject_id=j.work_id AND lr.subject_type='work_item' AND lr.kind='workflow.action_completed' AND lr.delivery_assertion=0
WHERE j.state='satisfied' AND j.satisfied_at IS NOT NULL AND j.satisfied_seq>0
 AND j.recorded_payload IS NOT NULL
 AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id)
 AND json_extract(r.state,'$.accepted.report.disposition')='continue'
 AND a.dispatch_payload IS NOT NULL AND a.terminal_payload IS NOT NULL
 AND dr.payload_digest IS NOT NULL AND ar.payload_digest IS NOT NULL AND tr.payload_digest IS NOT NULL AND er.payload_digest IS NOT NULL AND lr.payload_digest IS NOT NULL
 AND d.seq<a.dispatch_seq AND a.dispatch_seq<a.terminal_seq AND a.terminal_seq<=j.satisfied_seq
 AND (e.seq=j.satisfied_seq AND e.event_id=j.satisfied_result_ref OR e.first_acceptance_seq=j.satisfied_seq)
 AND (e.seq<>e.first_acceptance_seq OR json(e.payload)=json(e.first_acceptance_payload))
 AND json_extract(d.payload,'$.checks_repair_review') IS NULL
 AND NOT EXISTS (SELECT 1 FROM worker_attempt_dispositions conflict
   WHERE conflict.work_id=j.work_id AND conflict.attempt_id=a.attempt_id
   AND conflict.action_id IN ('reject_worker_result','record_worker_failure') AND conflict.seq>=j.satisfied_seq)
 AND NOT EXISTS (SELECT 1 FROM worker_authorizations used
   WHERE used.work_id=j.work_id AND (
    json_extract(used.payload,'$.worker_capability_class')='implementation'
    AND json_extract(used.payload,'$.worker_job.job_id')=j.job_id AND json_extract(used.payload,'$.worker_job.revision')=j.revision
    OR json_extract(used.payload,'$.checks_repair_review.worker_job.job_id')=j.job_id
    AND json_extract(used.payload,'$.checks_repair_review.worker_job.revision')=j.revision))
 AND NOT EXISTS (SELECT 1 FROM worker_attempts used
   WHERE used.work_id=j.work_id AND (used.capability_class='implementation' OR json_extract(used.dispatch_payload,'$.capability_class')='implementation')
   AND json_extract(used.dispatch_payload,'$.worker_job.job_id')=j.job_id AND json_extract(used.dispatch_payload,'$.worker_job.revision')=j.revision)
 AND NOT EXISTS (SELECT 1 FROM worker_job_revisions dependent,json_each(dependent.prerequisites) p
   WHERE dependent.work_id=j.work_id AND json_extract(p.value,'$.job_id')=j.job_id AND json_extract(p.value,'$.revision')=j.revision)
ORDER BY j.work_id,j.job_id,j.revision`)
	if err != nil {
		return err
	}
	type restoration struct {
		workID, jobID, digest, resultRef string
		revision, seq                    int64
	}
	var updates []restoration
	for rows.Next() {
		var update restoration
		var evidence continueReviewRestorationEvidence
		if err := rows.Scan(&update.workID, &update.jobID, &update.revision, &update.digest, &update.seq, &update.resultRef,
			&evidence.jobRaw, &evidence.reviewRaw, &evidence.attempt, &evidence.reportRef, &evidence.payloads[0], &evidence.payloads[1], &evidence.payloads[2], &evidence.payloads[3], &evidence.payloads[4],
			&evidence.digests[0], &evidence.digests[1], &evidence.digests[2], &evidence.digests[3], &evidence.digests[4], &evidence.actor); err != nil {
			rows.Close()
			return err
		}
		binding := WorkerJobBinding{JobID: update.jobID, Revision: update.revision, Digest: update.digest}
		if evidence.proves(binding) {
			updates = append(updates, update)
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(updates) == 0 {
		return nil
	}
	guard, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO write_guard(active) VALUES(1)`)
	if err != nil {
		return err
	}
	owned, err := guard.RowsAffected()
	if err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := q.ExecContext(ctx, `UPDATE worker_job_revisions SET state='recorded',satisfied_at=NULL,satisfied_result_ref=NULL,satisfied_seq=0
WHERE work_id=? AND job_id=? AND revision=? AND digest=? AND state='satisfied' AND satisfied_seq=? AND satisfied_result_ref=?`,
			update.workID, update.jobID, update.revision, update.digest, update.seq, update.resultRef); err != nil {
			return err
		}
	}
	if owned != 0 {
		_, err = q.ExecContext(ctx, `DELETE FROM write_guard`)
	}
	return err
}
