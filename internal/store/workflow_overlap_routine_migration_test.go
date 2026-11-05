package store

// Shared schema-dump helpers for the migration witness tests. They capture a
// table's declared shape straight from SQLite's metadata tables so a
// preservation check never restates that shape in prose.

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

// tableColumnInventory captures one table's columns in declaration order.
func tableColumnInventory(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

// foreignKeyDeclarations captures one table's foreign keys as declared.
func foreignKeyDeclarations(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	declarations := []string{}
	for rows.Next() {
		var target, from, to, onUpdate, onDelete string
		if err := rows.Scan(&target, &from, &to, &onUpdate, &onDelete); err != nil {
			t.Fatal(err)
		}
		declarations = append(declarations, strings.Join([]string{from, "->", target + "." + to, onUpdate, onDelete}, "\x1f"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return declarations
}

func assertUnchangedDump(t *testing.T, label string, before, after []string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("%s changed across the migration:\nbefore: %q\nafter:  %q", label, before, after)
	}
}
