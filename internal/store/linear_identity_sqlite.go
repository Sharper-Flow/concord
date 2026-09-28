package store

import (
	"database/sql/driver"

	sqlite "modernc.org/sqlite"
)

// concord_linear_external_issue_identity exposes
// normalizeLinearIssueExternalRef to SQL derivations: a work item's
// external_ref normalizes to its Linear issue identity, or NULL when the
// value names no Linear issue. The capture publication refusal and the
// publication-obligation derivation must agree on which external_refs name
// a Linear issue, so both call this one Go rule instead of a parallel SQL
// pattern. NULL never equals a column value, so a NULL result excludes
// nothing.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"concord_linear_external_issue_identity",
		1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			raw, ok := args[0].(string)
			if !ok {
				return nil, nil
			}
			identity, exists := normalizeLinearIssueExternalRef(raw)
			if !exists {
				return nil, nil
			}
			return identity, nil
		},
	)
}
