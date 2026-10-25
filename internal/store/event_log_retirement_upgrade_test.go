package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const retirementUpgradeTime = "2026-08-07T12:00:00Z"

// This fixture is an actual checksummed v150 database, not a current database
// with its manifest relabeled. Historical inserts never use the current writer.
func retirementUpgradeFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range migrations {
		if m.Version > 150 {
			break
		}
		if err := applyMigration(t.Context(), tx, m); err != nil {
			t.Fatalf("fixture migration %d (%s): %v", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking) VALUES(?,?,?,?,?)`, m.Version, m.Name, m.checksum(), retirementUpgradeTime, m.Breaking); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 150 {
		t.Fatalf("fixture applied %d migrations, want 150", count)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO fold_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-upgrade','Upgrade witness','prototype','operator_only',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-upgrade','Upgrade witness',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO product_projects VALUES('product-upgrade','project-upgrade','primary')`)
	for _, id := range []string{"work-upgrade-from", "work-upgrade-to"} {
		retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Upgrade witness','needed',0,1,?,?)`, id, retirementUpgradeTime, retirementUpgradeTime)
		retirementUpgradeExec(t, db, `INSERT INTO work_projects VALUES(?,'project-upgrade','primary')`, id)
	}
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'operator-upgrade','client-upgrade','agent-upgrade','session-upgrade','operator',?)`, actor, retirementUpgradeTime)
	for _, id := range []string{"work-upgrade-from", "work-upgrade-to"} {
		retirementUpgradeExec(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate) VALUES(?,1,'Preserve current rows','internal_sqlite','[]','[]',?,?,'[]')`, id, retirementUpgradeTime, actor)
	}
	for i, kind := range []string{EventExternalObservationCaptured, EventExternalObservationVerified, "workflow.overlap_resolved", "workflow.contract_superseded", "work.size_overridden"} {
		seq := []int{11, 13, 17, 19, 23}[i]
		retirementUpgradeEnvelope(t, db, seq, fmt.Sprintf("event-upgrade-%d", i), kind)
	}
	retirementUpgradeExec(t, db, `UPDATE effect_receipts SET relation_seq=1 WHERE effect_seq=17; UPDATE store_sequences SET last=1 WHERE name='relation'`)
	retirementUpgradeExec(t, db, `INSERT INTO external_observations(observation_id,work_id,subject_kind,subject_ref,capture_method,captured_at,reporting_authority_ref,subject_digest,observed_universe,freshness_policy_ref,divergence_policy_ref,verification_state,verification_method,verified_at,verifying_authority_ref,verification_result,verification_omissions,created_event_seq,verified_event_seq) VALUES('xobs:upgrade','work-upgrade-from','git_position','synthetic-subject','trusted_client_report',?,'authority-capture',?,'{ "coverage": "complete", "refs": ["aa", "bb"] }','freshness-upgrade','divergence-upgrade','verified','git_probe',?,'authority-verify','matched','[ "omitted-aa" ]',11,13)`, retirementUpgradeTime, "sha256:"+strings.Repeat("b", 64), retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_overlap_resolutions VALUES('resolution-upgrade',17,'product-upgrade','work-upgrade-from','work-upgrade-to',1,1,'compatible_with','Preserve reason: café\nsecond line','',?,19)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at) VALUES('assessment-upgrade','work-upgrade-from',1,1,'small','{}','{}',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_size_overrides VALUES('work-upgrade-from','event-upgrade-4',1,'larger',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM fold_guard`)
	retirementUpgradeExec(t, db, `INSERT INTO agent_installation_keys(key_name,key_bytes,created_at) VALUES('cursor',?,?)`, []byte(strings.Repeat("k", 32)), retirementUpgradeTime)
	retirementUpgradeHealthy(t, db)
	return db, path
}

func retirementUpgradeEnvelope(t *testing.T, db *sql.DB, seq int, id, kind string) {
	t.Helper()
	payload := fmt.Sprintf(`{ "witness": %d, "text": "preserve bytes" }`, seq)
	digest, err := effectPayloadDigest([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,'work_item','work-upgrade-from','operator',?,1,?)`, seq, id, kind, retirementUpgradeTime, payload)
	retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,'work_item','work-upgrade-from','operator',1,?)`, id, seq, kind, digest)
}

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
		state[name] = retirementUpgradeRows(t, db, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`" ORDER BY rowid`)
	}
	return state
}

