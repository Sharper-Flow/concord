package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestStepMigrationRewritesPlaceholderRowsOnAPopulatedStore runs the step
// migration against a store that already holds placeholder rows.
//
// workflow_instances is fold-only. A migration that rewrites it without holding
// the fold guard open aborts on the trigger, and it aborts on exactly the
// stores that carry the rows it must repair: an empty store has nothing to
// rewrite, so the trigger never fires and the migration looks correct.
func TestStepMigrationRewritesPlaceholderRowsOnAPopulatedStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-placeholder.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}

	const stepMigration = "workflow_instance_step_belongs_to_its_definition"
	index := -1
	for i, migration := range migrations {
		if migration.Name == stepMigration {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatalf("migration %q is absent", stepMigration)
	}

	for _, migration := range migrations[:index] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
	}

	// Seed the placeholder rows the migration exists to repair. The seed holds
	// the fold guard for the same reason the migration must.
	seed := `INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency)
    VALUES('work-1','task','Placeholder','needed',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','{}','','standard');
INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_model)
    VALUES('work-1','workflow.implementation',1,'sha256:0000000000000000000000000000000000000000000000000000000000000000','start','running','single_actor');
DELETE FROM fold_guard;`
	if _, err := db.ExecContext(ctx, seed); err != nil {
		t.Fatalf("seed placeholder instance: %v", err)
	}

	if _, err := db.ExecContext(ctx, migrations[index].SQL); err != nil {
		t.Fatalf("step migration on a populated store: %v", err)
	}

	var step string
	if err := db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, "work-1").Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "proposal" {
		t.Fatalf("current_step=%q, want the definition's declared start step", step)
	}

	// The guard closes behind the migration. A left-open guard would let any
	// later writer bypass the fold-only rule.
	var open int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fold_guard`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("fold guard rows=%d, want the guard closed", open)
	}
}
