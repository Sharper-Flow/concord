package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
)

const (
	WorkerDispatched = "worker.dispatched"
	WorkerCompleted  = "worker.completed"
	WorkerFailed     = "worker.failed"

	WorkerPacketSchemaVersion = "1.0"
	WorkerReportSchemaVersion = "1.0"
)

const (
	WorkerFailureFallbackBlocked = "fallback_blocked"
	WorkerFailureWorkerError     = "worker_error"
	WorkerFailureInvalidReport   = "invalid_report"
	WorkerFailureModelIdentity   = "model_identity_mismatch"
)

// WorkerDispatchedPayload is the v3 dispatch identity. The event subject is
// the owning work item; AttemptID identifies the worker attempt. The
// declared-side routing columns were dropped under CD-0056: Concord records
// what executed (readback_model) and nothing else.
type WorkerDispatchedPayload struct {
	AttemptID           string `json:"attempt_id"`
	LaneID              string `json:"lane_id"`
	LaneVersion         int64  `json:"lane_version"`
	LaneDigest          string `json:"lane_digest"`
	CapabilityClass     string `json:"capability_class"`
	PacketSchemaVersion string `json:"packet_schema_version"`
	ReportSchemaVersion string `json:"report_schema_version"`
	// HostProvenance is the declared record of unversioned host prompt
	// surfaces that shape the worker's behavior (issue #103 / CD-0034):
	// the adapter enumerates what it can bind — agent definition file,
	// AGENTS.md chain at spawn cwd, declared instruction files — and hashes
	// them. Injection is permitted only when recorded. Nil is legal only
	// for payloads older than v3.
	HostProvenance *WorkerHostProvenance `json:"host_provenance,omitempty"`
	// ReadbackModel records the model the host reports as having executed
	// the attempt (CD-0056 D2). It is the only model evidence Concord
	// records; the adapter writes the same value on dispatch and on the
	// worker.completed / worker.failed terminal events.
	ReadbackModel string `json:"readback_model,omitempty"`
	// Terminal records the immediate terminal outcome forced at dispatch
	// (issue #106): "failed" for an undeclared executing model or an
	// exhausted resolution chain, empty for an ordinary dispatch. A
	// terminal dispatch exists so the prohibited outcome is durable
	// evidence, never a usable attempt.
	Terminal string `json:"terminal,omitempty"`
	// TerminalFailureKind / TerminalDetail carry the typed failure when
	// Terminal is set.
	TerminalFailureKind string `json:"terminal_failure_kind,omitempty"`
	TerminalDetail      string `json:"terminal_detail,omitempty"`
}

type WorkerCompletedPayload struct {
	AttemptID           string `json:"attempt_id"`
	ReadbackModel       string `json:"readback_model"`
	ReportSchemaVersion string `json:"report_schema_version"`
}

type WorkerFailedPayload struct {
	AttemptID     string `json:"attempt_id"`
	ReadbackModel string `json:"readback_model"`
	FailureKind   string `json:"failure_kind"`
	Detail        string `json:"detail"`
}

// WorkerHostProvenance is the typed record of host prompt-injection surfaces
// present at dispatch (CD-0034: declared). TotalDigest binds the ordered
// manifest; Sources name each enumerated surface.
type WorkerHostProvenance struct {
	Digest  string                       `json:"digest"`
	Sources []WorkerHostProvenanceSource `json:"sources"`
}

// WorkerHostProvenanceSource names one enumerated injection surface. Kind is
// closed; Path is host-relative or absolute; SHA256 is the file's content
// hash. Unenumerable surfaces (provider hints, voice overlays) are recorded
// as kind "unenumerated" with an empty path and no hash — visible by name.
type WorkerHostProvenanceSource struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// WorkerAttempt is the fold-only current projection of one worker attempt.
// It intentionally contains no workflow step, verdict, or completion state.
// The declared-side routing columns were dropped under CD-0056.
type WorkerAttempt struct {
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	LaneID              string `json:"lane_id"`
	LaneVersion         int64  `json:"lane_version"`
	LaneDigest          string `json:"lane_digest"`
	CapabilityClass     string `json:"capability_class"`
	ReadbackModel       string `json:"readback_model"`
	PacketSchemaVersion string `json:"packet_schema_version"`
	ReportSchemaVersion string `json:"report_schema_version"`
	LifecycleState      string `json:"lifecycle_state"`
	FailureKind         string `json:"failure_kind,omitempty"`
	FailureDetail       string `json:"failure_detail,omitempty"`
	DispatchedAt        string `json:"dispatched_at"`
	CompletedAt         string `json:"completed_at,omitempty"`
	FailedAt            string `json:"failed_at,omitempty"`
}

var workerModelPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*/[^/ ]+$`)
var workerVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

func validateWorkerDispatchedPayload(event Event, payload WorkerDispatchedPayload) error {
	return validateWorkerDispatched(event, payload)
}

func validateWorkerCompletedPayload(_ Event, payload WorkerCompletedPayload) error {
	if payload.AttemptID == "" || !workerModelPattern.MatchString(payload.ReadbackModel) || payload.ReportSchemaVersion != WorkerReportSchemaVersion {
		return invalidWorkerPayload("worker.completed payload has invalid identity or report schema")
	}
	return nil
}

func validateWorkerFailedPayload(_ Event, payload WorkerFailedPayload) error {
	if payload.AttemptID == "" || !workerModelPattern.MatchString(payload.ReadbackModel) || !validWorkerFailureKind(payload.FailureKind) || len(payload.Detail) < 1 || len(payload.Detail) > 4096 {
		return invalidWorkerPayload("worker.failed payload has invalid identity or failure")
	}
	return nil
}

func decodeClosedWorkerPayload(event Event, target any) error {
	return decodeRegisteredPayload(event, target)
}

func validateWorkerDispatched(event Event, payload WorkerDispatchedPayload) error {
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" || payload.AttemptID == "" || !laneIDPattern.MatchString(payload.LaneID) || payload.LaneVersion < 1 || !laneDigestPattern.MatchString(payload.LaneDigest) || !workerVersionPattern.MatchString(payload.CapabilityClass) || payload.PacketSchemaVersion != WorkerPacketSchemaVersion || payload.ReportSchemaVersion != WorkerReportSchemaVersion {
		return invalidWorkerPayload("worker.dispatched payload has invalid identity")
	}
	if payload.ReadbackModel != "" && !workerModelPattern.MatchString(payload.ReadbackModel) {
		return invalidWorkerPayload("worker.dispatched readback_model has invalid shape")
	}
	lane, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest)
	if err != nil {
		return err
	}
	if payload.CapabilityClass != lane.CapabilityClass {
		return invalidWorkerPayload("worker.dispatched capability class does not match lane")
	}
	if payload.Terminal != "" && payload.Terminal != "failed" {
		return invalidWorkerPayload("worker.dispatched terminal value must be empty or 'failed'")
	}
	if payload.Terminal == "failed" && payload.TerminalFailureKind == "" {
		return invalidWorkerPayload("worker.dispatched terminal failure requires a typed kind")
	}
	return ValidateWorkerHostProvenance(payload.HostProvenance)
}

// workerProvenancePattern binds the total digest and per-source hashes.
var workerProvenancePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var workerProvenanceKinds = map[string]bool{
	"agent_definition": true, "agents_md": true, "instruction_file": true, "unenumerated": true,
}

// validateWorkerHostProvenance enforces the CD-0034 declared rule at the
// evidence boundary: when present the provenance must be complete and
// closed. Payload version 3 requires it; that gate is the emitter's
// contract, pinned by the adapter's own tests.
// ValidateWorkerHostProvenance is the exported gate for CLI/adapter
// callers composing dispatch evidence.
func ValidateWorkerHostProvenance(p *WorkerHostProvenance) error {
	if p == nil {
		return nil
	}
	if !workerProvenancePattern.MatchString(p.Digest) || len(p.Sources) < 1 || len(p.Sources) > 32 {
		return invalidWorkerPayload("worker host provenance has invalid digest or source bound")
	}
	seen := map[string]bool{}
	for _, source := range p.Sources {
		if !workerProvenanceKinds[source.Kind] || len(source.Path) > 512 {
			return invalidWorkerPayload("worker host provenance source has an unknown kind or oversized path")
		}
		if source.SHA256 != "" && !workerProvenancePattern.MatchString(source.SHA256) {
			return invalidWorkerPayload("worker host provenance source hash is not a sha256 digest")
		}
		if source.Kind == "unenumerated" && (source.Path != "" || source.SHA256 != "") {
			return invalidWorkerPayload("unenumerated provenance sources carry no path or hash")
		}
		if source.Kind != "unenumerated" && source.SHA256 == "" {
			return invalidWorkerPayload("enumerated provenance sources must carry their content hash")
		}
		key := source.Kind + ":" + source.Path
		if seen[key] {
			return invalidWorkerPayload("worker host provenance names a source twice")
		}
		seen[key] = true
	}
	return nil
}

func upcastWorkerDispatchedV1(event Event) (Event, error) {
	var payload WorkerDispatchedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return Event{}, err
	}
	if _, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest); err != nil {
		return Event{}, err
	}
	// CD-0056: declared-side routing fields were dropped. v1 payloads that
	// carried them are folded as-is — the columns simply do not survive the
	// migration to v3 and the fold reads only the surviving identity.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "worker_event_upcast", "cannot encode the worker dispatch payload", false, "repair the worker event payload", err)
	}
	event.PayloadVersion = 2
	event.Payload = encoded
	return event, nil
}

func invalidWorkerPayload(detail string) error {
	return newFailure(KindInvalidPayload, "validate_worker_event", detail, false, "repair the worker event payload")
}

func validWorkerFailureKind(value string) bool {
	switch value {
	case WorkerFailureFallbackBlocked, WorkerFailureWorkerError, WorkerFailureInvalidReport, WorkerFailureModelIdentity:
		return true
	default:
		return false
	}
}

func foldWorkerDispatched(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerDispatchedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	lifecycleState := "dispatched"
	failureKind, failureDetail := "", ""
	readbackModel := payload.ReadbackModel
	if payload.Terminal == "failed" {
		// Terminal-at-birth dispatch (issue #106): the evidence row records
		// a prohibited outcome as durable evidence, never a usable attempt.
		lifecycleState = "failed"
		failureKind = payload.TerminalFailureKind
		failureDetail = payload.TerminalDetail
	}
	failedAt := any(nil)
	if lifecycleState == "failed" {
		failedAt = now
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,failed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, payload.AttemptID, payload.LaneID, payload.LaneVersion, payload.LaneDigest, payload.CapabilityClass, readbackModel, payload.PacketSchemaVersion, payload.ReportSchemaVersion, lifecycleState, failureKind, failureDetail, now, failedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "worker attempt is already dispatched", false, "use a new attempt identity")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create worker attempt projection", true, "retry once the database is writable", err)
	}
	return nil
}

func foldWorkerCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerCompletedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	var workID, lifecycle string
	if err := readWorkerTerminalAttempt(ctx, tx, event, payload.AttemptID, &workID, &lifecycle); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='completed', completed_at=? WHERE attempt_id=? AND work_id=? AND lifecycle_state='dispatched'`, payload.ReadbackModel, now, payload.AttemptID, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot complete worker attempt projection", true, "retry once the database is writable", err)
	}
	if err := verifyWorkerTerminalUpdate(result, "cannot verify worker completion projection", "record worker.dispatched before worker.completed"); err != nil {
		return err
	}
	return nil
}

