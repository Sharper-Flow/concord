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

	got, err := SchemaVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("SchemaVersion() = %d, want %d", got, want)
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
	defer db.ExecContext(ctx, `DELETE FROM fold_guard`)
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('p','l','HEAD','invalid','indexed','test','`+strings.Repeat("a", 40)+`')`); err == nil {
		t.Fatal("invalid coverage kind passed CHECK")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES('w','lesson','T','2026-08-10T00:00:00Z','published','[]','completed',0,'S','p','l','docs/lessons/t.md','`+strings.Repeat("a", 40)+`','sha256:`+strings.Repeat("b", 64)+`','invalid')`); err == nil {
		t.Fatal("invalid scope mode passed CHECK")
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
	defer db.ExecContext(ctx, `DELETE FROM fold_guard`)
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
	defer db.ExecContext(ctx, `DELETE FROM fold_guard`)
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
	// columns (which migration 43 drops) remain visible to this v25-shape
	// test. Migration 43's own coverage lives in TestMigrateV42ToV43.
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
	defer db.ExecContext(ctx, `DELETE FROM fold_guard`)
	if _, err := db.ExecContext(ctx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,resolved_model,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('work','attempt', 'research',1,?,'research','routing-v1','openai/gpt-5.6-luna','', '1.0','1.0','dispatched','now')`, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatalf("default routing evidence insert failed: %v", err)
	}
	var digest, role, reason string
	if err := db.QueryRowContext(ctx, `SELECT routing_policy_digest,resolution_role,fallback_reason FROM worker_attempts WHERE attempt_id='attempt'`).Scan(&digest, &role, &reason); err != nil {
		t.Fatal(err)
	}
	if digest != historicalRoutingPolicyDigest || role != "preferred" || reason != "" {
		t.Fatalf("migration defaults = %s/%s/%s", digest, role, reason)
	}
	if _, err := db.ExecContext(ctx, `UPDATE worker_attempts SET resolution_role='fallback', fallback_reason='' WHERE attempt_id='attempt'`); err == nil {
		t.Fatal("fallback without typed reason bypassed CHECK")
	}
}

// historicalRoutingPolicyDigest is the DEFAULT literal frozen into migration
// 25's DDL. A database created before migration 43 carries this value, so the
// migration-history tests assert against the literal rather than a live
// constant.
const historicalRoutingPolicyDigest = "sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736"

// TestMigrateV42ToV43DropsWorkerRoutingEvidenceAndPreservesRows covers CD-0056
// D4: the declared-side worker attempt columns are removed under a rename +
// recreate + copy + drop, every pre-existing row survives, and the lifecycle
// CHECK that references readback_model is preserved.
func TestMigrateV42ToV43DropsWorkerRoutingEvidenceAndPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord-v42.db")
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
	for _, migration := range migrations[:42] {
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
	if _, err := db.ExecContext(ctx, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,routing_policy_version,routing_policy_digest,resolved_model,resolution_role,fallback_reason,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at) VALUES('pre-existing-work','pre-existing','research',1,?,'research','routing-v1',?,'openai/gpt-5.6-luna','preferred','','openai/gpt-5.6-luna','1.0','1.0','dispatched','2026-08-22T00:00:00Z')`, "sha256:"+strings.Repeat("a", 64), historicalRoutingPolicyDigest); err != nil {
		t.Fatalf("seed v42 worker attempt: %v", err)
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
		t.Fatalf("pre-existing row missing after migration 43: %v", err)
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
	for _, table := range []string{"agent_clients", "agent_client_keys", "agent_nonce_replay", "agent_grants", "agent_approval_challenges", "agent_approvals", "idempotency_records"} {
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

	before, err := SchemaVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if err := Migrate(ctx, s.DatabaseForTesting()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	after, err := SchemaVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
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
	if got, err := SchemaVersion(ctx, check); err != nil || got != 3 {
		t.Fatalf("SchemaVersion() = %d, error = %v, want exactly 3", got, err)
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
	got, err := SchemaVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if got != CurrentSchemaVersion() {
		t.Fatalf("SchemaVersion() = %d, want %d", got, CurrentSchemaVersion())
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

	got, err := SchemaVersion(ctx, verifier.DatabaseForTesting())
	if err != nil {
		t.Fatalf("SchemaVersion() error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("SchemaVersion() = %d, want %d", got, want)
	}

	var applied int
	if err := verifier.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

func TestSchemaCompatibilityRejectsCallerOlderThanDatabase(t *testing.T) {
	s := openTemp(t)
	_, err := CheckSchemaCompatibility(context.Background(), s.DatabaseForTesting(), CurrentSchemaVersion()-1)
	assertFailureKind(t, err, KindSchemaUnsupported)
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
