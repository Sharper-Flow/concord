package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CD-0039: native-run outcomes are attributed reports, not Concord findings.
// A native-run record means "authenticated trusted client X reported status Y
// about native run R at time T, with evidence E" — never that Concord
// performed, observed, or independently verified Y. CD-0040 D11 adds the shared
// capture component to every such event.

const EventWorkflowNativeRunRecorded = "workflow.native_run_recorded"

// NativeRunPhase is the runbook phase an action reports for. The action ID
// supplies the phase; callers never choose it independently (CD-0039 D3).
type NativeRunPhase string

const (
	NativePhaseStart    NativeRunPhase = "start"
	NativePhaseHealth   NativeRunPhase = "health"
	NativePhaseRollback NativeRunPhase = "rollback"
	NativePhaseCleanup  NativeRunPhase = "cleanup"
)

// nativeRunStatusVocabulary fixes the closed status set per phase.
var nativeRunStatusVocabulary = map[NativeRunPhase][]string{
	NativePhaseStart:    {"started", "failed_to_start"},
	NativePhaseHealth:   {"healthy", "degraded", "failed"},
	NativePhaseRollback: {"rolled_back", "partially_rolled_back", "rollback_failed"},
	NativePhaseCleanup:  {"cleaned", "cleanup_failed"},
}

// nativeRunActionPhase maps each reporting action to its phase.
var nativeRunActionPhase = map[string]NativeRunPhase{
	"start_run":     NativePhaseStart,
	"record_health": NativePhaseHealth,
	"rollback_run":  NativePhaseRollback,
	"cleanup_run":   NativePhaseCleanup,
}

func nativeRunPhaseForAction(actionID string) (NativeRunPhase, bool) {
	phase, ok := nativeRunActionPhase[actionID]
	return phase, ok
}

// NativeReport is the strict typed report the reporting authority supplies
// through a workflow action (CD-0039 D5). Arbitrary field bags are refused.
// ReportingAuthorityRef and ActorRef are derived by the core from the
// validated grant; they are absent from the input on purpose.
type NativeReport struct {
	RunID            string `json:"run_id"`
	NativeSubjectRef string `json:"native_subject_ref"`
	SubjectDigest    string `json:"subject_digest"`
	Status           string `json:"status"`
	AssertedAt       string `json:"asserted_at"`
	// Health reports require the observation the authority measured
	// (CD-0039 D3).
	ObservationRef    string `json:"observation_ref,omitempty"`
	ObservationDigest string `json:"observation_digest,omitempty"`
	EvidenceRef       string `json:"evidence_ref"`
	EvidenceDigest    string `json:"evidence_digest"`
	// capture_method anchors the embedded CD-0040 component. v1 accepts only
	// attributed trusted-client reports; the core has no native probes.
	CaptureMethod CaptureMethodKind `json:"capture_method"`
}

// workflowNativeRunRecordedPayload is the closed event body: the attributed
// report plus the shared CD-0040 capture component.
type workflowNativeRunRecordedPayload struct {
	WorkflowVersionFields
	RunID                 string                     `json:"run_id"`
	NativeSubjectRef      string                     `json:"native_subject_ref"`
	SubjectDigest         string                     `json:"subject_digest"`
	Phase                 string                     `json:"phase"`
	Status                string                     `json:"status"`
	EvidenceRef           string                     `json:"evidence_ref"`
	EvidenceDigest        string                     `json:"evidence_digest"`
	AssertedAt            string                     `json:"asserted_at"`
	ReportingAuthorityRef string                     `json:"reporting_authority_ref"`
	ActorRef              string                     `json:"actor_ref"`
	Capture               ExternalObservationCapture `json:"capture"`
}

// maxNativeReportSkew bounds how far the report's own time may sit after the
// core event time (CD-0039 D3 names the existing two-minute authority bound).
const maxNativeReportSkew = 2 * time.Minute

