package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// activeWorkflowContractVersion returns the only contract that can authorize
// work. It never selects a contract from an ambiguous projection.
func activeWorkflowContractVersion(ctx context.Context, q queryer, workID, subject string) (int64, error) {
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&count); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect active workflow contracts", true, "retry once the workflow contract projection is readable", err)
	}
	if count > 1 {
		return 0, newFailure(KindInvariantViolation, subject, "workflow contract projection has multiple active contracts", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if count == 0 {
		return 0, sql.ErrNoRows
	}
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the active workflow contract", true, "retry once the workflow contract projection is readable", err)
	}
	return version, nil
}

// Admission reads can expose an ambiguous projection without granting it
// contract authority. Writers still require one active contract.
func workflowDuplicateContractProjection(err error) bool {
	return workflowProjectionInvariant(err, "workflow contract projection has multiple active contracts")
}

// workflowMissingContractProjection reports the singular active-contract
// reader's missing-contract invariant, so an admission read over a workflow
// that runs without an approved contract degrades to its empty answer instead
// of refusing.
func workflowMissingContractProjection(err error) bool {
	return workflowProjectionInvariant(err, "approved workflow contract is missing")
}

func workflowProjectionInvariant(err error, detail string) bool {
	for err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindInvariantViolation && failure.Detail == detail {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// ensureInitialWorkflowContractApproval admits approve_contract only as the
// first contract a work item holds. Later versions belong to revise_intent.
func ensureInitialWorkflowContractApproval(ctx context.Context, q queryer, workID string, fields map[string]json.RawMessage) error {
	_, activeErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if activeErr != sql.ErrNoRows {
		if activeErr == nil {
			return newFailure(KindInvalidOperation, "workflow_action", "approve_contract cannot replace an existing workflow contract", false, "use revise_intent with the complete contract")
		}
		return activeErr
	}
	if workflowFieldInt(fields, "contract_version", 1) != 1 {
		return newFailure(KindInvalidPayload, "workflow_action", "initial workflow contract approval must be version 1", false, "approve the initial contract as version 1")
	}
	return nil
}

func activeWorkflowContractVersions(ctx context.Context, q queryer, workID string) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot inspect active workflow contract versions")
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var version int64
		if scanErr := rows.Scan(&version); scanErr != nil {
			return nil, workflowProjectionError(scanErr, "cannot scan active workflow contract version")
		}
		versions = append(versions, version)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, workflowProjectionError(rowsErr, "cannot scan active workflow contract versions")
	}
	return versions, nil
}