func foldWorkerFailed(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerFailedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	var workID, lifecycle string
	if err := readWorkerTerminalAttempt(ctx, tx, event, payload.AttemptID, &workID, &lifecycle); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='failed', failure_kind=?, failure_detail=?, failed_at=? WHERE attempt_id=? AND work_id=? AND lifecycle_state='dispatched'`, payload.ReadbackModel, payload.FailureKind, payload.Detail, now, payload.AttemptID, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot fail worker attempt projection", true, "retry once the database is writable", err)
	}
	if err := verifyWorkerTerminalUpdate(result, "cannot verify worker failure projection", "record worker.dispatched before worker.failed"); err != nil {
		return err
	}
	return nil
}

func readWorkerTerminalAttempt(ctx context.Context, tx *sql.Tx, event Event, attemptID string, workID, lifecycle *string) error {
	if event.SubjectType != SubjectWorkItem {
		return newFailure(KindInvalidPayload, "fold_event", "worker terminal event must target a work item", false, "use subject_type=work_item")
	}
	if err := tx.QueryRowContext(ctx, `SELECT work_id,lifecycle_state FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(workID, lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "worker dispatch row does not exist", false, "record worker.dispatched before the terminal worker event")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot read worker attempt projection", true, "retry once the database is readable", err)
	}
	if *workID != event.SubjectID {
		return newFailure(KindInvalidOperation, "fold_event", "worker terminal event subject does not own the worker attempt", false, "use the attempt's owning work item as the event subject")
	}
	if *lifecycle != "dispatched" {
		return newFailure(KindProjectionConflict, "fold_event", "worker attempt is already terminal", false, "use a new attempt identity")
	}
	return nil
}

