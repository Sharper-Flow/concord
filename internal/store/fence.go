package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StepKind is the closed set of durable workflow step authorities.
type StepKind string

const (
	StepInternalSQLite     StepKind = "internal_sqlite"
	StepCrossAuthority     StepKind = "cross_authority"
	StepExternalEffect     StepKind = "external_effect"
	StepKindInternalSQLite          = StepInternalSQLite
	StepKindCrossAuthority          = StepCrossAuthority
	StepKindExternalEffect          = StepExternalEffect
)

// ResultKind is the durable outcome of a claimed step.
type ResultKind string

const (
	ResultCompleted       ResultKind = "completed"
	ResultPending         ResultKind = "pending"
	ResultPartial         ResultKind = "partial"
	ResultFailed          ResultKind = "failed"
	ResultFailedStale     ResultKind = "failed_stale"
	ResultKindCompleted              = ResultCompleted
	ResultKindPending                = ResultPending
	ResultKindPartial                = ResultPartial
	ResultKindFailed                 = ResultFailed
	ResultKindFailedStale            = ResultFailedStale
)

// ClaimRequest identifies one logical claim and its immutable accepted scope.
// RequestID is transport-attempt identity; IdempotencyKey is logical retry
// identity and must be reused by callers retrying the same intent.
type ClaimRequest struct {
	OpID                  string    `json:"op_id"`
	WorkID                string    `json:"work_id"`
	WorkflowTypeRef       string    `json:"workflow_type_ref"`
	WorkflowTypeVersion   int       `json:"workflow_type_version"`
	StepID                string    `json:"step_id"`
	StepKind              StepKind  `json:"step_kind"`
	AcceptedInputsDigest  string    `json:"accepted_inputs_digest"`
	AcceptedScopeSnapshot string    `json:"accepted_scope_snapshot"`
	PrincipalRef          string    `json:"principal_ref"`
	Tool                  string    `json:"tool"`
	IdempotencyKey        string    `json:"idempotency_key"`
	RequestID             string    `json:"request_id"`
	ObservedAt            time.Time `json:"observed_at"`
	ApprovalRef           string    `json:"approval_ref,omitempty"`
	ContractVersion       string    `json:"contract_version"`
}

