package main

import (
	"database/sql"
	"strings"
	"testing"
)

func predecessorProjectColumns(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('projects')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var columns []string
	for _, name := range strings.Split("id,display_name,stage_maturity_override,stage_audience_commitment_override,version,created_at,updated_at", ",") {
		if present[name] {
			columns = append(columns, name)
		}
	}
	return strings.Join(columns, ",")
}

func upgradeFixtureSelect(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	if table == "projects" {
		return `SELECT ` + predecessorProjectColumns(t, db) + ` FROM projects`
	}
	return `SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
}
