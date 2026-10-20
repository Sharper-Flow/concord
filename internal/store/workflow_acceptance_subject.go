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
	if prepared := workerDispatchPreparationFrom(ctx); prepared != nil && prepared.proof.acceptance && prepared.proof.pinReceipt != nil {
		if prepared.proof.workID != workID || prepared.proof.observation == nil || prepared.proof.observation.State != "clean" {
			return newFailure(KindStaleRequiresReview, "workflow_action", "terminal acceptance requires a clean native subject observation", false, "verify the clean worktree and prepare acceptance again")
		}
		subjects = append(subjects, prepared.proof.observation.Commit, strings.TrimPrefix(prepared.proof.pinReceipt.SubjectRef, "commit:"))
	}
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

// WorkflowActionRequiresSubjectFreshness is the closed terminal-gate set the
// native mutation boundary prepares before opening its action transaction.
func WorkflowActionRequiresSubjectFreshness(actionID string) bool {
	return actionID == "record_verdict" || actionID == "confirm_premise" || actionID == "complete"
}

func validateWorkflowAcceptanceFreshness(ctx context.Context, tx *sql.Tx, workID string) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	subject, err := readCurrentOracleSubject(ctx, tx, workID)
	if err != nil || subject == "" {
		return err
	}
	prepared := workerDispatchPreparationFrom(ctx)
	if prepared == nil || !prepared.proof.acceptance || prepared.proof.workID != workID {
		return newFailure(KindInvalidOperation, "workflow_action", "terminal acceptance reached live assembly without its sealed subject preparation", false, "prepare acceptance through the native mutation boundary before opening its transaction")
	}
	if prepared.proof.pinReceipt == nil || prepared.proof.observation == nil || prepared.proof.observation.State != "clean" || prepared.proof.observation.Commit != subject {
		return newFailure(KindStaleRequiresReview, "workflow_action", "the live clean HEAD does not match the qualified terminal subject", false, "verify the clean worktree and prepare acceptance again")
	}
	if err := prepared.revalidateDispatchIdentitiesTx(ctx, tx, workID, true); err != nil {
		return err
	}
	return workflowAcceptanceSubjectCurrent(ctx, tx, workID, nil, nil)
}

// workflowAcceptedSubjectCurrent reads current per-predicate verdicts and
// accepted reviews that CD-0206 D3 has not retired. Review staleness uses the
// same owner as review acceptance, without rewriting historical evidence.
func workflowAcceptedSubjectCurrent(ctx context.Context, q queryer, workID string) error {
	if isWorkflowReplay(ctx) {
		return nil
	}
	candidates, err := readAcceptedWorkflowReviewsBefore(ctx, q, workID, 0)
	if err != nil {
		return err
	}
	var attempts []string
	for _, candidate := range candidates {
		stale, err := workflowStaleReviewAcceptBeforeTx(ctx, q, workID, candidate.attemptID, "workflow_action", 0)
		if err != nil {
			return err
		}
		if !stale {
			attempts = append(attempts, candidate.attemptID)
		}
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

type workflowAcceptedReview struct {
	attemptID string
	acceptSeq int64
}

func readAcceptedWorkflowReviewsBefore(ctx context.Context, q queryer, workID string, beforeSeq int64) ([]workflowAcceptedReview, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT a.attempt_id,acc.seq
 FROM domain_events acc JOIN worker_attempts a ON a.work_id=acc.subject_id
 AND a.attempt_id=json_extract(acc.payload,'$.worker_attempt_id') AND a.capability_class='review'
 JOIN domain_events auth ON auth.subject_type=acc.subject_type AND auth.subject_id=acc.subject_id
 AND auth.kind=? AND json_extract(auth.payload,'$.action_id')='dispatch_worker'
 AND json_extract(auth.payload,'$.worker_attempt_id')=a.attempt_id AND auth.seq<acc.seq
 WHERE acc.subject_type='work_item' AND acc.subject_id=? AND acc.kind=?
 AND json_extract(acc.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence')
 AND (?=0 OR (auth.seq<? AND acc.seq<?)) ORDER BY acc.seq DESC`, WorkflowActionCompleted, workID, WorkflowActionCompleted, beforeSeq, beforeSeq, beforeSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read accepted review subjects", true, "retry once the database is readable", err)
	}
	var candidates []workflowAcceptedReview
	for rows.Next() {
		var candidate workflowAcceptedReview
		if err := rows.Scan(&candidate.attemptID, &candidate.acceptSeq); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan accepted reviews", true, "retry once the database is readable", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot enumerate accepted reviews", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot close accepted reviews", true, "retry once the database is readable", err)
	}
	return candidates, nil
}