// CompleteRequest records the result of one attempt. ResultEventIDs are
// references to effects already committed by the owning authority; they are
// recorded for replay and are never emitted twice by this package.
type CompleteRequest struct {
	OpID           string     `json:"op_id"`
	AttemptEpoch   int64      `json:"attempt_epoch"`
	ResultKind     ResultKind `json:"result_kind"`
	ResultPayload  string     `json:"result_payload,omitempty"`
	EvidenceRefs   []string   `json:"evidence_refs,omitempty"`
	ChangedRefs    []string   `json:"changed_refs,omitempty"`
	ResumeCursor   string     `json:"resume_cursor,omitempty"`
	PrincipalRef   string     `json:"principal_ref"`
	Tool           string     `json:"tool"`
	IdempotencyKey string     `json:"idempotency_key"`
	RequestID      string     `json:"request_id"`
	ObservedAt     time.Time  `json:"observed_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ResultEventIDs []string   `json:"result_event_ids,omitempty"`
}

// FenceResult is the stable result of a claim, completion, or takeover.
type FenceResult struct {
	OpID                  string     `json:"op_id"`
	WorkID                string     `json:"work_id,omitempty"`
	AttemptEpoch          int64      `json:"attempt_epoch"`
	StepID                string     `json:"step_id,omitempty"`
	ResultKind            ResultKind `json:"result_kind,omitempty"`
	ResultPayload         string     `json:"result_payload,omitempty"`
	EvidenceRefs          []string   `json:"evidence_refs,omitempty"`
	ChangedRefs           []string   `json:"changed_refs,omitempty"`
	ResumeCursor          string     `json:"resume_cursor,omitempty"`
	AcceptedScopeSnapshot string     `json:"accepted_scope_snapshot,omitempty"`
	ResultEventIDs        []string   `json:"result_event_ids,omitempty"`
	Replayed              bool       `json:"replayed"`
	ApprovalRef           string     `json:"approval_ref,omitempty"`
	ContractVersion       string     `json:"contract_version,omitempty"`
}

// Step returns the newest durable attempt without changing its epoch. Reads
// are deliberately not leases, heartbeats, or liveness observations.
func Step(ctx context.Context, s *Store, opID string) (FenceResult, error) {
	if s == nil || s.db == nil {
		return FenceResult{}, newFailure(KindUnavailable, "step", "store is not open", false, "open a store before reading a step")
	}
	if opID == "" {
		return FenceResult{}, newFailure(KindInvalidOperation, "step", "operation ID is empty", false, "supply a durable operation ID")
	}
	if err := preflightWorkflowOperation(ctx, s, opID); err != nil {
		return FenceResult{}, err
	}
	return readStep(ctx, s.db, opID, false)
}

// ClaimStep starts the next fenced attempt. The connection's _txlock=immediate
// setting makes this transaction BEGIN IMMEDIATE across all callers.
func ClaimStep(ctx context.Context, s *Store, req ClaimRequest) (FenceResult, error) {
	return claimStepObserved(ctx, s, req, nil)
}

// ClaimStepAuthorized runs an authorization callback in the same transaction
// that creates the durable claim. It is used when approval consumption must be
// committed together with the durable operation identity.
func ClaimStepAuthorized(ctx context.Context, s *Store, req ClaimRequest, authorize func(*sql.Tx) error) (FenceResult, error) {
	return claimStepObservedAuthorized(ctx, s, req, nil, authorize)
}

func claimStepObserved(ctx context.Context, s *Store, req ClaimRequest, observer *operationObserver) (FenceResult, error) {
	return claimStepObservedAuthorized(ctx, s, req, observer, nil)
}

func claimStepObservedAuthorized(ctx context.Context, s *Store, req ClaimRequest, observer *operationObserver, authorize func(*sql.Tx) error) (FenceResult, error) {
	if req.ContractVersion == "" {
		req.ContractVersion = "1.0.0"
	}
	if err := validateClaim(req); err != nil {
		return FenceResult{}, err
	}
	if s == nil || s.db == nil {
		return FenceResult{}, newFailure(KindUnavailable, "claim_step", "store is not open", false, "open a store before claiming a step")
	}
	tx, err := beginObservedTx(ctx, s.db, observer)
	if err != nil {
		return FenceResult{}, wrapFailure(KindUnavailable, "claim_step", "cannot begin claim", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) (FenceResult, error) { _ = tx.Rollback(); return FenceResult{}, cause }
	if err := preflightWorkflowClaimTx(ctx, tx, req); err != nil {
		return rollback(err)
	}
	digest := claimDigest(req)
	if prior, found, err := findIdempotency(ctx, tx, req.PrincipalRef, req.Tool, "claim", req.IdempotencyKey); err != nil {
		return rollback(err)
	} else if found {
		if prior.digest != digest {
			return rollback(idempotencyConflict("claim", req.IdempotencyKey))
		}
		result, err := durableResult(ctx, tx, prior.opID, prior.resultEventIDs)
		if err != nil {
			return rollback(err)
		}
		if err := touchIdempotency(ctx, tx, prior, req.ObservedAt); err != nil {
			return rollback(err)
		}
		if err := commitObservedTx(tx, observer); err != nil {
			return FenceResult{}, wrapFailure(KindUnavailable, "claim_step", "cannot commit idempotent replay", true, "retry once the database is writable", err)
		}
		result.Replayed = true
		return result, nil
	}
	if authorize != nil {
		if err := authorize(tx); err != nil {
			return rollback(err)
		}
	}
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch), 0) + 1 FROM durable_operations WHERE op_id = ?`, req.OpID).Scan(&epoch); err != nil {
		return rollback(wrapFailure(KindUnavailable, "claim_step", "cannot allocate attempt epoch", true, "retry once the database is readable", err))
	}
	observed := req.ObservedAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_operations
		(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,
		 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_version)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, req.OpID, epoch, req.WorkID, req.WorkflowTypeRef,
		req.WorkflowTypeVersion, req.StepID, req.StepKind, req.AcceptedInputsDigest,
		req.AcceptedScopeSnapshot, req.PrincipalRef, req.RequestID, observed, req.ContractVersion); err != nil {
		return rollback(wrapFailure(KindUnavailable, "claim_step", "cannot persist claim", true, "retry once the database is writable", err))
	}
	if err := insertIdempotency(ctx, tx, req.PrincipalRef, req.Tool, "claim", req.IdempotencyKey, digest, req.OpID, nil, req.ObservedAt); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET authorized_scope_snapshot=? WHERE principal_ref=? AND tool=? AND operation_kind='claim' AND idempotency_key=?`, req.AcceptedScopeSnapshot, req.PrincipalRef, req.Tool, req.IdempotencyKey); err != nil {
		return rollback(err)
	}
	if err := commitObservedTx(tx, observer); err != nil {
		return FenceResult{}, wrapFailure(KindUnavailable, "claim_step", "cannot commit claim", true, "retry once the database is writable", err)
	}
	return FenceResult{OpID: req.OpID, AttemptEpoch: epoch, ApprovalRef: req.ApprovalRef}, nil
}

// CompleteStep authoritatively completes only the current attempt epoch.
func CompleteStep(ctx context.Context, s *Store, req CompleteRequest) (FenceResult, error) {
	return completeStepObserved(ctx, s, req, nil)
}

func completeStepObserved(ctx context.Context, s *Store, req CompleteRequest, observer *operationObserver) (FenceResult, error) {
	if err := validateComplete(req); err != nil {
		return FenceResult{}, err
	}
	if s == nil || s.db == nil {
		return FenceResult{}, newFailure(KindUnavailable, "complete_step", "store is not open", false, "open a store before completing a step")
	}
	tx, err := beginObservedTx(ctx, s.db, observer)
	if err != nil {
		return FenceResult{}, wrapFailure(KindUnavailable, "complete_step", "cannot begin completion", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) (FenceResult, error) { _ = tx.Rollback(); return FenceResult{}, cause }
	if err := preflightWorkflowOperationTx(ctx, tx, req.OpID); err != nil {
		return rollback(err)
	}
	digest := completeDigest(req)
	prior, found, err := findIdempotency(ctx, tx, req.PrincipalRef, req.Tool, "complete", req.IdempotencyKey)
	if err != nil {
		return rollback(err)
	}
	if found {
		if prior.digest != digest {
			return rollback(idempotencyConflict("complete", req.IdempotencyKey))
		}
		if hasStaleMarker(prior.resultEventIDs) {
			_ = tx.Rollback()
			return FenceResult{}, staleAttempt(req.OpID, req.AttemptEpoch)
		}
		result, err := durableResult(ctx, tx, prior.opID, prior.resultEventIDs)
		if err != nil {
			return rollback(err)
		}
		if err := touchIdempotency(ctx, tx, prior, req.ObservedAt); err != nil {
			return rollback(err)
		}
		if err := commitObservedTx(tx, observer); err != nil {
			return FenceResult{}, wrapFailure(KindUnavailable, "complete_step", "cannot commit idempotent replay", true, "retry once the database is writable", err)
		}
		result.Replayed = true
		return result, nil
	}
	current, err := readCurrentOperation(ctx, tx, req.OpID)
	if err != nil {
		return rollback(err)
	}
	if current.AttemptEpoch != req.AttemptEpoch {
		if err := insertIdempotency(ctx, tx, req.PrincipalRef, req.Tool, "complete", req.IdempotencyKey, digest, req.OpID, []string{staleMarker}, req.ObservedAt); err != nil {
			return rollback(err)
		}
		if err := commitObservedTx(tx, observer); err != nil {
			return FenceResult{}, wrapFailure(KindUnavailable, "complete_step", "cannot persist stale completion", true, "retry once the database is writable", err)
		}
		return FenceResult{}, staleAttempt(req.OpID, req.AttemptEpoch)
	}
	if current.ResultKind != "" {
		return rollback(newFailure(KindTakeoverRequired, "complete_step", "the current attempt already has a result", false, "reconcile the durable result or explicitly take over a new attempt"))
	}
	completed := ""
	if req.CompletedAt != nil {
		completed = req.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE durable_operations
        SET result_kind=?,result_payload=?,evidence_refs=?,changed_refs=?,resume_cursor=?,completed_at=?
        WHERE op_id=? AND attempt_epoch=?`, req.ResultKind, nullableText(req.ResultPayload), marshalStrings(req.EvidenceRefs),
		marshalStrings(req.ChangedRefs), nullableText(req.ResumeCursor), nullableText(completed), req.OpID, req.AttemptEpoch); err != nil {
		return rollback(wrapFailure(KindUnavailable, "complete_step", "cannot persist completion", true, "retry once the database is writable", err))
	}
	if err := insertIdempotency(ctx, tx, req.PrincipalRef, req.Tool, "complete", req.IdempotencyKey, digest, req.OpID, req.ResultEventIDs, req.ObservedAt); err != nil {
		return rollback(err)
	}
	if err := commitObservedTx(tx, observer); err != nil {
		return FenceResult{}, wrapFailure(KindUnavailable, "complete_step", "cannot commit completion", true, "retry once the database is writable", err)
	}
	return FenceResult{OpID: req.OpID, AttemptEpoch: req.AttemptEpoch, ResultKind: req.ResultKind, ResultPayload: req.ResultPayload,
		EvidenceRefs: append([]string(nil), req.EvidenceRefs...), ChangedRefs: append([]string(nil), req.ChangedRefs...), ResumeCursor: req.ResumeCursor,
		ResultEventIDs: append([]string(nil), req.ResultEventIDs...)}, nil
}

