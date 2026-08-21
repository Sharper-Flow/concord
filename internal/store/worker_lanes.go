package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

const (
	WorkerResolutionPreferred = "preferred"
	WorkerResolutionFallback  = "fallback"
)

// WorkerDispatchedPayload is the complete D5 dispatch identity. The event
// subject is the owning work item; AttemptID identifies the worker attempt.
type WorkerDispatchedPayload struct {
	AttemptID            string `json:"attempt_id"`
	LaneID               string `json:"lane_id"`
	LaneVersion          int64  `json:"lane_version"`
	LaneDigest           string `json:"lane_digest"`
	CapabilityClass      string `json:"capability_class"`
	RoutingPolicyVersion string `json:"routing_policy_version"`
	RoutingPolicyDigest  string `json:"routing_policy_digest"`
	ResolvedModel        string `json:"resolved_model"`
	ResolutionRole       string `json:"resolution_role"`
	FallbackReason       string `json:"fallback_reason"`
	PacketSchemaVersion  string `json:"packet_schema_version"`
	ReportSchemaVersion  string `json:"report_schema_version"`
	// HostProvenance is the declared record of unversioned host prompt
	// surfaces that shape the worker's behavior (issue #103 / CD-0034):
	// the adapter enumerates what it can bind — agent definition file,
	// AGENTS.md chain at spawn cwd, declared instruction files — and hashes
	// them. Injection is permitted only when recorded. Nil is legal only
	// for payloads older than v3.
	HostProvenance *WorkerHostProvenance `json:"host_provenance,omitempty"`
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
type WorkerAttempt struct {
	WorkID               string `json:"work_id"`
	AttemptID            string `json:"attempt_id"`
	LaneID               string `json:"lane_id"`
	LaneVersion          int64  `json:"lane_version"`
	LaneDigest           string `json:"lane_digest"`
	CapabilityClass      string `json:"capability_class"`
	RoutingPolicyVersion string `json:"routing_policy_version"`
	RoutingPolicyDigest  string `json:"routing_policy_digest"`
	ResolvedModel        string `json:"resolved_model"`
	ResolutionRole       string `json:"resolution_role"`
	FallbackReason       string `json:"fallback_reason"`
	ReadbackModel        string `json:"readback_model"`
	PacketSchemaVersion  string `json:"packet_schema_version"`
	ReportSchemaVersion  string `json:"report_schema_version"`
	LifecycleState       string `json:"lifecycle_state"`
	FailureKind          string `json:"failure_kind,omitempty"`
	FailureDetail        string `json:"failure_detail,omitempty"`
	DispatchedAt         string `json:"dispatched_at"`
	CompletedAt          string `json:"completed_at,omitempty"`
	FailedAt             string `json:"failed_at,omitempty"`
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
	modelShape := workerModelPattern.MatchString(payload.ResolvedModel)
	if payload.ResolutionRole == WorkerResolutionUndeclared && payload.ResolvedModel == "" {
		// Exhausted resolution chain: no model ran, so there is no model to
		// shape-check (issue #106).
		modelShape = true
	}
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" || payload.AttemptID == "" || !laneIDPattern.MatchString(payload.LaneID) || payload.LaneVersion < 1 || !laneDigestPattern.MatchString(payload.LaneDigest) || !workerVersionPattern.MatchString(payload.CapabilityClass) || !workerVersionPattern.MatchString(payload.RoutingPolicyVersion) || !laneDigestPattern.MatchString(payload.RoutingPolicyDigest) || !modelShape || payload.PacketSchemaVersion != WorkerPacketSchemaVersion || payload.ReportSchemaVersion != WorkerReportSchemaVersion {
		return invalidWorkerPayload("worker.dispatched payload has invalid identity")
	}
	lane, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest)
	if err != nil {
		return err
	}
	if payload.CapabilityClass != lane.CapabilityClass {
		return invalidWorkerPayload("worker.dispatched capability class does not match lane")
	}
	policy, err := LookupRoutingPolicy(payload.CapabilityClass, payload.RoutingPolicyVersion, payload.RoutingPolicyDigest)
	if err != nil {
		return err
	}
	if err := ValidateWorkerHostProvenance(payload.HostProvenance); err != nil {
		return err
	}
	return ValidateWorkerDispatchIdentity(lane, policy, payload.ResolvedModel, payload.ResolutionRole, payload.FallbackReason)
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
	lane, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest)
	if err != nil {
		return Event{}, err
	}
	policy, err := LookupRoutingPolicy(payload.CapabilityClass, payload.RoutingPolicyVersion, LoadedRoutingPolicyManifestDigest())
	if err != nil {
		return Event{}, err
	}
	if payload.CapabilityClass != lane.CapabilityClass || payload.ResolvedModel != policy.PreferredModel {
		return Event{}, newFailure(KindRoutingPolicyInvalid, "worker_event_upcast", "legacy worker dispatch is not a preferred routing-policy resolution", false, "repair the worker event evidence")
	}
	payload.RoutingPolicyDigest = LoadedRoutingPolicyManifestDigest()
	payload.ResolutionRole = WorkerResolutionPreferred
	payload.FallbackReason = ""
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
	if payload.Terminal == "failed" {
		// Terminal-at-birth dispatch (issue #106): the evidence row for an
		// undeclared executing model or an exhausted resolution chain. It
		// lands in the failed state immediately and can never bind a
		// completion.
		if payload.ResolutionRole != WorkerResolutionUndeclared || payload.TerminalFailureKind == "" {
			return newFailure(KindInvalidPayload, "fold_event", "terminal dispatch requires the undeclared resolution role and a typed failure kind", false, "record the prohibited outcome with its typed failure")
		}
		switch {
		case payload.ResolvedModel != "" && payload.TerminalFailureKind != string(KindModelIdentityMismatch):
			return newFailure(KindInvalidPayload, "fold_event", "an undeclared model dispatch must fail as model_identity_mismatch", false, "fail the attempt with the model identity mismatch kind")
		case payload.ResolvedModel == "" && payload.TerminalFailureKind == string(KindModelIdentityMismatch):
			return newFailure(KindInvalidPayload, "fold_event", "model identity mismatch requires the undeclared executing model to be recorded", false, "record the model that actually ran")
		}
		lifecycleState = "failed"
		failureKind = payload.TerminalFailureKind
		failureDetail = payload.TerminalDetail
	}
	failedAt := any(nil)
	if lifecycleState == "failed" {
		failedAt = now
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,failed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, payload.AttemptID, payload.LaneID, payload.LaneVersion, payload.LaneDigest, payload.CapabilityClass, payload.RoutingPolicyVersion, payload.RoutingPolicyDigest, payload.ResolvedModel, payload.ResolutionRole, payload.FallbackReason, payload.ResolvedModel, payload.PacketSchemaVersion, payload.ReportSchemaVersion, lifecycleState, failureKind, failureDetail, now, failedAt)
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
	var workID, lifecycle, resolvedModel string
	if err := readWorkerTerminalAttempt(ctx, tx, event, payload.AttemptID, &workID, &lifecycle, &resolvedModel); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if resolvedModel != payload.ReadbackModel {
		result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='failed', failure_kind=?, failure_detail=?, failed_at=? WHERE attempt_id=? AND work_id=? AND lifecycle_state='dispatched'`, payload.ReadbackModel, string(KindModelIdentityMismatch), "resolved model differs from host readback model", now, payload.AttemptID, workID)
		if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot record worker model identity mismatch", true, "retry once the database is writable", err)
		}
		if err := verifyWorkerTerminalUpdate(result, "cannot verify worker model identity mismatch", "record worker.dispatched before worker.completed"); err != nil {
			return err
		}
		return nil
	}
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
	var workID, lifecycle, resolvedModel string
	if err := readWorkerTerminalAttempt(ctx, tx, event, payload.AttemptID, &workID, &lifecycle, &resolvedModel); err != nil {
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

func readWorkerTerminalAttempt(ctx context.Context, tx *sql.Tx, event Event, attemptID string, workID, lifecycle, resolvedModel *string) error {
	if event.SubjectType != SubjectWorkItem {
		return newFailure(KindInvalidPayload, "fold_event", "worker terminal event must target a work item", false, "use subject_type=work_item")
	}
	if err := tx.QueryRowContext(ctx, `SELECT work_id,lifecycle_state,resolved_model FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(workID, lifecycle, resolvedModel); err != nil {
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

// ValidateWorkerCompletion provides the typed pre-fold mismatch check for
// callers that need to reject a result before constructing worker.completed.
func ValidateWorkerCompletion(resolvedModel, readbackModel string) error {
	if resolvedModel != readbackModel {
		return newFailure(KindModelIdentityMismatch, "worker_completion", fmt.Sprintf("resolved model %q differs from readback model %q", resolvedModel, readbackModel), false, "record a typed worker failure outcome")
	}
	return nil
}

// ValidateWorkerDispatchIdentity accepts only preferred or declared fallback
// resolutions. A fallback must carry a typed reason; the preferred path cannot
// carry fallback evidence.
// WorkerResolutionUndeclared records that the model which actually executed
// was outside the declared routing-policy resolution set. Such a dispatch is
// terminal at birth: evidence without usability (issue #106).
const WorkerResolutionUndeclared = "undeclared"

func ValidateWorkerDispatchIdentity(lane LaneDefinition, policy RoutingPolicyDefinition, resolvedModel, role, fallbackReason string) error {
	if role != WorkerResolutionUndeclared && resolvedModel == "" {
		return newFailure(KindLaneDefinitionInvalid, "worker_dispatch", "resolved model is empty", false, "use the registered lane pinned or fallback model")
	}
	if role == WorkerResolutionUndeclared {
		// The prohibited outcome itself (issue #106), in one of two shapes:
		// an executing model outside the declared set (recorded exactly as
		// read back), or an exhausted resolution chain where no model ran.
		// Both are recordable only as terminal evidence rows — the dispatch
		// carries its forced failure so no consumer can act on it.
		if resolvedModel != "" && containsModel(policy.ResolutionSet, resolvedModel) {
			return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "undeclared resolution must name a model outside the declared resolution set", false, "record the undeclared executing model exactly as read back")
		}
		return nil
	}
	if policy.ResolutionSet[0] != policy.PreferredModel || !containsModel(policy.ResolutionSet, resolvedModel) {
		return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "resolved model is not a declared routing-policy member", false, "use a model from the registered resolution set")
	}
	switch role {
	case WorkerResolutionPreferred:
		if resolvedModel != policy.PreferredModel || fallbackReason != "" {
			return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "preferred resolution must equal the policy preferred model and have no fallback reason", false, "record the preferred resolution identity")
		}
	case WorkerResolutionFallback:
		if resolvedModel == policy.PreferredModel || !validWorkerFallbackReason(fallbackReason) {
			return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "fallback resolution requires a declared non-preferred model and typed reason", false, "record a declared fallback and reason")
		}
	default:
		return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "resolution role is not recognized", false, "use preferred or fallback")
	}
	return nil
}

func containsModel(models []string, wanted string) bool {
	for _, model := range models {
		if model == wanted {
			return true
		}
	}
	return false
}

func validWorkerFallbackReason(value string) bool {
	switch value {
	case "rate_limit", "provider_unavailable", "budget_exhausted", "other":
		return true
	default:
		return false
	}
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
	err := q.QueryRowContext(ctx, `SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failure_detail,''),dispatched_at,COALESCE(completed_at,''),COALESCE(failed_at,'') FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(
		&attempt.WorkID, &attempt.AttemptID, &attempt.LaneID, &attempt.LaneVersion, &attempt.LaneDigest, &attempt.CapabilityClass, &attempt.RoutingPolicyVersion, &attempt.RoutingPolicyDigest, &attempt.ResolvedModel, &attempt.ResolutionRole, &attempt.FallbackReason, &attempt.ReadbackModel, &attempt.PacketSchemaVersion, &attempt.ReportSchemaVersion, &attempt.LifecycleState, &attempt.FailureKind, &attempt.FailureDetail, &attempt.DispatchedAt, &attempt.CompletedAt, &attempt.FailedAt,
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
