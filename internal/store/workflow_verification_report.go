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
	rows, err := q.QueryContext(ctx, `SELECT payload FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='verification' AND json_extract(payload,'$.immutable_subject_ref')=? AND json_type(payload,'$.verification_report')='object' ORDER BY binding_seq DESC LIMIT 1`, workID, ref)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read verification reports")
	}
	defer rows.Close()
	var reports []VerificationReport
	for rows.Next() {
		var raw string
		var binding workflowEvidenceBoundPayload
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&binding); err != nil || binding.VerificationReport == nil {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "stored verification report is malformed", false, "reconcile the evidence binding")
		}
		if err := validateVerificationReport(ref, binding.VerificationReport); err != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_action", "stored verification report is invalid", false, "reconcile the evidence binding")
		}
		reports = append(reports, *binding.VerificationReport)
	}
	return reports, rows.Err()
}

// verificationReportMovedCandidate reports whether the newest report bound
// after the accepted Review names a commit outside that Review's input and
// output. The Review then covers a mixed candidate: the report record, not
// live Git, shows the candidate moved. A report on the Review's input is old
// evidence, which proof admission already refuses.
func verificationReportMovedCandidate(ctx context.Context, q queryer, workID string, review WorkflowAcceptedReview) (bool, error) {
	reviewRef := review.ReportRef
	// A Review without a retained receipt has no position, so every report
	// counts as newer and any other commit stales it.
	reviewSeq, err := workflowReviewReportSequence(ctx, q, workID, reviewRef)
	if err == sql.ErrNoRows {
		reviewSeq, err = 0, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the accepted Review sequence")
	}
	var commit string
	err = q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.verification_report.commit') FROM workflow_evidence_bindings WHERE work_id=? AND binding_seq>? AND json_extract(payload,'$.evidence_kind')='verification' AND json_type(payload,'$.verification_report')='object' ORDER BY binding_seq DESC LIMIT 1`, workID, reviewSeq).Scan(&commit)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the newest verification report")
	}
	return commit != review.Report.OutputCandidate.Commit && commit != review.Report.InputCandidate.Commit, nil
}

func reportCovers(report VerificationReport, candidate *ReviewCandidate) bool {
	return candidate != nil && report.Result == "pass" && report.Commit == candidate.Commit
}