// AbortStep records a failed result through the same fenced completion path.
func AbortStep(ctx context.Context, s *Store, req CompleteRequest) (FenceResult, error) {
	req.ResultKind = ResultFailed
	return CompleteStep(ctx, s, req)
}

// OperatorTakeover is the only recovery path that advances an attempt after a
// prior claim. No expiry, heartbeat, hostname, or liveness inference exists.
func OperatorTakeover(ctx context.Context, s *Store, req ClaimRequest, approvalRef string) (FenceResult, error) {
	if strings.TrimSpace(approvalRef) == "" {
		return FenceResult{}, newFailure(KindApprovalRequired, "operator_takeover", "explicit approval reference is required", false, "supply a non-empty operator approval reference")
	}
	if strings.TrimSpace(req.PrincipalRef) == "" || strings.TrimSpace(req.RequestID) == "" {
		return FenceResult{}, newFailure(KindTakeoverRequired, "operator_takeover", "explicit principal and request ID are required", false, "supply both operator identity fields")
	}
	req.ApprovalRef = approvalRef
	var scope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(req.AcceptedScopeSnapshot), &scope); err != nil {
		return FenceResult{}, newFailure(KindInvalidOperation, "operator_takeover", "accepted_scope_snapshot is not valid JSON", false, "supply the original accepted scope snapshot")
	}
	approval, _ := json.Marshal(approvalRef)
	scope["approval_ref"] = approval
	encoded, _ := json.Marshal(scope)
	req.AcceptedScopeSnapshot = string(encoded)
	return ClaimStep(ctx, s, req)
}

