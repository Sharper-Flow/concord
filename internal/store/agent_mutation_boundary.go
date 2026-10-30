package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// MutationIdempotencyKey identifies one logical mutation request.
type MutationIdempotencyKey struct {
	PrincipalRef   string
	Tool           string
	OperationKind  string
	IdempotencyKey string
}

// MutationIdempotencyRecord contains only the bounded replay fields needed by
// mutation adapters.
type MutationIdempotencyRecord struct {
	CanonicalDigest         string
	OperationID             string
	ResultPayload           string
	ResultEventIDs          string
	ChangedRefs             string
	AuthorizedScopeSnapshot string
}

// MutationIdempotencyInsert contains the durable result written with a
// mutation's effects.
type MutationIdempotencyInsert struct {
	Key                     MutationIdempotencyKey
	CanonicalDigest         string
	OperationID             string
	ResultEventIDs          string
	ResultPayload           string
	ChangedRefs             string
	AuthorizedScopeSnapshot string
	ObservedAt              time.Time
}

// WorkflowInstanceDefinition carries the instance's stored definition metadata.
type WorkflowInstanceDefinition struct {
	DefinitionRef    string
	DefinitionDigest string
}

// DurableOperationRecord identifies the newest pending attempt for a work item.
type DurableOperationRecord struct {
	OperationID string
}

func (s *Store) LookupMutationIdempotency(ctx context.Context, key MutationIdempotencyKey) (MutationIdempotencyRecord, bool, error) {
	if s == nil || s.db == nil {
		return MutationIdempotencyRecord{}, false, newFailure(KindUnavailable, "mutation_idempotency", "store is not open", false, "open the authority database")
	}
	return lookupMutationIdempotency(ctx, s.db, key)
}

func LookupMutationIdempotencyTx(ctx context.Context, transaction *Transaction, key MutationIdempotencyKey) (MutationIdempotencyRecord, bool, error) {
	tx, err := transactionSQL(transaction, "mutation_idempotency")
	if err != nil {
		return MutationIdempotencyRecord{}, false, err
	}
	return lookupMutationIdempotency(ctx, tx, key)
}

func lookupMutationIdempotency(ctx context.Context, q queryer, key MutationIdempotencyKey) (MutationIdempotencyRecord, bool, error) {
	var record MutationIdempotencyRecord
	err := q.QueryRowContext(ctx, `SELECT canonical_digest,op_id,COALESCE(result_payload,''),result_event_ids,changed_refs,authorized_scope_snapshot FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey).
		Scan(&record.CanonicalDigest, &record.OperationID, &record.ResultPayload, &record.ResultEventIDs, &record.ChangedRefs, &record.AuthorizedScopeSnapshot)
	if err == sql.ErrNoRows {
		return MutationIdempotencyRecord{}, false, nil
	}
	if err != nil {
		return MutationIdempotencyRecord{}, false, wrapFailure(KindUnavailable, "mutation_idempotency", "cannot read idempotency record", true, "retry once the database is readable", err)
	}
	return record, true, nil
}

func TouchMutationIdempotencyTx(ctx context.Context, transaction *Transaction, key MutationIdempotencyKey, observed time.Time) error {
	tx, err := transactionSQL(transaction, "mutation_idempotency")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, observed.UTC().Format(time.RFC3339Nano), key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey)
	if err != nil {
		return wrapFailure(KindUnavailable, "mutation_idempotency", "cannot update idempotency replay count", true, "retry once the database is writable", err)
	}
	return nil
}

func InsertMutationIdempotencyTx(ctx context.Context, transaction *Transaction, input MutationIdempotencyInsert) error {
	tx, err := transactionSQL(transaction, "mutation_idempotency")
	if err != nil {
		return err
	}
	if err := ValidateMutationBoundaryRecords(input); err != nil {
		return err
	}
	observed := input.ObservedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at,last_observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, input.Key.PrincipalRef, input.Key.Tool, input.Key.OperationKind, input.Key.IdempotencyKey, input.CanonicalDigest, input.OperationID, input.ResultEventIDs, input.ResultPayload, input.ChangedRefs, input.AuthorizedScopeSnapshot, observed, observed)
	if err != nil {
		return wrapFailure(KindUnavailable, "mutation_idempotency", "cannot persist idempotency record", true, "retry once the database is writable", err)
	}
	return nil
}

// CompleteMutationResponseTx fills the workflow reservation in the same
// transaction as its result. A completed response cannot be overwritten.
func CompleteMutationResponseTx(ctx context.Context, transaction *Transaction, input MutationIdempotencyInsert) error {
	tx, err := transactionSQL(transaction, "mutation_idempotency")
	if err != nil {
		return err
	}
	if err := ValidateMutationBoundaryRecords(input); err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input.ResultPayload), &payload); err != nil || len(payload) == 0 {
		return newFailure(KindInvalidInput, "mutation_idempotency", "completed mutation response must be a nonempty result object", false, "supply the committed mutation result")
	}
	result, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET result_payload=?,changed_refs=?,authorized_scope_snapshot=?,result_event_ids=?,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=? AND canonical_digest=? AND op_id=? AND result_payload='{}' AND changed_refs='[]'`, input.ResultPayload, input.ChangedRefs, input.AuthorizedScopeSnapshot, input.ResultEventIDs, input.ObservedAt.UTC().Format(time.RFC3339Nano), input.Key.PrincipalRef, input.Key.Tool, input.Key.OperationKind, input.Key.IdempotencyKey, input.CanonicalDigest, input.OperationID)
	if err != nil {
		return wrapFailure(KindUnavailable, "mutation_idempotency", "cannot persist committed mutation response", true, "reconcile the retained operation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "mutation_idempotency", "cannot confirm committed mutation response", false, "reconcile the retained operation", err)
	}
	if count != 1 {
		return newFailure(KindOperationConflict, "mutation_idempotency", "mutation response reservation is missing, changed, or already complete", false, "reconcile the retained operation")
	}
	return nil
}

func (s *Store) AcceptedInputsDigest(ctx context.Context, operationID string) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "accepted_inputs", "store is not open", false, "open the authority database")
	}
	return acceptedInputsDigest(ctx, s.db, operationID)
}

