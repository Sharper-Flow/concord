package store

import (
	"context"
	"database/sql"
	"strings"
)

// workflowAcceptanceSubjectCurrent compares immutable evidence with the native
// pin on the caller's connection. Historical evidence stays readable; only an
// acceptance consumes this check.
func workflowAcceptanceSubjectCurrent(ctx context.Context, q queryer, workID string, reviewAttempts, evidenceRefs []string) error {
	var subjects []string
	if prepared := workerDispatchPreparationFrom(ctx); prepared != nil && prepared.proof.acceptance && prepared.proof.pinReceipt != nil {
		if prepared.proof.workID != workID || prepared.proof.observation == nil || prepared.proof.observation.State != "clean" {
			return newFailure(KindStaleRequiresReview, "workflow_action", "terminal acceptance requires a clean native subject observation", false, "verify the clean worktree and prepare acceptance again")
		}
		subjects = append(subjects, prepared.proof.observation.Commit, strings.TrimPrefix(prepared.proof.pinReceipt.SubjectRef, "commit:"))
	}
	for _, attempt := range reviewAttempts {
		_, result, err := readReviewAttempt(ctx, q, workID, attempt)
		if err != nil {
			return err
		}
		if result.Review == nil {
			return reviewFailure(KindMissingEvidence, "accepted Review has no output candidate", "obtain and accept the current candidate's exact Review report")
		}
		subjects = append(subjects, result.Review.OutputCandidate.Commit)
	}
	for _, ref := range evidenceRefs {
		receipt, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
		if err != nil {
			return err
		}
		if receipt != nil {
			subjects = append(subjects, strings.TrimPrefix(receipt.SubjectRef, "commit:"))
			continue
		}
		// Resolve the bound producer, not a commit claimed by the caller or a
		// locator that merely looks like a native operation reference.
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT json_extract(payload,'$.producer_run_ref') FROM workflow_evidence_bindings
 WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, ref)
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

// Only the current accepted report contributes readiness. Finishing an older
// report later does not replace that report or its integrated candidate.
func workflowAcceptedSubjectCurrent(ctx context.Context, q queryer, workID string) error {
	state, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return err
	}
	var attempts []string
	if state.Accepted != nil {
		attempts = append(attempts, state.Accepted.AttemptID)
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
