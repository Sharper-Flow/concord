// Package projection is leaf test support: a canonical digest of the workflow
// and derived-law projection tables, read row by row from the authority
// database, so rebuild checks compare the complete persisted projection before
// and after replay. The layout is discovered from the store, so every current
// column of every hashed table is covered.
//
// It imports no store package, so both internal store tests and consumer
// package tests can call it. Only test files may reference this package: no
// production call site and no runtime route exists.
package projection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// workflowProjectionTables is the fixed table order the projection hash covers.
var workflowProjectionTables = []string{
	"workflow_actors", "workflow_checkpoints", "workflow_contracts", "workflow_contract_predicates", "workflow_contract_law_revisions", "workflow_contract_law_modifications", "workflow_overlap_resolutions", "workflow_architecture_bindings", "workflow_contract_affected_domains", "workflow_contract_domain_modifications", "workflow_contract_domain_relation_modifications", "workflow_law_addition_reservations", "workflow_contract_law_additions", "workflow_contract_verification_obligations", "workflow_decision_records",
	"workflow_impact_edges", "workflow_context_checkpoints", "workflow_context_boundaries", "workflow_design_records", "workflow_proposal_records",
	"workflow_instances", "workflow_premise_confirmations", "workflow_candidate_sets",
	"work_items", "relations",
}

// Tables returns the covered projection tables in hash order, so callers can
// compare the discovered layout against pinned historical layouts.
func Tables() []string {
	return append([]string(nil), workflowProjectionTables...)
}

// Columns returns one projection table's physical column order, discovered
// from the authority database through the bound pragma metadata query.
func Columns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("projection hash: cannot inspect %s: %w", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("projection hash: cannot inspect %s: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection hash: cannot inspect %s: %w", table, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("projection hash: workflow projection table %s is missing", table)
	}
	return columns, nil
}

// Layout is one hashed projection table with its column order. A discovered
// layout comes from Tables and Columns; a pinned historical layout is a
// caller-supplied input only verification code builds.
type Layout struct {
	Table   string
	Columns []string
}

// HashLayouts encodes each layout's rows: the table marker, then per row the
// per-column name and value encoding in column order. Empty tables still
// contribute their marker. The encoding is byte-stable across discovered and
// pinned layouts, so a frozen baseline captured on a pinned layout still
// binds the discovered encoding.
func HashLayouts(ctx context.Context, db *sql.DB, layouts []Layout) (string, error) {
	if db == nil {
		return "", fmt.Errorf("projection hash: the authority database is not open")
	}
	h := sha256.New()
	for _, layout := range layouts {
		quotedColumns := make([]string, len(layout.Columns))
		for i, column := range layout.Columns {
			quotedColumns[i] = quoteSQLiteIdentifier(column)
		}
		query := strings.Join([]string{"SELECT", strings.Join(quotedColumns, ","), "FROM", quoteSQLiteIdentifier(layout.Table), "ORDER BY", strings.Join(quotedColumns, ",")}, " ")
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return "", fmt.Errorf("projection hash: cannot read %s: %w", layout.Table, err)
		}
		writeHashf(h, "table:%d:%s\x00", len(layout.Table), layout.Table)
		for rows.Next() {
			values := make([]any, len(layout.Columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return "", fmt.Errorf("projection hash: cannot scan %s: %w", layout.Table, err)
			}
			for i, value := range values {
				writeHashf(h, "%d:%s=", len(layout.Columns[i]), layout.Columns[i])
				writeHashValue(h, value)
				writeHashf(h, "%c", byte(0))
			}
			writeHashf(h, "%c", byte(0))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", fmt.Errorf("projection hash: cannot read %s: %w", layout.Table, err)
		}
		rows.Close()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Hash returns the canonical projection digest of one authority database. It
// reads rows from the authority rather than hashing a caller-supplied
// projection or expected-value map.
func Hash(ctx context.Context, db *sql.DB) (string, error) {
	if db == nil {
		return "", fmt.Errorf("projection hash: the authority database is not open")
	}
	layouts := make([]Layout, 0, len(workflowProjectionTables))
	for _, table := range workflowProjectionTables {
		columns, err := Columns(ctx, db, table)
		if err != nil {
			return "", err
		}
		layouts = append(layouts, Layout{Table: table, Columns: columns})
	}
	return HashLayouts(ctx, db, layouts)
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func writeHashValue(h hash.Hash, value any) {
	switch value := value.(type) {
	case nil:
		writeHashf(h, "null")
	case []byte:
		writeHashf(h, "bytes:%d:", len(value))
		writeHashf(h, "%s", value)
	default:
		writeHashf(h, "%T:%v", value, value)
	}
}

// hash.Hash.Write always returns a nil error by contract.
func writeHashf(h hash.Hash, format string, args ...any) {
	_, _ = fmt.Fprintf(h, format, args...)
}
