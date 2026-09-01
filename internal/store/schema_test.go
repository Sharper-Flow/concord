package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readSchemaManifestVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func TestOpenAppliesSchemaManifest(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	var applied int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Fatalf("applied migrations = %d, want %d", applied, len(migrations))
	}

	got, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("schema manifest version = %d, want %d", got, want)
	}
}

func TestMigrateV60ToV61PreservesWorkflowContractForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v60.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:60] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-20T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	actorRef := DeriveWorkflowActorRef("principal:migration", "client:migration", "agent:migration", "session:migration")
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-v61','Product','prototype','operator_only',1,'now','now')`, nil},
		{`INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at,intent_json) VALUES('work-v61','task','Work','needed',0,'standard',1,'now','now','{}')`, nil},
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:migration','client:migration','agent:migration','session:migration','operator','now')`, []any{actorRef}},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-v61',1,'migration','check','{"kind":"check","check_ref":"check:migration"}','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal')`, []any{actorRef}},
		{`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES('work-v61',1,'product-v61',?,'root',?)`, []any{hash, hash}},
		{`INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('work-v61',1,'root')`, nil},
		{`INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('work-v61',1,'root')`, nil},
		{`INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product-v61','law:migration','work-v61',1,'root')`, nil},
		{`INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('work-v61',1,'product-v61','law:migration','root','work-v61',1)`, nil},
		{`INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES('work-v61',1,'law:migration','obligation:migration')`, nil},
		{`INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('work-v61',1,'law:migration')`, nil},
		{`INSERT INTO workflow_candidate_sets(work_id,contract_version,candidate_kind,candidate_ref,candidate_role,candidate_scope,recorded_at,recorded_by) VALUES('work-v61',1,'work_item','candidate:v61','include','migration fixture','now',?)`, []any{actorRef}},
		{`INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES('work-v61',1,?,'now')`, []any{actorRef}},
		{`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('work-v61',1,'law:migration',?)`, []any{hash}},
		{`INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at,intent_json) VALUES('work-v61b','task','Overlap peer','needed',0,'standard',1,'now','now','{}')`, nil},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-v61b',1,'migration peer','check','{"kind":"check","check_ref":"check:migration-peer"}','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal')`, []any{actorRef}},
		{`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('evt-v61','workflow.overlap_resolved','work_item','work-v61',?,'now',1,'{}')`, []any{actorRef}},
		{`INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,approval_ref,created_at) VALUES('resolution-v61',1,'product-v61','work-v61','work-v61b',1,1,'compatible_with','migration fixture','approval:migration','now')`, nil},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed v60: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	var kind, payload string
	if err := db.QueryRowContext(ctx, `SELECT outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id='work-v61' AND contract_version=1 AND predicate_id='predicate:primary'`).Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "check" || payload != `{"kind":"check","check_ref":"check:migration"}` {
		t.Fatalf("predicate kind=%q payload=%q", kind, payload)
	}
	for _, table := range []string{
		"workflow_architecture_bindings",
		"workflow_contract_affected_domains",
		"workflow_contract_domain_modifications",
		"workflow_contract_law_additions",
		"workflow_contract_verification_obligations",
		"workflow_contract_law_modifications",
		"workflow_candidate_sets",
		"workflow_premise_confirmations",
		"workflow_contract_law_revisions",
	} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE work_id='work-v61' AND contract_version=1").Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s rows=%d err=%v", table, count, err)
		}
	}
	var overlaps int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_overlap_resolutions WHERE from_work_id='work-v61' AND to_work_id='work-v61b' AND from_contract_version=1 AND to_contract_version=1`).Scan(&overlaps); err != nil || overlaps != 1 {
		t.Fatalf("workflow_overlap_resolutions rows=%d err=%v", overlaps, err)
	}
	var peerPredicates int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_predicates WHERE work_id='work-v61b' AND contract_version=1 AND predicate_id='predicate:primary'`).Scan(&peerPredicates); err != nil || peerPredicates != 1 {
		t.Fatalf("peer contract predicate rows=%d err=%v", peerPredicates, err)
	}
	var fkViolations int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fkViolations); err != nil || fkViolations != 0 {
		t.Fatalf("foreign_key_check rows=%d err=%v", fkViolations, err)
	}
	for _, column := range []string{"outcome_kind", "outcome_payload"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name=?`, column).Scan(&count); err != nil || count != 0 {
			t.Fatalf("workflow_contracts.%s count=%d err=%v", column, count, err)
		}
	}
}

func TestMigrateV39ToV40BackfillsLawModificationsAndGuardsOverlapAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v39.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:39] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-19T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	actorRef := DeriveWorkflowActorRef("principal:v39", "client:v39", "agent:v39", "session:v39")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-v39','Product','prototype','operator_only',1,'now','now')`, nil},
		{`INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at,intent_json) VALUES('work-v39','task','Work','needed',0,'standard',1,'now','now','{}')`, nil},
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:v39','client:v39','agent:v39','session:v39','operator','now')`, []any{actorRef}},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-v39',1,'migration','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,'["law:a","law:b"]','["law:a","law:b"]',1,'prototype_internal')`, []any{actorRef}},
		{`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES('work-v39',1,'product-v39',?,'root',?)`, []any{hash, hash}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed v39: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var laws string
	if err := db.QueryRowContext(ctx, `SELECT group_concat(law_id,',') FROM (SELECT law_id FROM workflow_contract_law_modifications WHERE work_id='work-v39' ORDER BY law_id)`).Scan(&laws); err != nil {
		t.Fatal(err)
	}
	if laws != "law:a,law:b" {
		t.Fatalf("v40 law-modification backfill=%q", laws)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('work-v39',1,'law:c')`); err == nil || !strings.Contains(err.Error(), "fold-only") {
		t.Fatalf("law-modification projection bypassed fold guard: %v", err)
	}
	for _, index := range []string{"workflow_overlap_resolutions_pair", "workflow_overlap_resolutions_reverse_pair", "relations_merged_into_source"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v40 index %s count=%d err=%v", index, count, err)
		}
	}
}

func TestMigrateV18ToV19AddsClosedKnowledgeCoverageAndScopeGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v18.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:18] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-10T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('p','l','HEAD','lesson','indexed','test','`+strings.Repeat("a", 40)+`')`); err == nil {
		t.Fatal("coverage write bypassed fold guard")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('p','l','HEAD','invalid','indexed','test','`+strings.Repeat("a", 40)+`')`); err == nil {
		t.Fatal("invalid coverage kind passed CHECK")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES('w','lesson','T','2026-08-10T00:00:00Z','published','[]','completed',0,'S','p','l','docs/lessons/t.md','`+strings.Repeat("a", 40)+`','sha256:`+strings.Repeat("b", 64)+`','invalid')`); err == nil {
		t.Fatal("invalid scope mode passed CHECK")
	}
	// The migration 52 pair binding makes the anchor a precondition for a
	// successful knowledge write, so seed the referenced Project locator in
	// the same guarded window.
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('p','P',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('l','p','canonical_path','/test/l','/test/l','t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('p','l','HEAD','lesson','indexed','test','`+strings.Repeat("a", 40)+`')`); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateV19ToV20AddsProjectStageOverridesAndC14OrderingIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v19.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:19] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-10T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE projects SET stage_maturity_override='alpha' WHERE id='project'`); err == nil {
		t.Fatal("partial Project stage override bypassed pair invariant")
	}
	if _, err := db.ExecContext(ctx, `UPDATE projects SET stage_maturity_override='invalid', stage_audience_commitment_override='public' WHERE id='project'`); err == nil {
		t.Fatal("invalid Project maturity override bypassed closed constraint")
	}
	for _, index := range []string{"products_display_name_order"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count=%d err=%v", index, count, err)
		}
	}
}

