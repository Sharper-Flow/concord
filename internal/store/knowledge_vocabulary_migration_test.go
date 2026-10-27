package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

var knowledge140Tables = []string{
	"law_subjects", "law_relations", "law_cross_source_relations", "law_bodies",
	"law_domain_homes", "law_domain_applicability", "domain_relation_governing_laws",
	"archived_work", "archived_work_products", "archived_work_projects",
	"archived_work_components", "archived_work_domains", "archived_work_tags",
}

func knowledge140Exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func knowledge140Snapshot(t *testing.T, db *sql.DB, tables []string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table) //nolint:gosec // test-owned table inventory, never external input.
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			args := make([]any, len(columns))
			for i := range values {
				args[i] = &values[i]
			}
			if err := rows.Scan(args...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprintf("%#v", values))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out[table])
	}
	return out
}

func knowledge140Schema(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type,name,sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		out = append(out, kind+"|"+name+"|"+ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func knowledge140Seed(t *testing.T) (*sql.DB, map[string][]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knowledge-139.db")
	db := openMigratedTo(t, path, 139)
	s := openTemp(t)
	repo := initKnowledgeRepo(t)
	lawPath := ".concord/docs/decisions/CD-0001.md"
	body := "current law\n"
	writeKnowledgeFile(t, repo, lawPath, body)
	manifest := domainLawManifest("concord", lawPath, sha256.Sum256([]byte(body)))
	writeManifestShards(t, repo, manifest)
	commitKnowledgeRepo(t, repo, "current law")
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	currentTables := slices.DeleteFunc(slices.Clone(knowledge140Tables), func(table string) bool {
		return table == "archived_work_components"
	})
	copyFixtureDataToPrefix(t, s, db, append([]string{
		"products", "projects", "product_projects", "project_locators", "domain_registries", "domains", "domain_architecture_relations",
	}, currentTables...), nil, "fold_guard")
	knowledge140Exec(t, db, `INSERT INTO fold_guard(active) VALUES(1)`)
	knowledge140Exec(t, db, `INSERT INTO law_subjects SELECT home_project_id,home_locator_id,'law-b','spec','accepted',path,'B',content_hash,scanned_commit_oid,'legislated','[{"criterion":1,"exemption":"Verified by the migration fixture."}]' FROM law_subjects WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO law_subjects SELECT home_project_id,home_locator_id,'law-c','constitution','accepted',path,'C',content_hash,scanned_commit_oid,'derived','[]' FROM law_subjects WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO law_relations VALUES('project','locator','CD-0001','refines','law-b','commit',NULL),('project','locator','law-b','subordinate_to','law-c','commit',NULL),('project','locator','CD-0001','conflicts_with','law-c','commit','law-c')`)
	knowledge140Exec(t, db, `INSERT INTO law_cross_source_relations VALUES('project','locator','CD-0001','refines','peer-project','peer-law','commit'),('project','locator','law-b','subordinate_to','peer-project','peer-law','commit'),('project','locator','law-c','conflicts_with','peer-project','peer-law','commit')`)
	knowledge140Exec(t, db, `INSERT INTO archived_work SELECT 'work-old','work_note','Old work',completed_at,'superseded',lesson_tags,'superseded',priority,summary,'work-next',home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version FROM archived_work WHERE id='CD-0001'`)
	knowledge140Exec(t, db, `DELETE FROM fold_guard`)
	preserved := knowledge140Snapshot(t, db, knowledge140Tables)
	knowledge140Exec(t, db, `INSERT INTO fold_guard(active) VALUES(1)`)
	knowledge140Exec(t, db, `INSERT INTO law_subjects SELECT home_project_id,home_locator_id,'retired-law',kind,'superseded',path,'Retired',content_hash,scanned_commit_oid,authority_tier,criterion_bindings FROM law_subjects WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO law_relations VALUES('project','locator','CD-0001','supersedes','retired-law','commit',NULL),('project','locator','retired-law','refines','law-b','commit',NULL)`)
	knowledge140Exec(t, db, `INSERT INTO law_cross_source_relations VALUES('project','locator','CD-0001','supersedes','peer-project','old-law','commit'),('project','locator','retired-law','refines','peer-project','peer-law','commit')`)
	knowledge140Exec(t, db, `INSERT INTO law_bodies SELECT home_project_id,home_locator_id,'retired-law',body,content_hash,scanned_commit_oid FROM law_bodies WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO law_domain_homes SELECT home_project_id,home_locator_id,'retired-law',product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale FROM law_domain_homes WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO law_domain_applicability SELECT home_project_id,home_locator_id,'retired-law',product_id,domain_id,scanned_commit_oid FROM law_domain_applicability WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO domain_relation_governing_laws SELECT home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,'retired-law',law_content_hash FROM domain_relation_governing_laws WHERE law_id='CD-0001'`)
	knowledge140Exec(t, db, `INSERT INTO archived_work SELECT 'retired-law',type,'Retired',completed_at,'superseded',lesson_tags,'superseded',priority,summary,'CD-0001',home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version FROM archived_work WHERE id='CD-0001'`)
	for _, scope := range []struct{ table, value string }{{"products", "concord"}, {"projects", "project"}, {"components", "component"}, {"domains", "child"}, {"tags", "tag"}} {
		knowledge140Exec(t, db, `INSERT INTO archived_work_`+scope.table+` VALUES('project','locator','retired-law',?)`, scope.value)
	}
	knowledge140Exec(t, db, `DELETE FROM fold_guard`)
	return db, preserved
}

func knowledge140AssertFK(t *testing.T, db *sql.DB) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign_keys=%d: %v", enabled, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeVocabularyMigration140PreservesCurrentState(t *testing.T) {
	t.Parallel()
	db, preserved := knowledge140Seed(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	currentTables := slices.DeleteFunc(slices.Clone(knowledge140Tables), func(table string) bool { return table == "archived_work_components" })
	delete(preserved, "archived_work_components")
	if got := knowledge140Snapshot(t, db, currentTables); !reflect.DeepEqual(got, preserved) {
		t.Fatalf("current projection changed:\ngot %v\nwant %v", got, preserved)
	}
	// Compare this step's schema changes in isolation; later migrations may
	// independently extend otherwise unrelated tables.
	schemaDB, _ := knowledge140Seed(t)
	beforeSchema := knowledge140Schema(t, schemaDB)
	if err := applyMigration(context.Background(), schemaDB, migrations[139]); err != nil {
		t.Fatal(err)
	}
	afterSchema := knowledge140Schema(t, schemaDB)
	for _, entry := range beforeSchema {
		// Only these three table CHECK declarations change. All indexes,
		// triggers and unrelated work/contract schema must remain identical.
		if strings.HasPrefix(entry, "table|law_subjects|") || strings.HasPrefix(entry, "table|law_relations|") || strings.HasPrefix(entry, "table|law_cross_source_relations|") {
			continue
		}
		if !slices.Contains(afterSchema, entry) {
			t.Errorf("lost schema obligation: %s", entry)
		}
	}
	for _, query := range []string{`SELECT count(*) FROM knowledge_index_watermark`, `SELECT count(*) FROM write_guard`, `SELECT count(*) FROM sqlite_temp_schema WHERE name LIKE '%v140%'`} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s = %d: %v", query, count, err)
		}
	}
	var breaking int
	if err := db.QueryRow(`SELECT breaking FROM schema_migrations WHERE version=140`).Scan(&breaking); err != nil || breaking != 1 {
		t.Fatalf("migration 140 breaking=%d: %v", breaking, err)
	}
	knowledge140AssertFK(t, db)
	knowledge140Exec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	for _, query := range []string{
		`UPDATE law_subjects SET status='superseded' WHERE law_id='CD-0001'`,
		`UPDATE law_relations SET kind='supersedes' WHERE source_law_id='law-b'`,
		`UPDATE law_cross_source_relations SET kind='supersedes' WHERE source_law_id='law-b'`,
	} {
		if _, err := db.Exec(query); err == nil || !sqlitedb.IsCheckViolation(err) {
			t.Fatalf("retired vocabulary was not rejected by CHECK: %s: %v", query, err)
		}
	}
	knowledge140Exec(t, db, `DELETE FROM write_guard`)
	for _, query := range []string{`UPDATE law_subjects SET title=title`, `DELETE FROM law_relations`, `DELETE FROM law_cross_source_relations`} {
		if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
			t.Fatalf("write guard lost: %s: %v", query, err)
		}
	}
}