func acceptedInputsDigest(ctx context.Context, q queryer, operationID string) (string, error) {
	var digest string
	if err := q.QueryRowContext(ctx, `SELECT accepted_inputs_digest FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, operationID).Scan(&digest); err != nil {
		if err == sql.ErrNoRows {
			return "", newFailure(KindProjectionNotFound, "accepted_inputs", "durable operation does not exist", false, "reread the operation")
		}
		return "", wrapFailure(KindUnavailable, "accepted_inputs", "cannot read accepted input digest", true, "retry once the database is readable", err)
	}
	return digest, nil
}

func (s *Store) WorkflowInstanceDefinition(ctx context.Context, workID string) (WorkflowInstanceDefinition, bool, error) {
	if s == nil || s.db == nil {
		return WorkflowInstanceDefinition{}, false, newFailure(KindUnavailable, "workflow_instance", "store is not open", false, "open the authority database")
	}
	return workflowInstanceDefinition(ctx, s.db, workID)
}

func WorkflowInstanceDefinitionTx(ctx context.Context, transaction *Transaction, workID string) (WorkflowInstanceDefinition, bool, error) {
	tx, err := transactionSQL(transaction, "workflow_instance")
	if err != nil {
		return WorkflowInstanceDefinition{}, false, err
	}
	return workflowInstanceDefinition(ctx, tx, workID)
}

func workflowInstanceDefinition(ctx context.Context, q queryer, workID string) (WorkflowInstanceDefinition, bool, error) {
	var definition WorkflowInstanceDefinition
	err := q.QueryRowContext(ctx, `SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition.DefinitionRef, &definition.DefinitionDigest)
	if err == sql.ErrNoRows {
		return WorkflowInstanceDefinition{}, false, nil
	}
	if err != nil {
		return WorkflowInstanceDefinition{}, false, wrapFailure(KindUnavailable, "workflow_instance", "cannot read workflow definition", true, "retry once the database is readable", err)
	}
	return definition, true, nil
}

func (s *Store) TerminalWorkVersion(ctx context.Context, workID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "terminal_work", "store is not open", false, "open the authority database")
	}
	return terminalWorkVersion(ctx, s.db, workID)
}