func TestMigrateV20ToV21AddsDerivedLawProjectionAndAmendmentField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v20.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:20] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-11T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, table := range []string{"law_subjects", "law_relations"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit')`); err == nil {
		t.Fatal("law subject write bypassed fold guard")
	}
	var lawModifies, lawBoundaryVersion int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='law_modifies'`).Scan(&lawModifies); err != nil || lawModifies != 1 {
		t.Fatalf("law_modifies column count=%d err=%v", lawModifies, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='law_boundary_version'`).Scan(&lawBoundaryVersion); err != nil || lawBoundaryVersion != 1 {
		t.Fatalf("law_boundary_version column count=%d err=%v", lawBoundaryVersion, err)
	}
}

func TestMigrateV22ToV23AddsBoundedInitiativeNarrative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v22.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:22] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-11T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('pre-existing','initiative','Initiative','needed',1,1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var narrative string
	if err := db.QueryRowContext(ctx, `SELECT narrative FROM work_items WHERE id='pre-existing'`).Scan(&narrative); err != nil || narrative != "" {
		t.Fatalf("pre-existing narrative=%q err=%v, want empty default", narrative, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `UPDATE work_items SET narrative=? WHERE id='pre-existing'`, strings.Repeat("n", 16385)); err == nil {
		t.Fatal("oversize narrative bypassed bounded CHECK")
	}
	if _, err := db.ExecContext(ctx, `UPDATE work_items SET narrative=? WHERE id='pre-existing'`, strings.Repeat("n", 16384)); err != nil {
		t.Fatalf("bounded narrative rejected: %v", err)
	}
}

func TestMigrateV24ToV25AddsRoutingResolutionEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v24.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:25] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-11T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// Migrations beyond 25 are applied manually through 42 so the routing
	// columns (which migration 44 drops) and v25's hardcoded DEFAULT literal
	// (which migration 43 rewrites to a function call) remain visible to
	// this v25-shape test. Migrations 43 and 44 have their own coverage in
	// TestMigrateV43ToV44DropsWorkerRoutingEvidenceAndPreservesRows.
	for _, migration := range migrations[25:42] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-22T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"routing_policy_digest", "resolution_role", "fallback_reason"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('worker_attempts') WHERE name=?`, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("column %s count=%d err=%v", column, count, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,resolved_model,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('work','attempt', 'research',1,?,'research','routing-v1','openai/gpt-5.6-luna','', '1.0','1.0','dispatched','now')`, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatalf("default routing evidence insert failed: %v", err)
	}
	var digest, role, reason string
	if err := db.QueryRowContext(ctx, `SELECT routing_policy_digest,resolution_role,fallback_reason FROM worker_attempts WHERE attempt_id='attempt'`).Scan(&digest, &role, &reason); err != nil {
		t.Fatal(err)
	}
	if digest != RoutingPolicyManifestDigest || role != "preferred" || reason != "" {
		t.Fatalf("migration defaults = %s/%s/%s", digest, role, reason)
	}
	if _, err := db.ExecContext(ctx, `UPDATE worker_attempts SET resolution_role='fallback', fallback_reason='' WHERE attempt_id='attempt'`); err == nil {
		t.Fatal("fallback without typed reason bypassed CHECK")
	}
}

// TestMigrateV43ToV44DropsWorkerRoutingEvidenceAndPreservesRows covers CD-0058
// D4: the declared-side worker attempt columns are removed under a rename +
// recreate + copy + drop, every pre-existing row survives, and the lifecycle
// CHECK that references readback_model is preserved.
func TestMigrateV43ToV44DropsWorkerRoutingEvidenceAndPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v43.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:43] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-22T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('pre-existing-work','pre-existing','research',1,?,'research','routing-v1',?,'openai/gpt-5.6-luna','preferred','','openai/gpt-5.6-luna','1.0','1.0','dispatched','2026-08-22T00:00:00Z')`, "sha256:"+strings.Repeat("a", 64), RoutingPolicyManifestDigest); err != nil {
		t.Fatalf("seed v43 worker attempt: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// The fold guard must be re-deactivated so a direct INSERT is rejected
	// by the trigger rather than permitted because the guard is open.
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"routing_policy_version", "routing_policy_digest", "resolved_model", "resolution_role", "fallback_reason"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('worker_attempts') WHERE name=?`, column).Scan(&count); err != nil || count != 0 {
			t.Fatalf("column %s count=%d err=%v, want 0", column, count, err)
		}
	}
	var readback, lifecycle, capability string
	if err := db.QueryRowContext(ctx, `SELECT readback_model,lifecycle_state,capability_class FROM worker_attempts WHERE attempt_id='pre-existing'`).Scan(&readback, &lifecycle, &capability); err != nil {
		t.Fatalf("pre-existing row missing after migration 44: %v", err)
	}
	if readback != "openai/gpt-5.6-luna" || lifecycle != "dispatched" || capability != "research" {
		t.Fatalf("pre-existing row projection = %q/%q/%q, want preserved readback/lifecycle/capability", readback, lifecycle, capability)
	}
	// The lifecycle CHECK that references readback_model survives the
	// migration; a 'completed' attempt must still carry a 3+-char readback.
	if _, err := db.ExecContext(ctx, `UPDATE worker_attempts SET lifecycle_state='completed', readback_model='', completed_at='2026-08-22T00:00:00Z' WHERE attempt_id='pre-existing'`); err == nil {
		t.Fatal("completed-with-empty-readback bypassed the lifecycle CHECK")
	}
	// The fold-only triggers must be reinstalled so direct INSERT is refused.
	if _, err := db.ExecContext(ctx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('direct','direct','research',1,?,'research','openai/gpt-5.6-luna','1.0','1.0','dispatched','2026-08-22T00:00:00Z')`, "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("direct INSERT bypassed the fold guard")
	}
}

func TestMigrateV36ToV37AddsWorkflowLawRevisionProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v36.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:36] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-17T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var tableCount, indexCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workflow_contract_law_revisions'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='workflow_contract_law_revisions_reverse'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 || indexCount != 1 {
		t.Fatalf("law revision projection table/index = %d/%d", tableCount, indexCount)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('missing',1,'spec:law','sha256:`+strings.Repeat("a", 64)+`')`); err == nil {
		t.Fatal("law revision projection write bypassed fold guard or foreign keys")
	}
}

func TestMigrateV8ToV9AddsAgentAuthorityWithoutChangingPriorMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v8.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:8] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// agent_grants is absent from this list because migration 57 drops it
	// (CD-0080 D2). Migration 9 still creates it; the table this sequence
	// must arrive at is the one head declares.
	for _, table := range []string{"agent_clients", "agent_client_keys", "agent_nonce_replay", "agent_approval_challenges", "agent_approvals", "idempotency_records"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("table %s missing after v8->v9 migration", table)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", version, CurrentSchemaVersion())
	}
}

