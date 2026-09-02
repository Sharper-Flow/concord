package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A historical migration edited in place produces two databases that report the
// same schema version and hold different schemas. The variant table records the
// checksum of the earlier text so the older database still opens, and nothing
// gives it the objects the edit added: the first query against one of them fails
// with "no such table" long after migration reported success.
//
// Migration 60 repairs the two known divergences. These assertions cover the
// repair and the rule that prevents the next one.

// schemaObjects names every table, index, and trigger a database holds.
func schemaObjects(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("read schema objects: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan schema object: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema objects: %v", err)
	}
	return names
}

// openMigrated returns a database at the current schema version.
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repair.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestMigrationRepairsADatabaseBuiltFromTheEarlierHistory(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()

	// Reproduce a store whose migration 8 and 9 applied their earlier text: the
	// objects those edits added are absent, and the pre-rename table remains.
	for _, statement := range []string{
		`DROP TABLE project_governing_requirements`,
		`DROP TABLE initiative_entries`,
		`CREATE TABLE epic_entries (
			epic_work_id   TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
			child_work_id  TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
			position       INTEGER NOT NULL CHECK(position >= 0),
			required       INTEGER NOT NULL CHECK(required IN (0,1)),
			PRIMARY KEY(epic_work_id, child_work_id),
			UNIQUE(epic_work_id, position),
			CHECK(epic_work_id <> child_work_id)
		)`,
		`CREATE INDEX epic_entries_by_child ON epic_entries(child_work_id, epic_work_id)`,
		`DELETE FROM schema_migrations WHERE version = 60`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("stage earlier history (%s): %v", statement, err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("repair migrate: %v", err)
	}

	objects := schemaObjects(t, db)
	for _, required := range []string{
		"project_governing_requirements",
		"project_governing_requirements_by_project",
		"project_governing_requirements_guard_insert",
		"project_governing_requirements_guard_update",
		"project_governing_requirements_guard_delete",
		"initiative_entries",
		"initiative_entries_by_child",
	} {
		if !objects[required] {
			t.Fatalf("repair left %s absent", required)
		}
	}
	if objects["epic_entries"] || objects["epic_entries_by_child"] {
		t.Fatal("repair left the pre-rename table in place")
	}
}

func TestRepairMigrationIsIdempotentOnAFreshDatabase(t *testing.T) {
	fresh := schemaObjects(t, openMigrated(t))
	if !fresh["initiative_entries"] || !fresh["project_governing_requirements"] {
		t.Fatal("a fresh database is missing objects the repair migration creates")
	}
	if fresh["epic_entries"] {
		t.Fatal("the repair migration left its staging table on a fresh database")
	}
}

// TestShippedVariantTableIsFrozen keeps the escape hatch closed. Every entry
// here records a migration that was edited after it shipped; each one is a
// database in the field whose schema silently differs from the code. Adding
// another entry is how this defect class recurs, so the table is pinned: an
// in-place edit must land as a new repair migration instead.
func TestShippedVariantTableIsFrozen(t *testing.T) {
	t.Parallel()
	frozen := map[int]int{
		3: 1, 7: 1, 8: 1, 9: 2, 15: 1, 16: 1, 18: 1, 20: 1, 22: 1,
		25: 1, 26: 1, 35: 1, 36: 1, 37: 1, 39: 1, 40: 2,
	}
	if len(migrationShippedVariantChecksums) != len(frozen) {
		t.Fatalf("shipped variant table has %d migrations, frozen at %d; an in-place edit must ship a repair migration",
			len(migrationShippedVariantChecksums), len(frozen))
	}
	for version, count := range frozen {
		got, ok := migrationShippedVariantChecksums[version]
		if !ok {
			t.Fatalf("migration %d left the shipped variant table; applied databases still carry its earlier text", version)
		}
		if len(got) != count {
			t.Fatalf("migration %d records %d shipped variants, frozen at %d", version, len(got), count)
		}
	}
}

// The compatibility floor is the highest breaking migration applied, and every
// migration after it is additive by declaration. A binary defining the floor
// must open a database that has run later additive migrations.
func TestManifestAdmitsLaterAdditiveMigrations(t *testing.T) {
	floor := 0
	for _, m := range migrations {
		if m.Breaking && m.Version > floor {
			floor = m.Version
		}
	}
	if floor == 0 {
		t.Fatal("no breaking migration declared; the floor would never rise")
	}

	// A database that ran two migrations this binary does not define: one
	// additive, which must not raise the floor, and nothing breaking.
	applied := map[int]appliedMigration{}
	for _, m := range migrations {
		applied[m.Version] = appliedMigration{Checksum: m.checksum(), Breaking: m.Breaking}
	}
	applied[CurrentSchemaVersion()+1] = appliedMigration{Checksum: "future-additive", Breaking: false}
	applied[CurrentSchemaVersion()+2] = appliedMigration{Checksum: "future-additive-2", Breaking: false}
	if err := checkManifest(applied); err != nil {
		t.Fatalf("later additive migrations must not refuse this binary: %v", err)
	}

	// One breaking migration this binary does not define closes the door, and
	// the refusal names the version the operator needs.
	applied[CurrentSchemaVersion()+3] = appliedMigration{Checksum: "future-breaking", Breaking: true}
	err := checkManifest(applied)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("breaking future migration err=%v, want schema_unsupported", err)
	}
	if !strings.Contains(failure.Detail, strconv.Itoa(CurrentSchemaVersion()+3)) {
		t.Fatalf("refusal %q must name the version the database requires", failure.Detail)
	}
	if !failure.RetrySafe {
		t.Fatal("a refusal that writes nothing must be retry safe")
	}
}

// A manifest written before the compatibility column existed carries no bits.
// Absent must read as breaking, so such a database keeps exactly the refusal
// it had before this mechanism landed.
func TestManifestWithoutCompatibilityColumnReadsAsBreaking(t *testing.T) {
	db := openMigrated(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `ALTER TABLE schema_migrations DROP COLUMN breaking`); err != nil {
		t.Fatalf("stage a pre-column manifest: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate must re-add the column and succeed: %v", err)
	}
	var breaking int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE breaking = 1`).Scan(&breaking); err != nil {
		t.Fatalf("read the restored column: %v", err)
	}
	if breaking != CurrentSchemaVersion() {
		t.Fatalf("restored rows breaking=%d, want all %d rows breaking", breaking, CurrentSchemaVersion())
	}
}
