package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// VerificationReport records an attributed claim about checks, not a Git observation.
type VerificationReport struct {
	Source string `json:"source"`
	Commit string `json:"commit"`
	Result string `json:"result"`
	RunRef string `json:"run_ref"`
}

func validateVerificationReport(subject string, report *VerificationReport) error {
	invalid := func(detail string) error {
		return newFailure(KindInvalidPayload, "workflow_action", detail, false, "supply a complete verification report")
	}
	if report == nil || (report.Source != "local" && report.Source != "required_ci") || (report.Result != "pass" && report.Result != "fail") || !worktreeSHAPattern.MatchString(report.Commit) || !ValidReference(subject) || len(report.RunRef) < 1 || len(report.RunRef) > 512 || strings.TrimSpace(report.RunRef) == "" {
		return invalid("verification report has an invalid identity or bounds")
	}
	return nil
}

// readVerificationReports never treats a historical native binding as a report.
func readVerificationReports(ctx context.Context, q queryer, workID, ref string) ([]VerificationReport, error) {
	latest, _, err := latestVerificationReport(ctx, q, workID, "")
	if err != nil || latest == nil {
		return nil, err
	}
	report, _, err := latestVerificationReport(ctx, q, workID, ref)
	if err != nil || report == nil || report.Commit != latest.Commit {
		return nil, err
	}
	if latest.Result != "pass" && report.Result == "pass" {
		return nil, nil
	}
	return []VerificationReport{*report}, nil
}

// latestVerificationReport selects before decoding so malformed newer reports
// cannot be skipped in favour of older evidence. An absent report is a legacy binding.
func latestVerificationReport(ctx context.Context, q queryer, workID, ref string) (*VerificationReport, int64, error) {
	query := `SELECT binding_seq, payload FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='verification' AND json_type(payload,'$.verification_report') IS NOT NULL`
	args := []any{workID}
	if ref != "" {
		query += ` AND json_extract(payload,'$.immutable_subject_ref')=?`
		args = append(args, ref)
	}
	query += ` ORDER BY binding_seq DESC LIMIT 1`
	var seq int64
	var raw string
	err := q.QueryRowContext(ctx, query, args...).Scan(&seq, &raw)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, workflowProjectionError(err, "cannot read verification reports")
	}
	var binding workflowEvidenceBoundPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil || binding.VerificationReport == nil {
		return nil, 0, newFailure(KindInvariantViolation, "workflow_action", "stored verification report is malformed", false, "reconcile the evidence binding")
	}
	if err := validateVerificationReport(binding.ImmutableSubjectRef, binding.VerificationReport); err != nil {
		return nil, 0, newFailure(KindInvariantViolation, "workflow_action", "stored verification report is invalid", false, "reconcile the evidence binding")
	}
	return binding.VerificationReport, seq, nil
}

// verificationReportMovedCandidate marks a Review stale when a later report
// invalidates its cited proof or names a different candidate.
func verificationReportMovedCandidate(ctx context.Context, q queryer, workID string, review WorkflowAcceptedReview) (bool, error) {
	reviewSeq, err := workflowReviewReportSequence(ctx, q, workID, review.ReportRef)
	if err == sql.ErrNoRows {
		reviewSeq, err = 0, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the accepted Review sequence")
	}
	report, seq, err := latestVerificationReport(ctx, q, workID, "")
	if err != nil {
		return false, err
	}
	if report == nil || seq <= reviewSeq {
		return false, nil
	}
	if report.Commit != review.Report.OutputCandidate.Commit && report.Commit != review.Report.InputCandidate.Commit {
		return true, nil
	}
	for _, assessment := range review.Report.Assessments {
		if assessment.Assessment != "supported" {
			continue
		}
		for _, ref := range assessment.ReceiptRefs {
			reports, err := readVerificationReports(ctx, q, workID, ref)
			if err != nil {
				return false, err
			}
			if len(reports) == 0 || !reportCovers(reports[0], &review.Report.OutputCandidate) {
				return true, nil
			}
		}
	}
	return false, nil
}

func reportCovers(report VerificationReport, candidate *ReviewCandidate) bool {
	return candidate != nil && report.Result == "pass" && report.Commit == candidate.Commit
}