func TestMigrateV27ToV28PreservesImpactNoticesWithSourceOwnedEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v27.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:27] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-13T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,urgency,priority,version,created_at,updated_at) VALUES
('legacy-source','task','Source','needed','standard',1,1,'now','now'),
('legacy-target','task','Target','needed','standard',1,1,'now','now');
INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at)
VALUES('legacy-source','edge:legacy','depends_on','hard','legacy-target','work_item','breaking','now');
INSERT INTO workflow_impact_notices(notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_id,old_hash,new_hash,severity,recorded_at)
VALUES(?,'legacy-source',1,'spec','spec:one','legacy-target','edge:legacy',NULL,NULL,'breaking','now');
DELETE FROM fold_guard`, WorkflowNoticeID("legacy-source", 1, "spec", "spec:one", "legacy-target", "breaking")); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.QueryRowContext(ctx, `SELECT edge_owner_work_id FROM workflow_impact_notices WHERE source_work_id='legacy-source'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "legacy-source" {
		t.Fatalf("migrated edge owner = %q, want legacy-source", owner)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	before, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if err := Migrate(ctx, s.DatabaseForTesting()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	after, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if before != after {
		t.Errorf("schema version moved on re-migration: %d -> %d", before, after)
	}

	var applied int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

func TestMigrateV7ToV8PreservesValidMultiParentRelations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:7] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-07T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1); INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p','P','prototype','operator_only',1,'now','now'); INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('pr','PR',1,'now','now'); INSERT INTO product_projects(product_id,project_id,role) VALUES('p','pr','primary'); INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('parent','task','Parent','needed',1,1,'now','now'),('child-a','task','A','needed',1,1,'now','now'),('child-b','task','B','needed',1,1,'now','now'); INSERT INTO work_projects(work_id,project_id,role) VALUES('parent','pr','primary'),('child-a','pr','primary'),('child-b','pr','primary'); INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('parent','child-a','parent','now'),('parent','child-b','parent','now'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE kind='parent' AND work_id_from='parent'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("multi-parent relation count = %d, want 2", count)
	}
}

func TestMigrateLeavesPopulatedVersion3DatabaseUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	db := seedVersion3Database(t, path)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard (active) VALUES (1)`); err != nil {
		t.Fatalf("enable fold guard: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO products (id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at) VALUES ('product-1', 'Concord', 'prototype', 'operator_only', 1, '2026-08-07T12:00:00Z', '2026-08-07T12:00:00Z')`); err != nil {
		t.Fatalf("insert v3 Product fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard WHERE active = 1`); err != nil {
		t.Fatalf("disable fold guard: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("seed database Close() error = %v", err)
	}

	_, err := Open(ctx, path)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Open() error = %v, want *Failure", err)
	}
	if failure.Kind != KindMembershipMigrationRequired {
		t.Fatalf("Open() failure kind = %q, want %q", failure.Kind, KindMembershipMigrationRequired)
	}
	if failure.RetrySafe {
		t.Fatal("membership migration failure is retry-safe; want explicit recovery")
	}
	if !strings.Contains(failure.RecoveryAction, "stable IDs") || !strings.Contains(failure.RecoveryAction, "v3 binary") {
		t.Fatalf("RecoveryAction = %q, want explicit stable-ID or v3 recovery", failure.RecoveryAction)
	}

	check, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("reopen v3 database: %v", err)
	}
	check.SetMaxOpenConns(1)
	defer func() { _ = check.Close() }()
	if got, err := readSchemaManifestVersion(ctx, check); err != nil || got != 3 {
		t.Fatalf("schema manifest version = %d, error = %v, want exactly 3", got, err)
	}
	var membershipTables int
	if err := check.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('product_projects', 'work_projects')`).Scan(&membershipTables); err != nil {
		t.Fatalf("check membership tables: %v", err)
	}
	if membershipTables != 0 {
		t.Fatalf("membership tables = %d, want none", membershipTables)
	}
	var id, name string
	if err := check.QueryRowContext(ctx, `SELECT id, display_name FROM products`).Scan(&id, &name); err != nil {
		t.Fatalf("read original Product fixture: %v", err)
	}
	if id != "product-1" || name != "Concord" {
		t.Fatalf("original Product fixture = %q/%q, want product-1/Concord", id, name)
	}
}

func TestMigrateEmptyVersion3DatabaseToVersion4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	db := seedVersion3Database(t, path)
	if err := db.Close(); err != nil {
		t.Fatalf("seed database Close() error = %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() empty v3 database error = %v", err)
	}
	defer func() { _ = s.Close() }()
	got, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if got != CurrentSchemaVersion() {
		t.Fatalf("schema manifest version = %d, want %d", got, CurrentSchemaVersion())
	}
}

func seedVersion3Database(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("begin v3 seed transaction: %v", err)
	}
	rollback := func(err error) *sql.DB {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("seed v3 database: %v", err)
		return nil
	}
	if _, err := tx.ExecContext(context.Background(), schemaManifestDDL); err != nil {
		return rollback(err)
	}
	for _, m := range migrations[:3] {
		if _, err := tx.ExecContext(context.Background(), m.SQL); err != nil {
			return rollback(err)
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, '2026-08-07T12:00:00Z')`, m.Version, m.Name, m.checksum()); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatalf("commit v3 seed transaction: %v", err)
	}
	return db
}

func TestOpenConcurrentlyInitializesOneDatabase(t *testing.T) {
	const openers = 8

	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	// Seed only the empty manifest on a new file so every concurrent Open reaches
	// the pending-migration read-to-write path instead of serializing on table creation.
	seed, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("seed sql.Open() error = %v", err)
	}
	if _, err := seed.ExecContext(ctx, schemaManifestDDL); err != nil {
		_ = seed.Close()
		t.Fatalf("seed schema manifest: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close() error = %v", err)
	}
	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(openers)
	start.Add(1)
	done.Add(openers)

	stores := make([]*Store, openers)
	errs := make([]error, openers)
	for i := range openers {
		go func(i int) {
			defer done.Done()
			ready.Done()
			start.Wait()
			stores[i], errs[i] = Open(ctx, path)
		}(i)
	}
	ready.Wait()
	start.Done()
	done.Wait()

	for i, s := range stores {
		if s == nil {
			continue
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Errorf("Open(%d) Close() error = %v", i, err)
			}
		})
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("Open(%d) error = %v", i, err)
		}
	}

	var verifier *Store
	for _, s := range stores {
		if s != nil {
			verifier = s
			break
		}
	}
	if verifier == nil {
		t.Fatal("all concurrent Open calls failed")
	}

	got, err := readSchemaManifestVersion(ctx, verifier.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("schema manifest version = %d, want %d", got, want)
	}

	var applied int
	if err := verifier.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

// The manifest records a checksum per migration so an edited historical
// migration is detected instead of silently diverging from the live schema.
func TestMigrateDetectsEditedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'tampered' WHERE version = ?`, migrations[0].Version); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(ctx, path)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("Open() on tampered history error = %v, want *Failure", err)
	}
	if f.Kind != KindSchemaDrift {
		t.Errorf("Kind = %q, want %q", f.Kind, KindSchemaDrift)
	}
}

// A database written by a newer binary must fail closed rather than be operated
// on by an older schema definition.
func TestMigrateRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	future := migrations[len(migrations)-1].Version + 1
	if _, err := s.DatabaseForTesting().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, 'from-the-future', 'x', '2026-01-01T00:00:00Z')`,
		future); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(ctx, path)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("Open() on newer schema error = %v, want *Failure", err)
	}
	if f.Kind != KindSchemaUnsupported {
		t.Errorf("Kind = %q, want %q", f.Kind, KindSchemaUnsupported)
	}
	if !f.RetrySafe {
		t.Error("RetrySafe = false; upgrading the binary and retrying is the documented recovery")
	}
}

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	seen := make(map[int]bool, len(migrations))
	for i, m := range migrations {
		if m.Version <= 0 {
			t.Errorf("migration %d has non-positive version %d", i, m.Version)
		}
		if seen[m.Version] {
			t.Errorf("duplicate migration version %d", m.Version)
		}
		seen[m.Version] = true
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migration %d version %d is not greater than previous %d", i, m.Version, migrations[i-1].Version)
		}
		if m.Name == "" {
			t.Errorf("migration %d has no name", i)
		}
		if m.SQL == "" {
			t.Errorf("migration %d has no statements", i)
		}
	}
}

