package store

import (
	"context"
	"database/sql"
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

// MutationResultUpdate contains the durable result produced by a workflow
// mutation.
type MutationResultUpdate struct {
	Key            MutationIdempotencyKey
	ResultEventIDs string
	ResultPayload  string
	ChangedRefs    string
	ObservedAt     time.Time
}

// WorkflowContractSnapshot is the active contract data needed by mutation
// preflight and approval responses.
type WorkflowContractSnapshot struct {
	Version int64
	Premise string
}

// WorkflowInstanceDefinition is the immutable workflow definition reference.
type WorkflowInstanceDefinition struct {
	DefinitionRef string
}

// RelationRecord is the relation identity needed by unlink effects.
type RelationRecord struct {
	FromWorkID string
	ToWorkID   string
	Kind       string
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
	err := q.QueryRowContext(ctx, `SELECT canonical_digest,op_id,COALESCE(result_payload,''),changed_refs,authorized_scope_snapshot FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, key.PrincipalRef, key.Tool, key.OperationKind, key.IdempotencyKey).
		Scan(&record.CanonicalDigest, &record.OperationID, &record.ResultPayload, &record.ChangedRefs, &record.AuthorizedScopeSnapshot)
	if err == sql.ErrNoRows {
		return MutationIdempotencyRecord{}, false, nil
	}
	if err != nil {
		return MutationIdempotencyRecord{}, false, wrapFailure(KindUnavailable, "mutation_idempotency", "cannot read idempotency record", true, "retry once the database is readable", err)
	}
	return record, true, nil
}

func (s *Store) TouchMutationIdempotency(ctx context.Context, key MutationIdempotencyKey, observed time.Time) error {
	return s.Transact(ctx, func(transaction *Transaction) error {
		return TouchMutationIdempotencyTx(ctx, transaction, key, observed)
	})
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

func (s *Store) InsertMutationIdempotency(ctx context.Context, input MutationIdempotencyInsert) error {
	return s.Transact(ctx, func(transaction *Transaction) error {
		return InsertMutationIdempotencyTx(ctx, transaction, input)
	})
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

func (s *Store) UpdateMutationResult(ctx context.Context, input MutationResultUpdate) error {
	return s.Transact(ctx, func(transaction *Transaction) error {
		return UpdateMutationResultTx(ctx, transaction, input)
	})
}

func UpdateMutationResultTx(ctx context.Context, transaction *Transaction, input MutationResultUpdate) error {
	tx, err := transactionSQL(transaction, "mutation_idempotency")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE idempotency_records SET result_event_ids=?,result_payload=?,changed_refs=?,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, input.ResultEventIDs, input.ResultPayload, input.ChangedRefs, input.ObservedAt.UTC().Format(time.RFC3339Nano), input.Key.PrincipalRef, input.Key.Tool, input.Key.OperationKind, input.Key.IdempotencyKey)
	if err != nil {
		return wrapFailure(KindUnavailable, "mutation_idempotency", "cannot persist mutation result", true, "retry once the database is writable", err)
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

func (s *Store) LatestWorkflowContractVersion(ctx context.Context, workID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "workflow_contract", "store is not open", false, "open the authority database")
	}
	return latestWorkflowContractVersion(ctx, s.db, workID)
}

func latestWorkflowContractVersion(ctx context.Context, q queryer, workID string) (int64, error) {
	var version int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1),0)`, workID).Scan(&version)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_contract", "cannot read active workflow contract version", true, "retry once the database is readable", err)
	}
	return version, nil
}

func (s *Store) ActiveWorkflowContract(ctx context.Context, workID string) (WorkflowContractSnapshot, error) {
	if s == nil || s.db == nil {
		return WorkflowContractSnapshot{}, newFailure(KindUnavailable, "workflow_contract", "store is not open", false, "open the authority database")
	}
	return activeWorkflowContract(ctx, s.db, workID)
}

func activeWorkflowContract(ctx context.Context, q queryer, workID string) (WorkflowContractSnapshot, error) {
	var contract WorkflowContractSnapshot
	err := q.QueryRowContext(ctx, `SELECT contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&contract.Version, &contract.Premise)
	if err == sql.ErrNoRows {
		return WorkflowContractSnapshot{}, newFailure(KindProjectionNotFound, "workflow_contract", "active workflow contract does not exist", false, "approve a workflow contract first")
	}
	if err != nil {
		return WorkflowContractSnapshot{}, wrapFailure(KindUnavailable, "workflow_contract", "cannot read active workflow contract", true, "retry once the database is readable", err)
	}
	return contract, nil
}

func WorkflowInstanceDefinitionTx(ctx context.Context, transaction *Transaction, workID string) (WorkflowInstanceDefinition, bool, error) {
	tx, err := transactionSQL(transaction, "workflow_instance")
	if err != nil {
		return WorkflowInstanceDefinition{}, false, err
	}
	var definition WorkflowInstanceDefinition
	err = tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition.DefinitionRef)
	if err == sql.ErrNoRows {
		return WorkflowInstanceDefinition{}, false, nil
	}
	if err != nil {
		return WorkflowInstanceDefinition{}, false, wrapFailure(KindUnavailable, "workflow_instance", "cannot read workflow definition", true, "retry once the database is readable", err)
	}
	return definition, true, nil
}

func (s *Store) WorkVersion(ctx context.Context, workID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "work_version", "store is not open", false, "open the authority database")
	}
	return mutationWorkVersion(ctx, s.db, workID)
}

func mutationWorkVersion(ctx context.Context, q queryer, workID string) (int64, error) {
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return 0, newFailure(KindProjectionNotFound, "work_version", "work item does not exist", false, "reread the work item")
		}
		return 0, wrapFailure(KindUnavailable, "work_version", "cannot read work version", true, "retry once the database is readable", err)
	}
	return version, nil
}

func (s *Store) TerminalWorkVersion(ctx context.Context, workID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "terminal_work", "store is not open", false, "open the authority database")
	}
	return terminalWorkVersion(ctx, s.db, workID)
}

func terminalWorkVersion(ctx context.Context, q queryer, workID string) (int64, error) {
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
// connection, for callers that gate a request before opening a write
// transaction (the destroy approval preflight).
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

func RelationByIDTx(ctx context.Context, transaction *Transaction, relationID int64) (RelationRecord, error) {
	tx, err := transactionSQL(transaction, "relation")
	if err != nil {
		return RelationRecord{}, err
	}
	var relation RelationRecord
	err = tx.QueryRowContext(ctx, `SELECT work_id_from,work_id_to,kind FROM relations WHERE id=?`, relationID).Scan(&relation.FromWorkID, &relation.ToWorkID, &relation.Kind)
	if err == sql.ErrNoRows {
		return RelationRecord{}, newFailure(KindRelationNotFound, "relation", "relation does not exist", false, "reread the relation graph")
	}
	if err != nil {
		return RelationRecord{}, wrapFailure(KindUnavailable, "relation", "cannot read relation", true, "retry once the database is readable", err)
	}
	return relation, nil
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
	return productsByIDsTx(ctx, tx, ids, `SELECT wp.work_id,pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (`)
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
	rows, err := tx.QueryContext(ctx, `SELECT w.id FROM work_items w WHERE w.lifecycle='in_progress' AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=?) ORDER BY w.id LIMIT 100`, productID)
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