// ValidateNativeReport enforces the phase-specific requirements before any
// event is constructed. Unknown statuses and missing required evidence fail
// structurally.
func ValidateNativeReport(phase NativeRunPhase, report NativeReport, recordedAt time.Time) error {
	if !boundedString(report.RunID, 1, 128) {
		return newFailure(KindInvalidPayload, "native_run", "run id must be a bounded non-empty identifier", false, "supply the native run id")
	}
	if !boundedString(report.NativeSubjectRef, 1, 2048) {
		return newFailure(KindInvalidPayload, "native_run", "native subject reference must be bounded and non-secret", false, "supply the opaque subject reference")
	}
	if !validSHA256Prefixed(report.SubjectDigest) {
		return newFailure(KindInvalidPayload, "native_run", "subject digest must be a sha256 reference", false, "supply the subject content digest")
	}
	allowed := false
	for _, status := range nativeRunStatusVocabulary[phase] {
		if status == report.Status {
			allowed = true
			break
		}
	}
	if !allowed {
		return newFailure(KindInvalidPayload, "native_run", fmt.Sprintf("status %q is not in the %s-phase vocabulary", report.Status, phase), false, "supply a declared status for this phase")
	}
	if !boundedString(report.EvidenceRef, 1, 2048) {
		return newFailure(KindInvalidPayload, "native_run", "a native report requires its evidence reference", false, "supply the evidence the authority reports against")
	}
	if !validSHA256Prefixed(report.EvidenceDigest) {
		return newFailure(KindInvalidPayload, "native_run", "evidence digest must be a sha256 reference", false, "supply the evidence content digest")
	}
	assertedAt, ok := parseRFC3339(report.AssertedAt)
	if !ok {
		return newFailure(KindInvalidPayload, "native_run", "asserted_at must be RFC3339", false, "supply the report's own time as RFC3339")
	}
	// The report may be slightly older than the core event, but it may not be
	// from the future beyond the authority clock-skew bound.
	if assertedAt.Sub(recordedAt) > maxNativeReportSkew {
		return newFailure(KindInvalidPayload, "native_run", "asserted_at is more than two minutes after the recorded event time", false, "resubmit the report with the authority's current time")
	}
	if phase == NativePhaseHealth {
		if !boundedString(report.ObservationRef, 1, 2048) || !validSHA256Prefixed(report.ObservationDigest) {
			return newFailure(KindInvalidPayload, "native_run", "health reports require their observation reference and digest", false, "supply the health observation the authority measured")
		}
	}
	if report.CaptureMethod != CaptureTrustedClientReport {
		return newFailure(KindInvalidPayload, "native_run", "native run capture must be a trusted-client report in v1", false, "report through the authenticated client path")
	}
	return nil
}

// deriveNativeRunCapture builds the embedded CD-0040 component from the
// validated report. The universe is structural: one item, exactly scoped to
// the reported subject, complete by authoritative item read, one identity. No
// caller hand-builds a universe for a native report.
func deriveNativeRunCapture(report NativeReport, reportingAuthorityRef string) (ExternalObservationCapture, error) {
	observationID := fmt.Sprintf("xobs:%s", report.SubjectDigest[7:23])
	capture := ExternalObservationCapture{
		ObservationID:         observationID,
		SubjectKind:           "native_run",
		SubjectRef:            report.NativeSubjectRef,
		CaptureMethod:         report.CaptureMethod,
		CapturedAt:            report.AssertedAt,
		ReportingAuthorityRef: reportingAuthorityRef,
		SubjectDigest:         report.SubjectDigest,
		ObservedUniverse: ObservedUniverse{
			Shape:                UniverseItem,
			AppliedScope:         "native:" + report.NativeSubjectRef,
			AnchorToken:          report.SubjectDigest,
			Coverage:             CoverageComplete,
			ObservedCount:        1,
			TotalKind:            TotalEq,
			TotalValue:           1,
			CompletionEvidence:   CompletionAuthoritativeItemRead,
			CanonicalIdentityKey: "subject_digest",
		},
	}
	policy, _ := ExternalSubjectPolicyFor("native_run")
	capture.FreshnessPolicyRef = PolicyRef(policy)
	capture.DivergencePolicyRef = PolicyRef(policy)
	if err := ValidateExternalObservationCapture(capture); err != nil {
		return ExternalObservationCapture{}, err
	}
	return capture, nil
}

func foldWorkflowNativeRunRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p workflowNativeRunRecordedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	phase := NativeRunPhase(p.Phase)
	if _, known := nativeRunStatusVocabulary[phase]; !known {
		return newFailure(KindInvalidPayload, "fold_event", "native run phase is not in the closed vocabulary", false, "supply start, health, rollback, or cleanup")
	}
	if err := ValidateExternalObservationCapture(p.Capture); err != nil {
		return err
	}
	// The embedded component must agree with the attributed fields it mirrors;
	// two accounts of the same fact cannot diverge inside one event.
	if p.Capture.SubjectRef != p.NativeSubjectRef || p.Capture.SubjectDigest != p.SubjectDigest ||
		p.Capture.CapturedAt != p.AssertedAt || p.Capture.ReportingAuthorityRef != p.ReportingAuthorityRef ||
		p.Capture.SubjectKind != "native_run" {
		return newFailure(KindInvariantViolation, "fold_event", "capture component disagrees with the attributed report it mirrors", false, "repair the embedded capture component")
	}
	// A reused run id with a different subject or authority fails structurally
	// (CD-0039 D3).
	var priorSubject, priorAuthority string
	err := tx.QueryRowContext(ctx, `SELECT subject_digest,reporting_authority_ref FROM workflow_native_runs WHERE work_id=? AND run_id=? ORDER BY recorded_seq DESC LIMIT 1`, event.SubjectID, p.RunID).Scan(&priorSubject, &priorAuthority)
	switch {
	case err == sql.ErrNoRows:
		// first report for this run
	case err != nil:
		return err
	default:
		if priorSubject != p.SubjectDigest || priorAuthority != p.ReportingAuthorityRef {
			return newFailure(KindInvariantViolation, "fold_event", "run id is already bound to a different subject or reporting authority", false, "use a new run id")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,reporting_authority_ref,actor_ref,observation_id,verification_state,recorded_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'unverified',?)
		ON CONFLICT(work_id,run_id,phase) DO UPDATE SET status=excluded.status, subject_ref=excluded.subject_ref, subject_digest=excluded.subject_digest, evidence_ref=excluded.evidence_ref, evidence_digest=excluded.evidence_digest, asserted_at=excluded.asserted_at, reporting_authority_ref=excluded.reporting_authority_ref, actor_ref=excluded.actor_ref, observation_id=excluded.observation_id, recorded_seq=excluded.recorded_seq`,
		event.SubjectID, p.RunID, p.Phase, p.Status, p.NativeSubjectRef, p.SubjectDigest, p.EvidenceRef, p.EvidenceDigest, p.AssertedAt, p.ReportingAuthorityRef, p.ActorRef, p.Capture.ObservationID, event.Seq); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot fold native run record", true, "retry once the database is writable", err)
	}
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

// NativeRunRow is the attributed read projection (CD-0039 D4). Every read
// returns the reporting authority, both times, and evidence identity; dropping
// attribution from a display is a contract defect.
type NativeRunRow struct {
	WorkID                string                  `json:"work_id"`
	RunID                 string                  `json:"run_id"`
	Phase                 NativeRunPhase          `json:"phase"`
	Status                string                  `json:"status"`
	SubjectRef            string                  `json:"subject_ref"`
	SubjectDigest         string                  `json:"subject_digest"`
	EvidenceRef           string                  `json:"evidence_ref"`
	EvidenceDigest        string                  `json:"evidence_digest"`
	AssertedAt            string                  `json:"asserted_at"`
	ReportingAuthorityRef string                  `json:"reporting_authority_ref"`
	ActorRef              string                  `json:"actor_ref"`
	ObservationID         string                  `json:"observation_id"`
	VerificationState     FoldedVerificationState `json:"verification_state"`
}

// NativeRunsForWork returns the folded native-run rows for a work item,
// attribution included. The status is the reporter's claim; it never renders
// as an unqualified fact.
func (s *Store) NativeRunsForWork(ctx context.Context, workID string, limit int) ([]NativeRunRow, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_native_runs", "store is not open", false, "open the authority database")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,phase,status,subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,reporting_authority_ref,actor_ref,observation_id,verification_state FROM workflow_native_runs WHERE work_id=? ORDER BY recorded_seq DESC, run_id LIMIT ?`, workID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_native_runs", "cannot read native runs", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []NativeRunRow{}
	for rows.Next() {
		var row NativeRunRow
		if err := rows.Scan(&row.RunID, &row.Phase, &row.Status, &row.SubjectRef, &row.SubjectDigest, &row.EvidenceRef, &row.EvidenceDigest, &row.AssertedAt, &row.ReportingAuthorityRef, &row.ActorRef, &row.ObservationID, &row.VerificationState); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_native_runs", "cannot decode native run", true, "retry once the database is readable", err)
		}
		row.WorkID = workID
		out = append(out, row)
	}
	return out, rows.Err()
}
