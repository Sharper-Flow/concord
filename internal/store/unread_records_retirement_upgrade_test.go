package store

import (
	"database/sql"
	"testing"

	"github.com/sharper-flow/concord/internal/version"
)

// unreadRecordsMigration returns migration 190, which retires the
// readerless workflow records.
func unreadRecordsMigration(t *testing.T) migration {
	t.Helper()
	m := migrations[migrationPrefixBefore(t, "retire_unread_workflow_records")]
	if m.Version != 190 {
		t.Fatalf("expected migration 190, got %d", m.Version)
	}
	return m
}

// seedUnreadRecordsFixture builds a pre-190 store: contracts carrying the
// four retired fields, candidate-set rows, and receipts of the retired
// candidate-revision kind. The retired-kind receipts must survive the
// upgrade (migrations 185 and 186 set that precedent for effect_receipts).
func seedUnreadRecordsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	stmts := []string{
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-one','Product','prototype','operator_only',1,'t','t')`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-one','Project',1,'t','t')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('product-one','project-one','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('work-unread','task','Fixture','needed',1,'t','t')`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('work-unread','project-one','primary')`,
		`INSERT INTO workflow_instances(work_id,instance_state,prelude_state) VALUES('work-unread','intake','{"intake":{"outcome":"proceed","note":"retired prose","checks_found":["freshness"]}}')`,
	}
	execUnreadSeed(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:one','client:one','agent:one','session:one','agent','t')`, actor)
	for _, stmt := range stmts {
		execUnreadSeed(t, db, stmt)
	}
	execUnreadSeed(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,changes_product_truth,legislative_subjects)
VALUES('work-unread',1,'Retired premise fixture','internal_sqlite','["route:one"]','t',?,'["spec:one"]','[]',1,'production_public','["legal"]',1,'["spec:one"]')`, actor)
	execUnreadSeed(t, db, `INSERT INTO workflow_candidate_sets(work_id,contract_version,candidate_kind,candidate_ref,candidate_role,candidate_scope,recorded_at,recorded_by)
VALUES('work-unread',1,'work_item','candidate:one','include','{}','candidate-time',?)`, actor)
	execUnreadSeed(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest)
VALUES('event:candidate-retired',41,'workflow.candidate_set_revised','work_item','work-unread',?,1,'sha256:'||hex(randomblob(32)))`, actor)
}

func execUnreadSeed(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadRecordsRetirementUpgrade(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path := t.TempDir() + "/source.db"
	db := openMigratedTo(t, path, migrationPrefixBefore(t, "retire_unread_workflow_records"))
	seedUnreadRecordsFixture(t, db)
	report, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Applied) != 6 || report.Applied[0] != 190 || report.Applied[1] != 191 || report.Applied[2] != 192 || report.Applied[3] != 193 || report.Applied[4] != 194 || report.Applied[5] != 195 {
		t.Fatalf("applied = %v, want exactly [190 191 192 193 194 195]", report.Applied)
	}
	var candidateTable int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='workflow_candidate_sets'`).Scan(&candidateTable); err != nil || candidateTable != 0 {
		t.Fatalf("workflow_candidate_sets survives retirement: count=%d err=%v", candidateTable, err)
	}
	for _, column := range []string{"premise", "route_conventions", "rigor_class", "decision_domains"} {
		var present int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name=?`, column).Scan(&present); err != nil || present != 0 {
			t.Fatalf("retired column %s survives: present=%d err=%v", column, present, err)
		}
	}
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='workflow.candidate_set_revised' AND subject_id='work-unread'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("retired-kind receipt not preserved: count=%d err=%v", receipts, err)
	}
	var mandate, subjects string
	if err := db.QueryRow(`SELECT spec_mandate,legislative_subjects FROM workflow_contracts WHERE work_id='work-unread' AND contract_version=1`).Scan(&mandate, &subjects); err != nil || mandate != `["spec:one"]` || subjects != `["spec:one"]` {
		t.Fatalf("retained contract facts lost: mandate=%s subjects=%s err=%v", mandate, subjects, err)
	}
	var prelude string
	if err := db.QueryRow(`SELECT prelude_state FROM workflow_instances WHERE work_id='work-unread'`).Scan(&prelude); err != nil {
		t.Fatal(err)
	}
	if prelude != `{"intake":{"outcome":"proceed"}}` {
		t.Fatalf("retired prelude prose survived the upgrade: %s", prelude)
	}
	if got := retirementUpgradeRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign_key_check = %v", got)
	}
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,changes_product_truth,legislative_subjects) VALUES('work-unread',2,'internal_sqlite','t',?,'[]','[]',1,1,'[]')`, actor); err == nil {
		t.Fatal("unguarded contract write succeeded after migration 190")
	}
}