type idempotencyRow struct {
	principal, tool, operationKind, key, digest, opID string
	resultEventIDs                                    []string
}

const staleMarker = "__stale_attempt__"

func validateClaim(req ClaimRequest) error {
	if req.ContractVersion != "1.0.0" && req.ContractVersion != "2.0.0" {
		return newFailure(KindSchemaUnsupported, "claim_step", "contract_version is not supported", false, "upgrade Concord before claiming this operation")
	}
	for name, value := range map[string]string{"op_id": req.OpID, "work_id": req.WorkID, "workflow_type_ref": req.WorkflowTypeRef, "step_id": req.StepID, "principal_ref": req.PrincipalRef, "tool": req.Tool, "idempotency_key": req.IdempotencyKey, "request_id": req.RequestID} {
		if strings.TrimSpace(value) == "" {
			return newFailure(KindInvalidOperation, "claim_step", name+" is empty", false, "supply all durable identity fields")
		}
	}
	if req.WorkflowTypeVersion <= 0 {
		return newFailure(KindInvalidOperation, "claim_step", "workflow_type_version must be positive", false, "supply a supported workflow version")
	}
	if !validStepKind(req.StepKind) {
		return newFailure(KindInvalidOperation, "claim_step", "step_kind is not recognized", false, "use an accepted D4 step kind")
	}
	if !validDigest(req.AcceptedInputsDigest) {
		return newFailure(KindInvalidOperation, "claim_step", "accepted_inputs_digest is not a SHA-256 digest", false, "supply sha256:<64 hex characters>")
	}
	if !isJSONObject([]byte(req.AcceptedScopeSnapshot)) {
		return newFailure(KindInvalidOperation, "claim_step", "accepted_scope_snapshot is not a JSON object", false, "supply the accepted scope snapshot as a JSON object")
	}
	if req.ObservedAt.IsZero() {
		return newFailure(KindInvalidOperation, "claim_step", "observed_at is zero", false, "supply an observation timestamp")
	}
	return nil
}

