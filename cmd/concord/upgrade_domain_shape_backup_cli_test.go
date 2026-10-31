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
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE shape_drop_blocker(home_project_id TEXT,home_locator_id TEXT,law_id TEXT,path TEXT,FOREIGN KEY(home_project_id,home_locator_id,law_id,path) REFERENCES law_evidence_paths(home_project_id,home_locator_id,law_id,path) ON DELETE RESTRICT);
INSERT INTO shape_drop_blocker VALUES('shape-project','shape-locator','CD-0001','internal/store/store.go')`); err != nil {
		t.Fatal(err)
	}
	catalog := lawCoverageFixtureRows(t, db, "sqlite_schema")
	manifest := lawCoverageFixtureRows(t, db, "schema_migrations")
	guard := lawCoverageFixtureRows(t, db, "write_guard")
	blocker := lawCoverageFixtureRows(t, db, "shape_drop_blocker")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := shapeCLIState(t, path, true)
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	snapshotPath := ""
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		snapshotPath = destination
		return backup(ctx, source, destination)
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 || !strings.Contains(errOut, "migration 171 (retire_domain_shape) failed") || !strings.Contains(strings.ToLower(errOut), "foreign key") {
		t.Fatalf("induced final-drop failure code=%d: %s", code, errOut)
	}
	verified, err := store.VerifyBackup(context.Background(), snapshotPath)
	if err != nil || verified.SchemaVersion != 170 {
		t.Fatalf("failed migration recovery snapshot schema=%d err=%v", verified.SchemaVersion, err)
	}
	if snapshot := shapeCLIState(t, snapshotPath, true); !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("recovery snapshot changed rows:\nbefore: %v\nsnapshot: %v", before, snapshot)
	}
	if after := shapeCLIState(t, path, true); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed migration changed rows:\nbefore: %v\nafter: %v", before, after)
	}
	db = openRawSQLite(t, path)
	for table, want := range map[string][]string{"sqlite_schema": catalog, "schema_migrations": manifest, "write_guard": guard, "shape_drop_blocker": blocker} {
		if got := lawCoverageFixtureRows(t, db, table); !reflect.DeepEqual(got, want) {
			t.Fatalf("failed migration changed %s:\nbefore: %v\nafter: %v", table, want, got)
		}
	}
}