func TestMigration58MatchesIssuedBootstrapLedger(t *testing.T) {
	var migration58 migration
	for _, candidate := range migrations {
		if candidate.Version == 58 {
			migration58 = candidate
			break
		}
	}
	if migration58.Version != 58 {
		t.Fatal("migration 58 is missing")
	}
	if migration58.Name != "work_bootstrap_operations" {
		t.Fatalf("migration 58 name = %q, want work_bootstrap_operations", migration58.Name)
	}
	if got := migration58.checksum(); got != "ecfc4b59eadf07db45659fd92bc4fcfd1a88894d97ba727e3bc1cc2418c0cc19" {
		t.Fatalf("migration 58 checksum = %s, want issued checksum", got)
	}
}

func TestMigration49SeedsVocabularyRegistriesAndGuardsNativePairs(t *testing.T) {
	s := openTemp(t)
	db := s.DatabaseForTesting()
	ctx := context.Background()
	var workKinds, nativeStatuses int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_kinds`).Scan(&workKinds); err != nil || workKinds != 7 {
		t.Fatalf("work kind rows=%d err=%v", workKinds, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_native_run_statuses`).Scan(&nativeStatuses); err != nil || nativeStatuses != 10 {
		t.Fatalf("native status rows=%d err=%v", nativeStatuses, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('native-registry-work','task','Native registry','needed',1,1,'now','now',NULL)`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	pairs := []struct{ phase, status string }{
		{"start", "started"}, {"start", "failed_to_start"},
		{"health", "healthy"}, {"health", "degraded"}, {"health", "failed"},
		{"rollback", "rolled_back"}, {"rollback", "partially_rolled_back"}, {"rollback", "rollback_failed"},
		{"cleanup", "cleaned"}, {"cleanup", "cleanup_failed"},
	}
	for i, pair := range pairs {
		args := []any{"native-registry-work", "run-" + pair.phase, pair.phase, pair.status, "event-" + pair.phase, "client", "actor", "native://run", "sha256:" + strings.Repeat("a", 64), "evidence://run", "sha256:" + strings.Repeat("b", 64), "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z", "trusted_client_report", "{}", "policy", "policy"}
		if i > 0 {
			args[1] = "run-" + pair.phase + "-" + pair.status
		}
		if _, err := db.ExecContext(ctx, insert, args...); err != nil {
			t.Fatalf("declared native pair %s/%s rejected: %v", pair.phase, pair.status, err)
		}
	}
	for _, pair := range []struct{ phase, status string }{{"start", "healthy"}, {"health", "unknown"}} {
		args := []any{"native-registry-work", "bad-" + pair.phase + pair.status, pair.phase, pair.status, "event-bad", "client", "actor", "native://run", "sha256:" + strings.Repeat("a", 64), "evidence://run", "sha256:" + strings.Repeat("b", 64), "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z", "trusted_client_report", "{}", "policy", "policy"}
		if _, err := db.ExecContext(ctx, insert, args...); err == nil || !strings.Contains(err.Error(), "workflow native run phase and status are not a declared pair") {
			t.Fatalf("invalid native pair %s/%s error=%v", pair.phase, pair.status, err)
		}
	}
}

func TestMigration49RejectsInvalidPreMigrationRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed string
	}{
		{name: "work kind", seed: `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('invalid-pre-work','problem','Invalid','needed',1,1,'now','now',NULL);`},
		{name: "native status", seed: `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref) VALUES('pre-native-work','run','start','healthy','event','client','actor','native://run','sha256:` + strings.Repeat("a", 64) + `','evidence://run','sha256:` + strings.Repeat("b", 64) + `','2026-08-25T00:00:00Z','2026-08-25T00:00:00Z','trusted_client_report','{}','policy','policy');`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pre-migration.db")
			db, err := sql.Open(driverName, dataSourceName(path))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
				t.Fatal(err)
			}
			for _, migration := range migrations[:48] {
				if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
					t.Fatalf("migration %d: %v", migration.Version, err)
				}
				if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-25T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if tc.name == "native status" {
				if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('pre-native-work','task','Pre native','needed',1,1,'now','now',NULL);`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, tc.seed); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(ctx, db); err == nil {
				t.Fatal("migration preserved an invalid pre-migration row")
			}
		})
	}
}

func TestMigration49UpgradesValidPreMigrationRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid-pre-migration.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:48] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-25T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('valid-pre-work','task','Valid','needed',1,1,'now','now',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref) VALUES('valid-pre-work','run','health','degraded','event','client','actor','native://run','sha256:`+strings.Repeat("a", 64)+`','evidence://run','sha256:`+strings.Repeat("b", 64)+`','2026-08-25T00:00:00Z','2026-08-25T00:00:00Z','trusted_client_report','{}','policy','policy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("valid pre-migration rows failed upgrade: %v", err)
	}
	if version, err := readSchemaManifestVersion(ctx, db); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("upgraded schema version=%d err=%v", version, err)
	}
}

func TestMigration53UpgradesValidNativeRunVerificationState(t *testing.T) {
	ctx := context.Background()
	db := openV49(t, "native-run-v52-valid.db")
	seedV49NativeRun(t, db, string(VerificationVerified))

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("valid pre-migration row failed upgrade: %v", err)
	}
	if version, err := readSchemaManifestVersion(ctx, db); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("upgraded schema version=%d err=%v, want %d", version, err, CurrentSchemaVersion())
	}
}

func TestMigration53RejectsInvalidNativeRunVerificationState(t *testing.T) {
	ctx := context.Background()
	db := openV49(t, "native-run-v52-invalid.db")
	seedV49NativeRun(t, db, "definitely_bogus")

	if err := Migrate(ctx, db); err == nil {
		t.Fatal("migration preserved an invalid native-run verification state")
	}
}