func validateComplete(req CompleteRequest) error {
	for name, value := range map[string]string{"op_id": req.OpID, "principal_ref": req.PrincipalRef, "tool": req.Tool, "idempotency_key": req.IdempotencyKey, "request_id": req.RequestID} {
		if strings.TrimSpace(value) == "" {
			return newFailure(KindInvalidOperation, "complete_step", name+" is empty", false, "supply all durable identity fields")
		}
	}
	if req.AttemptEpoch <= 0 {
		return newFailure(KindInvalidOperation, "complete_step", "attempt_epoch must be positive", false, "supply the claimed attempt epoch")
	}
	if !validResultKind(req.ResultKind) {
		return newFailure(KindInvalidOperation, "complete_step", "result_kind is not recognized", false, "use a closed D4 result kind")
	}
	if req.ResultPayload != "" && !isJSONObject([]byte(req.ResultPayload)) {
		return newFailure(KindInvalidOperation, "complete_step", "result_payload is not a JSON object", false, "supply a JSON result payload")
	}
	if req.ObservedAt.IsZero() {
		return newFailure(KindInvalidOperation, "complete_step", "observed_at is zero", false, "supply an observation timestamp")
	}
	if err := validateStringRefs(req.EvidenceRefs, "evidence_refs"); err != nil {
		return err
	}
	if err := validateStringRefs(req.ChangedRefs, "changed_refs"); err != nil {
		return err
	}
	if err := validateStringRefs(req.ResultEventIDs, "result_event_ids"); err != nil {
		return err
	}
	return nil
}

func validStepKind(kind StepKind) bool {
	return kind == StepInternalSQLite || kind == StepCrossAuthority || kind == StepExternalEffect
}
func validResultKind(kind ResultKind) bool {
	return kind == ResultCompleted || kind == ResultPending || kind == ResultPartial || kind == ResultFailed || kind == ResultFailedStale
}
func validDigest(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func validateStringRefs(refs []string, field string) error {
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			return newFailure(KindInvalidOperation, "fence", field+" contains an empty reference", false, "supply bounded non-empty references")
		}
	}
	return nil
}

func claimDigest(req ClaimRequest) string {
	return digestJSON(struct {
		OpID, WorkID, WorkflowTypeRef                                                                string
		WorkflowTypeVersion                                                                          int
		StepID                                                                                       string
		StepKind                                                                                     StepKind
		AcceptedInputsDigest, AcceptedScopeSnapshot, PrincipalRef, Tool, IdempotencyKey, ApprovalRef string
	}{req.OpID, req.WorkID, req.WorkflowTypeRef, req.WorkflowTypeVersion, req.StepID, req.StepKind, req.AcceptedInputsDigest, req.AcceptedScopeSnapshot, req.PrincipalRef, req.Tool, req.IdempotencyKey, req.ApprovalRef})
}
func completeDigest(req CompleteRequest) string {
	return digestJSON(struct {
		OpID                                             string
		AttemptEpoch                                     int64
		ResultKind                                       ResultKind
		ResultPayload                                    string
		EvidenceRefs, ChangedRefs                        []string
		ResumeCursor, PrincipalRef, Tool, IdempotencyKey string
		ResultEventIDs                                   []string
	}{req.OpID, req.AttemptEpoch, req.ResultKind, req.ResultPayload, req.EvidenceRefs, req.ChangedRefs, req.ResumeCursor, req.PrincipalRef, req.Tool, req.IdempotencyKey, req.ResultEventIDs})
}
func digestJSON(value any) string {
	bytes, _ := json.Marshal(value)
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func findIdempotency(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, principal, tool, operationKind, key string) (idempotencyRow, bool, error) {
	var row idempotencyRow
	var raw string
	err := q.QueryRowContext(ctx, `SELECT principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, principal, tool, operationKind, key).Scan(&row.principal, &row.tool, &row.operationKind, &row.key, &row.digest, &row.opID, &raw)
	if err == sql.ErrNoRows {
		return row, false, nil
	}
	if err != nil {
		return row, false, wrapFailure(KindUnavailable, "fence", "cannot read idempotency record", true, "retry once the database is readable", err)
	}
	if err := json.Unmarshal([]byte(raw), &row.resultEventIDs); err != nil {
		return row, false, wrapFailure(KindUnavailable, "fence", "idempotency result references are corrupt", false, "repair or restore the database", err)
	}
	return row, true, nil
}

func insertIdempotency(ctx context.Context, tx *sql.Tx, principal, tool, operationKind, key, digest, opID string, eventIDs []string, observed time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records (principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) VALUES (?,?,?,?,?,?,?,?,?)`, principal, tool, operationKind, key, digest, opID, marshalStrings(eventIDs), observed.UTC().Format(time.RFC3339Nano), observed.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrapFailure(KindUnavailable, "fence", "cannot persist idempotency record", true, "retry once the database is writable", err)
	}
	return nil
}
func touchIdempotency(ctx context.Context, tx *sql.Tx, row idempotencyRow, observed time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, observed.UTC().Format(time.RFC3339Nano), row.principal, row.tool, row.operationKind, row.key)
	if err != nil {
		return wrapFailure(KindUnavailable, "fence", "cannot update idempotency replay count", true, "retry once the database is writable", err)
	}
	return nil
}

