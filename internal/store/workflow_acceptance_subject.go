package store

import "context"

// workflowAcceptanceSubjectCurrent compares recorded reports with the Review.
func workflowAcceptanceSubjectCurrent(ctx context.Context, q queryer, workID string, candidate *ReviewCandidate, evidenceRefs []string) error {
	research, err := workflowResearchCriterionFamily(ctx, q, workID)
	if err != nil {
		return err
	}
	if research {
		return nil
	}
	if candidate == nil {
		return nil
	}
	for _, ref := range evidenceRefs {
		reports, err := readVerificationReports(ctx, q, workID, ref)
		if err != nil {
			return err
		}
		matched := false
		for _, report := range reports {
			matched = matched || reportCovers(report, candidate)
		}
		if !matched {
			return newFailure(KindStaleRequiresReview, "workflow_action", "acceptance evidence has no passing report for the recorded Review candidate", false, "bind a passing report for the reviewed commit")
		}
	}
	return nil
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
