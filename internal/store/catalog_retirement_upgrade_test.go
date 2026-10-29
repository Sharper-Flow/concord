package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// seedV162CatalogDatabase applies every migration through 162 to a fresh
// database file and folds a synthetic population that spans the retired
// catalog tables and the independent current-state tables. The helper exists
// only for the catalog-retirement upgrade proofs.
func seedV162CatalogDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:162] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-10-29T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// One explicit transaction: deferred foreign keys surface at commit, not
	// per statement, so the circular registry/Domain pair seeds cleanly.
	seed, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		_ = seed.Rollback()
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('upgrade-project','Upgrade Project',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`, nil},
		{`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('upgrade-locator','upgrade-project','canonical_path','/test/upgrade','/test/upgrade','2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`, nil},
		{`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('upgrade-product','Upgrade Product','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`, nil},
		{`INSERT INTO product_projects(product_id,project_id,role) VALUES('upgrade-product','upgrade-project','primary')`, nil},
		{`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('upgrade-work','task','Upgrade work','needed',1,1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z',NULL)`, nil},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('upgrade-project','upgrade-locator','upgrade-product','upgrade-root','Root','test root',NULL,'current','sha256:` + repeatA64() + `','commit')`, nil},
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('upgrade-product','upgrade-project','upgrade-locator','upgrade','upgrade-root','1.0','sha256:` + repeatA64() + `','commit')`, nil},
		{`INSERT INTO domain_project_attachment_sets(product_id,domain_id,version) VALUES('upgrade-product','upgrade-root',1)`, nil},
		{`INSERT INTO domain_project_attachment_edges(product_id,domain_id,project_id,role) VALUES('upgrade-product','upgrade-root','upgrade-project','primary')`, nil},
		{`INSERT INTO managed_resources(resource_id,display_name,class,kind,purpose,stage_maturity,stage_audience_commitment,environments,locator_absence_reason,metadata_schema_version,metadata,version,created_at,updated_at) VALUES('upgrade-queue','Upgrade Queue','infrastructure','queue','dispatches upgrade work','production','limited','["production"]',NULL,'1','{}',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`, nil},
		{`INSERT INTO resource_products(resource_id,product_id,role,purpose,environments) VALUES('upgrade-queue','upgrade-product','owner','operates the queue','["production"]')`, nil},
		{`INSERT INTO domain_resource_attachment_sets(product_id,domain_id,version) VALUES('upgrade-product','upgrade-root',1)`, nil},
		{`INSERT INTO domain_resource_attachment_edges(product_id,domain_id,resource_id,purpose,environments) VALUES('upgrade-product','upgrade-root','upgrade-queue','uses the queue','["production"]')`, nil},
	}
	for _, statement := range statements {
		if _, err := seed.ExecContext(ctx, statement.query, statement.args...); err != nil {
			_ = seed.Rollback()
			t.Fatalf("seed v162 row (%.60s): %v", statement.query, err)
		}
	}
	if _, err := seed.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		_ = seed.Rollback()
		t.Fatal(err)
	}
	if err := seed.Commit(); err != nil {
		t.Fatalf("commit v162 seed: %v", err)
	}
	return db
}

