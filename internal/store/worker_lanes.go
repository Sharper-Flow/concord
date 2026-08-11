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

// WorkerDispatchedPayload is the complete D5 dispatch identity. The event
// subject is the owning work item; AttemptID identifies the worker attempt.
type WorkerDispatchedPayload struct {
	AttemptID            string `json:"attempt_id"`
	LaneID               string `json:"lane_id"`
	LaneVersion          int64  `json:"lane_version"`
	LaneDigest           string `json:"lane_digest"`
	CapabilityClass      string `json:"capability_class"`
	RoutingPolicyVersion string `json:"routing_policy_version"`
	ResolvedModel        string `json:"resolved_model"`
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
	ResolvedModel        string `json:"resolved_model"`
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
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" || payload.AttemptID == "" || !laneIDPattern.MatchString(payload.LaneID) || payload.LaneVersion < 1 || !laneDigestPattern.MatchString(payload.LaneDigest) || !workerVersionPattern.MatchString(payload.CapabilityClass) || !workerVersionPattern.MatchString(payload.RoutingPolicyVersion) || !workerModelPattern.MatchString(payload.ResolvedModel) || payload.PacketSchemaVersion != WorkerPacketSchemaVersion || payload.ReportSchemaVersion != WorkerReportSchemaVersion {
		return invalidWorkerPayload("worker.dispatched payload has invalid identity")
	}
	lane, err := LookupLane(payload.LaneID, payload.LaneVersion, payload.LaneDigest)
	if err != nil {
		return err
	}
	if payload.CapabilityClass != lane.CapabilityClass {
		return invalidWorkerPayload("worker.dispatched capability class does not match lane")
	}
	return nil
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
		(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,resolved_model,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, payload.AttemptID, payload.LaneID, payload.LaneVersion, payload.LaneDigest, payload.CapabilityClass, payload.RoutingPolicyVersion, payload.ResolvedModel, "", payload.PacketSchemaVersion, payload.ReportSchemaVersion, "dispatched", "", "", now)
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
	var resolvedModel string
	if err := tx.QueryRowContext(ctx, `SELECT resolved_model FROM worker_attempts WHERE attempt_id=?`, payload.AttemptID).Scan(&resolvedModel); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "worker dispatch row does not exist", false, "record worker.dispatched before worker.completed")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot read worker dispatch projection", true, "retry once the database is readable", err)
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if resolvedModel != payload.ReadbackModel {
		_, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='failed', failure_kind=?, failure_detail=?, failed_at=? WHERE attempt_id=?`, payload.ReadbackModel, string(KindModelIdentityMismatch), "resolved model differs from host readback model", now, payload.AttemptID)
		if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot record worker model identity mismatch", true, "retry once the database is writable", err)
		}
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='completed', completed_at=? WHERE attempt_id=?`, payload.ReadbackModel, now, payload.AttemptID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot complete worker attempt projection", true, "retry once the database is writable", err)
	}
	return nil
}

func foldWorkerFailed(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerFailedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	result, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET readback_model=?, lifecycle_state='failed', failure_kind=?, failure_detail=?, failed_at=? WHERE attempt_id=?`, payload.ReadbackModel, payload.FailureKind, payload.Detail, now, payload.AttemptID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot fail worker attempt projection", true, "retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify worker attempt projection", true, "retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "worker dispatch row does not exist", false, "record worker.dispatched before worker.failed")
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
