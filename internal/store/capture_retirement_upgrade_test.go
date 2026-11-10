package store

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func captureRetirementMigration(t *testing.T) migration {
	t.Helper()
	m := migrations[migrationPrefixBefore(t, "minimal_capture_retirement")]
	if m.Version != 186 {
		t.Fatalf("expected migration 186, got %d", m.Version)
	}
	return m
}

func TestCaptureRetirementPreservesForeignKeys(t *testing.T) {
	_, db := outcomeRetirementFixture(t)
	advanceHistoricalPrefix(t, db, 185)
	ctx := verifiedOutcomeRetirementContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := applyMigration(ctx, tx, captureRetirementMigration(t)); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var fk int
		if err := rows.Scan(&table, &rowID, &parent, &fk); err != nil {
			t.Fatal(err)
		}
		t.Errorf("foreign key violation: %s %v %s %d", table, rowID, parent, fk)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func captureRetirementFixture(t *testing.T) *sql.DB {
	t.Helper()
	_, db := outcomeRetirementFixture(t)
	seedRetirementRetainedFacts(t, db)
	advanceHistoricalPrefix(t, db, 185)
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	_, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE work_items SET lifecycle='superseded',transition_from='superseded',intent_json='{"task":"do this","title":"old title","kind":"bug","old_field":"discard"}' WHERE id='work-one';
INSERT INTO workflow_instances(work_id,instance_state) VALUES('work-one','superseded');
INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES('work-one','step',2);
INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq) VALUES('work-one','exit',41);
INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES('work-one',41,'binding:one',?1,'{}');
INSERT INTO work_version_actions VALUES('work-one',2,'action','session:one',41);
INSERT INTO workflow_completed_exits VALUES('work-one','definition','step','action',41);
INSERT INTO workflow_coordinator_actions VALUES('work-one',?1,41,'action','time');
INSERT INTO workflow_delivery_state VALUES('work-one',41,'delivery:one',?1,'time',1,'{"delivery_artifact":"artifact:one","delivery_state":"asserted"}');
INSERT INTO work_context_state(work_id,source_frontier) VALUES('work-one',41);
INSERT INTO work_context_findings VALUES('work-one',41,1,'generic','{}');
INSERT INTO project_governing_requirements VALUES('project-one','requirement:one','reason','time');
INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,first_observed_at,last_observed_at) VALUES('principal:one','tool','operation','key','digest','operation:one','time','time');
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES('event:retired',42,'workflow.overlap','work_item','work-one',?1,1),('event:merged',43,'workflow.overlap','work_item','work-one',?1,1);
INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,created_at) VALUES('resolution:retired',42,'product-one','work-one','work-two',1,1,'supersedes','reason','time'),('resolution:merged',43,'product-one','work-one','work-two',1,1,'merged_into','reason','time');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at,resolution_id) VALUES('work-one','work-two','supersedes','time','resolution:retired'),('work-one','work-two','merged_into','time','resolution:merged');
DELETE FROM write_guard;`, actor)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCaptureRetirementPreservesSeededRows(t *testing.T) {
	db := captureRetirementFixture(t)
	// A superseded holder's link survives beside a live holder of the same key,
	// as a cancelled holder's link must after migration 186.
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET lifecycle='needed' WHERE id='work-two'; INSERT INTO linear_issue_links VALUES('work-one','uuid:terminal','TEST-7','https://example.com/7','t','t'),('work-two','uuid:live','TEST-7','https://example.com/7','t','t');
INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('work-one',1,'Approach','[1]','[1]','time');
INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('work-one',1,'Problem','[1]','Stakes','[1]','[]','[]','time');
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeSnapshot(t, db)
	if want := [][]any{{"effect", int64(0)}, {"relation", int64(0)}}; !reflect.DeepEqual(before["store_sequences"], want) {
		t.Fatalf("pre-upgrade store sequences = %v, want %v", before["store_sequences"], want)
	}
	before["store_sequences"] = [][]any{{"effect", int64(0)}, {"relation", int64(3)}}
	workQuery := `SELECT id,kind,title,version,created_at,updated_at,terminal_time,narrative,effect_seq,last_progress_at,version_actions_floor,transition_reason,transition_actor,transition_evidence_refs,transition_seq,transition_version FROM work_items ORDER BY id`
	instanceQuery := `SELECT work_id,workflow_type,step_plan,prelude_state,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,completion_payload,completion_occurred_at FROM workflow_instances ORDER BY work_id`
	workBefore := retirementUpgradeRows(t, db, workQuery)
	instanceBefore := retirementUpgradeRows(t, db, instanceQuery)
	for _, table := range []string{"workflow_step_epochs", "workflow_phase_exits", "workflow_evidence_bindings", "work_version_actions", "workflow_completed_exits", "workflow_coordinator_actions", "workflow_delivery_state", "idempotency_records", "effect_receipts", "workflow_contracts", "work_projects", "product_projects", "linear_issue_links", "workflow_design_records", "workflow_proposal_records"} {
		if len(before[table]) == 0 {
			t.Fatalf("empty fixture table %s", table)
		}
	}
	frontier := retirementUpgradeRows(t, db, `SELECT seq FROM sqlite_sequence WHERE name='relations'`)[0][0].(int64)
	ctx := outcomeSnapshotContext(t, t.Context(), db)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	after := retirementUpgradeSnapshot(t, db)
	if got := retirementUpgradeRows(t, db, workQuery); !reflect.DeepEqual(got, workBefore) {
		t.Errorf("other work fields changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, instanceQuery); !reflect.DeepEqual(got, instanceBefore) {
		t.Errorf("other instance fields changed: %v", got)
	}
	m := captureRetirementMigration(t)
	if got := retirementUpgradeRows(t, db, `SELECT name,checksum,breaking FROM schema_migrations WHERE version=186`); !reflect.DeepEqual(got, [][]any{{m.Name, m.checksum(), int64(1)}}) {
		t.Errorf("migration manifest: %v", got)
	}
	for table, rows := range before {
		switch table {
		case "workflow_design_records", "workflow_proposal_records":
			if schema := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name=? OR tbl_name=?`, table, table); len(schema) != 0 {
				t.Errorf("retired schema %s survives: %v", table, schema)
			}
			continue
		case "schema", "schema_migrations", "sqlite_sequence", "store_revision", "work_items", "workflow_instances", "relations", "workflow_overlap_resolutions", "work_context_state", "work_context_findings", "project_governing_requirements", "workflow_context_checkpoints", "workflow_context_boundaries", "workflow_backlog_alignment":
			continue
		}
		if !reflect.DeepEqual(rows, after[table]) {
			t.Errorf("%s changed: before=%v after=%v", table, rows, after[table])
		}
	}
	priorRevision := before["store_revision"]
	if len(priorRevision) != 1 || len(priorRevision[0]) != 2 {
		t.Fatalf("unexpected pre-upgrade store revision: %v", priorRevision)
	}
	revision, ok := priorRevision[0][1].(int64)
	if !ok {
		t.Fatalf("unexpected pre-upgrade store revision: %v", priorRevision)
	}
	wantRevision := [][]any{{priorRevision[0][0], revision + int64(len(migrations)-migrationPrefixBefore(t, "minimal_capture_retirement"))}}
	if !reflect.DeepEqual(after["store_revision"], wantRevision) {
		t.Errorf("store revision = %v, want %v", after["store_revision"], wantRevision)
	}
	for _, table := range []string{"work_context_state", "work_context_findings", "project_governing_requirements"} {
		if _, ok := after[table]; ok {
			t.Errorf("%s survived", table)
		}
	}
	var intent string
	var lifecycle, transition, instance string
	if err := db.QueryRow(`SELECT intent_json,lifecycle,transition_from FROM work_items WHERE id='work-one'`).Scan(&intent, &lifecycle, &transition); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='work-one'`).Scan(&instance); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(intent), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]any{"title": "Fixture", "kind": "task", "task": "do this"}) || lifecycle != "cancelled" || transition != "cancelled" || instance != "cancelled" {
		t.Errorf("intent=%v lifecycle=%s transition=%s instance=%s", got, lifecycle, transition, instance)
	}
	for _, table := range []string{"relations", "workflow_overlap_resolutions"} {
		if len(after[table]) != 1 || !reflect.DeepEqual(after[table][0], before[table][0]) {
			t.Errorf("retained %s: before=%v after=%v", table, before[table], after[table])
		}
	}
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('work-two','work-one','blocks','time'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM relations WHERE kind='blocks'`).Scan(&id); err != nil || id <= frontier {
		t.Fatalf("new relation id=%d frontier=%d err=%v", id, frontier, err)
	}
	retirementUpgradeHealthy(t, db)
	if got := retirementUpgradeRows(t, db, `PRAGMA integrity_check`); !reflect.DeepEqual(got, [][]any{{"ok"}}) {
		t.Fatalf("integrity_check: %v", got)
	}
}

