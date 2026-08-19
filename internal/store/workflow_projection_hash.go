package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// WorkflowProjectionHash returns a canonical digest of workflow and derived-law
// projection tables. It reads rows from the authority rather than hashing a
// caller-supplied projection or expected-value map, so rebuild checks compare
// the complete persisted projection before and after replay.
func WorkflowProjectionHash(ctx context.Context, s *Store) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "workflow_projection_hash", "store is not open", false, "open the authority database")
	}
	tables := []string{
		"workflow_actors", "workflow_checkpoints", "workflow_contracts", "workflow_contract_law_revisions", "workflow_contract_law_modifications", "workflow_overlap_resolutions", "workflow_architecture_bindings", "workflow_contract_affected_domains", "workflow_contract_domain_modifications", "workflow_contract_domain_relation_modifications", "workflow_law_addition_reservations", "workflow_contract_law_additions", "workflow_contract_verification_obligations", "workflow_decision_records",
		"workflow_external_conditions", "workflow_impact_edges", "workflow_impact_notices", "workflow_context_checkpoints", "workflow_context_boundaries",
		"workflow_instances", "workflow_premise_confirmations", "workflow_candidate_sets",
		"law_subjects", "law_relations", "work_items", "relations",
	}
	h := sha256.New()
	for _, table := range tables {
		columns, err := projectionColumns(ctx, s.db, table)
		if err != nil {
			return "", err
		}
		query := "SELECT " + strings.Join(columns, ",") + " FROM " + table + " ORDER BY " + strings.Join(columns, ",")
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return "", wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot read "+table, true, "retry once the database is readable", err)
		}
		fmt.Fprintf(h, "table:%d:%s\x00", len(table), table)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return "", wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot scan "+table, true, "retry once the database is readable", err)
			}
			for i, value := range values {
				fmt.Fprintf(h, "%d:%s=", len(columns[i]), columns[i])
				writeHashValue(h, value)
				h.Write([]byte{0})
			}
			h.Write([]byte{0})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot read "+table, true, "retry once the database is readable", err)
		}
		rows.Close()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func projectionColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_projection_hash", "cannot inspect "+table, true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, newFailure(KindProjectionNotFound, "workflow_projection_hash", "workflow projection table is missing", false, "migrate the authority database")
	}
	return columns, nil
}

func writeHashValue(h interface{ Write([]byte) (int, error) }, value any) {
	switch value := value.(type) {
	case nil:
		fmt.Fprint(h, "null")
	case []byte:
		fmt.Fprintf(h, "bytes:%d:", len(value))
		h.Write(value)
	default:
		fmt.Fprintf(h, "%T:%v", value, value)
	}
}