// TestMigrateV162ToV163RetiresCatalogAndPreservesCurrentState proves the
// breaking catalog retirement drops only the retired objects while every
// unrelated current-state row survives, and that the pre-upgrade inventory
// remains recoverable through a verified snapshot.
func TestMigrateV162ToV163RetiresCatalogAndPreservesCurrentState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "concord-v162.db")
	db := seedV162CatalogDatabase(t, path)
	defer db.Close()

	// The pre-upgrade snapshot keeps the old inventory recoverable. The
	// snapshot goes through the same SQLite Online Backup mechanism Backup
	// uses, from a read-only source open. The dev-build verify/restore
	// pipeline refuses an old-schema snapshot (CD-0139: an unstamped build
	// never migrates an existing store), so this test proves the snapshot
	// bytes and that refusal disposition directly instead of pretending the
	// dev binary serves the old schema.
	snapshotDB, err := sqlitedb.Open(readOnlyDataSource(path))
	if err != nil {
		t.Fatalf("open the v162 database read-only: %v", err)
	}
	conn, err := snapshotDB.Conn(ctx)
	if err != nil {
		_ = snapshotDB.Close()
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(root, "pre-upgrade-snapshot.db")
	if err := mechanismFailure(sqlitedb.BackupSnapshot(ctx, conn, SQLiteFileURI(snapshotPath))); err != nil {
		_ = conn.Close()
		_ = snapshotDB.Close()
		t.Fatalf("snapshot the v162 database: %v", err)
	}
	_ = conn.Close()
	_ = snapshotDB.Close()
	// The dev-build admission refuses to migrate the pre-upgrade snapshot;
	// its recovery belongs to the release that produced it.
	if _, err := VerifyBackup(ctx, snapshotPath); err == nil {
		t.Fatal("the dev build verified a snapshot it cannot migrate")
	}
	restored, err := sql.Open(driverName, dataSourceName(snapshotPath))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var snapshotResources, snapshotLinks, snapshotEdges int
	if err := restored.QueryRow(`SELECT count(*) FROM managed_resources`).Scan(&snapshotResources); err != nil {
		t.Fatalf("restored snapshot lost managed_resources: %v", err)
	}
	if err := restored.QueryRow(`SELECT count(*) FROM resource_products`).Scan(&snapshotLinks); err != nil {
		t.Fatalf("restored snapshot lost resource_products: %v", err)
	}
	if err := restored.QueryRow(`SELECT count(*) FROM domain_resource_attachment_edges`).Scan(&snapshotEdges); err != nil {
		t.Fatalf("restored snapshot lost domain_resource_attachment_edges: %v", err)
	}
	if snapshotResources != 1 || snapshotLinks != 1 || snapshotEdges != 1 {
		t.Fatalf("restored pre-upgrade inventory resources=%d links=%d edges=%d, want one of each", snapshotResources, snapshotLinks, snapshotEdges)
	}

	// The upgrade applies the breaking step and keeps the unrelated
	// current-state rows byte-identical in meaning.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade to v163: %v", err)
	}
	for _, table := range []string{"managed_resources", "resource_products", "domain_resource_attachment_sets", "domain_resource_attachment_edges"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("retired catalog object survived the upgrade: %s", table)
		}
	}
	survivors := map[string]int{
		"projects": 1, "project_locators": 1, "products": 1, "product_projects": 1,
		"work_items": 1, "domain_registries": 1, "domains": 1,
		"domain_project_attachment_sets": 1, "domain_project_attachment_edges": 1,
		"resource_claims": 0,
	}
	for table, want := range survivors {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("survivor %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("current-state table %s holds %d rows after the upgrade, want %d", table, count, want)
		}
	}
	var version int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion() || version != 163 {
		t.Fatalf("schema version=%d, want 163", version)
	}
	var workTitle string
	if err := db.QueryRow(`SELECT title FROM work_items WHERE id='upgrade-work'`).Scan(&workTitle); err != nil || workTitle != "Upgrade work" {
		t.Fatalf("work row did not survive intact: %q %v", workTitle, err)
	}
	var projectEdgeRole string
	if err := db.QueryRow(`SELECT role FROM domain_project_attachment_edges WHERE product_id='upgrade-product' AND project_id='upgrade-project'`).Scan(&projectEdgeRole); err != nil || projectEdgeRole != "primary" {
		t.Fatalf("Domain Project edge did not survive intact: %q %v", projectEdgeRole, err)
	}
}

