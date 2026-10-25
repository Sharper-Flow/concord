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

// Migration 124 retires the planning mirror: the planning tables and the
// Product planning mode leave the schema, the includes relations the retired
// Initiative events wrote are deleted, and linear_issue_links keeps exactly the
// usable recorded identities in the retained columns.
func TestMigration124RetiresThePlanningMirror(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	index := planningRetirementMigrationIndex(t)
	path := filepath.Join(t.TempDir(), "pre-124.db")
	db := openMigratedTo(t, path, index)
	// The prerequisite history a backup replay must fold: the Project scope
	// the planning work hangs from, both work items, each work Project
	// membership, and the retained blocks edge. Append before the fixture
	// transaction; the store pools one connection.
	historicalWorkCreated := func(id, eventID, kind, title string) Event {
		created := operationEvent(eventID, "work.created", SubjectWorkItem, id, map[string]any{"work_kind": kind, "title": title, "priority": 0})
		created.PayloadVersion = CurrentEventPayloadVersion("work.created")
		return created
	}
	historical := []Event{
		locatorProductEvent("planning-product"), locatorProjectEvent("planning-project"),
		locatorMembershipEvent("planning-product", "planning-project"),
		historicalWorkCreated("child-a", "create-planning-child", "task", "Child"),
		historicalWorkCreated("initiative-a", "create-planning-initiative", "initiative", "Initiative"),
		operationEvent("planning-child-project", "work_project.added", SubjectWorkItem, "child-a", map[string]any{
			"work_id": "child-a", "project_id": "planning-project", "role": "primary", "reason": "test",
			"expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("planning-initiative-project", "work_project.added", SubjectWorkItem, "initiative-a", map[string]any{
			"work_id": "initiative-a", "project_id": "planning-project", "role": "primary", "reason": "test",
			"expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("planning-blocks", "relation.added", SubjectWorkItem, "initiative-a", map[string]any{
			"from": "initiative-a", "to": "child-a", "kind": "blocks", "reason": "test",
			"expected_version": 2, "resulting_version": 3, "to_expected_version": 2, "to_resulting_version": 3,
		}),
	}
	insertHistoricalEvents(t, db, historical)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	seedHistoricalLocatorParents(t, tx, historical[:3])
	for _, event := range historical {
		if event.Kind == "work.created" {
			seedPreQ7WorkCreated(t, tx, event)
			continue
		}
		// The pre-retirement schema has no effect receipt allocator. Seed its
		// retained relation below rather than invoke a current-schema writer.
		if event.Kind == "relation.added" {
			continue
		}
		if event.Kind == "work_project.added" {
			if _, err := tx.Exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,'planning-project','primary'); UPDATE work_items SET version=2 WHERE id=?`, event.SubjectID, event.SubjectID); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Legacy direct-authority rows a pre-retirement binary wrote outside the
	// log: the retired includes edge, the retained blocks identity, and the
	// recorded Linear identities. Migration 124 removes the retired rows only
	// in the migrated clone.
	if _, err := tx.ExecContext(ctx, `INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,resolution_id) VALUES(901,'initiative-a','child-a','includes','t',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,resolution_id) VALUES(2,'initiative-a','child-a','blocks','t','resolution-a');
UPDATE work_items SET version=3 WHERE id IN ('initiative-a','child-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,link_state,created_at,updated_at)
VALUES('child-a','uuid-confirmed','CON-1','https://linear.app/x/issue/CON-1','confirmed','t','t'),
      ('initiative-a','uuid-pending','','','pending','t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "planning-archive.db")
	if _, err := BackupDatabase(ctx, path, archive); err != nil {
		t.Fatalf("archive before planning retirement: %v", err)
	}
	advanceHistoricalPrefix(t, db, migrations[index].Version)
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
	if _, err := VerifyBackup(ctx, archive); err != nil {
		t.Fatalf("retained planning archive no longer verifies: %v", err)
	}
	archived, err := sql.Open(driverName, readOnlyDataSource(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer archived.Close()
	var archivedIncludes, archivedPending int
	if err := archived.QueryRow(`SELECT count(*) FROM relations WHERE kind='includes' AND id=901`).Scan(&archivedIncludes); err != nil {
		t.Fatal(err)
	}
	if err := archived.QueryRow(`SELECT count(*) FROM linear_issue_links WHERE work_id='initiative-a' AND remote_issue_uuid='uuid-pending' AND link_state='pending'`).Scan(&archivedPending); err != nil {
		t.Fatal(err)
	}
	if archivedIncludes != 1 || archivedPending != 1 {
		t.Fatalf("archive lost retired planning rows: includes=%d pending=%d, want 1 each", archivedIncludes, archivedPending)
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
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("planning prefix through the current tail: %v", err)
	}
	if err := db.QueryRow(`SELECT resolution_id FROM relations WHERE id=2`).Scan(&resolutionID); err != nil || resolutionID != "resolution-a" {
		t.Fatalf("current tail changed retained relation: %q, %v", resolutionID, err)
	}
	if _, err := db.Exec(`UPDATE relations SET resolution_id=resolution_id WHERE id=2`); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
		t.Fatalf("current relation guard lost: %v", err)
	}
}
