package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// CD-0039: native-run outcomes are attributed reports, not Concord findings.
// A record means an authenticated trusted client reported a status about a
// native run at a time with evidence; it never means Concord performed or
// verified the status. CD-0040 D11 adds the shared capture component, pinned
// here to the operator-approved native-run report policy until a reviewed
// policy registry exists.
const (
	WorkflowNativeRunRecorded = "workflow.native_run_recorded"
)

// nativeRunPolicyRefs returns the derived freshness/divergence references for
// the native-run kind. CD-0040 D7: a same-ID policy edit changes the hash and
// so produces a new reference instead of silently rebinding existing records.
func nativeRunPolicyRefs() (string, string) {
	policy, _ := ExternalSubjectPolicyFor("native_run")
	ref := PolicyRef(policy)
	return ref, ref
}

type nativeRunPayload struct {
	WorkID                string `json:"work_id"`
	RunID                 string `json:"run_id"`
	NativeSubjectRef      string `json:"native_subject_ref"`
	SubjectDigest         string `json:"subject_digest"`
	Phase                 string `json:"phase"`
	Status                string `json:"status"`
	EvidenceRef           string `json:"evidence_ref"`
	EvidenceDigest        string `json:"evidence_digest"`
	AssertedAt            string `json:"asserted_at"`
	ReportingAuthorityRef string `json:"reporting_authority_ref"`
	ActorRef              string `json:"actor_ref"`
	// CD-0040 D11 capture component: the shared closed structure, not a free
	// map. Core-derived; callers cannot author it.
	CaptureMethod       CaptureMethodKind `json:"capture_method"`
	ObservedUniverse    ObservedUniverse  `json:"observed_universe"`
	FreshnessPolicyRef  string            `json:"freshness_policy_ref"`
	DivergencePolicyRef string            `json:"divergence_policy_ref"`
	ObservationID       string            `json:"observation_id"`
	ExpectedVersion     int64             `json:"expected_version"`
	ResultingVersion    int64             `json:"resulting_version"`
}

func validateNativeRunPayload(event Event, payload nativeRunPayload) error {
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	return validateNativeRunShape(&payload)
}

func validateNativeRunShape(payload *nativeRunPayload) error {
	if len(payload.RunID) < 1 || len(payload.RunID) > 128 || len(payload.NativeSubjectRef) < 1 || len(payload.NativeSubjectRef) > 2048 {
		return newFailure(KindInvalidPayload, "validate_event", "native run identity is not bounded", false, "supply a bounded run ID and native subject reference")
	}
	if len(payload.EvidenceRef) < 1 || len(payload.EvidenceRef) > 2048 || len(payload.EvidenceDigest) < 1 || len(payload.EvidenceDigest) > 256 {
		return newFailure(KindInvalidPayload, "validate_event", "native run evidence is not bounded", false, "supply the native authority's evidence reference and digest")
	}
	if !NativeRunStatusAllowed(payload.Phase, payload.Status) {
		return newFailure(KindInvalidPayload, "validate_event", "native run status is outside the phase vocabulary", false, "use the closed status vocabulary for the reported phase")
	}
	if payload.SubjectDigest == "" {
		return newFailure(KindInvalidPayload, "validate_event", "native run subject digest is missing", false, "carry the subject digest derived from the native subject reference")
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.AssertedAt); err != nil {
		return newFailure(KindInvalidPayload, "validate_event", "native run asserted_at is not RFC3339", false, "supply the reporting authority's own observation time")
	}
	if err := ValidateObservedUniverse(payload.ObservedUniverse); err != nil {
		return newFailure(KindInvalidPayload, "validate_event", "native run observed universe is not a closed structure: "+err.Error(), false, "the universe is core-derived; this is an encoding defect")
	}
	if payload.FreshnessPolicyRef == "" || payload.FreshnessPolicyRef != payload.DivergencePolicyRef {
		return newFailure(KindInvalidPayload, "validate_event", "native run policy references are not bound", false, "the policy references are core-derived; this is an encoding defect")
	}
	freshness, divergence := nativeRunPolicyRefs()
	if payload.FreshnessPolicyRef != freshness || payload.DivergencePolicyRef != divergence {
		return newFailure(KindInvalidPayload, "validate_event", "native run policy references do not match the reviewed register", false, "the policy references are core-derived; this is an encoding defect")
	}
	if payload.ObservationID == "" {
		return newFailure(KindInvalidPayload, "validate_event", "native run observation identity is missing", false, "the observation identity is core-derived; this is an encoding defect")
	}
	return nil
}

