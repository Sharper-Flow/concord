package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var domainShapeRetiredTables = []string{
	"knowledge_domain_facts", "knowledge_domain_roots", "knowledge_domain_shapes",
	"knowledge_package_domains", "law_evidence_paths",
}

var domainShapePreservedTables = []string{
	"projects", "project_locators", "products", "product_projects", "work_items", "work_projects",
	"domain_registries", "domains", "domain_architecture_relations", "domain_relation_governing_laws",
	"law_subjects", "law_bodies", "law_domain_homes", "law_domain_applicability",
}

func seedV171DomainShapeDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, path, 171)
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
		`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('shape-product','shape-project','shape-locator','shape-product','shape-root','1.0','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','shape-product','shape-root','Root','Own product law','current','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','shape-product','shape-child','Child','Own child law','shape-root','current','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','decision','accepted','.concord/law/decisions/CD-0001.md','Retained law','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','Retained law body.','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','shape-product','shape-root','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_domain_applicability(home_project_id,home_locator_id,law_id,product_id,domain_id,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','shape-product','shape-child','commit')`,
		`INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,registry_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','shape-product','shape-child','depends_on','shape-root','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO domain_relation_governing_laws(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,law_id,law_content_hash) VALUES('shape-project','shape-locator','shape-product','shape-child','depends_on','shape-root','CD-0001','sha256:` + repeatA64() + `')`,
		`INSERT INTO knowledge_domain_shapes(home_project_id,home_locator_id,scanned_commit_oid,product_id,content_hash,registry_content_hash,map_content_hash,tooling_commit_oid,tooling_content_hash,required_checks) VALUES('shape-project','shape-locator','commit','shape-product','sha256:` + repeatA64() + `','sha256:` + repeatA64() + `','sha256:` + repeatA64() + `','commit','sha256:` + repeatA64() + `','[]')`,
		`INSERT INTO knowledge_domain_roots(home_project_id,home_locator_id,scanned_commit_oid,root_path,root_json) VALUES('shape-project','shape-locator','commit','internal/store','{"role":"domain","owner_domain_id":"shape-root"}')`,
		`INSERT INTO knowledge_domain_facts(home_project_id,home_locator_id,scanned_commit_oid,fact_id,fact_json,content_hash) VALUES('shape-project','shape-locator','commit','shape-fact','{"id":"shape-fact"}','sha256:` + repeatA64() + `')`,
		`INSERT INTO knowledge_package_domains(home_project_id,home_locator_id,package_path,domain_id,map_content_hash,scanned_commit_oid) VALUES('shape-project','shape-locator','internal/store','shape-root','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_evidence_paths(home_project_id,home_locator_id,law_id,path,scanned_commit_oid) VALUES('shape-project','shape-locator','CD-0001','internal/store/store.go','commit')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed v171 (%s): %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func domainShapeRetiredObjects() []string {
	objects := []string{"law_evidence_paths_path"}
	for _, table := range domainShapeRetiredTables {
		objects = append(objects, table, table+"_guard_insert", table+"_guard_update", table+"_guard_delete")
	}
	return objects
}

func TestMigrateV171ToV172RetiresDomainShapeAndPreservesCurrentState(t *testing.T) {
	t.Parallel()
	db := seedV171DomainShapeDatabase(t, filepath.Join(t.TempDir(), "concord-v171.db"))
	before := catalogUpgradeRows(t, db, domainShapePreservedTables...)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertRetirementUpgrade(t, db, domainShapeRetiredObjects(), domainShapePreservedTables, before)
	var breaking bool
	if err := db.QueryRow(`SELECT breaking FROM schema_migrations WHERE version=172 AND name='retire_domain_shape'`).Scan(&breaking); err != nil || !breaking {
		t.Fatalf("Domain Shape retirement breaking=%v, err=%v", breaking, err)
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check rows=%d err=%v", violations, err)
	}
}

func TestMigrateV171ToV172FailedDropRollsBack(t *testing.T) {
	t.Parallel()
	db := seedV171DomainShapeDatabase(t, filepath.Join(t.TempDir(), "concord-rollback.db"))
	// Block the last drop so rollback must restore the preceding four tables,
	// their dependent guards and indexes, and the migration manifest together.
	if _, err := db.Exec(`CREATE TABLE shape_drop_blocker(home_project_id TEXT,home_locator_id TEXT,law_id TEXT,path TEXT,FOREIGN KEY(home_project_id,home_locator_id,law_id,path) REFERENCES law_evidence_paths(home_project_id,home_locator_id,law_id,path) ON DELETE RESTRICT);