func retirementUpgradeHealthy(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := retirementUpgradeRows(t, db, `PRAGMA foreign_keys`); !reflect.DeepEqual(got, [][]any{{int64(1)}}) {
		t.Fatalf("foreign_keys = %v, want ON", got)
	}
	if got := retirementUpgradeRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign_key_check = %v", got)
	}
}

func retirementUpgradeMigrate(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate v150 -> v151: %v", err)
	}
	got := retirementUpgradeRows(t, db, `SELECT name,checksum,breaking FROM schema_migrations WHERE version=151`)
	var expected [][]any
	for _, m := range migrations {
		if m.Version == 151 {
			expected = [][]any{{"event_log_retirement", m.checksum(), int64(1)}}
		}
	}
	if len(expected) != 1 || !reflect.DeepEqual(got, expected) {
		t.Fatalf("migration151 manifest = %v, want %v", got, expected)
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name IN ('domain_events','fold_guard') OR instr(sql,'domain_events')>0 OR instr(sql,'fold_guard')>0 OR instr(sql,'fold-only')>0`); len(got) != 0 {
		t.Fatalf("obsolete schema dependencies = %v", got)
	}
	retirementUpgradeHealthy(t, db)
}

func TestEventLogRetirementUpgradePreservesCurrentRows(t *testing.T) {
	db, path := retirementUpgradeFixture(t)
	tables := []string{"external_observations", "workflow_overlap_resolutions", "work_size_overrides"}
	before := make(map[string][][]any)
	indexes := make(map[string][][]any)
	indexColumns := make(map[string][][]any)
	for _, table := range tables {
		before[table] = retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
		indexes[table] = retirementUpgradeRows(t, db, `SELECT name,"unique",origin,partial FROM pragma_index_list(?) ORDER BY name`, table)
		for _, index := range indexes[table] {
			name := index[0].(string)
			indexColumns[name] = retirementUpgradeRows(t, db, `SELECT * FROM pragma_index_xinfo(?) ORDER BY seqno`, name)
		}
	}
	guards := retirementUpgradeRows(t, db, `SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND instr(sql,'fold_guard')>0 ORDER BY name`)
	if len(guards) == 0 {
		t.Fatal("fixture has no guarded triggers")
	}
	advanceHistoricalPrefix(t, db, 151)
	for _, guard := range guards {
		got := retirementUpgradeRows(t, db, `SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, guard[0])
		want := strings.ReplaceAll(strings.ReplaceAll(guard[1].(string), "fold_guard", "write_guard"), "fold-only", "write-seam-only")
		if !reflect.DeepEqual(got, [][]any{{want}}) {
			t.Errorf("guard %s changed beyond its rename: %v", guard[0], got)
		}
	}
	retirementUpgradeMigrate(t, db)
	for _, table := range tables {
		if got := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`); !reflect.DeepEqual(got, before[table]) {
			t.Errorf("%s changed bytes/types: before=%#v after=%#v", table, before[table], got)
		}
		if got := retirementUpgradeRows(t, db, `SELECT name,"unique",origin,partial FROM pragma_index_list(?) ORDER BY name`, table); !reflect.DeepEqual(got, indexes[table]) {
			t.Errorf("%s indexes changed: before=%v after=%v", table, indexes[table], got)
		}
		for _, operation := range []string{"INSERT INTO " + table + " SELECT * FROM " + table, "UPDATE " + table + " SET " + map[string]string{"external_observations": "subject_ref=subject_ref", "workflow_overlap_resolutions": "reason=reason", "work_size_overrides": "size_class=size_class"}[table], "DELETE FROM " + table} {
			if _, err := db.ExecContext(t.Context(), operation); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
				t.Errorf("unguarded %s = %v, want write-seam-only refusal", operation, err)
			}
		}
	}
	for name, want := range indexColumns {
		if got := retirementUpgradeRows(t, db, `SELECT * FROM pragma_index_xinfo(?) ORDER BY seqno`, name); !reflect.DeepEqual(got, want) {
			t.Errorf("index %s columns changed: before=%v after=%v", name, want, got)
		}
	}
	for table, want := range map[string][][]any{
		"external_observations":        {{"created_event_seq", "effect_receipts", "effect_seq", "RESTRICT"}, {"verified_event_seq", "effect_receipts", "effect_seq", "RESTRICT"}},
		"workflow_overlap_resolutions": {{"event_seq", "effect_receipts", "effect_seq", "RESTRICT"}, {"invalidated_seq", "effect_receipts", "effect_seq", "RESTRICT"}},
		"work_size_overrides":          {{"event_id", "effect_receipts", "event_id", "RESTRICT"}},
	} {
		got := retirementUpgradeRows(t, db, `SELECT "from","table","to",on_delete FROM pragma_foreign_key_list(?) WHERE "table"='effect_receipts' ORDER BY "from"`, table)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s receipt FKs = %v, want %v", table, got, want)
		}
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for _, query := range []string{
		`UPDATE external_observations SET created_event_seq=9999`,
		`UPDATE external_observations SET verified_event_seq=9999`,
		`UPDATE workflow_overlap_resolutions SET event_seq=9999`,
		`UPDATE workflow_overlap_resolutions SET invalidated_seq=9999`,
		`UPDATE work_size_overrides SET event_id='missing-receipt'`,
		`UPDATE work_size_overrides SET model_sequence=0`,
		`UPDATE workflow_overlap_resolutions SET from_work_id=to_work_id`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err == nil {
			t.Errorf("retained constraint accepted %s", query)
		}
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	state := retirementUpgradeSnapshot(t, db)
	for range 2 {
		if err := Migrate(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("Open migrated store: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, state) {
		t.Error("repeated migrate/open changed current store or manifest")
	}
}

func TestEventLogRetirementUpgradeBackfillsNullDigest(t *testing.T) {
	db, _ := retirementUpgradeFixture(t)
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM domain_events WHERE event_id='event-upgrade-0'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	want, err := effectPayloadDigest(payload)
	if err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `UPDATE effect_receipts SET payload_digest=NULL WHERE event_id='event-upgrade-0'`)
	retirementUpgradeMigrate(t, db)
	if got := retirementUpgradeRows(t, db, `SELECT payload_digest FROM effect_receipts WHERE event_id='event-upgrade-0'`); !reflect.DeepEqual(got, [][]any{{want}}) {
		t.Fatalf("backfilled digest = %v, want %q", got, want)
	}
}

func TestEventLogRetirementUpgradeRefusesAtomically(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
		repair string
	}{
		{"empty_digest", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `UPDATE effect_receipts SET payload_digest='' WHERE event_id='event-upgrade-1'`)
		}, `UPDATE effect_receipts SET payload_digest=NULL WHERE event_id='event-upgrade-1'`},
		{"short_digest", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `UPDATE effect_receipts SET payload_digest='sha256:aa' WHERE event_id='event-upgrade-1'`)
		}, `UPDATE effect_receipts SET payload_digest=NULL WHERE event_id='event-upgrade-1'`},
		{"nonhex_digest", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `UPDATE effect_receipts SET payload_digest=? WHERE event_id='event-upgrade-1'`, "sha256:"+strings.Repeat("z", 64))
		}, `UPDATE effect_receipts SET payload_digest=NULL WHERE event_id='event-upgrade-1'`},
		{"missing_digest_source", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES('orphan-receipt',24,'work.created','work_item','work-upgrade-from','operator',1); UPDATE store_sequences SET last=24 WHERE name='effect'`)
		}, `DELETE FROM effect_receipts WHERE event_id='orphan-receipt'`},
		{"lagged_effect", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=22 WHERE name='effect'`)
		}, `UPDATE store_sequences SET last=23 WHERE name='effect'`},
		{"missing_effect", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `DELETE FROM store_sequences WHERE name='effect'`)
		}, `INSERT INTO store_sequences VALUES('effect',23)`},
		{"lagged_relation", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=0 WHERE name='relation'`)
		}, `UPDATE store_sequences SET last=1 WHERE name='relation'`},
		{"missing_relation", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `DELETE FROM store_sequences WHERE name='relation'`)
		}, `INSERT INTO store_sequences VALUES('relation',1)`},
		{"stranded_guard", func(t *testing.T, db *sql.DB) { retirementUpgradeExec(t, db, `INSERT INTO fold_guard VALUES(1)`) }, `DELETE FROM fold_guard`},
		{"missing_receipt_identity", func(t *testing.T, db *sql.DB) {
			retirementUpgradeEnvelope(t, db, 29, "unreceipted-envelope", "work.updated")
			retirementUpgradeExec(t, db, `DELETE FROM effect_receipts WHERE event_id='unreceipted-envelope'`)
		}, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) SELECT event_id,seq,kind,subject_type,subject_id,actor,payload_version FROM domain_events WHERE event_id='unreceipted-envelope'`},
		{"orphan_receipt_identity", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) SELECT 'orphan-receipt',24,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts WHERE event_id='event-upgrade-0'; UPDATE store_sequences SET last=24 WHERE name='effect'`)
		}, `DELETE FROM effect_receipts WHERE event_id='orphan-receipt'`},
		{"mismatched_receipt_identity", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `UPDATE effect_receipts SET actor='different-operator' WHERE event_id='event-upgrade-0'`)
		}, `UPDATE effect_receipts SET actor='operator' WHERE event_id='event-upgrade-0'`},
		{"unknown_populated_log_fk", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `CREATE TABLE unknown_log_fk(seq INTEGER REFERENCES domain_events(seq) ON DELETE RESTRICT); INSERT INTO unknown_log_fk VALUES(11)`)
		}, `DROP TABLE unknown_log_fk`},
		{"unknown_empty_log_fk", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `CREATE TABLE unknown_log_fk(seq INTEGER REFERENCES domain_events(seq) ON DELETE RESTRICT)`)
		}, `DROP TABLE unknown_log_fk`},
		{"unknown_log_view", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `CREATE VIEW unknown_log_view AS SELECT seq FROM domain_events`)
		}, `DROP VIEW unknown_log_view`},
		{"unknown_log_trigger", func(t *testing.T, db *sql.DB) {
			retirementUpgradeExec(t, db, `CREATE TRIGGER unknown_log_trigger AFTER UPDATE ON products BEGIN SELECT count(*) FROM domain_events; END`)
		}, `DROP TRIGGER unknown_log_trigger`},
		{"lagged_deleted_log_high_water", func(t *testing.T, db *sql.DB) {
			retirementUpgradeDeletedHighWater(t, db)
			retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=23 WHERE name='effect'`)
		}, `UPDATE store_sequences SET last=1000 WHERE name='effect'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := retirementUpgradeFixture(t)
			tc.mutate(t, db)
			// A valid missing digest must not leak its backfill if any later
			// precondition or table rebuild refuses migration151.
			retirementUpgradeExec(t, db, `UPDATE effect_receipts SET payload_digest=NULL WHERE event_id='event-upgrade-4'`)
			before := retirementUpgradeSnapshot(t, db)
			err := Migrate(t.Context(), db)
			if err == nil {
				t.Fatal("Migrate accepted unsafe v150 fixture; want atomic migration151 refusal")
			}
			t.Logf("migration151 refusal: %v", err)
			if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("failed migration151 changed original schema, manifest, log, receipts, sequences, or current rows")
			}
			retirementUpgradeHealthy(t, db)
			retirementUpgradeExec(t, db, tc.repair)
			retirementUpgradeMigrate(t, db)
			state := retirementUpgradeSnapshot(t, db)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatalf("repeat valid migrate after refusal: %v", err)
			}
			if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, state) {
				t.Error("repeated valid migration changed store")
			}
		})
	}
}

