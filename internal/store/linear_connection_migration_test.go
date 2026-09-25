package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

// CD-0171 removed the repository-to-Linear-Project mapping. Connections
// recorded while the mapping still existed keep its retired project_id and
// project_ids members in stored metadata, and the update path reaches a
// document only when an operator edits the connection. Migration 103 removes
// the retired members from every stored document; this test seeds a
// pre-migration store and proves the strip leaves every other member intact,
// including a document that already carries the current convention.
func TestMigration103StripsRetiredLinearProjectMappingFromStoredMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-v101.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 102 {
			break
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d (%s): %v", migration.Version, migration.Name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
			migration.Version, migration.Name, migration.checksum(), "2026-09-24T00:00:00Z",
		); err != nil {
			t.Fatalf("manifest record for migration %d: %v", migration.Version, err)
		}
	}

	const legacy = `{"unrelated":"preserved","linear":{"workspace_url":"https://linear.app/example","team_id":"team-uuid","auth_mode":"personal_api_key","project_id":"legacy-project","project_ids":{"legacy-project":"proj-uuid","stale-project":"stale-uuid"},"status_ids":{"completed":"status-completed"},"label_ids":{"project:concord-project":"label-repo"}}}`
	const current = `{"linear":{"workspace_url":"https://linear.app/example","team_id":"team-uuid","status_ids":{"completed":"status-completed"}}}`
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ resourceID, metadata string }{
		{"linear-conn-legacy", legacy},
		{"linear-conn-current", current},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO managed_resources(resource_id,display_name,class,kind,purpose,stage_maturity,stage_audience_commitment,environments,metadata_schema_version,metadata,version,created_at,updated_at)
			 VALUES(?,'Linear connection','saas','saas_account','Linear planning connection','prototype','operator_only','[]','linear-connection-v1',?,1,'2026-09-24T00:00:00Z','2026-09-24T00:00:00Z')`,
			row.resourceID, row.metadata,
		); err != nil {
			t.Fatalf("%s: %v", row.resourceID, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	var gotLegacy string
	if err := db.QueryRowContext(ctx, `SELECT metadata FROM managed_resources WHERE resource_id='linear-conn-legacy'`).Scan(&gotLegacy); err != nil {
		t.Fatal(err)
	}
	var legacyDoc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(gotLegacy), &legacyDoc); err != nil {
		t.Fatal(err)
	}
	if string(legacyDoc["unrelated"]) != `"preserved"` {
		t.Fatalf("unrelated member = %s, want preserved", legacyDoc["unrelated"])
	}
	var linear map[string]json.RawMessage
	if err := json.Unmarshal(legacyDoc["linear"], &linear); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"project_id", "project_ids"} {
		if _, exists := linear[member]; exists {
			t.Fatalf("retired %s remains in Linear metadata: %s", member, linear[member])
		}
	}
	for member, want := range map[string]string{
		"workspace_url": `"https://linear.app/example"`,
		"team_id":       `"team-uuid"`,
		"auth_mode":     `"personal_api_key"`,
		"status_ids":    `{"completed":"status-completed"}`,
		"label_ids":     `{"project:concord-project":"label-repo"}`,
	} {
		got, ok := linear[member]
		if !ok || string(got) != want {
			t.Fatalf("linear %s = %s, want %s", member, got, want)
		}
	}

	var gotCurrent string
	if err := db.QueryRowContext(ctx, `SELECT metadata FROM managed_resources WHERE resource_id='linear-conn-current'`).Scan(&gotCurrent); err != nil {
		t.Fatal(err)
	}
	if gotCurrent != current {
		t.Fatalf("current-convention document changed: %s, want %s", gotCurrent, current)
	}
}
