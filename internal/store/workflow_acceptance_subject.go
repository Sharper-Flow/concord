package store

import (
	"context"
	"database/sql"
	"strings"
)

// workflowAcceptanceSubjectCurrent compares immutable evidence with the native
// pin on the caller's connection. Historical evidence stays readable; only an
// acceptance consumes this check.
func workflowAcceptanceSubjectCurrent(ctx context.Context, q queryer, workID string, candidate *ReviewCandidate, evidenceRefs []string) error {
	var subjects []string
	if prepared := workflowSubjectPreparationFrom(ctx); prepared != nil && prepared.receipt != nil {
		if prepared.workID != workID || prepared.candidate == nil {
			return newFailure(KindStaleRequiresReview, "workflow_action", "terminal acceptance requires a clean native subject observation", false, "verify the clean worktree and prepare acceptance again")
		}
		subjects = append(subjects, prepared.candidate.Commit, strings.TrimPrefix(prepared.receipt.SubjectRef, "commit:"))
	}
	if candidate != nil {
		subjects = append(subjects, candidate.Commit)
		if prepared := workflowSubjectPreparationFrom(ctx); prepared != nil && prepared.candidate != nil && *prepared.candidate != *candidate {
			return reviewFailure(KindStaleRequiresReview, "accepted Review does not identify the live candidate's Project, commit and tree", workflowReviewRecovery("", "identifying the exact current candidate"))
		}
	}
	for _, ref := range evidenceRefs {
		resolved, err := workflowAcceptanceEvidenceSubjects(ctx, q, workID, ref)
		if err != nil {
			return err
		}
		subjects = append(subjects, resolved...)
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

func workflowAcceptanceEvidenceSubjects(ctx context.Context, q queryer, workID, ref string) ([]string, error) {
	receipt, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		return []string{strings.TrimPrefix(receipt.SubjectRef, "commit:")}, nil
	}
	// Resolve the bound producer, not a commit claimed by the caller or a
	// locator that merely looks like a native operation reference.
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT json_extract(payload,'$.producer_run_ref') FROM workflow_evidence_bindings
 WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, ref)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the bound verdict producers", true, "retry once the database is readable", err)
	}
	var runs []string
	for rows.Next() {
		var run string
		if err := rows.Scan(&run); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the bound verdict producers", true, "retry once the database is readable", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot enumerate the bound verdict producers", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot close the bound verdict producers", true, "retry once the database is readable", err)
	}
	var subjects []string
	for _, run := range runs {
		receipt, err := readOracleVerifySubjectReceipt(ctx, q, workID, run)
		if err != nil {
			return nil, err
		}
		if receipt != nil {
			subjects = append(subjects, strings.TrimPrefix(receipt.SubjectRef, "commit:"))
		}
	}
	return subjects, nil
}

// WorkflowActionRequiresSubjectFreshness is the closed terminal-gate set the
// native mutation boundary prepares before opening its action transaction.
func WorkflowActionRequiresSubjectFreshness(actionID string) bool {
	return actionID == "record_review" || actionID == "record_verdict" || actionID == "confirm_premise" || actionID == "complete"
}

func validateWorkflowAcceptanceFreshness(ctx context.Context, tx *sql.Tx, workID string) error {
	subject, err := readCurrentOracleSubject(ctx, tx, workID)
	if err != nil || subject == "" {
		return err
	}
	prepared := workflowSubjectPreparationFrom(ctx)
	if prepared == nil || prepared.workID != workID {
		return newFailure(KindInvalidOperation, "workflow_action", "terminal acceptance reached live assembly without its sealed subject preparation", false, "prepare acceptance through the native mutation boundary before opening its transaction")
	}
	if prepared.receipt == nil || prepared.candidate == nil || prepared.candidate.Commit != subject {
		return newFailure(KindStaleRequiresReview, "workflow_action", "the live clean HEAD does not match the qualified terminal subject", false, "verify the clean worktree and prepare acceptance again")
	}
	if err := prepared.revalidateTx(ctx, tx, workID); err != nil {
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
	var candidate *ReviewCandidate
	if state.Accepted != nil {
		candidate = &state.Accepted.Report.OutputCandidate
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
	return workflowAcceptanceSubjectCurrent(ctx, q, workID, candidate, refs)
}