INSERT INTO shape_drop_blocker VALUES('shape-project','shape-locator','CD-0001','internal/store/store.go')`); err != nil {
		t.Fatal(err)
	}
	tables := append([]string{"sqlite_schema", "schema_migrations", "write_guard", "shape_drop_blocker"}, domainShapeRetiredTables...)
	tables = append(tables, domainShapePreservedTables...)
	before := catalogUpgradeRows(t, db, domainShapePreservedTables...)
	assertMigrationForeignKeyRollback(t, db, tables, "migration 172 (retire_domain_shape) failed")
	if _, err := db.Exec(`DROP TABLE shape_drop_blocker`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("upgrade after removing blocker: %v", err)
	}
	assertRetirementUpgrade(t, db, domainShapeRetiredObjects(), domainShapePreservedTables, before)
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign_key_check rows=%d err=%v", violations, err)
	}
}

func TestMigrateDomainShapeReadingRefusesBeforeRetirement(t *testing.T) {
	t.Parallel()
	for _, lifecycle := range []string{"needed", "completed", "cancelled", "superseded"} {
		t.Run(lifecycle, func(t *testing.T) {
			db := seedV171DomainShapeDatabase(t, filepath.Join(t.TempDir(), "legacy-reading.db"))
			payload := `{"required_reading":[{"domain_id":"shape-child","reason":"Required reading","source":{"kind":"domain_shape","source_id":"shape-project","selector":{"path":"internal/store"}}},{"domain_id":"shape-child","reason":"Retained reading","source":{"kind":"knowledge","source_id":"concord_knowledge","law_id":"CD-0001","content_hash":"sha256:` + repeatA64() + `"}}],"finding_refs":[],"context_findings":[]}`
			if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE work_items SET lifecycle='` + lifecycle + `' WHERE id='shape-work';
INSERT INTO work_context_state(work_id,source_frontier,declaration_seq,declaration_id,declaration_payload) VALUES('shape-work',1,1,'legacy-declaration','` + payload + `');
DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			tables := append([]string{"sqlite_schema", "schema_migrations", "store_revision", "write_guard", "work_context_state"}, domainShapeRetiredTables...)
			tables = append(tables, domainShapePreservedTables...)
			before := catalogUpgradeRows(t, db, tables...)
			err := Migrate(context.Background(), db)
			if err == nil {
				t.Fatal("upgrade admitted a persisted domain_shape required reading; retirement strands work-context decoding")
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindUpgradeBlocked || !failure.RetrySafe {
				t.Fatalf("upgrade refusal = %v, want retry-safe upgrade_blocked", err)
			}
			fragments := []string{"domain_shape", "shape-work", "pre-retirement binary", "schema 171", "retry"}
			if lifecycle == "needed" {
				fragments = append(fragments, "record_work_context", "replace or remove only obsolete readings", "preserving the rest of the declaration")
			} else {
				fragments = append(fragments, "immutable", "do not replace or rewrite", "concord_work_compact.publish", "concord_work_compact.reconcile", "exact reviewed closure bytes", "all existing gates", "no supported upgrade recovery")
			}
			for _, fragment := range fragments {
				if !strings.Contains(failure.Detail+" "+failure.RecoveryAction, fragment) {
					t.Errorf("upgrade refusal %+v lacks actionable recovery fragment %q", failure, fragment)
				}
			}
			if after := catalogUpgradeRows(t, db, tables...); !reflect.DeepEqual(after, before) {
				t.Fatalf("refused upgrade changed schema, manifest, revision, or rows:\nbefore: %v\nafter: %v", before, after)
			}
			if lifecycle != "needed" {
				return
			}

			// Model only the live declaration's persisted replacement. Terminal
			// declarations cannot be rewritten and require published cleanup.
			if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
UPDATE work_context_state SET declaration_seq=2,declaration_id='replacement-declaration',source_frontier=2,declaration_payload=json_remove(declaration_payload,'$.required_reading[0]') WHERE work_id='shape-work';
DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			remediated := catalogUpgradeRows(t, db, "work_context_state")
			retained := catalogUpgradeRows(t, db, domainShapePreservedTables...)
			if err := Migrate(context.Background(), db); err != nil {
				t.Fatalf("upgrade after modeled live reading removal: %v", err)
			}
			if after := catalogUpgradeRows(t, db, "work_context_state"); !reflect.DeepEqual(after, remediated) {
				t.Fatalf("upgrade rewrote the remediated authored declaration: %v", after)
			}
			assertRetirementUpgrade(t, db, domainShapeRetiredObjects(), domainShapePreservedTables, retained)
			view, err := readWorkContextView(context.Background(), db, "shape-work")
			if err != nil || view == nil || len(view.RequiredReading) != 1 || view.RequiredReading[0].Reason != "Retained reading" || view.RequiredReading[0].Source.Kind != WorkContextSourceKnowledge {
				t.Fatalf("remediated context remains unreadable: view=%+v err=%v", view, err)
			}
		})
	}
}
