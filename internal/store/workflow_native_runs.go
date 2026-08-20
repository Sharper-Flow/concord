package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// CD-0039: Concord records an attributed report of what a native authority
// says it did. It never records that Concord did it, observed it, or verified
// it. Every field here serves that attribution; dropping one from a read or
// projection is a contract defect (D1).

// Native run phases and their closed status vocabularies (D3). The action ID
// supplies the phase; callers never choose them independently.
var nativeRunStatusVocabulary = map[string]map[string]bool{
	"start":    {"started": true, "failed_to_start": true},
	"health":   {"healthy": true, "degraded": true, "failed": true},
	"rollback": {"rolled_back": true, "partially_rolled_back": true, "rollback_failed": true},
	"cleanup":  {"cleaned": true, "cleanup_failed": true},
}

var nativeRunPhasesByAction = map[string]string{
	"start_run":     "start",
	"record_health": "health",
	"rollback_run":  "rollback",
	"cleanup_run":   "cleanup",
}

// The native-report skew bound (D3): a report may not be asserted more than
// two minutes after the core records it. The value rides the existing
// authority clock-skew bound; a different value requires contract evidence.
const nativeReportMaxSkew = 2 * time.Minute

type workflowNativeRunRecordedPayload struct {
	WorkflowVersionFields
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
	// CD-0040 D11: the shared external-observation capture component. A native
	// status may be folded and read as an attributed report while unverified;
	// the component records how it was captured and which policies govern it.
	CaptureMethod       string `json:"capture_method"`
	ObservedUniverse    string `json:"observed_universe"`
	FreshnessPolicyRef  string `json:"freshness_policy_ref"`
	DivergencePolicyRef string `json:"divergence_policy_ref"`
	Verified            bool   `json:"verified"`
}

func nativeRunFields() map[string]any {
	return map[string]any{
		"run_id": true, "native_subject_ref": true, "subject_digest": true,
		"status": true, "evidence_ref": true, "evidence_digest": true,
		"asserted_at": true, "capture_method": true, "observed_universe": true,
		"freshness_policy_ref": true, "divergence_policy_ref": true,
	}
}

// nativeRunRequiredFields states, per phase, which typed fields the report
// cannot omit (D3: health requires an observation reference and digest;
// rollback requires rollback evidence). The subject, authority, and capture
// component are required by every phase.
func nativeRunRequiredFields(phase string) []string {
	required := []string{"run_id", "native_subject_ref", "subject_digest", "asserted_at", "capture_method", "observed_universe", "freshness_policy_ref", "divergence_policy_ref"}
	switch phase {
	case "health", "rollback":
		required = append(required, "evidence_ref", "evidence_digest")
	}
	return required
}

