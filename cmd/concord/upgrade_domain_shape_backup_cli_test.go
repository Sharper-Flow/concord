package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

var shapeRetiredCLITables = []string{
	"knowledge_domain_facts", "knowledge_domain_roots", "knowledge_domain_shapes",
	"knowledge_package_domains", "law_evidence_paths",
}

var shapeRetainedCLITables = []string{
	"projects", "project_locators", "products", "product_projects", "work_items", "work_projects",
	"domain_registries", "domains", "domain_architecture_relations", "domain_relation_governing_laws",
	"law_subjects", "law_bodies", "law_domain_homes", "law_domain_applicability",
}

func seedV170ShapeCLIStore(t *testing.T) (string, string) {
	t.Helper()
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	seedCLIMigrationPrefix(t, path, 170)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('shape-project','Shape Project',1,'t','t')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('shape-locator','shape-project','canonical_path','/test/shape','/test/shape','t','t')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('shape-product','Shape Product','prototype','operator_only',1,'t','t')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('shape-product','shape-project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('shape-work','task','Shape retirement witness','needed',1,1,'t','t')`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('shape-work','shape-project','primary')`,
		`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('shape-product','shape-project','shape-locator','shape-product','shape-root','1.0','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','shape-product','shape-root','Root','Own product law','current','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','shape-product','shape-child','Child','Own child law','shape-root','current','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','decision','accepted','.concord/law/decisions/CD-0001.md','Retained law','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','Retained law body.','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','shape-product','shape-root','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_domain_applicability(home_project_id,home_locator_id,law_id,product_id,domain_id,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','shape-product','shape-child','commit')`,
		`INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,registry_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','shape-product','shape-child','depends_on','shape-root','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO domain_relation_governing_laws(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,law_id,law_content_hash) VALUES('shape-project','shape-locator','shape-product','shape-child','depends_on','shape-root','CD-0001','sha256:` + repeatCLI64() + `')`,
		`INSERT INTO knowledge_domain_shapes(home_project_id,home_locator_id,scanned_commit_oid,product_id,content_hash,registry_content_hash,map_content_hash,tooling_commit_oid,tooling_content_hash,required_checks) VALUES('shape-project','shape-locator','commit','shape-product','sha256:` + repeatCLI64() + `','sha256:` + repeatCLI64() + `','sha256:` + repeatCLI64() + `','commit','sha256:` + repeatCLI64() + `','[]')`,
		`INSERT INTO knowledge_domain_roots(home_project_id,home_locator_id,scanned_commit_oid,root_path,root_json) VALUES('shape-project','shape-locator','commit','internal/store','{"role":"domain","owner_domain_id":"shape-root"}')`,
		`INSERT INTO knowledge_domain_facts(home_project_id,home_locator_id,scanned_commit_oid,fact_id,fact_json,content_hash) VALUES('shape-project','shape-locator','commit','shape-fact','{"id":"shape-fact"}','sha256:` + repeatCLI64() + `')`,
		`INSERT INTO knowledge_package_domains(home_project_id,home_locator_id,package_path,domain_id,map_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','internal/store','shape-root','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_evidence_paths(home_project_id,home_locator_id,law_id,path,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','internal/store/store.go','commit')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed v170 (%s): %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return path, root
}

func shapeCLIState(t *testing.T, path string, includeRetired bool) map[string][]string {
	t.Helper()
	db := openRawSQLite(t, path)
	defer db.Close()
	tables := append([]string(nil), shapeRetainedCLITables...)
	if includeRetired {
		tables = append(tables, shapeRetiredCLITables...)
	}
	state := make(map[string][]string, len(tables))
	for _, table := range tables {
		state[table] = lawCoverageFixtureRows(t, db, table)
		if len(state[table]) == 0 {
			t.Fatalf("fixture premise: %s is empty", table)
		}
	}
	return state
}

func TestUpgradeRetiringDomainShapeSnapshotsRowsBeforeDrop(t *testing.T) {
	path, root := seedV170ShapeCLIStore(t)
	before := shapeCLIState(t, path, true)
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("upgrade v170 to current: %d %s", code, errOut)
	}
	var report struct {
		Applied       []int  `json:"applied"`
		SchemaVersion int    `json:"schema_version"`
		Backup        string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("upgrade report: %v: %s", err, out)
	}
	if !reflect.DeepEqual(report.Applied, []int{171}) || report.SchemaVersion != 171 {
		t.Fatalf("upgrade report = %+v, want migration 171 only", report)
	}
	if !strings.HasPrefix(report.Backup, filepath.Join(root, "backups")+string(filepath.Separator)) {
		t.Fatalf("snapshot is outside the existing backup route: %+v", report)
	}
	manifest, err := store.VerifyBackup(context.Background(), report.Backup)
	if err != nil || manifest.SchemaVersion != 170 {
		t.Fatalf("verified pre-upgrade snapshot schema=%d err=%v", manifest.SchemaVersion, err)
	}
	if snapshot := shapeCLIState(t, report.Backup, true); !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("snapshot changed retiring or retained rows:\nbefore: %v\nsnapshot: %v", before, snapshot)
	}
	db := openRawSQLite(t, path)
	for _, table := range shapeRetiredCLITables {
		var objects int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=? OR tbl_name=?`, table, table).Scan(&objects); err != nil || objects != 0 {
			t.Fatalf("retired %s catalog objects=%d err=%v", table, objects, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for table, rows := range shapeCLIState(t, path, false) {
		if !reflect.DeepEqual(rows, before[table]) {
			t.Fatalf("upgrade changed retained %s:\nbefore: %v\nafter: %v", table, before[table], rows)
		}
	}
}

func TestUpgradeSnapshotFailurePreventsDomainShapeDrop(t *testing.T) {
	path, _ := seedV170ShapeCLIStore(t)
	before := shapeCLIState(t, path, true)
	db := openRawSQLite(t, path)
	catalog := lawCoverageFixtureRows(t, db, "sqlite_schema")
	manifest := lawCoverageFixtureRows(t, db, "schema_migrations")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	calls := 0
	snapshotDatabase = func(context.Context, string, string) (store.BackupManifest, error) {
		calls++
		return store.BackupManifest{}, errors.New("synthetic snapshot failure")
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 || calls != 1 || !strings.Contains(errOut, "no migration was applied") {
		t.Fatalf("snapshot refusal code=%d calls=%d: %s", code, calls, errOut)
	}
	if after := shapeCLIState(t, path, true); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed snapshot changed rows:\nbefore: %v\nafter: %v", before, after)
	}
	db = openRawSQLite(t, path)
	if !reflect.DeepEqual(catalog, lawCoverageFixtureRows(t, db, "sqlite_schema")) || !reflect.DeepEqual(manifest, lawCoverageFixtureRows(t, db, "schema_migrations")) {
		t.Fatal("failed snapshot changed the schema or migration manifest")
	}
}

func TestUpgradeFailedDomainShapeDropKeepsVerifiedSnapshotAndRollsBack(t *testing.T) {
	path, _ := seedV170ShapeCLIStore(t)
	before := shapeCLIState(t, path, true)
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	snapshotPath := ""
	var liveBefore map[string][]string
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		manifest, err := backup(ctx, source, destination)
		if err != nil {
			return manifest, err
		}
		snapshotPath = destination
		// Backup verification stages an upgrade too. Inject only into the live
		// store after the real snapshot and its verification have succeeded.
		db, err := sql.Open("sqlite", store.SQLiteFileURI(source)+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE shape_drop_blocker(home_project_id TEXT,home_locator_id TEXT,law_id TEXT,path TEXT,FOREIGN KEY(home_project_id,home_locator_id,law_id,path) REFERENCES law_evidence_paths(home_project_id,home_locator_id,law_id,path) ON DELETE RESTRICT);
INSERT INTO shape_drop_blocker VALUES('shape-project','shape-locator','CD-0001','internal/store/store.go')`); err != nil {
			t.Fatal(err)
		}
		liveBefore = shapeCLIState(t, source, true)
		for _, table := range []string{"sqlite_schema", "schema_migrations", "write_guard", "shape_drop_blocker"} {
			liveBefore[table] = lawCoverageFixtureRows(t, db, table)
		}
		return manifest, nil
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 || liveBefore == nil || !strings.Contains(errOut, "migration 171 (retire_domain_shape) failed") || !strings.Contains(strings.ToLower(errOut), "foreign key") {
		t.Fatalf("induced final-drop failure code=%d: %s", code, errOut)
	}
	verified, err := store.VerifyBackup(context.Background(), snapshotPath)
	if err != nil || verified.SchemaVersion != 170 {
		t.Fatalf("failed migration recovery snapshot schema=%d err=%v", verified.SchemaVersion, err)
	}
	if snapshot := shapeCLIState(t, snapshotPath, true); !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("recovery snapshot changed rows:\nbefore: %v\nsnapshot: %v", before, snapshot)
	}
	after := shapeCLIState(t, path, true)
	db := openRawSQLite(t, path)
	for _, table := range []string{"sqlite_schema", "schema_migrations", "write_guard", "shape_drop_blocker"} {
		after[table] = lawCoverageFixtureRows(t, db, table)
	}
	if !reflect.DeepEqual(after, liveBefore) {
		t.Fatalf("failed migration changed live pre-migration state:\nbefore: %v\nafter: %v", liveBefore, after)
	}
}

func TestUpgradeDomainShapeReadingReportsPreviousBinaryRecovery(t *testing.T) {
	path, _ := seedV170ShapeCLIStore(t)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO work_context_state(work_id,source_frontier,declaration_seq,declaration_id,declaration_payload) VALUES('shape-work',1,1,'legacy-declaration','{"required_reading":[{"domain_id":"shape-child","reason":"Required reading","source":{"kind":"domain_shape","source_id":"shape-project","selector":{"path":"internal/store"}}},{"domain_id":"shape-child","reason":"Retained reading","source":{"kind":"knowledge","source_id":"concord_knowledge","law_id":"CD-0001","content_hash":"sha256:` + repeatCLI64() + `"}}],"finding_refs":[],"context_findings":[]}');
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	tables := []string{"sqlite_schema", "schema_migrations", "store_revision", "write_guard", "work_context_state"}
	before := shapeCLIState(t, path, true)
	for _, table := range tables {
		before[table] = lawCoverageFixtureRows(t, db, table)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("upgrade admitted an obsolete reading: code=%d diagnostic=%s", code, errOut)
	}
	for _, fragment := range []string{"domain_shape", "shape-work", "pre-retirement binary", "schema 170", "record_work_context", "replace", "remove", "preserving the rest of the declaration", "retry"} {
		if !strings.Contains(errOut, fragment) {
			t.Errorf("upgrade diagnostic %q lacks actionable recovery fragment %q", errOut, fragment)
		}
	}
	after := shapeCLIState(t, path, true)
	db = openRawSQLite(t, path)
	defer db.Close()
	for _, table := range tables {
		after[table] = lawCoverageFixtureRows(t, db, table)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("refused upgrade changed the live store:\nbefore: %v\nafter: %v", before, after)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	// Model the live declaration's replacement, not a terminal-work mutation.
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE work_context_state SET declaration_seq=2,declaration_id='replacement-declaration',source_frontier=2,declaration_payload=json_remove(declaration_payload,'$.required_reading[0]') WHERE work_id='shape-work';
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	remediated := lawCoverageFixtureRows(t, db, "work_context_state")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("upgrade after modeled live removal: code=%d diagnostic=%s", code, errOut)
	}
	var report struct {
		Applied []int  `json:"applied"`
		Backup  string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || !reflect.DeepEqual(report.Applied, []int{171}) {
		t.Fatalf("remediated upgrade report=%s err=%v", out, err)
	}
	manifest, err := store.VerifyBackup(context.Background(), report.Backup)
	if err != nil || manifest.SchemaVersion != 170 {
		t.Fatalf("remediated upgrade snapshot schema=%d err=%v", manifest.SchemaVersion, err)
	}
	for _, source := range []string{report.Backup, path} {
		db = openRawSQLite(t, source)
		if rows := lawCoverageFixtureRows(t, db, "work_context_state"); !reflect.DeepEqual(rows, remediated) {
			t.Fatalf("upgrade changed the remediated declaration in %s: %v", source, rows)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
