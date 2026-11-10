package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRedundantIndexesUpgrade(t *testing.T) {
	useStampedBuild(t)
	const predecessor = 192
	if len(migrations) < predecessor || migrations[predecessor-1].Version != predecessor {
		t.Fatalf("upgrade fixture requires migration %d; current schema is %d", predecessor, CurrentSchemaVersion())
	}
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "predecessor.db"), predecessor)
	ctx := t.Context()
	retirementUpgradeExec(t, db, `PRAGMA foreign_keys=ON`)
	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", fk, err)
	}
	exec := func(query string, args ...any) { retirementUpgradeExec(t, db, query, args...) }
	exec(`INSERT INTO write_guard(active) VALUES(1)`)
	for _, id := range []string{"work-design", "work-proposal", "work-native"} {
		exec(`INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES(?,'task','Witness','needed',1,'time','time')`, id)
	}
	exec(`INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,handoff_json,handoff_digest,state,event_id,created_at,updated_at) VALUES('op-one','idem-one','work-removed',2,'shelved','operator','{}',?,'committed','event-one','time','time')`, "sha256:"+strings.Repeat("a", 64))
	exec(`INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('work-design',1,'Approach','[1]','[1]','time'),('work-design',2,'Second approach','[2]','[2]','time')`)
	exec(`INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('work-proposal',1,'Problem','[1]','Stakes','[1]','[]','[]','time'),('work-proposal',2,'Second problem','[2]','Stakes','[2]','[]','[]','time')`)
	exec(`INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id,verification_state) VALUES('work-native','run-one','start','started','event-one','authority','actor','subject',?,'evidence','digest','time','time','trusted_client_report','{}','freshness','divergence','observation','verified'),('work-native','run-one','health','healthy','event-two','authority','actor','subject',?,'evidence','digest','time','time','trusted_client_report','{}','freshness','divergence',NULL,'unverified')`, "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("b", 64))
	exec(`DELETE FROM write_guard`)

	tables := []string{"work_removal_operations", "workflow_design_records", "workflow_proposal_records", "workflow_native_runs"}
	indexes := []string{"work_removal_operations_work", "workflow_design_records_latest", "workflow_proposal_records_latest", "workflow_native_runs_work"}
	before := make(map[string][][]any)
	for i, table := range tables {
		before[table] = retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
		if len(before[table]) == 0 {
			t.Fatalf("no predecessor rows in %s", table)
		}
		if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE type='index' AND name=?`, indexes[i]); len(got) != 1 {
			t.Fatalf("predecessor index %s: %v", indexes[i], got)
		}
	}

	// Exercise the index retirement at its historical boundary.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, tx, migrations[192]); err != nil {
		_ = tx.Rollback()
		t.Fatalf("upgrade: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,breaking,applied_at) VALUES(?,?,?,?,?)`, migrations[192].Version, migrations[192].Name, migrations[192].checksum(), migrations[192].Breaking, "time"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version, breaking int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version < 193 {
		t.Fatalf("upgrade must apply migration 193: version=%d err=%v", version, err)
	}
	// Open applies an additive step without the upgrade gate.
	if err := db.QueryRowContext(ctx, `SELECT breaking FROM schema_migrations WHERE version=193`).Scan(&breaking); err != nil || breaking != 0 {
		t.Fatalf("migration 193 breaking=%d err=%v", breaking, err)
	}
	for i, table := range tables {
		if got := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`); !reflect.DeepEqual(got, before[table]) {
			t.Errorf("%s rows changed: got %v, want %v", table, got, before[table])
		}
		if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE type='index' AND name=?`, indexes[i]); len(got) != 0 {
			t.Errorf("retired index %s remains: %v", indexes[i], got)
		}
	}

	// The guard permits writes so failures below must come from SQLite constraints.
	exec(`INSERT INTO write_guard(active) VALUES(1)`)
	for _, tc := range []struct{ name, query, want string }{
		{"removal work unique", `INSERT INTO work_removal_operations SELECT 'op-two','idem-two',work_id,expected_version,reason,actor,product_id,handoff_json,handoff_digest,state,event_id,created_at,updated_at,linear_confirmation_json FROM work_removal_operations WHERE operation_id='op-one'`, "UNIQUE constraint failed: work_removal_operations.work_id"},
		{"design primary key", `INSERT INTO workflow_design_records SELECT * FROM workflow_design_records WHERE work_id='work-design' AND work_version=1`, "UNIQUE constraint failed: workflow_design_records.work_id, workflow_design_records.work_version"},
		{"proposal primary key", `INSERT INTO workflow_proposal_records SELECT * FROM workflow_proposal_records WHERE work_id='work-proposal' AND work_version=1`, "UNIQUE constraint failed: workflow_proposal_records.work_id, workflow_proposal_records.work_version"},
		{"native primary key", `INSERT INTO workflow_native_runs SELECT * FROM workflow_native_runs WHERE work_id='work-native' AND phase='start'`, "UNIQUE constraint failed: workflow_native_runs.work_id, workflow_native_runs.run_id, workflow_native_runs.phase"},
		{"design parent", `DELETE FROM work_items WHERE id='work-design'`, "FOREIGN KEY constraint failed"},
		{"proposal parent", `DELETE FROM work_items WHERE id='work-proposal'`, "FOREIGN KEY constraint failed"},
		{"native parent", `DELETE FROM work_items WHERE id='work-native'`, "FOREIGN KEY constraint failed"},
		{"design orphan", `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('missing',1,'Approach','[1]','[1]','time')`, "FOREIGN KEY constraint failed"},
		{"proposal orphan", `INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('missing',1,'Problem','[1]','Stakes','[1]','[]','[]','time')`, "FOREIGN KEY constraint failed"},
		{"native orphan", `INSERT INTO workflow_native_runs SELECT 'missing',run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id,verification_state FROM workflow_native_runs WHERE work_id='work-native' AND phase='start'`, "FOREIGN KEY constraint failed"},
	} {
		if _, err := db.ExecContext(ctx, tc.query); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
	exec(`DELETE FROM write_guard`)

	// A guarded work-item insert plans foreign-key child probes, so the parent
	// insert and delete must each search every child table by work_id.
	const workInsert = `INSERT INTO work_items (id, kind, title, lifecycle, version, created_at, updated_at, terminal_time, intent_json, transition_actor, transition_seq) VALUES (?, ?, ?, 'needed', 1, ?, ?, NULL, ?, ?, ?)`
	workInsertArgs := []any{"work-new", "task", "Witness", "time", "time", "{}", "operator", 1}
	for _, tc := range []struct {
		query, table string
		args         []any
	}{
		{`SELECT operation_id FROM work_removal_operations WHERE idempotency_key=? OR work_id=? LIMIT 1`, "work_removal_operations", []any{"idem-one", "work-removed"}},
		{`SELECT state FROM work_removal_operations WHERE work_id=? ORDER BY CASE state WHEN 'committed' THEN 0 ELSE 1 END LIMIT 1`, "work_removal_operations", []any{"work-removed"}},
		{`SELECT expected_version FROM work_removal_operations WHERE work_id=? AND event_id=? AND state='committed'`, "work_removal_operations", []any{"work-removed", "event-one"}},
		{`SELECT run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,COALESCE(observation_id,''),COALESCE(verification_state,'unverified') FROM workflow_native_runs WHERE work_id=? ORDER BY run_id,phase`, "workflow_native_runs", []any{"work-native"}},
		{`SELECT COALESCE(verification_state,'unverified') FROM workflow_native_runs WHERE work_id=? AND observation_id=?`, "workflow_native_runs", []any{"work-native", "observation"}},
		{`UPDATE workflow_native_runs SET verification_state=? WHERE work_id=? AND observation_id=?`, "workflow_native_runs", []any{"verified", "work-native", "observation"}},
		{`DELETE FROM workflow_design_records WHERE work_id=?`, "workflow_design_records", []any{"work-design"}},
		{`DELETE FROM workflow_proposal_records WHERE work_id=?`, "workflow_proposal_records", []any{"work-proposal"}},
		{`DELETE FROM workflow_native_runs WHERE work_id=?`, "workflow_native_runs", []any{"work-native"}},
		{`DELETE FROM work_items WHERE id=?`, "workflow_design_records", []any{"work-design"}},
		{`DELETE FROM work_items WHERE id=?`, "workflow_proposal_records", []any{"work-proposal"}},
		{`DELETE FROM work_items WHERE id=?`, "workflow_native_runs", []any{"work-native"}},
		{workInsert, "workflow_design_records", workInsertArgs},
		{workInsert, "workflow_proposal_records", workInsertArgs},
		{workInsert, "workflow_native_runs", workInsertArgs},
	} {
		assertRedundantIndexSearch(t, db, tc.query, tc.table, tc.args...)
	}
}

func assertRedundantIndexSearch(t *testing.T, db *sql.DB, query, table string, args ...any) {
	t.Helper()
	rows := retirementUpgradeRows(t, db, `EXPLAIN QUERY PLAN `+query, args...)
	for _, row := range rows {
		detail, ok := row[3].(string)
		if ok && strings.Contains(detail, "SEARCH "+table+" USING") && strings.Contains(detail, "INDEX") && strings.Contains(detail, "work_id=?") {
			return
		}
	}
	t.Errorf("no indexed work_id search for %s in plan for %s: %v", table, query, rows)
}