func TestWorkflowContractRigorClassVocabulary(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	db := s.DatabaseForTesting()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	actorRef := seedRigorClassPrerequisites(t, db)
	if _, err := insertRigorClassContract(ctx, db, actorRef, "prototype/internal"); err == nil || !strings.Contains(err.Error(), "workflow contract rigor class is not a declared maturity-audience composition") {
		t.Fatalf("invalid rigor class error=%v", err)
	}
	if _, err := insertRigorClassContract(ctx, db, actorRef, "prototype_internal"); err != nil {
		t.Fatalf("valid rigor class rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_contracts SET rigor_class='prototype/internal' WHERE work_id='rigor-class-work'`); err == nil || !strings.Contains(err.Error(), "workflow contract rigor class is not a declared maturity-audience composition") {
		t.Fatalf("invalid rigor class update error=%v", err)
	}
}

func TestMigration54UpgradesValidRigorClass(t *testing.T) {
	ctx := context.Background()
	db := openV53(t, "rigor-class-v53-valid.db")
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	actorRef := seedRigorClassPrerequisites(t, db)
	if _, err := insertRigorClassContract(ctx, db, actorRef, "production_public"); err != nil {
		t.Fatalf("seed valid rigor class: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("valid rigor class failed upgrade: %v", err)
	}
}

func TestMigration54RejectsInvalidRigorClass(t *testing.T) {
	ctx := context.Background()
	db := openV53(t, "rigor-class-v53-invalid.db")
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	actorRef := seedRigorClassPrerequisites(t, db)
	if _, err := insertRigorClassContract(ctx, db, actorRef, "prototype/internal"); err != nil {
		t.Fatalf("seed invalid rigor class: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "workflow contract rigor class is not a declared maturity-audience composition") {
		t.Fatalf("invalid rigor class migration error=%v", err)
	}
	if version, err := readSchemaManifestVersion(ctx, db); err != nil || version != 53 {
		t.Fatalf("failed migration advanced schema version=%d err=%v", version, err)
	}
}

func TestMigration55AllowsDeclaredApprovalConsequences(t *testing.T) {
	ctx := context.Background()
	db := openV54(t, "approval-consequence-v54-valid.db")
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration 55 failed: %v", err)
	}
	for _, consequence := range []string{"research", "claim"} {
		clientRef := seedApprovalChallengeClient(t, db, consequence)
		if err := insertApprovalChallengeAtHead(ctx, db, clientRef, consequence, strings.Repeat(consequence[:1], 64)); err != nil {
			t.Fatalf("insert %s consequence: %v", consequence, err)
		}
	}
	clientRef := seedApprovalChallengeClient(t, db, "bogus")
	if err := insertApprovalChallengeAtHead(ctx, db, clientRef, "bogus", strings.Repeat("b", 64)); err == nil {
		t.Fatal("bogus approval consequence was accepted")
	}
}

func TestMigration55UpgradesValidApprovalConsequences(t *testing.T) {
	ctx := context.Background()
	db := openV54(t, "approval-consequence-v54-upgrade.db")
	grantRef := seedApprovalChallengeGrant(t, db, "intent")
	if err := insertApprovalChallenge(ctx, db, grantRef, "intent", strings.Repeat("i", 64)); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("valid approval consequence failed upgrade: %v", err)
	}
	// Migration 57 copies the challenge through a join on agent_grants and
	// then drops that table, so the upgraded row is addressed by its own
	// reference and carries the identity tuple the grant used to hold.
	var consequence, clientRef, sessionRef, worktree string
	if err := db.QueryRowContext(ctx, `SELECT consequence,client_ref,session_ref,worktree FROM agent_approval_challenges WHERE challenge_ref=?`, strings.Repeat("i", 64)).Scan(&consequence, &clientRef, &sessionRef, &worktree); err != nil {
		t.Fatal(err)
	}
	if consequence != "intent" {
		t.Fatalf("upgraded consequence=%q, want intent", consequence)
	}
	if clientRef != "approval-client-intent" || sessionRef != "session-intent" || worktree != "/repo/worktree" {
		t.Fatalf("identity tuple not carried across migration 57: client=%q session=%q worktree=%q", clientRef, sessionRef, worktree)
	}
	_ = grantRef
}

func TestMigration55RejectsUndeclaredApprovalConsequence(t *testing.T) {
	ctx := context.Background()
	db := openV54(t, "approval-consequence-v54-invalid.db")
	grantRef := seedApprovalChallengeGrant(t, db, "invalid")
	if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if err := insertApprovalChallenge(ctx, db, grantRef, "undeclared", strings.Repeat("u", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("migration accepted an undeclared approval consequence")
	}
	if version, err := readSchemaManifestVersion(ctx, db); err != nil || version != 54 {
		t.Fatalf("failed migration advanced schema version=%d err=%v", version, err)
	}
}

func TestArchivedWorkKindVocabulary(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	db := s.DatabaseForTesting()
	seedArchivedWorkKindHome(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()

	for _, kind := range []string{"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"} {
		if err := insertArchivedWorkKind(ctx, db, "archived-"+kind, kind); err != nil {
			t.Fatalf("declared kind %s rejected: %v", kind, err)
		}
	}
	if err := insertArchivedWorkKind(ctx, db, "archived-bogus", "bogus"); err == nil || !strings.Contains(err.Error(), "archived work type is not a declared knowledge kind") {
		t.Fatalf("undeclared kind insert error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE archived_work SET type='bogus' WHERE id='archived-work_note'`); err == nil || !strings.Contains(err.Error(), "archived work type is not a declared knowledge kind") {
		t.Fatalf("undeclared kind update error = %v", err)
	}
}

func TestMigration56UpgradesValidArchivedWorkKind(t *testing.T) {
	ctx := context.Background()
	db := openV55(t, "archived-work-kind-v55-valid.db")
	seedArchivedWorkKindHome(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := insertArchivedWorkKind(ctx, db, "pre-56-valid", "research"); err != nil {
		t.Fatalf("seed valid archived work kind: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("valid archived work kind failed upgrade: %v", err)
	}
	if version, err := readSchemaManifestVersion(ctx, db); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("upgraded schema version=%d err=%v, want %d", version, err, CurrentSchemaVersion())
	}
}

func TestMigration56RejectsUndeclaredArchivedWorkKind(t *testing.T) {
	ctx := context.Background()
	db := openV55(t, "archived-work-kind-v55-invalid.db")
	seedArchivedWorkKindHome(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := insertArchivedWorkKind(ctx, db, "pre-56-invalid", "bogus"); err != nil {
		t.Fatalf("seed undeclared archived work kind: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "archived work type is not a declared knowledge kind") {
		t.Fatalf("undeclared archived work kind migration error = %v", err)
	}
	if version, err := readSchemaManifestVersion(ctx, db); err != nil || version != 55 {
		t.Fatalf("failed migration advanced schema version=%d err=%v, want 55", version, err)
	}
}

func openV55(t *testing.T, name string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), name)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:55] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-27T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedArchivedWorkKindHome(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('archived-kind-project','Archived kind project',1,'now','now')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('archived-kind-product','Archived kind product','prototype','operator_only',1,'now','now')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('archived-kind-product','archived-kind-project','secondary')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('archived-kind-locator','archived-kind-project','canonical_path','/test/archived-kind','/test/archived-kind','now','now')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func insertArchivedWorkKind(ctx context.Context, db *sql.DB, id, kind string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, kind, "Archived work", "2026-08-27T00:00:00Z", "completed", "[]", "completed", 0, "summary", "archived-kind-project", "archived-kind-locator", "docs/work/archived.md", strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64))
	return err
}

func openV54(t *testing.T, name string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), name)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:54] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-27T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedApprovalChallengeGrant(t *testing.T, db *sql.DB, suffix string) string {
	t.Helper()
	ctx := context.Background()
	clientRef := "approval-client-" + suffix
	grantRef := strings.Repeat(suffix[:1], 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`, clientRef, "active", "human", `[]`, `[]`, `[]`, "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_grants(grant_ref,grant_hash,principal_ref,client_ref,session_ref,agent_ref,directory,worktree,client_key_id,manifest_digest,capabilities_json,product_scope_json,project_scope_json,issued_at,expires_at,max_uses,used_count,scope_version,scope_snapshot_json,candidate_products_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, grantRef, []byte(strings.Repeat(suffix[:1], 32)), "human", clientRef, "session-"+suffix, "agent-"+suffix, "/repo", "/repo/worktree", "key", "sha256:"+strings.Repeat("0", 64), `[]`, `[]`, `[]`, "2026-08-27T00:00:00Z", "2026-08-28T00:00:00Z", 1, 0, "v1", `{}`, `[]`); err != nil {
		t.Fatal(err)
	}
	return grantRef
}

// seedApprovalChallengeClient registers only the client a head-shape challenge
// needs. Migration 57 removed agent_grants, so a challenge no longer has a
// grant parent to seed.
func seedApprovalChallengeClient(t *testing.T, db *sql.DB, suffix string) string {
	t.Helper()
	clientRef := "approval-client-" + suffix
	if _, err := db.ExecContext(context.Background(), `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`, clientRef, "active", "human", `[]`, `[]`, `[]`, "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	return clientRef
}

func insertApprovalChallengeAtHead(ctx context.Context, db *sql.DB, clientRef, consequence, challengeRef string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO agent_approval_challenges(challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at,max_uses,used_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, challengeRef, clientRef, "human", "session-"+consequence, "agent-"+consequence, "/repo", "/repo/worktree", `[]`, "operation", `{}`, `{}`, consequence, "host", "2026-08-27T00:00:00Z", "2026-08-28T00:00:00Z", "active", nil, 1, 0)
	return err
}

func insertApprovalChallenge(ctx context.Context, db *sql.DB, grantRef, consequence, challengeRef string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO agent_approval_challenges(challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, challengeRef, grantRef, "operation", `{}`, `{}`, consequence, "host", "2026-08-27T00:00:00Z", "2026-08-28T00:00:00Z", "active", nil)
	return err
}

func openV53(t *testing.T, name string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), name)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:53] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-27T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedRigorClassPrerequisites(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	actorRef := DeriveWorkflowActorRef("principal:rigor", "client:rigor", "agent:rigor", "session:rigor")
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:rigor','client:rigor','agent:rigor','session:rigor','operator','now')`, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('rigor-class-work','task','Rigor class','needed',1,1,'now','now',NULL)`); err != nil {
		t.Fatal(err)
	}
	return actorRef
}

func insertRigorClassContract(ctx context.Context, db *sql.DB, actorRef, rigorClass string) (sql.Result, error) {
	var legacyOutcomeColumns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='outcome_kind'`).Scan(&legacyOutcomeColumns); err != nil {
		return nil, err
	}
	if legacyOutcomeColumns == 1 {
		return db.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('rigor-class-work',1,'rigor class','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,'[]','[]',0,?)`, actorRef, rigorClass)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('rigor-class-work',1,'rigor class','internal_sqlite','[]','[]','now',?,'[]','[]',0,?)`, actorRef, rigorClass)
	if err != nil {
		return result, err
	}
	var predicatesTable int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workflow_contract_predicates'`).Scan(&predicatesTable); err != nil {
		return result, err
	}
	if predicatesTable == 0 {
		return result, nil
	}
	_, err = db.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('rigor-class-work',1,'predicate:primary',0,'check','{"kind":"check"}')`)
	return result, err
}

func seedV49NativeRun(t *testing.T, db *sql.DB, verificationState string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('native-run-v52-work','task','Native run','needed',1,1,'now','now',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id,verification_state) VALUES('native-run-v52-work','run','health','healthy','event','client','actor','native://run','sha256:`+strings.Repeat("a", 64)+`','evidence://run','sha256:`+strings.Repeat("b", 64)+`','2026-08-25T00:00:00Z','2026-08-25T00:00:00Z','trusted_client_report','{}','policy','policy','xobs:v52',?)`, verificationState); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func TestMigration40AddsDomainOverlapProjectionTables(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	db := s.DatabaseForTesting()
	for _, table := range []string{
		"domain_registries", "domains", "domain_architecture_relations", "law_domain_homes",
		"law_domain_applicability", "archived_work_domains", "domain_project_attachment_sets",
		"domain_project_attachment_edges", "domain_resource_attachment_sets",
		"domain_resource_attachment_edges", "managed_resources", "resource_products",
		"workflow_architecture_bindings", "workflow_contract_affected_domains", "workflow_contract_domain_modifications",
		"workflow_contract_domain_relation_modifications", "workflow_law_addition_reservations", "workflow_contract_law_additions", "workflow_contract_verification_obligations",
		"workflow_contract_law_modifications", "workflow_overlap_resolutions",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("schema version=%d err=%v, want %d", version, err, CurrentSchemaVersion())
	}
	var nativeRuns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workflow_native_runs'`).Scan(&nativeRuns); err != nil || nativeRuns != 1 {
		t.Fatalf("workflow_native_runs count=%d err=%v", nativeRuns, err)
	}
	for _, table := range []string{"domains", "domain_project_attachment_edges", "domain_resource_attachment_edges", "managed_resources", "resource_products"} {
		var err error
		if _, err = db.ExecContext(ctx, "INSERT INTO "+table+" DEFAULT VALUES"); err == nil {
			t.Fatalf("direct write to %s bypassed fold guard", table)
		}
	}
	for _, table := range []string{"domain_registries", "domains", "domain_architecture_relations", "law_domain_homes", "law_domain_applicability", "domain_project_attachment_sets", "domain_resource_attachment_sets"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, table+"_guard_insert").Scan(&count); err != nil || count != 1 {
			t.Fatalf("fold guard for %s count=%d err=%v", table, count, err)
		}
	}
}

// TestMigrationReplayFromScratchDropsWorkerRoutingEvidence proves the
// migrations are replayable from an empty database: every step runs in order,
// migration 43's worker_attempts DEFAULT resolves through the
// concord_routing_policy_manifest_digest() SQLite function during the replay,
// and migration 44 leaves worker_attempts without the five CD-0058 columns.
func TestMigrationReplayFromScratchDropsWorkerRoutingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-replay.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatalf("schema manifest DDL: %v", err)
	}
	appliedAt := "2026-08-22T00:00:00Z"
	// Stop after migration 43 so the concord_routing_policy_manifest_digest()
	// DEFAULT is live and the column is still present.
	for _, migration := range migrations[:43] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d (%s): %v", migration.Version, migration.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			migration.Version, migration.Name, migration.checksum(), appliedAt,
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", migration.Version, err)
		}
	}
	// Migration 43's DEFAULT invoked concord_routing_policy_manifest_digest().
	// Exercise it now while the column still exists: an INSERT that omits
	// routing_policy_digest must succeed and store RoutingPolicyManifestDigest.
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("fold guard: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,resolved_model,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"replay-work", "replay-43-attempt", "research", 1,
		"sha256:"+strings.Repeat("a", 64), "research", "routing-v1",
		"openai/gpt-5.6-luna", "openai/gpt-5.6-luna",
		"1.0", "1.0", "dispatched", appliedAt,
	); err != nil {
		t.Fatalf("migration 43 default failed to resolve: %v", err)
	}
	var replayDigest string
	if err := db.QueryRowContext(ctx,
		`SELECT routing_policy_digest FROM worker_attempts WHERE attempt_id='replay-43-attempt'`,
	).Scan(&replayDigest); err != nil {
		t.Fatalf("read replay digest: %v", err)
	}
	if replayDigest != RoutingPolicyManifestDigest {
		t.Fatalf("replay routing_policy_digest = %q, want %q", replayDigest, RoutingPolicyManifestDigest)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatalf("release fold guard: %v", err)
	}
	// Apply migration 44 and confirm the five CD-0058 columns are gone, the
	// pre-existing row survives with its readback_model intact, and the
	// fold triggers are reinstalled.
	if _, err := db.ExecContext(ctx, migrations[43].SQL); err != nil {
		t.Fatalf("migration %d (%s): %v", migrations[43].Version, migrations[43].Name, err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
		migrations[43].Version, migrations[43].Name, migrations[43].checksum(), appliedAt,
	); err != nil {
		t.Fatalf("manifest record for migration 44: %v", err)
	}
	// Everything above targets migration 44. Carry the replay the rest of the
	// way so the head assertion below keeps meaning what it says: the replay
	// reached the current schema, not merely the migration this test opened on.
	for _, m := range migrations[44:] {
		if _, err := db.ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("migration %d (%s): %v", m.Version, m.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			m.Version, m.Name, m.checksum(), appliedAt,
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", m.Version, err)
		}
	}
	var latest int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; latest != want {
		t.Fatalf("replay ended at version %d, want %d", latest, want)
	}
	for _, column := range []string{"routing_policy_version", "routing_policy_digest", "resolved_model", "resolution_role", "fallback_reason"} {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM pragma_table_info('worker_attempts') WHERE name=?`,
			column,
		).Scan(&count); err != nil || count != 0 {
			t.Fatalf("column %s count=%d err=%v after replay, want 0", column, count, err)
		}
	}
	var replayReadback string
	if err := db.QueryRowContext(ctx,
		`SELECT readback_model FROM worker_attempts WHERE attempt_id='replay-43-attempt'`,
	).Scan(&replayReadback); err != nil {
		t.Fatalf("read replay readback_model: %v", err)
	}
	if replayReadback != "openai/gpt-5.6-luna" {
		t.Fatalf("replay readback_model = %q, want openai/gpt-5.6-luna", replayReadback)
	}
	// The fold triggers must be reinstalled by migration 44: a direct INSERT
	// with no fold_guard row active must be rejected.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"replay-work", "replay-44-attempt", "research", 1,
		"sha256:"+strings.Repeat("a", 64), "research",
		"openai/gpt-5.6-luna", "1.0", "1.0", "dispatched", appliedAt,
	); err == nil {
		t.Fatal("post-migration-44 INSERT bypassed the fold guard")
	}
}

// TestMigrateV45ToV46DropsOrchestratorReservationAndNarrowsBoundaryKind covers
// CD-0061 D3: the typed_agent_type, typed_agent_version, and
// typed_agent_ruleset_digest columns on workflow_context_boundaries are
// removed because no code reads or writes them (the reservation was orphaned
// after CD-0027 excluded the restart dispatch that would have populated them),
// and boundary_kind narrows to admit only 'summary', encoding CD-0027's
// exclusion in the schema rather than only in prose.
func TestMigrateV45ToV46DropsOrchestratorReservationAndNarrowsBoundaryKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v45.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatalf("schema manifest DDL: %v", err)
	}
	appliedAt := "2026-08-23T00:00:00Z"
	for _, migration := range migrations[:45] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d (%s): %v", migration.Version, migration.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			migration.Version, migration.Name, migration.checksum(), appliedAt,
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", migration.Version, err)
		}
	}
	// Pre-migration: the three typed_agent_* columns must exist (the
	// reservation they encode was declared and is what migration 46 removes).
	for _, column := range []string{"typed_agent_type", "typed_agent_version", "typed_agent_ruleset_digest"} {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM pragma_table_info('workflow_context_boundaries') WHERE name=?`,
			column,
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("pre-migration column %s count=%d err=%v, want 1", column, count, err)
		}
	}
	// Pre-migration: the column-level CHECK on boundary_kind admits 'restart'
	// (the table-level narrowing is migration 46).
	preTableSQL := readTableSQL(t, ctx, db, "workflow_context_boundaries")
	if !strings.Contains(preTableSQL, "boundary_kind IN ('summary','restart')") {
		t.Fatalf("pre-migration column CHECK missing the legacy restart member:\n%s", preTableSQL)
	}
	// Apply migration 46.
	v46 := migrations[45]
	if v46.Version != 46 {
		t.Fatalf("migrations[45].Version = %d, want 46", v46.Version)
	}
	if _, err := db.ExecContext(ctx, v46.SQL); err != nil {
		t.Fatalf("migration %d (%s): %v", v46.Version, v46.Name, err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
		v46.Version, v46.Name, v46.checksum(), appliedAt,
	); err != nil {
		t.Fatalf("manifest record for migration %d: %v", v46.Version, err)
	}
	// The three typed_agent_* columns are gone.
	for _, column := range []string{"typed_agent_type", "typed_agent_version", "typed_agent_ruleset_digest"} {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM pragma_table_info('workflow_context_boundaries') WHERE name=?`,
			column,
		).Scan(&count); err != nil || count != 0 {
			t.Fatalf("post-migration column %s count=%d err=%v, want 0", column, count, err)
		}
	}
	// Post-migration: the column-level CHECK narrows to admit only 'summary'.
	postTableSQL := readTableSQL(t, ctx, db, "workflow_context_boundaries")
	if strings.Contains(postTableSQL, "boundary_kind IN ('summary','restart')") {
		t.Fatalf("post-migration column CHECK still admits 'restart':\n%s", postTableSQL)
	}
	if !strings.Contains(postTableSQL, "boundary_kind='summary'") {
		t.Fatalf("post-migration column CHECK does not pin 'summary':\n%s", postTableSQL)
	}
	if strings.Contains(postTableSQL, "typed_agent_") {
		t.Fatalf("post-migration table SQL still references typed_agent_:\n%s", postTableSQL)
	}
	// boundary_kind no longer admits 'restart' — a direct INSERT must fail at
	// the column-level CHECK.
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("fold guard: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`,
		DeriveWorkflowActorRef("principal:v45", "client:v45", "agent:v45", "session:v45"),
		"principal:v45", "client:v45", "agent:v45", "session:v45", "operator", "now",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at,intent_json) VALUES('work-v45','task','Work','needed',0,'standard',1,'now','now','{}')`,
	); err != nil {
		t.Fatalf("seed work: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work-v45',1,1,1,'v45-restart','restart','v45-checkpoint:context-checkpoint',1,1,'restart attempted','workflow.implementation',1,?,?,'request:v45-restart','2026-08-23T00:00:00Z')`,
		"sha256:"+strings.Repeat("a", 64),
		DeriveWorkflowActorRef("principal:v45", "client:v45", "agent:v45", "session:v45"),
	); err == nil {
		t.Fatal("post-migration 'restart' boundary_kind was admitted")
	} else if !strings.Contains(err.Error(), "boundary_kind") {
		t.Fatalf("post-migration 'restart' was rejected, but not by the boundary_kind CHECK: %v", err)
	}
	// boundary_kind still admits 'summary' — the surviving path.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work-v45',1,2,2,'v45-summary','summary','v45-checkpoint:context-checkpoint',1,1,'summary wrote','workflow.implementation',1,?,?,'request:v45-summary','2026-08-23T00:00:00Z')`,
		"sha256:"+strings.Repeat("a", 64),
		DeriveWorkflowActorRef("principal:v45", "client:v45", "agent:v45", "session:v45"),
	); err != nil {
		t.Fatalf("post-migration 'summary' boundary_kind was refused: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatalf("release fold guard: %v", err)
	}
	// The fold triggers survived the rebuild: a direct INSERT with no fold
	// guard active must still be rejected.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work-v45',1,3,3,'v45-direct','summary','v45-checkpoint:context-checkpoint',1,1,'direct insert','workflow.implementation',1,?,?,'request:v45-direct','2026-08-23T00:00:00Z')`,
		"sha256:"+strings.Repeat("a", 64),
		DeriveWorkflowActorRef("principal:v45", "client:v45", "agent:v45", "session:v45"),
	); err == nil {
		t.Fatal("post-migration-46 direct INSERT bypassed the fold guard")
	}
}

