package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Observe an immutable migration before a later migration retires its source
// tables. The prefix itself comes from openMigratedTo; the final tail uses Migrate.
func advanceHistoricalPrefix(t *testing.T, db *sql.DB, through int) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if _, done := applied[m.Version]; done || m.Version > through {
			continue
		}
		if err := applyMigration(ctx, tx, m); err != nil {
			t.Fatal(err)
		}
		if m.Version >= 138 {
			if err := advanceStoreRevision(ctx, tx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,breaking,applied_at) VALUES(?,?,?,?,?)`, m.Version, m.Name, m.checksum(), m.Breaking, "2026-10-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Fixture data for the stable pre-128 Product/Project/membership shapes. Never
// submit these historical rows to current production writers.
func seedHistoricalLocatorParents(t *testing.T, tx *sql.Tx, events []Event) {
	t.Helper()
	for _, e := range events {
		stamp := e.OccurredAt.UTC().Format(time.RFC3339Nano)
		var p struct {
			DisplayName      string `json:"display_name"`
			ProductID        string `json:"product_id"`
			ProjectID        string `json:"project_id"`
			Role             string `json:"role"`
			ResultingVersion int64  `json:"resulting_version"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		var err error
		switch e.Kind {
		case "product.created":
			_, err = tx.Exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?,?,'prototype','operator_only',1,?,?)`, e.SubjectID, p.DisplayName, stamp, stamp)
		case "project.created":
			_, err = tx.Exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES(?,?,1,?,?)`, e.SubjectID, p.DisplayName, stamp, stamp)
		case "product_project.added":
			_, err = tx.Exec(`INSERT INTO product_projects(product_id,project_id,role) VALUES(?,?,?)`, p.ProductID, p.ProjectID, p.Role)
			if err == nil {
				_, err = tx.Exec(`UPDATE products SET version=?,updated_at=? WHERE id=?`, p.ResultingVersion, stamp, p.ProductID)
			}
		default:
			t.Fatalf("unsupported historical parent fixture %s", e.Kind)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The log's bytes are checked before retirement. After retirement its surviving
// acknowledgment must bind the same headers, coordinate, and canonical bytes.
func assertHistoricalReceipts(t *testing.T, db *sql.DB, events []histEventRow) {
	t.Helper()
	for _, e := range events {
		wantDigest := historicalFixturePayloadDigest(t, e.Payload)
		var seq int64
		var kind, subjectType, subjectID, actor, digest string
		var version int
		if err := db.QueryRow(`SELECT effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id=?`, e.EventID).Scan(&seq, &kind, &subjectType, &subjectID, &actor, &version, &digest); err != nil {
			t.Fatal(err)
		}
		if seq != e.Seq || kind != e.Kind || subjectType != e.SubjectType || subjectID != e.SubjectID || actor != e.Actor || version != e.Version || digest != wantDigest {
			t.Fatalf("historical receipt changed %s: %d/%s/%s/%s/%s/%d/%s", e.EventID, seq, kind, subjectType, subjectID, actor, version, digest)
		}
	}
}

func historicalFixturePayloadDigest(t *testing.T, payload string) string {
	t.Helper()
	var value any
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
}

// Copy fixture DATA, never schema, into tables made by the real prefix. Callers
// declare every old-only value; no identity or source-event history is inferred.
func copyFixtureDataToPrefix(t *testing.T, source *Store, db *sql.DB, tables []string, oldValues map[string]map[string]string, guard string) {
	t.Helper()
	if _, err := db.Exec(`ATTACH DATABASE ? AS fixture_source`, source.path); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DETACH DATABASE fixture_source`); err != nil {
			t.Error(err)
		}
	}()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON; INSERT INTO ` + guard + `(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		columns := func(schema string) []string {
			t.Helper()
			rows, err := tx.Query(`SELECT name FROM pragma_table_info(?,?) ORDER BY cid`, table, schema)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				out = append(out, name)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return out
		}
		sourceColumns := map[string]bool{}
		for _, column := range columns("fixture_source") {
			sourceColumns[column] = true
		}
		var names, expressions []string
		for _, column := range columns("main") {
			expression, declared := oldValues[table][column]
			if !declared {
				if !sourceColumns[column] {
					t.Fatalf("%s.%s needs an explicit historical fixture value", table, column)
				}
				expression = `"` + column + `"`
			}
			names = append(names, `"`+column+`"`)
			expressions = append(expressions, expression)
		}
		// Migration-owned defaults and registries may already exist here.
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM "` + table + `"`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			var differs bool
			left := `SELECT ` + strings.Join(names, ",") + ` FROM main."` + table + `"`
			right := `SELECT ` + strings.Join(expressions, ",") + ` FROM fixture_source."` + table + `"`
			if err := tx.QueryRow(`SELECT EXISTS(` + left + ` EXCEPT ` + right + `) OR EXISTS(` + right + ` EXCEPT ` + left + `)`).Scan(&differs); err != nil {
				t.Fatal(err)
			}
			if !differs {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM "` + table + `"`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO "` + table + `" (` + strings.Join(names, ",") + `) SELECT ` + strings.Join(expressions, ",") + ` FROM fixture_source."` + table + `"`); err != nil {
			t.Fatalf("copy historical %s data: %v", table, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM ` + guard); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
