package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

var upgradeResearchTables = []string{
	"active_research_packs", "active_research_revisions", "active_research_findings",
	"active_research_sources", "active_research_finding_sources",
	"active_research_finding_scopes", "active_research_consumers",
}

func seedUpgradeResearch(t *testing.T, path string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO fold_guard(active) VALUES(1)`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('research-product','Product','prototype','operator_only',1,'now','now')`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('research-project','Project',1,'now','now')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('research-product','research-project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES ('research-owner','task','Owner','needed',0,1,'now','now',NULL),('research-consumer','task','Consumer','needed',0,1,'now','now',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('research-owner','research-project','primary'),('research-consumer','research-project','primary')`,
		`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES ('research-pack','research-owner',1,'current',1,'now','now')`,
		`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES ('research-pack',1,'Question','{}','{}','[]','Method','now')`,
		`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) VALUES ('research-pack',1,'finding','observation','Finding','high','current','active','explicit')`,
		`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at) VALUES ('research-pack',1,'source','official_doc','https://example.com/research','Source','Example',NULL,'now')`,
		`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) VALUES ('research-pack',1,'finding','source')`,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) VALUES ('research-pack',1,'finding','project','project')`,
		`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES ('research-pack',1,'research-consumer','context',1,'now')`,
		`DELETE FROM fold_guard`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed research (%s): %v", statement, err)
		}
	}
	return upgradeResearchRows(t, db)
}

func upgradeResearchRows(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	result := make(map[string][]string, len(upgradeResearchTables))
	for _, table := range upgradeResearchTables {
		result[table] = lawCoverageFixtureRows(t, db, table)
		if len(result[table]) != 1 {
			t.Fatalf("%s: expected one fixture row, got %v", table, result[table])
		}
	}
	return result
}

func assertUpgradeResearchRows(t *testing.T, path string, want map[string][]string) {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := upgradeResearchRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("research rows changed: got %v, want %v", got, want)
	}
}

func TestUpgradeResearchExportsVerifiedSnapshot(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	want := seedUpgradeResearch(t, path)

	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("upgrade failed: %d %s", code, errOut)
	}
	var report struct {
		Applied        []int  `json:"applied"`
		Backup         string `json:"backup"`
		ResearchExport string `json:"research_export"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid upgrade JSON: %v: %s", err, out)
	}
	rel, err := filepath.Rel(root, report.ResearchExport)
	if err != nil || !filepath.IsAbs(report.ResearchExport) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("research export must be under data root: %+v (%v)", report, err)
	}
	if info, err := os.Stat(report.ResearchExport); err != nil || !info.IsDir() {
		t.Fatalf("research export is not a directory: %v", err)
	}
	if _, err := store.VerifyBackup(context.Background(), report.Backup); err != nil {
		t.Fatalf("verify snapshot: %v", err)
	}
	if err := store.VerifyResearchExport(context.Background(), report.Backup, report.ResearchExport); err != nil {
		t.Fatalf("verify export against snapshot: %v", err)
	}
	assertUpgradeResearchRows(t, report.Backup, want)
	assertSnapshotLacksMigration(t, report.Backup, 183)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=183`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 183: count=%d, error=%v", applied, err)
	}
	for _, table := range upgradeResearchTables {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Errorf("retired table %s: count=%d, error=%v", table, count, err)
		}
	}
}

func TestUpgradeResearchExportRefusalKeepsSnapshotAndStore(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, _ := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	want := seedUpgradeResearch(t, path)
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	var snapshot string
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		manifest, err := backup(ctx, source, destination)
		if err != nil {
			return manifest, err
		}
		snapshot = destination
		return manifest, os.WriteFile(destination+".research", []byte("keep"), 0o600)
	}
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 || !strings.Contains(errOut, "research export") {
		t.Fatalf("expected research export refusal: code=%d stdout=%s stderr=%s", code, out, errOut)
	}
	if snapshot == "" {
		t.Fatal("snapshot was not created")
	}
	if _, err := store.VerifyBackup(context.Background(), snapshot); err != nil {
		t.Fatalf("verify retained snapshot: %v", err)
	}
	assertSnapshotLacksMigration(t, snapshot, 183)
	assertSnapshotLacksMigration(t, path, 111)
	assertSnapshotLacksMigration(t, path, 183)
	assertUpgradeResearchRows(t, snapshot, want)
	assertUpgradeResearchRows(t, path, want)
	if data, err := os.ReadFile(snapshot + ".research"); err != nil || string(data) != "keep" {
		t.Fatalf("collision changed: %q, %v", data, err)
	}
}
