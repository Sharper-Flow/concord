package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ReviewInput describes the native-qualified subject and current obligations.
// Recording a Review rechecks them through its own sealed preparation.
type ReviewInput struct {
	InputCandidate ReviewCandidate    `json:"input_candidate"`
	Dependencies   ReviewDependencies `json:"dependencies"`
}

type continuityReviewSubject struct {
	receipt *WorktreeVerifyResult
}

func sampleContinuityReviewSubjectTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) (continuityReviewSubject, error) {
	var subject continuityReviewSubject
	if out.WorkPin == nil || out.WorkPin.Review == nil || out.WorkPin.Review.Dependencies == nil {
		return subject, nil
	}
	var err error
	subject.receipt, err = readCurrentOracleSubjectReceipt(ctx, tx, work)
	if err != nil || subject.receipt == nil {
		return subject, err
	}
	return subject, nil
}

func observeContinuityReviewSubject(ctx context.Context, out *ContinuitySnapshot, subject continuityReviewSubject) {
	if subject.receipt == nil {
		return
	}
	commit := strings.TrimPrefix(subject.receipt.SubjectRef, "commit:")
	tree, err := readReviewCandidateTree(ctx, CommittedReadGitRunner{}, subject.receipt.Path, commit)
	if err != nil {
		out.ReviewInputWarning = err
		return
	}
	out.ReviewInput = &ReviewInput{InputCandidate: ReviewCandidate{ProjectID: subject.receipt.ProjectID, Commit: commit, Tree: tree}, Dependencies: *out.WorkPin.Review.Dependencies}
}

func readReviewCandidateTree(ctx context.Context, runner GitRunner, path, commit string) (string, error) {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := runner.Run(probe, path, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return "", wrapFailure(KindGitUnreachable, "workflow_subject", "cannot read the qualified candidate tree", true, "restore the referenced Git tree", err)
	}
	tree := strings.TrimSpace(string(raw))
	if !worktreeSHAPattern.MatchString(tree) {
		return "", newFailure(KindGitUnreachable, "workflow_subject", "the candidate tree is not one raw tree OID", false, "restore the referenced tree object")
	}
	return tree, nil
}