func TestCaptureRetirementSnapshotRefusalDoesNotMutate(t *testing.T) {
	for _, tc := range []string{"absent", "mismatch"} {
		t.Run(tc, func(t *testing.T) {
			db := captureRetirementFixture(t)
			ctx := t.Context()
			if tc == "mismatch" {
				ctx = outcomeSnapshotContext(t, ctx, db)
				if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET title='changed' WHERE id='work-one'; DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
			}
			before := retirementUpgradeSnapshot(t, db)
			if err := Migrate(ctx, db); !hasFailureKind(err, KindUpgradeBlocked) {
				t.Fatalf("%s snapshot: %v", tc, err)
			}
			if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
				t.Fatal("snapshot refusal changed schema, manifest, or rows")
			}
			retirementUpgradeHealthy(t, db)
		})
	}
}

func TestCaptureRetirementDuplicateLiveKeyRefusesWithoutMutation(t *testing.T) {
	db := captureRetirementFixture(t)
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_items SET lifecycle='needed' WHERE id='work-one'; INSERT INTO linear_issue_links VALUES('work-one','uuid:one','TEST-1','https://example.com/1','t','t'),('work-two','uuid:two','TEST-1','https://example.com/2','t','t'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeSnapshot(t, db)
	ctx := outcomeSnapshotContext(t, t.Context(), db)
	if err := Migrate(ctx, db); !hasFailureKind(err, KindUpgradeBlocked) || !strings.Contains(err.Error(), "TEST-1") {
		t.Fatalf("collision: %v", err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("collision changed schema, manifest, or rows")
	}
	retirementUpgradeHealthy(t, db)
}

func TestCaptureRetirementLaterFailureRollsBack(t *testing.T) {
	db := captureRetirementFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	ctx := verifiedOutcomeRetirementContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := captureRetirementMigration(t)
	m.SQL += `
INSERT INTO nonexistent_capture_failure VALUES(1);`
	if err := applyMigration(ctx, tx, m); err == nil || !strings.Contains(err.Error(), "nonexistent_capture_failure") {
		t.Fatalf("later failure: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("rollback changed schema, manifest, or rows")
	}
	retirementUpgradeHealthy(t, db)
	if got := retirementUpgradeRows(t, db, `PRAGMA integrity_check`); !reflect.DeepEqual(got, [][]any{{"ok"}}) {
		t.Fatalf("integrity_check: %v", got)
	}
}