// buildNativeRunEvent validates the caller's typed report fields and derives
// the attributed event. Reporting authority is the authenticated actor of the
// dispatch (D2): never a workflow field, never agent prose.
func buildNativeRunEvent(eventID, workID, actor string, now time.Time, expected int64, actionID string, fields map[string]json.RawMessage) (Event, error) {
	phase, ok := nativeRunPhasesByAction[actionID]
	if !ok {
		return Event{}, newFailure(KindInvalidOperation, "workflow_action", "action does not record a native run", false, "use a native-run action")
	}
	values := map[string]any{"phase": phase}
	for name := range nativeRunFields() {
		raw, present := fields[name]
		if !present || string(raw) == "null" {
			continue
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return Event{}, newFailure(KindInvalidPayload, "workflow_action", "native run field "+name+" is not valid JSON", false, "send typed fields only")
		}
		values[name] = decoded
	}
	missing := []string{}
	for _, name := range nativeRunRequiredFields(phase) {
		if _, present := values[name]; !present {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Event{}, newFailure(KindInvalidPayload, "workflow_action", "native run report is missing required fields: "+strings.Join(missing, ", "), false, "supply the typed report fields")
	}
	status, _ := values["status"].(string)
	if !nativeRunStatusVocabulary[phase][status] {
		return Event{}, newFailure(KindInvalidPayload, "workflow_action", "status is not in the "+phase+" vocabulary", false, "use the phase's declared statuses")
	}
	assertedAt, _ := values["asserted_at"].(string)
	asserted, err := time.Parse(time.RFC3339Nano, assertedAt)
	if err != nil {
		return Event{}, newFailure(KindInvalidPayload, "workflow_action", "asserted_at is not RFC3339", false, "send the report's own time")
	}
	if asserted.Sub(now) > nativeReportMaxSkew {
		return Event{}, newFailure(KindInvalidPayload, "workflow_action", "asserted_at is more than the native-report skew bound after the recording time", false, "send the report's own time")
	}
	values["reporting_authority_ref"] = actor
	values["actor_ref"] = actor
	values["verified"] = false
	return workflowTypedEvent(eventID, WorkflowNativeRunRecorded, workID, actor, now, expected, values), nil
}

func foldWorkflowNativeRunRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowNativeRunRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	// Evidence is required by the health and rollback phases (D3); the other
	// phases may omit it, so the empty string is legal exactly there.
	evidenceBound := func(value, digest string) bool {
		if p.Phase == "health" || p.Phase == "rollback" {
			return workflowString(value, 512) && workflowString(digest, 71)
		}
		return value == "" || workflowString(value, 512)
	}
	if !workflowString(p.RunID, 128) || !workflowString(p.NativeSubjectRef, 2048) || !workflowDigest(p.SubjectDigest, "sha256:") ||
		!evidenceBound(p.EvidenceRef, p.EvidenceDigest) || !workflowString(p.ReportingAuthorityRef, 128) ||
		!workflowString(p.ActorRef, 70) || !workflowString(p.CaptureMethod, 64) || !workflowString(p.ObservedUniverse, 2048) ||
		!workflowString(p.FreshnessPolicyRef, 128) || !workflowString(p.DivergencePolicyRef, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "native run report is incomplete or outside its bounds", false, "supply the attributed report fields within their bounds")
	}
	if !nativeRunStatusVocabulary[p.Phase][p.Status] {
		return newFailure(KindInvalidPayload, "fold_event", "native run status is outside its phase vocabulary", false, "use the phase's declared statuses")
	}
	asserted, err := time.Parse(time.RFC3339Nano, p.AssertedAt)
	if err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "native run asserted_at is not RFC3339", false, "send the report's own time")
	}
	if asserted.Sub(event.OccurredAt) > nativeReportMaxSkew {
		return newFailure(KindInvalidPayload, "fold_event", "native run report exceeds the skew bound", false, "send the report's own time")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}

	// D3: a reused run ID must not change its subject or authority. The row is
	// the latest phase of an attributed run, not a log of attempts.
	var existingSubject, existingAuthority string
	err = tx.QueryRowContext(ctx, `SELECT native_subject_ref, reporting_authority_ref FROM workflow_native_runs WHERE work_id=? AND run_id=?`, event.SubjectID, p.RunID).Scan(&existingSubject, &existingAuthority)
	switch {
	case err == sql.ErrNoRows:
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,verified,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			event.SubjectID, p.RunID, p.Phase, p.Status, event.EventID, p.ReportingAuthorityRef, p.ActorRef, p.NativeSubjectRef, p.SubjectDigest, p.EvidenceRef, p.EvidenceDigest, p.AssertedAt, event.OccurredAt.UTC().Format(time.RFC3339Nano), p.Verified, p.CaptureMethod, p.ObservedUniverse, p.FreshnessPolicyRef, p.DivergencePolicyRef)
		if err != nil {
			return workflowProjectionError(err, "cannot record native run")
		}
	case err != nil:
		return workflowProjectionError(err, "cannot read existing native run")
	default:
		if existingSubject != p.NativeSubjectRef || existingAuthority != p.ReportingAuthorityRef {
			return newFailure(KindInvariantViolation, "fold_event", "run ID is reused with a different subject or reporting authority", false, "use a new run ID")
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_native_runs SET phase=?,status=?,event_id=?,actor_ref=?,evidence_ref=?,evidence_digest=?,asserted_at=?,recorded_at=?,verified=?,capture_method=?,observed_universe=?,freshness_policy_ref=?,divergence_policy_ref=? WHERE work_id=? AND run_id=?`,
			p.Phase, p.Status, event.EventID, p.ActorRef, p.EvidenceRef, p.EvidenceDigest, p.AssertedAt, event.OccurredAt.UTC().Format(time.RFC3339Nano), p.Verified, p.CaptureMethod, p.ObservedUniverse, p.FreshnessPolicyRef, p.DivergencePolicyRef, event.SubjectID, p.RunID)
		if err != nil {
			return workflowProjectionError(err, "cannot advance native run")
		}
	}
	return nil
}

// nativeRunSnapshot is the attributed read of one run (D1/D4): the status
// never travels without its reporter, evidence identity, and both times.
type nativeRunSnapshot struct {
	WorkID                string
	RunID                 string
	Phase                 string
	Status                string
	EventID               string
	ReportingAuthorityRef string
	ActorRef              string
	NativeSubjectRef      string
	SubjectDigest         string
	EvidenceRef           string
	EvidenceDigest        string
	AssertedAt            string
	RecordedAt            string
	Verified              bool
}

// readNativeRun returns the folded, attributed report for one run.
func readNativeRun(ctx context.Context, tx *sql.Tx, workID, runID string) (nativeRunSnapshot, bool, error) {
	var snapshot nativeRunSnapshot
	err := tx.QueryRowContext(ctx, `SELECT work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,verified FROM workflow_native_runs WHERE work_id=? AND run_id=?`, workID, runID).
		Scan(&snapshot.WorkID, &snapshot.RunID, &snapshot.Phase, &snapshot.Status, &snapshot.EventID, &snapshot.ReportingAuthorityRef, &snapshot.ActorRef, &snapshot.NativeSubjectRef, &snapshot.SubjectDigest, &snapshot.EvidenceRef, &snapshot.EvidenceDigest, &snapshot.AssertedAt, &snapshot.RecordedAt, &snapshot.Verified)
	if err == sql.ErrNoRows {
		return nativeRunSnapshot{}, false, nil
	}
	if err != nil {
		return nativeRunSnapshot{}, false, workflowProjectionError(err, "cannot read native run")
	}
	return snapshot, true, nil
}

// classifyNativeRunOutcome derives the durable classification and the typed
// native facts a D7 response carries. The health-failure fact lives in the
// event log, not the latest-row projection — one row per run is the accepted
// fold shape, so the earlier phase is read back from the attributed events
// that recorded it.
func classifyNativeRunOutcome(ctx context.Context, tx *sql.Tx, workID string, snapshot nativeRunSnapshot) (string, map[string]any, bool) {
	attributed := func(from nativeRunSnapshot) map[string]any {
		return map[string]any{
			"status": from.Status, "evidence_ref": from.EvidenceRef, "evidence_digest": from.EvidenceDigest,
			"asserted_at": from.AssertedAt, "reporting_authority_ref": from.ReportingAuthorityRef,
		}
	}
	// Only the rollback phase changes the durable classification. A failed
	// health report alone is a recorded attributed fact; the logical operation
	// is still in flight until the declared rollback either succeeds or fails.
	// The result schema is closed, so facts ride only the response that
	// classified non-completed.
	switch snapshot.Phase {
	case "rollback":
		switch snapshot.Status {
		case "rolled_back", "partially_rolled_back", "rollback_failed":
		default:
			return "", nil, false
		}
		history, err := nativeRunHistory(ctx, tx, workID, snapshot.RunID)
		if err != nil {
			return "", nil, false
		}
		healthAny, healthFailed := history["health_failed"]
		health, healthIsSnapshot := healthAny.(nativeRunSnapshot)
		if !healthFailed || !healthIsSnapshot {
			return "", nil, false
		}
		kind := "partial"
		if snapshot.Status == "rollback_failed" {
			kind = "failed"
		}
		steps := []string{}
		for _, action := range []string{"start_run", "record_health", "rollback_run"} {
			if _, done := history["done_"+action]; done {
				steps = append(steps, action)
			}
		}
		return kind, map[string]any{
			"native_change":          map[string]any{"status": snapshot.Status, "run_id": snapshot.RunID},
			"health_failure":         attributed(health),
			"rollback_result":        attributed(snapshot),
			"completed_native_steps": steps,
		}, true
	}
	return "", nil, false
}

// nativeRunHistory reads one run's attributed reports from the event log and
// reduces them to the facts the D7 classification cites: the failed health
// report, and which native steps actually completed.
func nativeRunHistory(ctx context.Context, tx *sql.Tx, workID, runID string) (map[string]any, error) {
	history := map[string]any{}
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM domain_events WHERE kind=? AND subject_type=? AND subject_id=? ORDER BY occurred_at`, WorkflowNativeRunRecorded, SubjectWorkItem, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read native run history")
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, workflowProjectionError(err, "cannot scan native run history")
		}
		var payload workflowNativeRunRecordedPayload
		if err := json.Unmarshal(raw, &payload); err != nil || payload.RunID != runID {
			continue
		}
		switch payload.Phase {
		case "health":
			history["health_"+payload.Status] = nativeRunSnapshot{
				Status: payload.Status, EvidenceRef: payload.EvidenceRef, EvidenceDigest: payload.EvidenceDigest,
				AssertedAt: payload.AssertedAt, ReportingAuthorityRef: payload.ReportingAuthorityRef,
			}
		case "start":
			history["done_start_run"] = struct{}{}
		case "rollback":
			history["done_rollback_run"] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, ok := history["health_failed"]; ok {
		// The health step completed as a report, whatever it reported.
		history["done_record_health"] = struct{}{}
	}
	return history, nil
}

// AdvanceWorkflowStepForTesting moves a workflow instance's current step
// inside a fold-authorized transaction. It exists for corpus bindings whose
// dispatch-path walk is blocked by a named engine gap (the agent surface
// passes no condition resolver, so the ops runbook's condition-gated advance
// is undispatchable end to end); production code must never call it.
func AdvanceWorkflowStepForTesting(ctx context.Context, tx *Transaction, workID, step string) error {
	if err := enterFold(ctx, tx.tx); err != nil {
		return err
	}
	defer func() { _ = leaveFold(ctx, tx.tx) }()
	raw, err := transactionSQL(tx, "advance_workflow_step_for_testing")
	if err != nil {
		return err
	}
	_, err = raw.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, step, workID)
	return workflowProjectionError(err, "cannot advance workflow step for testing")
}