func readCurrentOperation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string) (FenceResult, error) {
	return readStep(ctx, q, opID, true)
}
func readStep(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string, txRead bool) (FenceResult, error) {
	var result FenceResult
	var kind, payload, evidence, changed, cursor, scope, contractVersion sql.NullString
	err := q.QueryRowContext(ctx, `SELECT op_id,work_id,attempt_epoch,step_id,COALESCE(result_kind,''),COALESCE(result_payload,''),evidence_refs,changed_refs,COALESCE(resume_cursor,''),accepted_scope_snapshot,contract_version FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&result.OpID, &result.WorkID, &result.AttemptEpoch, &result.StepID, &kind, &payload, &evidence, &changed, &cursor, &scope, &contractVersion)
	if err == sql.ErrNoRows {
		return result, newFailure(KindProjectionNotFound, "step", "operation does not exist", false, "claim the operation before reading it")
	}
	if err != nil {
		return result, wrapFailure(KindUnavailable, "step", "cannot read durable operation", true, "retry once the database is readable", err)
	}
	result.ContractVersion = contractVersion.String
	if result.ContractVersion == "" {
		result.ContractVersion = "1.0.0"
	}
	if result.ContractVersion != "1.0.0" && result.ContractVersion != "2.0.0" {
		return result, newFailure(KindSchemaUnsupported, "step", "durable operation uses an unsupported contract version", false, "upgrade Concord before replaying this operation")
	}
	result.ResultKind, result.ResultPayload, result.ResumeCursor = ResultKind(kind.String), payload.String, cursor.String
	if result.ResultKind != "" && !validResultKind(result.ResultKind) {
		return result, newFailure(KindSchemaUnsupported, "step", "durable operation uses an unsupported result classification", false, "upgrade Concord before replaying this operation")
	}
	result.AcceptedScopeSnapshot = scope.String
	if err := json.Unmarshal([]byte(evidence.String), &result.EvidenceRefs); err != nil {
		return result, wrapFailure(KindUnavailable, "step", "durable evidence references are corrupt", false, "repair or restore the database", err)
	}
	if err := json.Unmarshal([]byte(changed.String), &result.ChangedRefs); err != nil {
		return result, wrapFailure(KindUnavailable, "step", "durable changed references are corrupt", false, "repair or restore the database", err)
	}
	var acceptedScope map[string]json.RawMessage
	if json.Unmarshal([]byte(scope.String), &acceptedScope) == nil {
		var approval string
		if raw := acceptedScope["approval_ref"]; len(raw) > 0 && json.Unmarshal(raw, &approval) == nil {
			result.ApprovalRef = approval
		}
	}
	_ = txRead
	return result, nil
}

func durableResult(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string, eventIDs []string) (FenceResult, error) {
	result, err := readStep(ctx, q, opID, true)
	if err != nil {
		return result, err
	}
	result.ResultEventIDs = append([]string(nil), eventIDs...)
	return result, nil
}
func idempotencyConflict(operationKind, key string) *Failure {
	return newFailure(KindIdempotencyConflict, operationKind, fmt.Sprintf("idempotency key %q was reused with a different canonical request", key), false, "reuse the original canonical request or choose a new key")
}

// IdempotencyConflict exposes the stable typed failure to higher-level runtime
// boundaries that perform their own transaction orchestration.
func IdempotencyConflict(operationKind, key string) error {
	return idempotencyConflict(operationKind, key)
}
func staleAttempt(opID string, epoch int64) *Failure {
	return newFailure(KindStaleAttempt, "complete_step", fmt.Sprintf("attempt epoch %d for %s is not current", epoch, opID), false, "reconcile the current attempt or obtain an explicit operator takeover")
}
func hasStaleMarker(ids []string) bool {
	for _, id := range ids {
		if id == staleMarker {
			return true
		}
	}
	return false
}
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
