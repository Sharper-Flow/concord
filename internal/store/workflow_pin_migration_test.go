package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRepinMigrationRepinsOrphanedDefinitionPins(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-repin.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}

	index := -1
	for i, migration := range migrations {
		if migration.Name == "repin_orphaned_workflow_definition_pins" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("repin migration is absent")
	}
	for _, migration := range migrations[:index] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
	}
	const orphanedDigest = "sha256:90fed5c22d8493fd4b4d20ecdd28fd4dbccfb8f0aaef4645b8990a704b12ab50"
	const historicalDigest = "sha256:deaeec1077f5360b23b4c6ca78328d45a620668c503760855ec28e7bf6ecf155"
	seed := func(id, step, digest string) string {
		return `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency) VALUES('` + id + `','task','Repin','needed',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','{}','','standard'); INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES('` + id + `','workflow.implementation',1,'` + digest + `','` + step + `','planned');`
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);`+seed("work-repin", "proposal", orphanedDigest)+seed("work-foreign-step", "not-a-step", orphanedDigest)+seed("work-historical", "execution", historicalDigest)+` DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, migrations[index].SQL); err != nil {
		t.Fatalf("repin migration: %v", err)
	}

	var version int64
	var digest string
	if err := db.QueryRowContext(ctx, `SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id='work-repin'`).Scan(&version, &digest); err != nil {
		t.Fatal(err)
	}
	if version != authoredWorkflowDefinitionVersion || digest != "sha256:e16dfed665a50ece82f33040d2cb0e4a6abfd72dbc5b4743098eab22f0faab89" {
		t.Fatalf("repinned definition = v%d %s", version, digest)
	}
	// A row whose current_step is not a step of the version-2 definition for
	// its ref stays on its recorded digest: the migration cannot place it, so
	// verification keeps refusing it by name instead of hiding the gap.
	if err := db.QueryRowContext(ctx, `SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id='work-foreign-step'`).Scan(&version, &digest); err != nil {
		t.Fatal(err)
	}
	if version != 1 || digest != orphanedDigest {
		t.Fatalf("unplaceable row moved = v%d %s", version, digest)
	}
	// A valid historical pin stays attached to the definition it was pinned
	// under.
	if err := db.QueryRowContext(ctx, `SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id='work-historical'`).Scan(&version, &digest); err != nil {
		t.Fatal(err)
	}
	if version != 1 || digest != historicalDigest {
		t.Fatalf("historical pin moved = v%d %s", version, digest)
	}
}
