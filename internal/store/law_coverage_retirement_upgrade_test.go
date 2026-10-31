package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

// CD-0249 D1 retires per-law coverage shards and the law_coverage_anchors
// table. These tests prove the retirement and
// the independent protections it must leave alone: the knowledge-kind
// coverage rows and law projection survive untouched. The package map and
// law evidence paths are seeded at v165 but retire at v172 with Domain Shape.

var lawCoverageRetiredTables = []string{"law_coverage_anchors"}

var lawCoverageRetiredObjects = []string{
	"law_coverage_anchors",
	"law_coverage_anchors_guard_insert",
	"law_coverage_anchors_guard_update",
	"law_coverage_anchors_guard_delete",
}

var lawCoveragePreservedTables = []string{
	"products", "project_locators", "work_items",
	"knowledge_kind_coverage",
	"law_subjects", "law_bodies",
}

func seedV165LawCoverageDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, path, 165)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('coverage-project','Coverage Project',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('coverage-locator','coverage-project','canonical_path','/test/coverage','/test/coverage','2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('coverage-product','Coverage Product','prototype','operator_only',1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('coverage-product','coverage-project','primary')`,
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('coverage-work','task','Coverage retirement witness','needed',1,1,'2026-10-29T00:00:00Z','2026-10-29T00:00:00Z',NULL)`,
		`INSERT INTO work_projects(work_id,project_id,role) VALUES('coverage-work','coverage-project','primary')`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249','decision','accepted','.concord/docs/decisions/CD-0249.md','Retire law coverage','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249','Retire per-law coverage.','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO law_evidence_paths(home_project_id,home_locator_id,law_id,path,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249','internal/store/store.go','commit')`,
		`INSERT INTO knowledge_package_domains(home_project_id,home_locator_id,package_path,domain_id,map_content_hash,scanned_commit_oid) VALUES('coverage-project','coverage-locator','internal/store','coverage-root','sha256:` + repeatA64() + `','commit')`,
		`INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('coverage-project','coverage-locator','HEAD','work_note','indexed','canonical .concord/docs/work directory scanned','commit')`,
		`INSERT INTO law_coverage_anchors(home_project_id,home_locator_id,law_id,ordinal,kind,value,scanned_commit_oid) VALUES('coverage-project','coverage-locator','CD-0249',0,'go_test','internal/store.TestRetired','commit')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("seed v165 (%s): %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateV165ToV166RetiresLawCoverageStorageAndPreservesCurrentState(t *testing.T) {
	t.Parallel()
	db := seedV165LawCoverageDatabase(t, filepath.Join(t.TempDir(), "concord-v165.db"))
	before := catalogUpgradeRows(t, db, lawCoveragePreservedTables...)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("upgrade from v165: %v", err)
	}
	assertRetirementUpgrade(t, db, lawCoverageRetiredObjects, lawCoveragePreservedTables, before)
}

func TestMigration166PreservesPreShapeRetirementRows(t *testing.T) {
	t.Parallel()
	db := seedV165LawCoverageDatabase(t, filepath.Join(t.TempDir(), "concord-v166.db"))
	tables := append([]string{"knowledge_package_domains", "law_evidence_paths"}, lawCoveragePreservedTables...)
	before := catalogUpgradeRows(t, db, tables...)
	// Prove migration 166's own preservation boundary independently of later
	// retirements. Do not change the shared migration manifest to stop at 166.
	if err := applyMigration(context.Background(), db, migrations[165]); err != nil {
		t.Fatal(err)
	}
	if after := catalogUpgradeRows(t, db, tables...); !reflect.DeepEqual(after, before) {
		t.Fatalf("migration 166 changed pre-Shape-retirement rows:\nbefore: %v\nafter: %v", before, after)
	}
	for _, object := range lawCoverageRetiredObjects {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration 166 retained %s: count=%d err=%v", object, count, err)
		}
	}
}

func TestMigrateV165ToV166FailedStepRollsBack(t *testing.T) {
	t.Parallel()
	db := seedV165LawCoverageDatabase(t, filepath.Join(t.TempDir(), "concord-rollback.db"))
	if _, err := db.Exec(`CREATE TABLE coverage_drop_blocker(home_project_id TEXT NOT NULL, home_locator_id TEXT NOT NULL, law_id TEXT NOT NULL, ordinal INTEGER NOT NULL, FOREIGN KEY(home_project_id,home_locator_id,law_id,ordinal) REFERENCES law_coverage_anchors(home_project_id,home_locator_id,law_id,ordinal) ON DELETE RESTRICT);
INSERT INTO coverage_drop_blocker VALUES('coverage-project','coverage-locator','CD-0249',0)`); err != nil {
		t.Fatal(err)
	}
	tables := append([]string{"sqlite_schema", "schema_migrations", "coverage_drop_blocker"}, lawCoverageRetiredTables...)
	tables = append(tables, "knowledge_package_domains", "law_evidence_paths")
	tables = append(tables, lawCoveragePreservedTables...)
	current := catalogUpgradeRows(t, db, lawCoveragePreservedTables...)
	assertMigrationForeignKeyRollback(t, db, tables, "migration 166 (law_coverage_retirement) failed")
	if _, err := db.Exec(`DROP TABLE coverage_drop_blocker`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("clean upgrade after failed Migrate: %v", err)
	}
	assertRetirementUpgrade(t, db, lawCoverageRetiredObjects, lawCoveragePreservedTables, current)
}

// A knowledge home whose head still carries coverage shards from before the
// retirement rebuilds: the leftover shards are ignored members of the shard
// tree, not a refusal, and the rebuilt index holds no coverage anywhere.
func TestKnowledgeIndexRebuildIgnoresLeftoverCoverageShards(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-alpha.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Alpha law", Summary: "Governs alpha", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law"}}},
	)
	// The retired tree path, written literally: the constant retired with
	// the discovery it named (CD-0249 D1).
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/coverage/CD-0001.json", `{"id":"CD-0001","state":"satisfied"}`)
	commitKnowledgeRepo(t, repo, "head still carries a pre-retirement coverage shard")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "retire-project", HomeLocatorID: "retire-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "retire-product", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("rebuild must ignore a leftover coverage shard: %v", err)
	}
	var anchors int
	err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='law_coverage_anchors'`).Scan(&anchors)
	if err != nil {
		t.Fatal(err)
	}
	if anchors != 0 {
		t.Fatal("law_coverage_anchors survived the retirement")
	}
}
