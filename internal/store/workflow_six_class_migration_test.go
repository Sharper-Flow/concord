package store

import (
	"database/sql"
	"slices"
	"testing"
)

func sixClassSnapshot(t *testing.T, db *sql.DB, tables ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table) //nolint:gosec // fixed test-owned table names.
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, column := range columns {
				row[column] = values[i]
			}
			out[table] = append(out[table], workflowJSON(row))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out[table])
	}
	return out
}
