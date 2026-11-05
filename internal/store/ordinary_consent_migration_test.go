package store

import (
	"database/sql"
	"testing"
)

// ordinaryConsentMigrationTables names the tables migration 160 rebuilt; the
// schema-metadata helper below exists for the tests that still assert current
// constraints on them.
var ordinaryConsentMigrationTables = []string{"workflow_overlap_resolutions", "outside_repair_dispositions", "outside_repair_reconciliations"}

func ordinaryConsentSchemaMetadata(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	metadata := map[string][]string{}
	for _, table := range ordinaryConsentMigrationTables {
		metadata[table+" columns"] = tableColumnInventory(t, db, table)
		metadata[table+" foreign keys"] = foreignKeyDeclarations(t, db, table)
		rows, err := db.Query(`SELECT sql FROM sqlite_schema WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY name`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var ddl string
			if err := rows.Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			metadata[table+" indexes and guards"] = append(metadata[table+" indexes and guards"], ddl)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return metadata
}
