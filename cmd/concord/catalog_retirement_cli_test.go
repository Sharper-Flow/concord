package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

const catalogRecoveryTag = "v20.5.5"
const catalogRecoveryCommit = "869c7b6a24be642014c265bde9c5e83c23a19137"
const catalogRecoverySchema = 162

var catalogRecoveryInventoryTables = []string{
	"managed_resources", "resource_products", "domain_resource_attachment_sets", "domain_resource_attachment_edges",
}

var catalogRecoveryCurrentTables = []string{
	"projects", "project_locators", "products", "product_projects", "work_items", "work_projects", "domains", "domain_registries",
}

// catalogRecoveryRetainedTables is the part of the current state the upgrade
// tail keeps: schema 174 retires the Domain projection (domains,
// domain_registries) with the legacy law index, so the live post-upgrade
// comparison covers the retained tables and asserts the retired two absent.
var catalogRecoveryRetainedTables = []string{
	"projects", "project_locators", "products", "product_projects", "work_items", "work_projects",
}

func catalogRecoveryRows(t *testing.T, path string, tables []string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := make(map[string][]string, len(tables))
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var population []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			population = append(population, mustJSON(t, values))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(population)
		result[table] = population
	}
	return result
}

// The seed uses test-only SQL for independent current state. Inventory and
// attachment rows enter through the released CLI's own guarded operations.
func seedCatalogRecoveryCurrentRows(t *testing.T, path string) {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`PRAGMA defer_foreign_keys=ON`,
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('recovery-project','Recovery Project',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('recovery-locator','recovery-project','canonical_path','/test/recovery','/test/recovery','2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('recovery-owner','Recovery Owner','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z'),('recovery-consumer','Recovery Consumer','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('recovery-owner','recovery-project','primary'),('recovery-consumer','recovery-project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('recovery-work','task','Recovery work','needed',1,1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('recovery-work','recovery-project','primary')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('recovery-project','recovery-locator','recovery-owner','recovery-root','Root','recovery fixture',NULL,'current','sha256:` + strings.Repeat("a", 64) + `','commit')`,
		`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('recovery-owner','recovery-project','recovery-locator','recovery','recovery-root','1.0','sha256:` + strings.Repeat("a", 64) + `','commit')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed schema 162 (%s): %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRetirementReleasedBackupRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the released recovery core and candidate core")
	}
	source := extractReleasedSource(t, catalogRecoveryTag)
	oid, err := exec.Command("git", "-C", repoSourceDir(t), "rev-parse", catalogRecoveryTag+"^{commit}").Output()
	if err != nil || strings.TrimSpace(string(oid)) != catalogRecoveryCommit {
		t.Fatalf("recovery tag must resolve to exact commit %s: %q %v", catalogRecoveryCommit, oid, err)
	}
	released := buildCoreFrom(t, source, catalogRecoveryTag)
	if descriptor := coreDescriptor(t, released); descriptor["version"] != catalogRecoveryTag || descriptor["schema_version"] != float64(catalogRecoverySchema) {
		t.Fatalf("released recovery core identity: %+v", descriptor)
	}
	candidate := buildCoreFrom(t, repoSourceDir(t), "v0.0.0-catalog-retirement-candidate")
	currentSchema := store.CurrentSchemaVersion()
	if descriptor := coreDescriptor(t, candidate); descriptor["schema_version"] != float64(currentSchema) {
		t.Fatalf("candidate must define current schema %d: %+v", currentSchema, descriptor)
	}
	root := t.TempDir()
	path := filepath.Join(root, "source.db")
	run := func(binary, verb string, request any) string {
		t.Helper()
		code, out, diagnostic := runRelease(t, binary, path, verb, mustJSON(t, request))
		if code != 0 {
			t.Fatalf("%s %s: exit=%d stdout=%s stderr=%s", binary, verb, code, out, diagnostic)
		}
		return out
	}
	run(released, "upgrade", map[string]any{})
	seedCatalogRecoveryCurrentRows(t, path)
	run(released, "resource-create", map[string]any{
		"event_id": "recovery-resource-create", "resource_id": "recovery-queue", "product_id": "recovery-owner",
		"display_name": "Recovery Queue", "class": "infrastructure", "kind": "queue", "purpose": "dispatch recovery work",
		"stage_maturity": "production", "stage_audience_commitment": "limited", "environments": []string{"production", "test"},
		"metadata_schema_version": "1", "metadata": map[string]any{"fixture": "recovery"}, "expected_product_version": 1,
	})
	run(released, "resource-share", map[string]any{
		"event_id": "recovery-resource-share", "resource_id": "recovery-queue", "product_id": "recovery-consumer",
		"purpose": "consume recovery work", "environments": []string{"test"}, "expected_resource_version": 1,
	})
	run(released, "domain-resource-attachments-replace", map[string]any{
		"event_id": "recovery-attachment", "product_id": "recovery-owner", "domain_id": "recovery-root", "expected_version": 0,
		"attachments": []any{map[string]any{"resource_id": "recovery-queue", "purpose": "dispatch Domain work", "environments": []string{"production"}}},
	})
	inventory := catalogRecoveryRows(t, path, catalogRecoveryInventoryTables)
	for table, want := range map[string]int{"managed_resources": 1, "resource_products": 2, "domain_resource_attachment_sets": 1, "domain_resource_attachment_edges": 1} {
		if len(inventory[table]) != want {
			t.Fatalf("fixture %s rows=%d, want %d", table, len(inventory[table]), want)
		}
	}
	current := catalogRecoveryRows(t, path, catalogRecoveryCurrentTables)
	currentRetained := catalogRecoveryRows(t, path, catalogRecoveryRetainedTables)
	snapshot := filepath.Join(root, "inventory-snapshot.db")
	var manifest store.BackupManifest
	if err := json.Unmarshal([]byte(run(released, "backup", map[string]any{"destination": snapshot})), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != catalogRecoverySchema || manifest.DBChecksum == "" {
		t.Fatalf("released backup must produce a schema 162 manifest: %+v", manifest)
	}
	verifyAndRecover := func(destination string) {
		t.Helper()
		verified, err := store.VerifyBackup(context.Background(), snapshot, catalogRecoverySchema)
		if err != nil || mustJSON(t, verified) != mustJSON(t, manifest) {
			t.Fatalf("verify exact released snapshot: %+v %v", verified, err)
		}
		// The released backup command verifies its output; restore independently
		// verifies the snapshot before recovering into a new destination.
		var report store.RestoreReport
		if err := json.Unmarshal([]byte(run(released, "restore", map[string]any{"source": snapshot, "destination": destination})), &report); err != nil {
			t.Fatal(err)
		}
		if mustJSON(t, report.SourceSnapshot) != mustJSON(t, manifest) || report.RestoredDatabase.SchemaVersion != catalogRecoverySchema {
			t.Fatalf("released restore must recover the exact source at schema 162: %+v", report)
		}
		if recovered := catalogRecoveryRows(t, destination, catalogRecoveryInventoryTables); !reflect.DeepEqual(recovered, inventory) {
			t.Fatalf("released restore changed inventory:\nwant: %v\ngot: %v", inventory, recovered)
		}
		if recovered := catalogRecoveryRows(t, destination, catalogRecoveryCurrentTables); !reflect.DeepEqual(recovered, current) {
			t.Fatalf("released restore changed current rows:\nwant: %v\ngot: %v", current, recovered)
		}
	}
	verifyAndRecover(filepath.Join(root, "recovered-before-upgrade.db"))
	var upgrade struct {
		SchemaVersion int   `json:"schema_version"`
		Applied       []int `json:"applied"`
	}
	if err := json.Unmarshal([]byte(run(candidate, "upgrade", map[string]any{})), &upgrade); err != nil {
		t.Fatal(err)
	}
	var expectedApplied []int
	for version := catalogRecoverySchema + 1; version <= currentSchema; version++ {
		expectedApplied = append(expectedApplied, version)
	}
	if upgrade.SchemaVersion != currentSchema || !reflect.DeepEqual(upgrade.Applied, expectedApplied) {
		t.Fatalf("candidate must apply the complete breaking tail: %+v", upgrade)
	}
	db := mustOpenCutoverStore(t, path)
	for _, table := range append(append([]string(nil), catalogRecoveryInventoryTables...), "domains", "domain_registries") {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("catalog object %s survived candidate upgrade: count=%d error=%v", table, count, err)
		}
	}
	if after := catalogRecoveryRows(t, path, catalogRecoveryRetainedTables); !reflect.DeepEqual(currentRetained, after) {
		t.Fatalf("candidate changed current rows:\nbefore: %v\nafter: %v", currentRetained, after)
	}
	verifyAndRecover(filepath.Join(root, "recovered-after-upgrade.db"))
	if after := catalogRecoveryRows(t, snapshot, catalogRecoveryInventoryTables); !reflect.DeepEqual(inventory, after) {
		t.Fatalf("verification or upgrade changed snapshot inventory: %v", after)
	}
}
