package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// ActiveWorkflowContractVersions returns every active contract version so a
// recovery request can bind an ambiguous projection without selecting one.
func (s *Store) ActiveWorkflowContractVersions(ctx context.Context, workID string) ([]int64, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_contract", "store is not open", false, "open the authority database")
	}
	return activeWorkflowContractVersions(ctx, s.db, workID)
}

// activeWorkflowContractVersion returns the only contract that can authorize
// work. It never selects a contract from an ambiguous projection.
func activeWorkflowContractVersion(ctx context.Context, q queryer, workID, subject string) (int64, error) {
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&count); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect active workflow contracts", true, "retry once the workflow contract projection is readable", err)
	}
	if count > 1 {
		return 0, newFailure(KindInvariantViolation, subject, "workflow contract projection has multiple active contracts", false, "use the typed operator recovery for duplicate active contracts")
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

func activeWorkflowContractCount(ctx context.Context, q queryer, workID, subject string) (int64, error) {
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&count); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect active workflow contracts", true, "retry once the workflow contract projection is readable", err)
	}
	return count, nil
}

func validWorkflowContractVersionList(versions []int64) bool {
	if len(versions) == 0 || len(versions) > 32 {
		return false
	}
	seen := make(map[int64]struct{}, len(versions))
	for _, version := range versions {
		if version <= 0 || version > 2147483647 {
			return false
		}
		if _, exists := seen[version]; exists {
			return false
		}
		seen[version] = struct{}{}
	}
	return true
}

func containsInt64(values []int64, wanted int64) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func workflowFieldInt64List(fields map[string]json.RawMessage, name string) ([]int64, bool, error) {
	raw, present := fields[name]
	if !present {
		return nil, false, nil
	}
	var values []int64
	if err := json.Unmarshal(raw, &values); err != nil || !validWorkflowContractVersionList(values) {
		return nil, true, newFailure(KindInvalidPayload, "workflow_action", "predecessor_contract_versions must contain unique positive contract versions", false, "supply the exact active contract versions")
	}
	return values, true, nil
}

// ensureInitialWorkflowContractApproval admits approve_contract only as the
// first contract a work item holds. A later approval must supersede, so an
// approval can never leave two active contracts behind it.
func ensureInitialWorkflowContractApproval(ctx context.Context, q queryer, workID string, fields map[string]json.RawMessage) error {
	_, activeErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if activeErr != sql.ErrNoRows {
		if activeErr == nil {
			return newFailure(KindInvalidOperation, "workflow_action", "approve_contract cannot replace an existing workflow contract", false, "use supersede_contract with the typed successor contract")
		}
		return activeErr
	}
	if workflowFieldInt(fields, "contract_version", 1) != 1 {
		return newFailure(KindInvalidPayload, "workflow_action", "initial workflow contract approval must be version 1", false, "approve the initial contract as version 1")
	}
	return nil
}

func maxInt64(values []int64) int64 {
	highest := values[0]
	for _, value := range values[1:] {
		if value > highest {
			highest = value
		}
	}
	return highest
}

// resolveWorkflowContractPredecessors returns the exact set of active contract
// versions a supersession retires. An omitted list is admitted only when one
// contract is active, so a duplicated projection must name every version it
// replaces rather than retiring an arbitrary one.
func resolveWorkflowContractPredecessors(ctx context.Context, q queryer, workID string, fields map[string]json.RawMessage) ([]int64, int64, error) {
	active, activeErr := activeWorkflowContractVersions(ctx, q, workID)
	if activeErr != nil {
		return nil, 0, activeErr
	}
	if len(active) == 0 {
		return nil, 0, newFailure(KindInvariantViolation, "workflow_action", "contract recovery requires an active workflow contract", false, "rebuild the workflow contract projection")
	}
	predecessors, supplied, listErr := workflowFieldInt64List(fields, "predecessor_contract_versions")
	if listErr != nil {
		return nil, 0, listErr
	}
	if !supplied {
		if len(active) != 1 {
			return nil, 0, newFailure(KindInvalidPayload, "workflow_action", "duplicate active contracts require predecessor_contract_versions", false, "supply the exact active contract versions")
		}
		predecessors = []int64{active[0]}
	}
	if len(predecessors) != len(active) {
		return nil, 0, newFailure(KindInvariantViolation, "workflow_action", "predecessor_contract_versions does not match active workflow contracts", false, "supply every exact active contract version")
	}
	for _, version := range active {
		if !containsInt64(predecessors, version) {
			return nil, 0, newFailure(KindInvariantViolation, "workflow_action", "predecessor_contract_versions names a non-active contract set", false, "supply the exact active contract versions")
		}
	}
	return predecessors, predecessors[0], nil
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

// duplicateActiveContractOmissions names the work items a Domain read leaves
// out because their contract projection is ambiguous. A Domain read joins the
// active contract, so an item holding two would appear twice and misreport the
// Domain. Excluding it keeps the rest of the Product readable, and naming it
// here keeps the exclusion visible rather than silent: a caller that sees an
// omission knows the view is partial and which item to repair.
func duplicateActiveContractOmissions(ctx context.Context, q queryer, productID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.work_id
		FROM workflow_contracts c
		WHERE c.superseded_by IS NULL
		  AND EXISTS (SELECT 1 FROM workflow_architecture_bindings b WHERE b.work_id=c.work_id AND b.product_id=?)
		  AND (SELECT count(*) FROM workflow_contracts c2 WHERE c2.work_id=c.work_id AND c2.superseded_by IS NULL)>1
		GROUP BY c.work_id
		ORDER BY c.work_id`, productID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot inspect active workflow contracts")
	}
	defer rows.Close()
	omissions := []string{}
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			return nil, workflowProjectionError(err, "cannot scan an ambiguous workflow contract projection")
		}
		omissions = append(omissions, "ambiguous-contract:"+workID)
	}
	if err := rows.Err(); err != nil {
		return nil, workflowProjectionError(err, "cannot enumerate ambiguous workflow contract projections")
	}
	return omissions, nil
}