// nativeRunProjectionRow is the workflow_native_runs projection one recorded
// report folds into. The current fold and the released-schema fold share it,
// so the projection INSERT has one owner and a legacy row can differ only in
// its observation binding.
type nativeRunProjectionRow struct {
	workID                string
	runID                 string
	phase                 string
	status                string
	eventID               string
	reportingAuthorityRef string
	actorRef              string
	nativeSubjectRef      string
	subjectDigest         string
	evidenceRef           string
	evidenceDigest        string
	assertedAt            string
	recordedAt            string
	captureMethod         string
	observedUniverse      string
	freshnessPolicyRef    string
	divergencePolicyRef   string
	// observationID binds the row to the shared external_observations
	// component. A released-schema row records NULL: its writer predated the
	// shared component, so no verification event can ever bind to it and the
	// row reads unverified.
	observationID sql.NullString
}

// upsertWorkflowNativeRunTx is the single workflow_native_runs projection SQL
// owner for both folds.
func upsertWorkflowNativeRunTx(ctx context.Context, tx *sql.Tx, row nativeRunProjectionRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(work_id,run_id,phase) DO UPDATE SET status=excluded.status,event_id=excluded.event_id,reporting_authority_ref=excluded.reporting_authority_ref,actor_ref=excluded.actor_ref,native_subject_ref=excluded.native_subject_ref,subject_digest=excluded.subject_digest,evidence_ref=excluded.evidence_ref,evidence_digest=excluded.evidence_digest,asserted_at=excluded.asserted_at,recorded_at=excluded.recorded_at,capture_method=excluded.capture_method,observed_universe=excluded.observed_universe,freshness_policy_ref=excluded.freshness_policy_ref,divergence_policy_ref=excluded.divergence_policy_ref,observation_id=excluded.observation_id`,
		row.workID, row.runID, row.phase, row.status, row.eventID, row.reportingAuthorityRef, row.actorRef, row.nativeSubjectRef, row.subjectDigest, row.evidenceRef, row.evidenceDigest, row.assertedAt, row.recordedAt, row.captureMethod, row.observedUniverse, row.freshnessPolicyRef, row.divergencePolicyRef, row.observationID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot record native run report", true, "retry once the database is writable", err)
	}
	return nil
}

func foldNativeRunRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload nativeRunPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateNativeRunShape(&payload); err != nil {
		return err
	}
	universe, err := json.Marshal(payload.ObservedUniverse)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode native run observed universe", false, "repair the stored native run report", err)
	}
	// CD-0040 D11: the embedded component's shared provenance home. The run's
	// first reported phase registers the observation; later phases of the
	// same run share it, and verification events bind it here.
	if _, err := tx.ExecContext(ctx, `INSERT INTO external_observations(observation_id,work_id,subject_kind,subject_ref,capture_method,captured_at,reporting_authority_ref,subject_digest,observed_universe,freshness_policy_ref,divergence_policy_ref,verification_state,created_event_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,'unverified',?) ON CONFLICT(observation_id) DO NOTHING`,
		payload.ObservationID, event.SubjectID, "native_run", payload.NativeSubjectRef, string(payload.CaptureMethod), payload.AssertedAt, payload.ReportingAuthorityRef, payload.SubjectDigest, string(universe), payload.FreshnessPolicyRef, payload.DivergencePolicyRef, event.Seq); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot register the native run observation", true, "retry once the database is writable", err)
	}
	if err := upsertWorkflowNativeRunTx(ctx, tx, nativeRunProjectionRow{
		workID: event.SubjectID, runID: payload.RunID, phase: payload.Phase, status: payload.Status,
		eventID: event.EventID, reportingAuthorityRef: payload.ReportingAuthorityRef, actorRef: payload.ActorRef,
		nativeSubjectRef: payload.NativeSubjectRef, subjectDigest: payload.SubjectDigest,
		evidenceRef: payload.EvidenceRef, evidenceDigest: payload.EvidenceDigest,
		assertedAt: payload.AssertedAt, recordedAt: event.OccurredAt.UTC().Format(time.RFC3339Nano),
		captureMethod: string(payload.CaptureMethod), observedUniverse: string(universe),
		freshnessPolicyRef: payload.FreshnessPolicyRef, divergencePolicyRef: payload.DivergencePolicyRef,
		observationID: sql.NullString{String: payload.ObservationID, Valid: true},
	}); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, payload.ExpectedVersion, payload.ResultingVersion, "work item")
}

// NativeRunReport is the attributed read projection of one native-run phase.
// Reporter, subject, evidence, and both times always ride with the status;
// dropping attribution from a display is a contract defect (CD-0039 D1/D4).
type NativeRunReport struct {
	RunID                 string `json:"run_id"`
	Phase                 string `json:"phase"`
	Status                string `json:"status"`
	EventID               string `json:"event_id"`
	ReportingAuthorityRef string `json:"reporting_authority_ref"`
	ActorRef              string `json:"actor_ref"`
	NativeSubjectRef      string `json:"native_subject_ref"`
	SubjectDigest         string `json:"subject_digest"`
	EvidenceRef           string `json:"evidence_ref"`
	EvidenceDigest        string `json:"evidence_digest"`
	AssertedAt            string `json:"asserted_at"`
	RecordedAt            string `json:"recorded_at"`
	ObservationID         string `json:"observation_id"`
	VerificationState     string `json:"verification_state"`
	Unverified            bool   `json:"unverified"`
}

// readWorkflowNativeRunsTx returns the attributed native-run reports for one
// work item, newest phase report per run, on the caller's transaction. The
// store pools a single SQLite connection, so this read must share the
// continuity snapshot's transaction rather than open a second connection.
// Every report carries its verification participation (CD-0040 D9/D11):
// unverified and diverged reports remain readable, and a consequential
// consumer fails closed at workflow.evidence_bound until the observation is
// verified.
func readWorkflowNativeRunsTx(ctx context.Context, tx *sql.Tx, workID string) ([]NativeRunReport, error) {
	rows, err := tx.QueryContext(ctx, `SELECT run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,COALESCE(observation_id,''),COALESCE(verification_state,'unverified') FROM workflow_native_runs WHERE work_id=? ORDER BY run_id,phase`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "native_runs", "cannot read native run reports", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []NativeRunReport{}
	for rows.Next() {
		var report NativeRunReport
		if err := rows.Scan(&report.RunID, &report.Phase, &report.Status, &report.EventID, &report.ReportingAuthorityRef, &report.ActorRef, &report.NativeSubjectRef, &report.SubjectDigest, &report.EvidenceRef, &report.EvidenceDigest, &report.AssertedAt, &report.RecordedAt, &report.ObservationID, &report.VerificationState); err != nil {
			return nil, wrapFailure(KindUnavailable, "native_runs", "cannot decode native run reports", true, "retry once the database is readable", err)
		}
		if report.VerificationState == "" {
			report.VerificationState = string(VerificationUnverified)
		}
		report.Unverified = report.VerificationState != string(VerificationVerified)
		out = append(out, report)
	}
	return out, rows.Err()
}