// TestMigrateV162ToV163FailedStepRollsBack proves a failure inside the
// upgrade's migration transaction leaves the pre-upgrade state untouched:
// the retired tables keep their rows, the manifest stays at 162, and a later
// Migrate succeeds on the same database.
func TestMigrateV162ToV163FailedStepRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-rollback.db")
	db := seedV162CatalogDatabase(t, path)
	defer db.Close()

	write, err := beginWriteTx(ctx, db, nil, schemaWriteAdmission)
	if err != nil {
		t.Fatalf("begin upgrade transaction: %v", err)
	}
	tx := write.Tx
	if err := applyMigration(ctx, tx, migrations[162]); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply migration 163: %v", err)
	}
	var dropped int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='managed_resources'`).Scan(&dropped); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if dropped != 0 {
		_ = tx.Rollback()
		t.Fatal("migration 163 did not drop managed_resources inside the transaction")
	}
	// Force the failure the rollback must cover: a step past the drop that
	// cannot apply. The same rollback closure migrateOnce uses restores the
	// pre-upgrade database.
	if _, err := tx.ExecContext(ctx, `DROP TABLE managed_resources`); err == nil {
		_ = tx.Rollback()
		t.Fatal("re-drop unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback the failed upgrade: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 162 {
		t.Fatalf("manifest version=%d after the rolled-back upgrade, want 162", version)
	}
	var resources int
	if err := db.QueryRow(`SELECT count(*) FROM managed_resources`).Scan(&resources); err != nil {
		t.Fatalf("pre-upgrade inventory lost by the rollback: %v", err)
	}
	if resources != 1 {
		t.Fatalf("managed_resources rows=%d after the rollback, want the pre-upgrade row", resources)
	}
	// The database still upgrades cleanly after the failed attempt.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade after a rolled-back failure: %v", err)
	}
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='managed_resources'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatal("catalog table survived a clean post-rollback upgrade")
	}
}

// TestDomainFactResolvedSourceModes proves a fact's resource source identity
// needs no registration in either reference mode: unverified mode resolves
// the rebuild route through Project membership, resolved mode through the
// resolved anchor set, and both admit unregistered bounded resource strings.
func TestDomainFactResolvedSourceModes(t *testing.T) {
	anchor := DomainFactRepositoryAnchor{ProjectID: "source-project", Path: "src/state.go", Symbol: "Rebuild"}
	fact := DomainFact{SchemaVersion: DomainFactSchemaVersion, ID: "resolved-source", Kind: DomainFactStateAuthority,
		OwnerDomainID: "state-owner", OwningRoot: "src", Standing: "observed",
		StateAuthority: &DomainStateAuthorityFact{
			State:                   "current state",
			AuthoritativeResourceID: "source-database",
			DerivedCopies:           []DomainFactDerivedCopy{{ResourceID: "derived-cache", RebuildRoute: anchor}},
			DisagreementBehavior:    "Rebuild the derived cache from its source.",
		}}
	refs := DomainFactReferences{
		Registry:          KnowledgeDomainRegistry{Domains: []KnowledgeDomain{{DomainID: "state-owner", Status: "current"}}},
		Roots:             map[string]DomainFactRoot{"src": {Role: "domain", OwnerDomainIDs: []string{"state-owner"}}},
		RepositoryAnchors: map[DomainFactRepositoryAnchor]bool{anchor: true},
		ProjectIDs:        map[string]bool{"source-project": true},
		ValidationMode:    DomainFactResolvedReferences,
	}
	data, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDomainFact("resolved-source.json", data, refs); err != nil {
		t.Fatalf("resolved mode refused an unregistered fact-local source: %v", err)
	}
	// Resolved mode proves the rebuild route, not the resource identity: a
	// copy whose anchor is unresolved is still refused.
	unresolved := fact
	unresolved.StateAuthority.DerivedCopies[0].RebuildRoute.ProjectID = "other-project"
	unresolvedData, err := json.Marshal(unresolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDomainFact("resolved-source.json", unresolvedData, refs); err == nil {
		t.Fatal("resolved mode admitted an unresolved rebuild route")
	}
	// Unverified mode resolves the same route through Project membership
	// alone; the anchor map stays empty and the source stays unregistered.
	refs.RepositoryAnchors = nil
	refs.ValidationMode = DomainFactUnverifiedReferences
	if _, err := ParseDomainFact("resolved-source.json", data, refs); err != nil {
		t.Fatalf("unverified mode refused an unregistered fact-local source: %v", err)
	}
	memberless := refs
	memberless.ProjectIDs = map[string]bool{}
	if _, err := ParseDomainFact("resolved-source.json", data, memberless); err == nil {
		t.Fatal("unverified mode admitted a rebuild route outside Project membership")
	}
}
