package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openMigratedTo builds a database with the first n migrations applied and no
// further, so a test can observe migration n+1 as a change.
func openMigratedTo(t *testing.T, path string, n int) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:n] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-27T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// openV51 migrates a v51 database the rest of the way, so the binding's
// refusals can be exercised against the current schema.
func openV51(t *testing.T, name string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), name), 51)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	})
	return db
}

func TestMigrateV51ToV52BindsKnowledgeHomePairsToProjectLocators(t *testing.T) {
	ctx := context.Background()
	db := openV51(t, "concord-v51.db")
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('p','P',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('l','p','canonical_path','/test/l','/test/l','t','t')`); err != nil {
		t.Fatal(err)
	}

	unanchored := map[string]string{
		"law_subjects":            `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('nope','l','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:` + strings.Repeat("a", 64) + `','commit')`,
		"law_relations":           `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('nope','l','a','supersedes','b','commit')`,
		"archived_work":           `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES('w','lesson','T','2026-08-27T00:00:00Z','published','[]','completed',0,'S','nope','l','docs/lessons/t.md','` + strings.Repeat("a", 40) + `','sha256:` + strings.Repeat("b", 64) + `')`,
		"knowledge_kind_coverage": `INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES('nope','l','HEAD','lesson','indexed','test','` + strings.Repeat("a", 40) + `')`,
		"domains":                 `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('nope','l','prod','d','D','purpose','current','sha256:` + strings.Repeat("c", 64) + `','commit')`,
	}
	for table, stmt := range unanchored {
		if _, err := db.ExecContext(ctx, stmt); err == nil {
			t.Fatalf("unanchored %s insert succeeded", table)
		}
	}

	// A locator owned by a different Project is as unanchored as a missing one.
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','missing','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:`+strings.Repeat("a", 64)+`','commit')`); err == nil {
		t.Fatal("mismatched home pair insert succeeded")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:`+strings.Repeat("a", 64)+`','commit')`); err != nil {
		t.Fatalf("anchored law_subjects insert refused: %v", err)
	}

	// The parent side: a locator Git-derived knowledge references cannot be
	// deleted, even inside a fold.
	if _, err := db.ExecContext(ctx, `DELETE FROM project_locators WHERE locator_id='l'`); err == nil {
		t.Fatal("deleting a knowledge-referenced locator succeeded")
	}

	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

func TestMigrateV52RefusesAStoredUnanchoredHomePair(t *testing.T) {
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "concord-v51-dirty.db"), 51)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('p','P',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('l','p','canonical_path','/test/l','/test/l','t','t')`); err != nil {
		t.Fatal(err)
	}
	// Seed through the pre-binding schema state: v51 has no pair triggers, so
	// an unanchored row is storable here and migration 52 must refuse it.
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('ghost','missing','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:`+strings.Repeat("a", 64)+`','commit')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("migration admitted a stored unanchored home pair")
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if seeded := migrations[50].Version; version != seeded {
		t.Fatalf("schema version=%d after refused migration, want %d", version, seeded)
	}
}

// TestKnowledgeHomePairTableListMatchesTriggers pins the Go-side table list to
// the trigger set migration 52 declares, so the two cannot drift apart.
func TestKnowledgeHomePairTableListMatchesTriggers(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for _, table := range knowledgeHomePairTables {
		for _, verb := range []string{"insert", "update"} {
			var count int
			if err := s.DatabaseForTesting().QueryRowContext(ctx,
				`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, table+"_home_pair_bound_"+verb).Scan(&count); err != nil || count != 1 {
				t.Fatalf("table %s missing %s pair trigger (count=%d err=%v)", table, verb, count, err)
			}
		}
	}
	var boundTriggers int
	if err := s.DatabaseForTesting().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE '%\_home\_pair\_bound\_%' ESCAPE '\'`).Scan(&boundTriggers); err != nil {
		t.Fatal(err)
	}
	if boundTriggers != len(knowledgeHomePairTables)*2 {
		t.Fatalf("pair-bound triggers=%d, want exactly two per listed table (%d)", boundTriggers, len(knowledgeHomePairTables)*2)
	}
}

func TestRemovingAKnowledgeReferencedLocatorIsRefusedWithATypedFailure(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	anchorHomePair(t, s, "law-project", "law-locator")
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('law-project','law-locator','CD-0001','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:`+strings.Repeat("a", 64)+`','commit')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	// anchorHomePair seeds the Project through SQL, so it stays at version 1
	// and the pre-fold version check passes with expected_version 1.
	err := s.RemoveProjectLocator(ctx, "law-project", "law-locator", 1)
	assertFailureKind(t, err, KindProjectionConflict)
}

func TestRebuildFromLogPreservesAnchoredKnowledgeAndRebindsGuard(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	repo := t.TempDir()
	seedEventDerivedLocator(t, s, "surviving-project", "surviving-locator", repo)
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('surviving-project','surviving-locator','CD-0002','decision','accepted','docs/decisions/CD-0002-b.md','B','sha256:`+strings.Repeat("b", 64)+`','commit')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	var surviving int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM law_subjects WHERE law_id='CD-0002'`).Scan(&surviving); err != nil || surviving != 1 {
		t.Fatalf("law_subjects rows=%d err=%v after rebuild, want 1", surviving, err)
	}
	// The guard trigger is re-created after replay: an unanchored write is
	// still refused post-rebuild.
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('ghost','missing','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:`+strings.Repeat("a", 64)+`','commit')`)
	if err == nil {
		t.Fatal("unanchored law_subjects insert succeeded after rebuild")
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildFromLogRefusesOrphanedKnowledgePairs(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	// The locator is seeded through SQL only: the event log holds no
	// locator event, so replay cannot restore what knowledge references.
	anchorHomePair(t, s, "orphan-project", "orphan-locator")
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES('w','lesson','L','2026-08-27T00:00:00Z','published','[]','completed',1,'S','orphan-project','orphan-locator','docs/lessons/l.md','`+strings.Repeat("a", 40)+`','sha256:`+strings.Repeat("b", 64)+`')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	err := RebuildFromLog(ctx, s)
	assertFailureKind(t, err, KindProjectionConflict)

	// The refusal rolled back: the seeded row is still present and the store
	// is usable.
	var remaining int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM archived_work WHERE id='w'`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("archived_work rows=%d err=%v after refused rebuild, want 1 (rollback must preserve)", remaining, err)
	}
}
