package store

import (
	"context"
	"database/sql"
)

// ReviewInput carries the last recorded candidate and current obligations.
type ReviewInput struct {
	InputCandidate ReviewCandidate    `json:"input_candidate"`
	Dependencies   ReviewDependencies `json:"dependencies"`
}

func populateContinuityReviewInput(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	if out.WorkPin == nil || out.WorkPin.Review == nil || out.WorkPin.Review.Dependencies == nil || out.WorkPin.Review.Candidate == nil {
		return nil
	}
	out.ReviewInput = &ReviewInput{InputCandidate: *out.WorkPin.Review.Candidate, Dependencies: *out.WorkPin.Review.Dependencies}
	return nil
}