func TestKnowledgeVocabularyMigration140RollsBackAtomically(t *testing.T) {
	t.Parallel()
	db, _ := knowledge140Seed(t)
	knowledge140Exec(t, db, `CREATE TRIGGER knowledge140_fail_restore BEFORE INSERT ON law_domain_homes BEGIN SELECT RAISE(ABORT,'injected restore failure'); END`)
	before, schema := knowledge140Snapshot(t, db, knowledge140Tables), knowledge140Schema(t, db)
	if err := Migrate(context.Background(), db); err == nil || !strings.Contains(err.Error(), "injected restore failure") {
		t.Fatalf("migration did not reach the injected restore failure: %v", err)
	}
	if after := knowledge140Snapshot(t, db, knowledge140Tables); !reflect.DeepEqual(before, after) {
		t.Fatal("failed migration changed projection rows")
	}
	if after := knowledge140Schema(t, db); !reflect.DeepEqual(schema, after) {
		t.Fatal("failed migration changed schema")
	}
	var version, guards, temps int
	if err := db.QueryRow(`SELECT max(version),(SELECT count(*) FROM fold_guard),(SELECT count(*) FROM sqlite_temp_schema WHERE name LIKE '%v140%') FROM schema_migrations`).Scan(&version, &guards, &temps); err != nil || version != 139 || guards != 0 || temps != 0 {
		t.Fatalf("rollback version=%d guards=%d temps=%d: %v", version, guards, temps, err)
	}
	knowledge140AssertFK(t, db)
	knowledge140Exec(t, db, `DROP TRIGGER knowledge140_fail_restore`)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}
