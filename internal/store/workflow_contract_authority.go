package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

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

func ensureNoDuplicateActiveWorkflowContracts(ctx context.Context, q queryer, subject string) error {
	var duplicateCount int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT work_id FROM workflow_contracts WHERE superseded_by IS NULL GROUP BY work_id HAVING count(*)>1)`).Scan(&duplicateCount); err != nil {
		return wrapFailure(KindUnavailable, subject, "cannot inspect active workflow contracts", true, "retry once the workflow contract projection is readable", err)
	}
	if duplicateCount != 0 {
		return newFailure(KindInvariantViolation, subject, "workflow contract projection has multiple active contracts", false, "use the typed operator recovery for duplicate active contracts")
	}
	return nil
}
