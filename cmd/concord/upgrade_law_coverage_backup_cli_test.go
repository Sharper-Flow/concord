package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

// CD-0249 D1 retires per-law coverage through the existing breaking-migration
// path, and its verified-snapshot preservation requirement applies before the
// table drop. These tests drive the real concord upgrade route over a v165
// store carrying populated law_coverage_anchors rows: the route must write a
// verified snapshot of the pre-upgrade store, the live store must lose only
// the coverage table, and a snapshot failure must apply no migration.

var lawCoverageFixtureTables = []string{
	"law_subjects", "law_bodies", "law_evidence_paths",
	"knowledge_package_domains", "knowledge_kind_coverage",
	"projects", "work_items",
}

// seedV165LawCoverageRows populates a v165 store the way the retired
// mechanism left it: one law's coverage anchors beside the rows migration 166
// must preserve. The inserts ride write_guard because every guarded table at
// v165 is fold-only.
func seedV165LawCoverageRows(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('coverage-project','Coverage Project',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('coverage-locator','coverage-project','canonical_path','/test/coverage','/test/coverage','2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('coverage-product','Coverage Product','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('coverage-product','coverage-project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('coverage-work','task','Coverage retirement witness','needed',1,1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('coverage-work','coverage-project','primary')`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249','decision','accepted','.concord/docs/decisions/CD-0249.md','Retire law coverage','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249','Retire per-law coverage.','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO law_evidence_paths(home_project_id,home_locator_id,law_id,path,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249','internal/store/store.go','commit')`,
		`INSERT INTO knowledge_package_domains(home_project_id,home_locator_id,package_path,domain_id,map_content_hash,scanned_commit_oid) VALUES('coverage-project','coverage-locator','internal/store','coverage-root','sha256:` + repeatCLI64() + `','commit')`,
		`INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('coverage-project','coverage-locator','HEAD','work_note','indexed','canonical .concord/docs/work directory scanned','commit')`,
		`INSERT INTO law_coverage_anchors(home_project_id,home_locator_id,law_id,ordinal,kind,value,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249',0,'go_test','internal/store.TestRetired','commit')`,
		`INSERT INTO law_coverage_anchors(home_project_id,home_locator_id,law_id,ordinal,kind,value,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249',1,'validator','scripts/check-retired.py','commit')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed v165 coverage storage (%s): %v", statement, err)
		}
	}
}

func repeatCLI64() string { return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }

// lawCoverageFixtureRows renders every row of one table, so the live store
// and the verified snapshot compare by content.
func lawCoverageFixtureRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var rendered []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		rendered = append(rendered, string(encoded))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return rendered
}

func lawCoverageFixtureState(t *testing.T, db *sql.DB, includeCoverage bool) map[string][]string {
	t.Helper()
	state := make(map[string][]string, len(lawCoverageFixtureTables)+1)
	if includeCoverage {
		state["law_coverage_anchors"] = lawCoverageFixtureRows(t, db, "law_coverage_anchors")
	}
	for _, table := range lawCoverageFixtureTables {
		state[table] = lawCoverageFixtureRows(t, db, table)
	}
	return state
}

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestUpgradeRetiringLawCoverageSnapshotsTheCoverageTableBeforeTheDrop runs
// the concord upgrade route over a v165 store with populated coverage rows
// and proves the verified pre-upgrade snapshot holds the original table and
// rows while the live store loses only the coverage objects.
func TestUpgradeRetiringLawCoverageSnapshotsTheCoverageTableBeforeTheDrop(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	seedCLIMigrationPrefix(t, path, 165)
	seedV165LawCoverageRows(t, path)

	beforeDB := openRawSQLite(t, path)
	before := lawCoverageFixtureState(t, beforeDB, true)
	if len(before["law_coverage_anchors"]) != 2 {
		t.Fatalf("fixture premise: coverage rows = %d, want 2", len(before["law_coverage_anchors"]))
	}
	if err := beforeDB.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("the v165 to current upgrade must apply: %d %s", code, errOut)
	}
	var report struct {
		Applied       []int  `json:"applied"`
		SchemaVersion int    `json:"schema_version"`
		Backup        string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("upgrade output is not JSON: %v: %s", err, out)
	}
	var expectedApplied []int
	for schema := 166; schema <= store.CurrentSchemaVersion(); schema++ {
		expectedApplied = append(expectedApplied, schema)
	}
	if !reflect.DeepEqual(report.Applied, expectedApplied) || report.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("applied = %+v schema = %d, want %v", report.Applied, report.SchemaVersion, expectedApplied)
	}
	if !strings.HasPrefix(report.Backup, filepath.Join(root, "backups")+string(filepath.Separator)) {
		t.Fatalf("the report must name a snapshot under the data root: %+v", report)
	}

	// The snapshot is verified by the route's own verification rules, and its
	// manifest records the pre-upgrade schema the store actually had.
	manifest, err := store.VerifyBackup(context.Background(), report.Backup)
	if err != nil {
		t.Fatalf("the pre-upgrade snapshot must verify: %v", err)
	}
	if manifest.SchemaVersion != 165 {
		t.Fatalf("snapshot manifest schema = %d, want the unmigrated 165", manifest.SchemaVersion)
	}

	// The verified snapshot contains the original coverage table and rows and
	// the preserved inventory, exactly as they were before the drop.
	snapshotDB := openRawSQLite(t, report.Backup)
	snapshot := lawCoverageFixtureState(t, snapshotDB, true)
	if !reflect.DeepEqual(snapshot["law_coverage_anchors"], before["law_coverage_anchors"]) {
		t.Fatalf("snapshot lost the original coverage rows:\nbefore: %v\nsnapshot: %v", before["law_coverage_anchors"], snapshot["law_coverage_anchors"])
	}
	for _, table := range lawCoverageFixtureTables {
		if !reflect.DeepEqual(snapshot[table], before[table]) {
			t.Fatalf("snapshot changed %s:\nbefore: %v\nsnapshot: %v", table, before[table], snapshot[table])
		}
	}
	if err := snapshotDB.Close(); err != nil {
		t.Fatal(err)
	}

	// The live store lost only the coverage objects and preserves every other
	// seeded row at the new schema head.
	liveDB := openRawSQLite(t, path)
	var objects int
	if err := liveDB.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'law_coverage_anchors%'`).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if objects != 0 {
		t.Fatalf("the live store kept %d law_coverage_anchors objects", objects)
	}
	var head int
	if err := liveDB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != store.CurrentSchemaVersion() {
		t.Fatalf("live schema head = %d, want %d", head, store.CurrentSchemaVersion())
	}
	after := lawCoverageFixtureState(t, liveDB, false)
	for _, table := range lawCoverageFixtureTables {
		if !reflect.DeepEqual(after[table], before[table]) {
			t.Fatalf("the upgrade changed %s:\nbefore: %v\nafter: %v", table, before[table], after[table])
		}
	}
	var coverage int
	err = liveDB.QueryRow(`SELECT count(*) FROM law_coverage_anchors`).Scan(&coverage)
	if err == nil {
		t.Fatal("the retired table still answers on the live store")
	}
}

// TestUpgradeSnapshotFailurePreventsTheCoverageDrop overrides the route's
// snapshot seam to fail and proves the refusal: no migration applies, the
// coverage table and its rows survive, and the manifest stays at 165.
func TestUpgradeSnapshotFailurePreventsTheCoverageDrop(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	seedCLIMigrationPrefix(t, path, 165)
	seedV165LawCoverageRows(t, path)

	beforeDB := openRawSQLite(t, path)
	before := lawCoverageFixtureState(t, beforeDB, true)
	if err := beforeDB.Close(); err != nil {
		t.Fatal(err)
	}

	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		return store.BackupManifest{}, errors.New("synthetic snapshot failure")
	}

	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("a failed snapshot must fail the upgrade: %d", code)
	}
	if !strings.Contains(errOut, "no migration was applied") {
		t.Fatalf("a failed snapshot must refuse before any migration: %s", errOut)
	}

	liveDB := openRawSQLite(t, path)
	var head int
	if err := liveDB.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != 165 {
		t.Fatalf("live schema head = %d, want the untouched 165", head)
	}
	var anchors int
	if err := liveDB.QueryRow(`SELECT count(*) FROM law_coverage_anchors`).Scan(&anchors); err != nil {
		t.Fatalf("the coverage table must survive a failed snapshot: %v", err)
	}
	if anchors != len(before["law_coverage_anchors"]) {
		t.Fatalf("coverage rows = %d, want the seeded %d", anchors, len(before["law_coverage_anchors"]))
	}
	after := lawCoverageFixtureState(t, liveDB, true)
	for _, table := range lawCoverageFixtureTables {
		if !reflect.DeepEqual(after[table], before[table]) {
			t.Fatalf("the refused upgrade changed %s:\nbefore: %v\nafter: %v", table, before[table], after[table])
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed snapshot step must leave no snapshot behind: %v", entries)
	}
}
