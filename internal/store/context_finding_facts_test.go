package store

// Unit fixtures use the actual migration schema, not parallel declarations
// that could drift from the installed current-state owners.
var (
	oracleFindingFactsDDL  = contextFindingFactsMigrationSQL()
	workflowReviewFactsDDL = contextFindingFactsMigrationSQL()
)

func contextFindingFactsMigrationSQL() string {
	for _, migration := range migrations {
		if migration.Name == "context_finding_review_facts" {
			return migration.SQL
		}
	}
	panic("missing context finding facts migration")
}