func retirementUpgradeDeletedHighWater(t *testing.T, db *sql.DB) {
	t.Helper()
	retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(1000,'deleted-envelope','work.updated','work_item','work-upgrade-from','operator',?,1,'{}')`, retirementUpgradeTime)
	ddl := retirementUpgradeRows(t, db, `SELECT sql FROM sqlite_schema WHERE name='domain_events_no_delete'`)[0][0].(string)
	// Model a historical deletion while preserving every FK and CHECK. Restore
	// the exact append-only trigger before exercising the migration.
	retirementUpgradeExec(t, db, `DROP TRIGGER domain_events_no_delete; DELETE FROM domain_events WHERE seq=1000`)
	retirementUpgradeExec(t, db, ddl)
	if got := retirementUpgradeRows(t, db, `SELECT (SELECT max(seq) FROM domain_events),seq FROM sqlite_sequence WHERE name='domain_events'`); !reflect.DeepEqual(got, [][]any{{int64(23), int64(1000)}}) {
		t.Fatalf("deleted log high-water fixture = %v", got)
	}
}

func TestEventLogRetirementUpgradeWriterRollbackAndIdentity(t *testing.T) {
	for _, mode := range []string{"upgraded_gap", "fresh"} {
		t.Run(mode, func(t *testing.T) {
			var db *sql.DB
			var highWater int64
			if mode == "upgraded_gap" {
				db, _ = retirementUpgradeFixture(t)
				retirementUpgradeDeletedHighWater(t, db)
				highWater = 1000
			} else {
				var err error
				db, err = sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "fresh.db")))
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = db.Close() })
			}
			retirementUpgradeMigrate(t, db)
			before := retirementUpgradeSnapshot(t, db)
			e := testEvent()
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			seq := retirementUpgradeApplyProduct(t, tx, e)
			if seq <= highWater {
				t.Errorf("allocated sequence = %d, want > deleted high-water %d", seq, highWater)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("writer rollback changed receipts, current rows, guard, or sequences")
			}
			tx, err = db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			committedSeq := retirementUpgradeApplyProduct(t, tx, e)
			if committedSeq != seq {
				t.Errorf("rollback consumed effect position: first=%d committed=%d", seq, committedSeq)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			committed := retirementUpgradeSnapshot(t, db)
			for _, different := range []bool{false, true} {
				attempt := e
				attempt.OccurredAt = attempt.OccurredAt.AddDate(0, 0, 1)
				wantKind := KindDuplicateEvent
				if different {
					attempt.Payload = []byte(`{"display_name":"Different","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)
					wantKind = KindIdempotencyConflict
				}
				tx, err = db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				_, appendErr := AppendEvent(t.Context(), tx, attempt)
				var failure *Failure
				if !errors.As(appendErr, &failure) || failure.Kind != wantKind {
					t.Errorf("retry different=%v: %v, want %s", different, appendErr, wantKind)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, committed) {
					t.Fatal("duplicate/conflicting retry changed committed store")
				}
			}
			retirementUpgradeHealthy(t, db)
		})
	}
}

func retirementUpgradeApplyProduct(t *testing.T, tx *sql.Tx, e Event) Sequence {
	t.Helper()
	seq, err := AppendEvent(t.Context(), tx, e)
	if err != nil {
		t.Fatal(err)
	}
	e.Seq = seq
	scope, err := beginWrite(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEffect(t.Context(), tx, e, scope); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	return seq
}
