package store

import (
	"context"
	"database/sql"
	"strings"
)

// workflowAcceptanceSubjectCurrent compares immutable evidence with the native
// pin on the caller's connection. Historical evidence stays readable; only a
// live acceptance consumes this check. Replay reconstructs recorded decisions,
// not acceptance against the worktree that happens to exist during replay.
func workflowAcceptanceSubjectCurrent(ctx context.Context, q queryer, workID string, reviewAttempts, evidenceRefs []string) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	var subjects []string
	for _, attempt := range reviewAttempts {
		var subject string
		err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_subject_commit'),'') FROM domain_events
 WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker'
 AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, attempt).Scan(&subject)
		if err != nil && err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, "workflow_action", "cannot read the review subject", true, "retry once the database is readable", err)
		}
		if subject != "" {
			subjects = append(subjects, subject)
		}
	}
	for _, ref := range evidenceRefs {
		// Resolve the bound producer, not a commit claimed by the caller or a
		// locator that merely looks like a native operation reference.
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT json_extract(payload,'$.producer_run_ref') FROM domain_events
 WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, ref)
		if err != nil {
			return wrapFailure(KindUnavailable, "workflow_action", "cannot read the bound verdict producers", true, "retry once the database is readable", err)
		}
		var runs []string
		for rows.Next() {
			var run string
			if err := rows.Scan(&run); err != nil {
				rows.Close()
				return wrapFailure(KindUnavailable, "workflow_action", "cannot scan the bound verdict producers", true, "retry once the database is readable", err)
			}
			runs = append(runs, run)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "workflow_action", "cannot enumerate the bound verdict producers", true, "retry once the database is readable", err)
		}
		if err := rows.Close(); err != nil {
			return wrapFailure(KindUnavailable, "workflow_action", "cannot close the bound verdict producers", true, "retry once the database is readable", err)
		}
		for _, run := range runs {
			receipt, err := readOracleVerifySubjectReceipt(ctx, q, workID, run)
			if err != nil {
				return err
			}
			if receipt != nil {
				subjects = append(subjects, strings.TrimPrefix(receipt.SubjectRef, "commit:"))
			}
		}
	}
	if len(subjects) == 0 {
		return nil
	}
	current, err := readCurrentOracleSubject(ctx, q, workID)
	if err != nil {
		return err
	}
	for _, subject := range subjects {
		if current == "" || subject != current {
			return newFailure(KindStaleRequiresReview, "workflow_action", "acceptance evidence does not identify the current native subject", false, "claim and verify the worktree, then obtain evidence for its current subject")
		}
	}
	return nil
}

// workflowAcceptedSubjectCurrent reads the evidence that terminal acceptance
// consumes: current per-predicate verdicts and the latest accepted review for
// each step, lane, and job. A fresh review replaces that scope's old proof; it
// neither rewrites old records nor replaces another scope's proof.
func workflowAcceptedSubjectCurrent(ctx context.Context, q queryer, workID string) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT attempt_id FROM (
 SELECT json_extract(acc.payload,'$.worker_attempt_id') AS attempt_id,
 ROW_NUMBER() OVER (PARTITION BY json_extract(acc.payload,'$.step_id'),a.lane_id,
 COALESCE(json_extract(auth.payload,'$.worker_job.job_id'),'') ORDER BY acc.seq DESC) AS position
 FROM domain_events acc JOIN worker_attempts a ON a.work_id=acc.subject_id
 AND a.attempt_id=json_extract(acc.payload,'$.worker_attempt_id') AND a.capability_class='review'
 JOIN domain_events auth ON auth.subject_type=acc.subject_type AND auth.subject_id=acc.subject_id
 AND auth.kind=? AND json_extract(auth.payload,'$.action_id')='dispatch_worker'
 AND json_extract(auth.payload,'$.worker_attempt_id')=a.attempt_id AND auth.seq<acc.seq
 WHERE acc.subject_type='work_item' AND acc.subject_id=? AND acc.kind=?
 AND json_extract(acc.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence')
 ) WHERE position=1`, WorkflowActionCompleted, workID, WorkflowActionCompleted)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read accepted review subjects", true, "retry once the database is readable", err)
	}
	var attempts []string
	for rows.Next() {
		var attempt string
		if err := rows.Scan(&attempt); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "workflow_action", "cannot scan accepted reviews", true, "retry once the database is readable", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "workflow_action", "cannot enumerate accepted reviews", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot close accepted reviews", true, "retry once the database is readable", err)
	}
	version, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if err != nil {
		return err
	}
	verdicts, err := latestWorkflowVerdicts(ctx, q, workID, version)
	if err != nil {
		return err
	}
	var refs []string
	for _, verdict := range verdicts {
		refs = append(refs, verdict.EvaluationEvidence...)
	}
	return workflowAcceptanceSubjectCurrent(ctx, q, workID, attempts, refs)
}
