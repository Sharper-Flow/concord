package store

import (
	"context"
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

func reportCovers(report VerificationReport, candidate *ReviewCandidate) bool {
	return candidate != nil && report.Result == "pass" && report.Commit == candidate.Commit
}