// TestMigrateV47ToV48RenamesResearchScopeComponentToDomain proves the CD-0041
// execution on the research scope surface: the active_research_finding_scopes
// CHECK on scope_kind drops 'component' in favor of 'domain', every legacy
// 'component' row is rewritten to 'domain' in the same transaction, the home
// guard trigger still refuses home-with-IDs, and the rest of the replay
// reaches the head schema with no step depending on the retired enum member.
func TestMigrateV47ToV48RenamesResearchScopeComponentToDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v47-scopes.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatalf("schema manifest DDL: %v", err)
	}
	appliedAt := "2026-08-23T00:00:00Z"
	for _, migration := range migrations[:47] {
		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("migration %d (%s): %v", migration.Version, migration.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			migration.Version, migration.Name, migration.checksum(), appliedAt,
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", migration.Version, err)
		}
	}
	// Pre-migration: the CHECK on active_research_finding_scopes admits the
	// retired 'component' enum member.
	preTableSQL := readTableSQL(t, ctx, db, "active_research_finding_scopes")
	if !strings.Contains(preTableSQL, "'product','project','component','tag'") {
		t.Fatalf("pre-migration CHECK missing legacy component member:\n%s", preTableSQL)
	}
	// Seed a research finding whose scope is 'component' so the migration has
	// at least one row to rewrite. The home guard requires scope_mode='home'
	// for empty scopes and refuses INSERTs that name IDs while home; an
	// explicit-mode finding is the seeded shape the migration must rewrite.
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("seed fold guard: %v", err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Logf("release fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-v47','p','alpha','operator_only',1,'now','now')`,
	); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at,intent_json) VALUES('work-v47','task','Work','needed',0,'standard',1,'now','now','{}')`,
	); err != nil {
		t.Fatalf("seed work: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('pack-v47','work-v47',1,'current',1,'now','now')`,
	); err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,freshness,created_at) VALUES('pack-v47',1,'q','[]','[]','[]','m','current','now')`,
	); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES('pack-v47',1,'finding-v47','observation','s','high','current','active','explicit')`,
	); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES('pack-v47',1,'finding-v47','component','auth')`,
	); err != nil {
		t.Fatalf("seed legacy component scope: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES('pack-v47',1,'finding-v47','tag','security')`,
	); err != nil {
		t.Fatalf("seed surviving tag scope: %v", err)
	}
	// Apply migration 48.
	v48 := migrations[47]
	if v48.Version != 48 {
		t.Fatalf("migrations[47].Version = %d, want 48", v48.Version)
	}
	if _, err := db.ExecContext(ctx, v48.SQL); err != nil {
		t.Fatalf("migration %d (%s): %v", v48.Version, v48.Name, err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
		v48.Version, v48.Name, v48.checksum(), appliedAt,
	); err != nil {
		t.Fatalf("manifest record for migration %d: %v", v48.Version, err)
	}
	// Post-migration: the rewritten 'component' scope_kind becomes 'domain';
	// the surviving 'tag' row is untouched; no row carries 'component' any
	// more, and the new CHECK refuses a fresh 'component' insert.
	rows, err := db.QueryContext(ctx,
		`SELECT scope_kind, scope_id FROM active_research_finding_scopes WHERE pack_id='pack-v47' AND revision=1 AND finding_id='finding-v47' ORDER BY scope_kind`,
	)
	if err != nil {
		t.Fatalf("read rewritten scopes: %v", err)
	}
	defer rows.Close()
	want := map[string][]string{}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatalf("scan rewritten scope: %v", err)
		}
		want[kind] = append(want[kind], id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rewritten scopes: %v", err)
	}
	if got := want["component"]; len(got) != 0 {
		t.Fatalf("legacy 'component' rows survived migration: %v", got)
	}
	if got := want["domain"]; len(got) != 1 || got[0] != "auth" {
		t.Fatalf("rewritten 'domain' rows = %v, want [auth]", got)
	}
	if got := want["tag"]; len(got) != 1 || got[0] != "security" {
		t.Fatalf("untouched 'tag' rows = %v, want [security]", got)
	}
	postTableSQL := readTableSQL(t, ctx, db, "active_research_finding_scopes")
	if strings.Contains(postTableSQL, "'product','project','component','tag'") {
		t.Fatalf("post-migration CHECK still admits retired 'component':\n%s", postTableSQL)
	}
	if !strings.Contains(postTableSQL, "'product','project','domain','tag'") {
		t.Fatalf("post-migration CHECK does not pin 'domain':\n%s", postTableSQL)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES('pack-v47',2,'finding-v47','component','retired')`,
	); err == nil {
		t.Fatal("post-migration INSERT with retired 'component' was admitted")
	}
	// The home guard survived the rebuild: a fresh explicit finding with no
	// scopes, then a mode flip to home, must succeed; an INSERT of scope IDs
	// while home must be refused by the recreated trigger.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,freshness,created_at) VALUES('pack-v47',2,'q','[]','[]','[]','m','current','now')`,
	); err != nil {
		t.Fatalf("seed second revision: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES('pack-v47',2,'finding-v47-home','observation','s','high','current','active','home')`,
	); err != nil {
		t.Fatalf("seed home finding: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES('pack-v47',2,'finding-v47-home','domain','refused')`,
	); err == nil {
		t.Fatal("post-migration home guard admitted an explicit scope ID")
	}
	// Carry the replay through the rest of the migrations so this test
	// confirms the rename is not the only step that has to survive the
	// upgrade: every later migration must still apply on top of the rebuilt
	// table.
	for _, m := range migrations[48:] {
		if _, err := db.ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("migration %d (%s): %v", m.Version, m.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			m.Version, m.Name, m.checksum(), appliedAt,
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", m.Version, err)
		}
	}
	var latest int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; latest != want {
		t.Fatalf("replay ended at version %d, want %d", latest, want)
	}
}

// readTableSQL returns the CREATE TABLE statement SQLite stored for name.
func readTableSQL(t *testing.T, ctx context.Context, db *sql.DB, name string) string {
	t.Helper()
	var sql string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&sql); err != nil {
		t.Fatalf("read CREATE TABLE %s: %v", name, err)
	}
	return sql
}

// TestMigrationReplayFromScratchReachesHead proves every migration (including
// the CD-0061 D3 reservation removal) replays in order to the head version on
// a fresh database, with no step that depends on a column or CHECK constraint
// removed by a later step.
func TestMigrationReplayFromScratchReachesHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-replay-cd0061.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatalf("schema manifest DDL: %v", err)
	}
	appliedAt := "2026-08-23T00:00:00Z"
	for _, m := range migrations {
		if _, err := db.ExecContext(ctx, m.SQL); err != nil {
			t.Fatalf("migration %d (%s): %v", m.Version, m.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			m.Version, m.Name, m.checksum(), appliedAt,
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", m.Version, err)
		}
	}
	var latest int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&latest); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; latest != want {
		t.Fatalf("replay ended at version %d, want %d", latest, want)
	}
}
