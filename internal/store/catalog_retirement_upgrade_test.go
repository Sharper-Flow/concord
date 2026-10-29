package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var catalogCurrentTables = []string{
	"projects", "project_locators", "products", "product_projects",
	"work_items", "work_projects", "domains", "domain_registries",
}

var catalogRetiredTables = []string{
	"managed_resources", "resource_products", "domain_resource_attachment_sets", "domain_resource_attachment_edges",
}

func seedV162CatalogDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, path, 162)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('upgrade-project','Upgrade Project',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('upgrade-locator','upgrade-project','canonical_path','/test/upgrade','/test/upgrade','2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('upgrade-product','Upgrade Product','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('upgrade-product','upgrade-project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('upgrade-work','task','Upgrade work','needed',1,1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('upgrade-work','upgrade-project','primary')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('upgrade-project','upgrade-locator','upgrade-product','upgrade-root','Root','test root',NULL,'current','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('upgrade-product','upgrade-project','upgrade-locator','upgrade','upgrade-root','1.0','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO managed_resources(resource_id,display_name,class,kind,purpose,stage_maturity,stage_audience_commitment,environments,locator_absence_reason,metadata_schema_version,metadata,version,created_at,updated_at) VALUES('upgrade-queue','Upgrade Queue','infrastructure','queue','dispatches upgrade work','production','limited','["production"]',NULL,'1','{}',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO resource_products(resource_id,product_id,role,purpose,environments) VALUES('upgrade-queue','upgrade-product','owner','operates the queue','["production"]')`,
		`INSERT INTO domain_resource_attachment_sets(product_id,domain_id,version) VALUES('upgrade-product','upgrade-root',1)`,
		`INSERT INTO domain_resource_attachment_edges(product_id,domain_id,resource_id,purpose,environments) VALUES('upgrade-product','upgrade-root','upgrade-queue','uses the queue','["production"]')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed v162 (%s): %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func catalogUpgradeRows(t *testing.T, db *sql.DB, tables ...string) map[string][]string {
	t.Helper()
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
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			population = append(population, string(encoded))
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

func assertCatalogUpgrade(t *testing.T, db *sql.DB, before map[string][]string) {
	t.Helper()
	for _, table := range catalogRetiredTables {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("retired catalog object survived: %s", table)
		}
	}
	if after := catalogUpgradeRows(t, db, catalogCurrentTables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("current rows changed:\nbefore: %v\nafter: %v", before, after)
	}
	var version int
	var breaking bool
	if err := db.QueryRow(`SELECT version,breaking FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &breaking); err != nil {
		t.Fatal(err)
	}
	if version != 165 || version != CurrentSchemaVersion() || !breaking {
		t.Fatalf("schema head=(%d, breaking=%v), want breaking 165", version, breaking)
	}
}

func TestMigrateV162ToV165RetiresCatalogAndPreservesCurrentState(t *testing.T) {
	t.Parallel()
	db := seedV162CatalogDatabase(t, filepath.Join(t.TempDir(), "concord-v162.db"))
	before := catalogUpgradeRows(t, db, catalogCurrentTables...)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("upgrade to v165: %v", err)
	}
	assertCatalogUpgrade(t, db, before)
}

func TestMigrateV162ToV165FailedStepRollsBack(t *testing.T) {
	t.Parallel()
	db := seedV162CatalogDatabase(t, filepath.Join(t.TempDir(), "concord-rollback.db"))
	if _, err := db.Exec(`CREATE TABLE catalog_drop_blocker(resource_id TEXT NOT NULL REFERENCES managed_resources(resource_id) ON DELETE RESTRICT);
INSERT INTO catalog_drop_blocker(resource_id) VALUES('upgrade-queue')`); err != nil {
		t.Fatal(err)
	}
	tables := append([]string{"sqlite_schema", "schema_migrations", "catalog_drop_blocker"}, catalogRetiredTables...)
	tables = append(tables, catalogCurrentTables...)
	before := catalogUpgradeRows(t, db, tables...)
	current := catalogUpgradeRows(t, db, catalogCurrentTables...)
	err := Migrate(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "migration 165 (managed_resource_catalog_retirement) failed") || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("Migrate must fail on the induced catalog foreign key: %v", err)
	}
	if after := catalogUpgradeRows(t, db, tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed Migrate changed pre-upgrade schema, manifest or rows:\nbefore: %v\nafter: %v", before, after)
	}
	if _, err := db.Exec(`DROP TABLE catalog_drop_blocker`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("clean upgrade after failed Migrate: %v", err)
	}
	assertCatalogUpgrade(t, db, current)
}

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
	unresolved := fact
	unresolved.StateAuthority.DerivedCopies[0].RebuildRoute.ProjectID = "other-project"
	unresolvedData, err := json.Marshal(unresolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDomainFact("resolved-source.json", unresolvedData, refs); err == nil {
		t.Fatal("resolved mode admitted an unresolved rebuild route")
	}
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
