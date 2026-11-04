package store

import (
	"context"
	"slices"
)

func guardPlanDelivery(ctx context.Context, q queryer, workID string) error {
	required, _, _, err := workflowEvidenceRequirementInputs(ctx, q, workID)
	if err != nil {
		return err
	}
	review, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return err
	}
	if err := workflowAdmitReview(review); err != nil {
		return err
	}
	if !slices.Contains(required, "verification") {
		return nil
	}
	refs, err := workflowCandidateVerificationRefs(ctx, q, workID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		why, err := workflowCandidateVerificationDisqualifier(ctx, q, workID, ref)
		if err != nil {
			return err
		}
		if why == "" {
			return nil
		}
	}
	return newFailure(KindMissingEvidence, "workflow_action", "record_delivery requires a passing verification report on the current reviewed candidate", false, "bind a passing verification report on the current reviewed candidate before recording delivery")
}

func workflowCandidateVerificationRefs(ctx context.Context, q queryer, workID string) ([]string, error) {
	return workflowVerificationBindingsAfter(ctx, q, workID, 0, "workflow_action")
}

func workflowCandidateVerificationDisqualifier(ctx context.Context, q queryer, workID, ref string) (string, error) {
	reports, err := readVerificationReports(ctx, q, workID, ref)
	if err != nil {
		return "", err
	}
	candidate, err := workflowVerificationCandidate(ctx, q, workID)
	if err != nil {
		return "", err
	}
	for _, report := range reports {
		if reportCovers(report, candidate) {
			return "", nil
		}
	}
	return "verification does not cover the current reviewed candidate", nil
}

func workflowVerificationCandidate(ctx context.Context, q queryer, workID string) (*ReviewCandidate, error) {
	review, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	return review.Candidate, nil
}
