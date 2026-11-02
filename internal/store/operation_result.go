package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// OperationResult is the committed result of one atomic workflow mutation.
type OperationResult struct {
	OpID          string   `json:"op_id"`
	WorkID        string   `json:"work_id"`
	ResultKind    string   `json:"result_kind"`
	ResultPayload string   `json:"result_payload,omitempty"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
	ChangedRefs   []string `json:"changed_refs,omitempty"`
}

// OperationResultTx reads a committed result in the caller's transaction.
func OperationResultTx(ctx context.Context, transaction *Transaction, opID string) (OperationResult, error) {
	tx, err := transactionSQL(transaction, "operation_result")
	if err != nil {
		return OperationResult{}, err
	}
	if opID == "" {
		return OperationResult{}, newFailure(KindInvalidOperation, "operation_result", "operation ID is empty", false, "supply an atomic operation ID")
	}
	return readOperationResult(ctx, tx, opID)
}

func readOperationResult(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opID string) (OperationResult, error) {
	var result OperationResult
	var evidence, changed string
	err := q.QueryRowContext(ctx, `SELECT op_id,work_id,result_kind,COALESCE(result_payload,''),evidence_refs,changed_refs FROM durable_operations WHERE op_id=?`, opID).Scan(&result.OpID, &result.WorkID, &result.ResultKind, &result.ResultPayload, &evidence, &changed)
	if err == sql.ErrNoRows {
		return result, newFailure(KindProjectionNotFound, "operation_result", "operation does not exist", false, "read a committed atomic operation")
	}
	if err != nil {
		return result, wrapFailure(KindUnavailable, "operation_result", "cannot read atomic operation", true, "retry once the database is readable", err)
	}
	if result.ResultKind != "completed" {
		return result, newFailure(KindSchemaUnsupported, "operation_result", "operation has no supported atomic result", false, "upgrade Concord before replaying this operation")
	}
	if err := json.Unmarshal([]byte(evidence), &result.EvidenceRefs); err != nil {
		return result, wrapFailure(KindUnavailable, "operation_result", "durable evidence references are corrupt", false, "repair or restore the database", err)
	}
	if err := json.Unmarshal([]byte(changed), &result.ChangedRefs); err != nil {
		return result, wrapFailure(KindUnavailable, "operation_result", "durable changed references are corrupt", false, "repair or restore the database", err)
	}
	return result, nil
}

func validDigest(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func idempotencyConflict(operationKind, key string) *Failure {
	return newFailure(KindIdempotencyConflict, operationKind, fmt.Sprintf("idempotency key %q was reused with a different canonical request", key), false, "reuse the original canonical request or choose a new key")
}

// IdempotencyConflict reports a key reused for different canonical inputs.
func IdempotencyConflict(operationKind, key string) error {
	return idempotencyConflict(operationKind, key)
}
