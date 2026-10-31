package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
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

// A corpus is authored before either execution. Current receipts must cover it
// exactly; historical envelopes are not reconstructed from current projections.
type historicalFactCorpus struct {
	events  []Event
	parents func(*testing.T, *Store, string)
	write   func(context.Context, *sql.Tx, Event) error
}

func (c historicalFactCorpus) prefix(t *testing.T, n int) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "corpus.db")
	// Receipt migration 145 must see the corpus, rather than starting with an
	// empty receipt table and appending unacknowledged historical events later.
	s := &Store{db: openMigratedTo(t, path, min(n, 144)), path: path}
	c.parents(t, s, "fold_guard")
	for i, e := range c.events {
		if _, err := s.db.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`, i+1, e.EventID, e.Kind, e.SubjectType, e.SubjectID, e.Actor, e.OccurredAt.UTC().Format(time.RFC3339Nano), e.PayloadVersion, string(e.Payload)); err != nil {
			t.Fatal(err)
		}
	}
	if n > 144 {
		advanceHistoricalPrefix(t, s.db, n)
	}
	return s
}

func (c historicalFactCorpus) current(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	c.parents(t, s, "write_guard")
	for _, e := range c.events {
		err := s.Transact(t.Context(), func(tx *Transaction) error {
			scope, err := beginWrite(t.Context(), tx.tx)
			if err != nil {
				return err
			}
			if e.Seq > 0 {
				var last int64
				if err := tx.tx.QueryRowContext(t.Context(), `SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil {
					return err
				}
				if last >= e.Seq {
					return fmt.Errorf("corpus effect coordinate %d is not after %d", e.Seq, last)
				}
				for last < e.Seq-1 {
					if _, err := allocateStoreSequence(t.Context(), tx.tx, "effect"); err != nil {
						return err
					}
					last++
				}
			}
			seq, err := appendPreparedEvent(t.Context(), tx.tx, e)
			if err != nil {
				return err
			}
			e.Seq = seq
			if err := c.write(t.Context(), tx.tx, e); err != nil {
				return err
			}
			if err := advanceWorkEffectSeqTx(t.Context(), tx.tx, e); err != nil {
				return err
			}
			return scope.close(t.Context())
		})
		if err != nil {
			t.Fatalf("current corpus %s (%s): %v", e.EventID, e.Kind, err)
		}
	}
	want := make([]string, len(c.events))
	for i, e := range c.events {
		want[i] = e.EventID
	}
	slices.Sort(want)
	rows, err := s.db.Query(`SELECT event_id FROM effect_receipts ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("corpus/receipt IDs differ in either direction: got %v want %v", got, want)
	}
	return s
}

func corpusWorkParents(t *testing.T, s *Store, guard string, works ...string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO ` + guard + ` VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, work := range works {
		if _, err := s.db.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task',?,'needed',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, work, work); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM ` + guard); err != nil {
		t.Fatal(err)
	}
}

func corpusEvent(id, kind, work string, payload any) Event {
	return operationEvent(id, kind, SubjectWorkItem, work, payload)
}

// Mutate a selection, not a checksum, guard or manifest. The mutant runs only
// the target step on a separate real prefix and must disagree with the green oracle.
func assertCorpusSelectionRed(t *testing.T, c historicalFactCorpus, version int, from, to string, queries []string, want [][]string) {
	t.Helper()
	s := c.prefix(t, version-1)
	m := migrations[version-1]
	if !strings.Contains(m.SQL, from) {
		t.Fatalf("migration %d selection mutant has no source", version)
	}
	m.SQL = strings.Replace(m.SQL, from, to, 1)
	if err := applyMigration(t.Context(), s.db, m); err != nil {
		t.Fatalf("selection mutant failed before comparison: %v", err)
	}
	got := workerFactsQueryRows(t, s, queries)
	if reflect.DeepEqual(got, want) {
		t.Fatalf("migration %d selection mutant escaped the parity oracle", version)
	}
	t.Logf("RED migration %d: broken selection rejected by the same row oracle", version)
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
		// Source-only tables cannot seed an earlier schema. Qualify main:
		// SQLite would otherwise resolve them in the attached current store.
		var present bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE type='table' AND name=?)`, table).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if !present {
			continue
		}
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

func TestCopyFixtureDataToPrefixSkipsLaterTables(t *testing.T) {
	s := worktreeFixture(t)
	// These are source-only fixture data, not reconstructed historical DDL.
	if _, err := s.db.Exec(`CREATE TABLE fixture_future_table(id TEXT); INSERT INTO fixture_future_table VALUES('future-only')`); err != nil {
		t.Fatal(err)
	}
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "prefix151.db"), 151)
	copyFixtureDataToPrefix(t, s, db, []string{"fixture_future_table", "products"}, nil, "write_guard")
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='fixture_future_table')`).Scan(&exists); err != nil || exists {
		t.Fatalf("source-only table entered historical schema: exists=%t error=%v", exists, err)
	}
	var products int
	if err := db.QueryRow(`SELECT count(*) FROM products WHERE id='product-w'`).Scan(&products); err != nil || products != 1 {
		t.Fatalf("shared historical table lost fixture data: products=%d error=%v", products, err)
	}
}
