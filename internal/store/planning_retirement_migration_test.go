package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// planningRetirementMigrationIndex is the position of migration 124, which
// retires the planning mirror (CD-0213).
func planningRetirementMigrationIndex(t *testing.T) int {
	t.Helper()
	for i, migration := range migrations {
		if migration.Name == "planning_mirror_retirement" {
			return i
		}
	}
	t.Fatal("planning mirror retirement migration is missing")
	return -1
}

type schemaObject struct {
	kind, name, sql string
}

// undoMigration124 restores the pre-124 schema shape on a store migrated
// through 124, so a test whose manifest tail was removed can re-apply the
// step. The pre-124 objects are read from a fresh store migrated through 123,
// never hand-copied, so the restored shape cannot drift from the migrations
// that built it.
func undoMigration124(t *testing.T, ctx context.Context, db *sql.DB) error {
	t.Helper()
	pre := openMigratedTo(t, filepath.Join(t.TempDir(), "pre-124.db"), planningRetirementMigrationIndex(t))
	rows, err := pre.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		return err
	}
	preObjects := []schemaObject{}
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			rows.Close()
			return err
		}
		preObjects = append(preObjects, object)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE linear_issue_links`); err != nil {
		return err
	}
	for _, object := range preObjects {
		if object.kind != "table" || object.name != "relations" {
			continue
		}
		ddl := strings.Replace(object.sql, "CREATE TABLE relations", "CREATE TABLE relations_pre124", 1)
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) SELECT 'relations_pre124',seq FROM sqlite_sequence WHERE name='relations';
INSERT INTO relations_pre124 SELECT * FROM relations;
DROP TABLE relations;
ALTER TABLE relations_pre124 RENAME TO relations;`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE products ADD COLUMN planning_mode TEXT NOT NULL DEFAULT 'local_only' CHECK (planning_mode IN ('local_only','linear_enabled'))`); err != nil {
		return err
	}
	present := map[string]bool{}
	current, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master`)
	if err != nil {
		return err
	}
	for current.Next() {
		var name string
		if err := current.Scan(&name); err != nil {
			current.Close()
			return err
		}
		present[name] = true
	}
	current.Close()
	if err := current.Err(); err != nil {
		return err
	}
	for _, kind := range []string{"table", "index", "view", "trigger"} {
		for _, object := range preObjects {
			if object.kind != kind || present[object.name] {
				continue
			}
			if _, err := tx.ExecContext(ctx, object.sql); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		return err
	}
	return tx.Commit()
}

// dropMigration122Objects removes the one object migration 122 creates.
func dropMigration122Objects(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS worktree_ref_outcomes`)
	return err
}

// dropMigration120Objects removes the Initiative violation projection
// migration 120 creates, triggers first because they attach to tables that
// predate it.
func dropMigration120Objects(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS initiative_projection_work_items_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_items_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_items_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_projects_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_projects_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_work_projects_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_product_projects_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_product_projects_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_product_projects_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_relations_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_relations_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_relations_update`,
		`DROP TRIGGER IF EXISTS initiative_projection_initiative_entries_insert`,
		`DROP TRIGGER IF EXISTS initiative_projection_initiative_entries_delete`,
		`DROP TRIGGER IF EXISTS initiative_projection_initiative_entries_update`,
		`DROP VIEW IF EXISTS initiative_entry_violation_rows`,
		`DROP VIEW IF EXISTS initiative_scope_violation_rows`,
		`DROP VIEW IF EXISTS initiative_work_scope`,
		`DROP TABLE IF EXISTS initiative_entry_violations`,
		`DROP TABLE IF EXISTS initiative_scope_violations`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// Migration 124 retires the planning mirror: the planning tables and the
// Product planning mode leave the schema, the includes relations the retired
// Initiative events wrote are deleted, and linear_issue_links keeps exactly the
// usable recorded identities in the retained columns.
func TestMigration124RetiresThePlanningMirror(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	index := planningRetirementMigrationIndex(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "pre-124.db"), index)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency)
VALUES('initiative-a','initiative','Initiative','needed',0,1,'t','t','{}','','standard'),
      ('child-a','task','Child','needed',0,1,'t','t','{}','','standard');
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,resolution_id) VALUES(901,'initiative-a','child-a','includes','t',NULL),(2,'initiative-a','child-a','blocks','t','resolution-a');
INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,link_state,created_at,updated_at)
VALUES('child-a','uuid-confirmed','CON-1','https://linear.app/x/issue/CON-1','confirmed','t','t'),
      ('initiative-a','uuid-pending','','','pending','t','t');
DELETE FROM fold_guard;`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, migrations[index]); err != nil {
		t.Fatalf("migration 124: %v", err)
	}
	for _, name := range []string{"initiative_entries", "initiative_scope_violations", "initiative_entry_violations", "initiative_work_scope", "linear_outbox", "linear_outbox_dispositions", "linear_project_links"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s survives the planning mirror retirement", name)
		}
	}
	var planningMode int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('products') WHERE name='planning_mode'`).Scan(&planningMode); err != nil {
		t.Fatal(err)
	}
	if planningMode != 0 {
		t.Fatal("products.planning_mode survives the planning mirror retirement")
	}
	columns := []string{}
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('linear_issue_links') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	want := []string{"work_id", "remote_issue_uuid", "human_key", "url", "created_at", "updated_at"}
	if len(columns) != len(want) {
		t.Fatalf("linear_issue_links columns %v, want %v", columns, want)
	}
	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("linear_issue_links columns %v, want %v", columns, want)
		}
	}
	var links int
	var key string
	if err := db.QueryRowContext(ctx, `SELECT count(*), max(human_key) FROM linear_issue_links`).Scan(&links, &key); err != nil {
		t.Fatal(err)
	}
	if links != 1 || key != "CON-1" {
		t.Fatalf("migration kept %d links (max key %q); want only the usable CON-1 identity", links, key)
	}
	var includes, blocks int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE kind='includes'), count(*) FILTER (WHERE kind='blocks') FROM relations`).Scan(&includes, &blocks); err != nil {
		t.Fatal(err)
	}
	if includes != 0 || blocks != 1 {
		t.Fatalf("relations after migration: includes=%d blocks=%d; want 0 and 1", includes, blocks)
	}
	var resolutionID string
	if err := db.QueryRowContext(ctx, `SELECT resolution_id FROM relations WHERE id=2`).Scan(&resolutionID); err != nil || resolutionID != "resolution-a" {
		t.Fatalf("retained relation lost its identity or resolution: %q, %v", resolutionID, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('child-a','initiative-a','blocks','t')`); err == nil {
		t.Fatal("relations lost its fold guard")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('initiative-a','child-a','includes','t')`); err == nil {
		t.Fatal("relations still admits the retired includes kind")
	}
	result, err := db.ExecContext(ctx, `INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('child-a','initiative-a','blocks','t')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := result.LastInsertId(); err != nil || id <= 901 {
		t.Fatalf("relation sequence reused a retired identity: %d, %v", id, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES('child-b','uuid-x','CON-2','https://linear.app/x/issue/CON-2','t','t')`); err == nil {
		t.Fatal("linear_issue_links lost its fold guard")
	}
}
