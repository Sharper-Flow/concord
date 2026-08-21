package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	nativeRunAssertedSkewBound = 2 * time.Minute
)

// nativeRunObservationID derives the shared-component observation identity
// from the subject digest the core already computed. The same run's reports
// share one observation, so one verification answers every phase row.
func nativeRunObservationID(subjectDigest string) string {
	return "xobs:" + subjectDigest[len("sha256:"):len("sha256:")+16]
}

// nativeRunPolicyRefs returns the derived freshness/divergence references for
// the native-run kind. CD-0040 D7: a same-ID policy edit changes the hash and
// so produces a new reference instead of silently rebinding existing records.
func nativeRunPolicyRefs() (string, string) {
	policy, _ := ExternalSubjectPolicyFor("native_run")
	ref := PolicyRef(policy)
	return ref, ref
}

var nativeRunStatusVocab = map[string]map[string]bool{
	"start":    {"started": true, "failed_to_start": true},
	"health":   {"healthy": true, "degraded": true, "failed": true},
	"rollback": {"rolled_back": true, "partially_rolled_back": true, "rollback_failed": true},
	"cleanup":  {"cleaned": true, "cleanup_failed": true},
}

// nativeRunFailureStatuses are the report statuses under which the approved
// logical operation did not complete successfully; the action outcome is
// partial or failed, never ok (CD-0039 D8).
// NativeRunStatusIsFailure is the exported classification the agent surface
// uses to derive the partial outcome (CD-0039 D7/D8).
func NativeRunStatusIsFailure(phase, status string) bool {
	return nativeRunStatusIsFailure(phase, status)
}

func nativeRunStatusIsFailure(phase, status string) bool {
	// Every rollback-phase report means the approved change was undone or its
	// undo failed; either way the logical operation did not complete
	// successfully (CD-0039 D7).
	if phase == "rollback" {
		return true
	}
	switch status {
	case "failed_to_start", "failed", "cleanup_failed":
		return true
	}
	return false
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
	if !nativeRunStatusVocab[payload.Phase][payload.Status] {
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

// buildNativeRunEvent derives the attributed record from a workflow action's
// typed fields. The capture component and policy references are core-derived;
// the reporting authority is the authenticated actor's client, never a caller
// string.
func buildNativeRunEvent(eventID, workID string, actor WorkflowActor, now time.Time, expected int64, phase, runID, subjectRef, status, evidenceRef, evidenceDigest, assertedAt string) (Event, error) {
	digest := sha256.Sum256([]byte(subjectRef))
	subjectDigest := "sha256:" + hex.EncodeToString(digest[:])
	freshness, divergence := nativeRunPolicyRefs()
	payload := nativeRunPayload{
		WorkID: workID, RunID: runID, NativeSubjectRef: subjectRef, Phase: phase,
		SubjectDigest: subjectDigest,
		Status:        status, EvidenceRef: evidenceRef, EvidenceDigest: evidenceDigest,
		AssertedAt: assertedAt, ReportingAuthorityRef: actor.ClientRef,
		ActorRef:      DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef),
		CaptureMethod: CaptureTrustedClientReport,
		// The universe is structural: one item, exactly scoped to the reported
		// subject, complete by authoritative item read, one identity. No
		// caller hand-builds a universe for a native report (CD-0040 D4).
		ObservedUniverse: ObservedUniverse{
			Shape:                UniverseItem,
			AppliedScope:         subjectRef,
			AnchorToken:          subjectDigest,
			StructureDigest:      subjectDigest,
			Coverage:             CoverageComplete,
			ObservedCount:        1,
			TotalKind:            TotalEq,
			TotalValue:           1,
			CompletionEvidence:   CompletionClosedStructureDigest,
			CanonicalIdentityKey: workID + "/" + runID,
		},
		FreshnessPolicyRef:  freshness,
		DivergencePolicyRef: divergence,
		ObservationID:       nativeRunObservationID(subjectDigest),
		ExpectedVersion:     expected, ResultingVersion: expected + 1,
	}
	if err := validateNativeRunShape(&payload); err != nil {
		return Event{}, err
	}
	// asserted_at may not trail the core event time by more than the
	// authority clock-skew bound (CD-0039 D3).
	if asserted, parseErr := time.Parse(time.RFC3339Nano, payload.AssertedAt); parseErr == nil && asserted.After(now.Add(nativeRunAssertedSkewBound)) {
		return Event{}, newFailure(KindInvalidPayload, "workflow_action", "native run asserted_at exceeds the report skew bound", false, "supply the reporting authority's own observation time")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "workflow_action", "cannot encode native run report", false, "supply a JSON-safe report", err)
	}
	return Event{EventID: eventID, Kind: WorkflowNativeRunRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), OccurredAt: now, PayloadVersion: 1, Payload: encoded}, nil
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
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(work_id,run_id,phase) DO UPDATE SET status=excluded.status,event_id=excluded.event_id,reporting_authority_ref=excluded.reporting_authority_ref,actor_ref=excluded.actor_ref,native_subject_ref=excluded.native_subject_ref,subject_digest=excluded.subject_digest,evidence_ref=excluded.evidence_ref,evidence_digest=excluded.evidence_digest,asserted_at=excluded.asserted_at,recorded_at=excluded.recorded_at,capture_method=excluded.capture_method,observed_universe=excluded.observed_universe,freshness_policy_ref=excluded.freshness_policy_ref,divergence_policy_ref=excluded.divergence_policy_ref,observation_id=excluded.observation_id`,
		event.SubjectID, payload.RunID, payload.Phase, payload.Status, event.EventID, payload.ReportingAuthorityRef, payload.ActorRef, payload.NativeSubjectRef, payload.SubjectDigest, payload.EvidenceRef, payload.EvidenceDigest, payload.AssertedAt, event.OccurredAt.UTC().Format(time.RFC3339Nano), string(payload.CaptureMethod), string(universe), payload.FreshnessPolicyRef, payload.DivergencePolicyRef, payload.ObservationID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot record native run report", true, "retry once the database is writable", err)
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