func verifyWorkerTerminalUpdate(result sql.Result, unavailableDetail, missingDetail string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", unavailableDetail, true, "retry once the worker attempt projection is readable", err)
	}
	if affected != 1 {
		return newFailure(KindProjectionConflict, "fold_event", "worker attempt terminal transition was not uniquely applied", false, missingDetail)
	}
	return nil
}

// WorkerAttemptByID returns the durable dispatch identity needed to validate a
// completion or failure callback. The lookup is read-only; worker projections
// remain fold-only and can only be changed by appending an event.
func (s *Store) WorkerAttemptByID(ctx context.Context, attemptID string) (WorkerAttempt, error) {
	if s == nil || s.db == nil {
		return WorkerAttempt{}, newFailure(KindUnavailable, "worker_attempt_read", "database is not open", true, "open the authority database")
	}
	return workerAttemptByID(ctx, s.db, attemptID)
}

// WorkerAttemptByIDTx is the transaction-scoped lookup. Authenticating a worker
// evidence write, checking that the attempt has not already reached a recorded
// outcome, and appending the evidence must observe one snapshot, so the caller
// reads the attempt through its own transaction rather than through the pooled
// connection.
func WorkerAttemptByIDTx(ctx context.Context, transaction *Transaction, attemptID string) (WorkerAttempt, error) {
	tx, err := transactionSQL(transaction, "worker_attempt_read")
	if err != nil {
		return WorkerAttempt{}, err
	}
	return workerAttemptByID(ctx, tx, attemptID)
}

// WorkerAttemptIsTerminal reports whether an attempt already reached a recorded
// outcome. A terminal attempt refuses further evidence, so a valid signature
// cannot overwrite a completion with a failure or a failure with a completion.
func WorkerAttemptIsTerminal(attempt WorkerAttempt) bool {
	return attempt.LifecycleState == "completed" || attempt.LifecycleState == "failed"
}

func workerAttemptByID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, attemptID string) (WorkerAttempt, error) {
	var attempt WorkerAttempt
	err := q.QueryRowContext(ctx, `SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failure_detail,''),dispatched_at,COALESCE(completed_at,''),COALESCE(failed_at,'') FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(
		&attempt.WorkID, &attempt.AttemptID, &attempt.LaneID, &attempt.LaneVersion, &attempt.LaneDigest, &attempt.CapabilityClass, &attempt.ReadbackModel, &attempt.PacketSchemaVersion, &attempt.ReportSchemaVersion, &attempt.LifecycleState, &attempt.FailureKind, &attempt.FailureDetail, &attempt.DispatchedAt, &attempt.CompletedAt, &attempt.FailedAt,
	)
	if err == sql.ErrNoRows {
		return WorkerAttempt{}, newFailure(KindProjectionNotFound, "worker_attempt_read", "worker dispatch row does not exist", false, "record worker.dispatched before the worker result")
	}
	if err != nil {
		return WorkerAttempt{}, wrapFailure(KindUnavailable, "worker_attempt_read", "cannot read worker attempt projection", true, "retry once the database is readable", err)
	}
	return attempt, nil
}

// upcastWorkerDispatchedV2 stamps legacy v2 dispatch evidence with an honest
// provenance marker: recorded before host prompt provenance was declared
// (CD-0034), so the injection surfaces are unknown by construction.
func upcastWorkerDispatchedV2(event Event) (Event, error) {
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return Event{}, invalidWorkerPayload("worker event payload does not match its closed schema")
	}
	if _, exists := payload["host_provenance"]; !exists {
		payload["host_provenance"] = map[string]any{
			"digest":  "sha256:" + strings.Repeat("0", 64),
			"sources": []map[string]any{{"kind": "unenumerated"}},
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	event.PayloadVersion = 3
	event.Payload = raw
	return event, nil
}
