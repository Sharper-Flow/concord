package store

import (
	"database/sql"
	"strings"
	"testing"
)

const retirementUpgradeTime = "2026-08-07T12:00:00Z"

func retirementUpgradeExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("fixture SQL %s: %v", query, err)
	}
}

// Keep text, NULLs, integer storage classes, and blobs distinct in comparisons.
func retirementUpgradeRows(t *testing.T, db *sql.DB, query string, args ...any) [][]any {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query, args...)
	if err != nil {
		t.Fatalf("snapshot %s: %v", query, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if blob, ok := value.([]byte); ok {
				values[i] = append([]byte(nil), blob...)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func retirementUpgradeSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	state := map[string][][]any{"schema": retirementUpgradeRows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name`)}
	for _, table := range retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`) {
		name := table[0].(string)
		query := `SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `" ORDER BY rowid`
		state[name] = retirementUpgradeRows(t, db, query)
	}
	return state
}
