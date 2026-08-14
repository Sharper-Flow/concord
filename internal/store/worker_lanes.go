package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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

func validateWorkerPayloadShape(event Event) error {
	switch event.Kind {
	case WorkerDispatched:
		var payload WorkerDispatchedPayload
		if err := decodeClosedWorkerPayload(event, &payload); err != nil {
			return err
		}
		return validateWorkerDispatched(event, payload)
	case WorkerCompleted:
		var payload WorkerCompletedPayload
		if err := decodeClosedWorkerPayload(event, &payload); err != nil {
			return err
		}
		if payload.AttemptID == "" || !workerModelPattern.MatchString(payload.ReadbackModel) || payload.ReportSchemaVersion != WorkerReportSchemaVersion {
			return invalidWorkerPayload("worker.completed payload has invalid identity or report schema")
		}
	case WorkerFailed:
		var payload WorkerFailedPayload
		if err := decodeClosedWorkerPayload(event, &payload); err != nil {
			return err
		}
		if payload.AttemptID == "" || !workerModelPattern.MatchString(payload.ReadbackModel) || !validWorkerFailureKind(payload.FailureKind) || len(payload.Detail) < 1 || len(payload.Detail) > 4096 {
			return invalidWorkerPayload("worker.failed payload has invalid identity or failure")
		}
	default:
		return newFailure(KindUnknownEventKind, "validate_worker_event", "unknown worker event kind", false, "use a registered worker event kind")
	}
	return nil
}

func decodeClosedWorkerPayload(event Event, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalidWorkerPayload("worker event payload does not match its closed schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return invalidWorkerPayload("worker event payload contains trailing data")
	}
	return nil
}

func validateWorkerDispatched(event Event, payload WorkerDispatchedPayload) error {
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" || payload.AttemptID == "" || !laneIDPattern.MatchString(payload.LaneID) || payload.LaneVersion < 1 || !laneDigestPattern.MatchString(payload.LaneDigest) || !workerVersionPattern.MatchString(payload.CapabilityClass) || !workerVersionPattern.MatchString(payload.RoutingPolicyVersion) || !laneDigestPattern.MatchString(payload.RoutingPolicyDigest) || !workerModelPattern.MatchString(payload.ResolvedModel) || payload.PacketSchemaVersion != WorkerPacketSchemaVersion || payload.ReportSchemaVersion != WorkerReportSchemaVersion {
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
	return ValidateWorkerDispatchIdentity(lane, policy, payload.ResolvedModel, payload.ResolutionRole, payload.FallbackReason)
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
	policy, err := LookupRoutingPolicy(payload.CapabilityClass, payload.RoutingPolicyVersion, RoutingPolicyManifestDigest)
	if err != nil {
		return Event{}, err
	}
	if payload.CapabilityClass != lane.CapabilityClass || payload.ResolvedModel != policy.PreferredModel {
		return Event{}, newFailure(KindRoutingPolicyInvalid, "worker_event_upcast", "legacy worker dispatch is not a preferred routing-policy resolution", false, "repair the worker event evidence")
	}
	payload.RoutingPolicyDigest = RoutingPolicyManifestDigest
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
	_, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, payload.AttemptID, payload.LaneID, payload.LaneVersion, payload.LaneDigest, payload.CapabilityClass, payload.RoutingPolicyVersion, payload.RoutingPolicyDigest, payload.ResolvedModel, payload.ResolutionRole, payload.FallbackReason, "", payload.PacketSchemaVersion, payload.ReportSchemaVersion, "dispatched", "", "", now)
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
func ValidateWorkerDispatchIdentity(lane LaneDefinition, policy RoutingPolicyDefinition, resolvedModel, role, fallbackReason string) error {
	if resolvedModel == "" {
		return newFailure(KindLaneDefinitionInvalid, "worker_dispatch", "resolved model is empty", false, "use the registered lane pinned or fallback model")
	}
	if policy.PreferredModel != lane.PinnedModel || !containsModel(policy.ResolutionSet, resolvedModel) {
		return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "resolved model is not a declared routing-policy member", false, "use a model from the registered resolution set")
	}
	switch role {
	case WorkerResolutionPreferred:
		if resolvedModel != policy.PreferredModel || fallbackReason != "" {
			return newFailure(KindRoutingPolicyInvalid, "worker_dispatch", "preferred resolution must equal the lane pinned model and have no fallback reason", false, "record the preferred resolution identity")
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
	var attempt WorkerAttempt
	err := s.db.QueryRowContext(ctx, `SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failure_detail,''),dispatched_at,COALESCE(completed_at,''),COALESCE(failed_at,'') FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(
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