func terminalWorkVersion(ctx context.Context, q queryer, workID string) (int64, error) {
	if err := publishedWorkAbsenceQ(ctx, q, workID, "terminal_work"); err != nil {
		return 0, err
	}
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=? AND lifecycle IN ('completed','cancelled','superseded')`, workID).Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return 0, newFailure(KindProjectionNotFound, "terminal_work", "terminal work item does not exist", false, "reread the work item")
		}
		return 0, wrapFailure(KindUnavailable, "terminal_work", "cannot read terminal work version", true, "retry once the database is readable", err)
	}
	return version, nil
}

func (s *Store) PendingOperationForWork(ctx context.Context, workID string) (DurableOperationRecord, error) {
	if s == nil || s.db == nil {
		return DurableOperationRecord{}, newFailure(KindUnavailable, "pending_operation", "store is not open", false, "open the authority database")
	}
	return pendingOperationForWork(ctx, s.db, workID)
}

func pendingOperationForWork(ctx context.Context, q queryer, workID string) (DurableOperationRecord, error) {
	var operation DurableOperationRecord
	err := q.QueryRowContext(ctx, `SELECT op_id FROM durable_operations WHERE work_id=? AND (result_kind IS NULL OR result_kind IN ('pending','partial')) ORDER BY attempt_epoch DESC LIMIT 1`, workID).Scan(&operation.OperationID)
	if err == sql.ErrNoRows {
		return DurableOperationRecord{}, newFailure(KindProjectionNotFound, "pending_operation", "pending durable operation does not exist", false, "reread the work item")
	}
	if err != nil {
		return DurableOperationRecord{}, wrapFailure(KindUnavailable, "pending_operation", "cannot read pending durable operation", true, "retry once the database is readable", err)
	}
	return operation, nil
}

func WorkLifecycleTx(ctx context.Context, transaction *Transaction, workID string) (string, error) {
	tx, err := transactionSQL(transaction, "work_lifecycle")
	if err != nil {
		return "", err
	}
	return workLifecycleCore(ctx, tx, workID)
}

// WorkLifecycle reads one work item's lifecycle state on the store's own
// connection, for callers that gate a request before opening a write transaction.
func (s *Store) WorkLifecycle(ctx context.Context, workID string) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "work_lifecycle", "store is not open", false, "open the authority database")
	}
	return workLifecycleCore(ctx, s.db, workID)
}

func workLifecycleCore(ctx context.Context, q queryer, workID string) (string, error) {
	var lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return "", newFailure(KindProjectionNotFound, "work_lifecycle", "work item does not exist", false, "reread the work item")
		}
		return "", wrapFailure(KindUnavailable, "work_lifecycle", "cannot read work lifecycle", true, "retry once the database is readable", err)
	}
	return lifecycle, nil
}

func ProductsForProjectIDsTx(ctx context.Context, transaction *Transaction, ids []string) (map[string][]string, error) {
	tx, err := transactionSQL(transaction, "scope")
	if err != nil {
		return nil, err
	}
	return productsByIDsTx(ctx, tx, ids, `SELECT project_id,product_id FROM product_projects WHERE project_id IN (`)
}

func ProductsForWorkIDsTx(ctx context.Context, transaction *Transaction, ids []string) (map[string][]string, error) {
	tx, err := transactionSQL(transaction, "scope")
	if err != nil {
		return nil, err
	}
	return productsForWorkIDs(ctx, tx, ids)
}

func productsByIDsTx(ctx context.Context, tx *sql.Tx, ids []string, queryPrefix string) (map[string][]string, error) {
	out := make(map[string][]string)
	if tx == nil {
		return nil, newFailure(KindUnavailable, "scope", "transaction is not open", false, "open the authority database")
	}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := tx.QueryContext(ctx, queryPrefix+strings.Join(placeholders, ",")+") ORDER BY 1,2", args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "scope", "cannot resolve mutation Product scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var owner, product string
		if err := rows.Scan(&owner, &product); err != nil {
			return nil, wrapFailure(KindUnavailable, "scope", "cannot scan mutation Product scope", true, "retry once the database is readable", err)
		}
		out[owner] = append(out[owner], product)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "scope", "cannot scan mutation Product scope", true, "retry once the database is readable", err)
	}
	return out, nil
}

func ActiveWorkIDsTx(ctx context.Context, transaction *Transaction, productID string) ([]string, error) {
	tx, err := transactionSQL(transaction, "active_work")
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT w.id FROM work_items w WHERE w.lifecycle='in_progress' AND w.`+actionableWorkKindSQL+` AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=?) ORDER BY w.id LIMIT 100`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "active_work", "cannot read bounded in-progress work", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapFailure(KindUnavailable, "active_work", "cannot scan bounded in-progress work", true, "retry once the database is readable", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "active_work", "cannot scan bounded in-progress work", true, "retry once the database is readable", err)
	}
	return out, nil
}

// AgentMutationDigest is the one canonical mutation-digest implementation the
// agent adapter's request digests and the store's live destructive-overlap
// effect binding share: {Tool,Operation,Product,Project,Input} over the raw
// input's decoded JSON, marshaled with sorted map keys, SHA-256 hex-encoded.
func AgentMutationDigest(tool, operation, selectedProduct, ambientProject string, raw []byte) string {
	var input any
	_ = json.Unmarshal(raw, &input)
	if object, ok := input.(map[string]any); ok {
		delete(object, "idempotency_key")
	}
	canonical, _ := json.Marshal(struct {
		Tool, Operation, Product, Project string
		Input                             any
	}{tool, operation, selectedProduct, ambientProject, input})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidateMutationBoundaryRecords keeps JSON-bearing fields bounded at the
// store edge while allowing adapters to preserve their existing wire values.
func ValidateMutationBoundaryRecords(input MutationIdempotencyInsert) error {
	for _, value := range []string{input.ResultEventIDs, input.ResultPayload, input.ChangedRefs, input.AuthorizedScopeSnapshot} {
		if len(value) > 1<<20 {
			return newFailure(KindLimitExceeded, "mutation_idempotency", "idempotency result field exceeds the bounded maximum", false, "reduce the mutation result")
		}
	}
	return nil
}
