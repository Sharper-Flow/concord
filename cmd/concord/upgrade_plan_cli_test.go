package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
	_ "modernc.org/sqlite"
)

// cliStoreRoot builds a store path under a temporary data root and points
// the CLI at it the way the operator's environment would. The returned data
// root holds the lease directory, the fence, and the prepared record.
func cliStoreRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "concord.db")
	t.Setenv(dbOverrideEnv, path)
	return path, root
}

func runUpgradeStdin(t *testing.T, stdin string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"upgrade"}, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// The plan input obeys the release-verb discipline like every other input:
// arguments are refused by the shared decoder, and only the declared fields
// of the JSON body are accepted (CON-807).
func TestUpgradePlanDecodesThroughTheReleaseInput(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"upgrade", "--plan"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("upgrade --plan as an argument = %d, want the decoder's exit 2: %s", code, errOut.String())
	}
	if code, _, errText := runUpgradeStdin(t, `{"plan":true,"surprise":1}`); code != 1 {
		t.Fatalf("an unknown plan field = %d %q, want a refusal", code, errText)
	}
}

// The plan reports the exact prepared-candidate commands: the migration
// command names the absolute binary that produced the plan, and the
// activation command comes from the prepared record when one exists
// (CON-807: exact candidate commands, never placeholders).
func TestUpgradePlanReportsExactCommands(t *testing.T) {
	_, root := cliStoreRoot(t)
	code, out, errOut := runUpgradeStdin(t, `{"plan":true}`)
	if code != 0 {
		t.Fatalf("plan over a fresh store = %d: %s", code, errOut)
	}
	var report struct {
		MigrationCommand  string           `json:"migration_command"`
		ActivationCommand string           `json:"activation_command"`
		ActivationBlocked bool             `json:"activation_blocked"`
		FreshStore        bool             `json:"fresh_store"`
		MaintenanceFence  *hostlease.Fence `json:"maintenance_fence"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("plan output is not JSON: %v: %s", err, out)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatal(err)
	}
	if want := self + " upgrade"; report.MigrationCommand != want {
		t.Fatalf("migration command = %q, want the exact candidate invocation %q", report.MigrationCommand, want)
	}
	if !report.FreshStore || report.ActivationBlocked || report.MaintenanceFence != nil {
		t.Fatalf("a fresh store must plan unblocked and unfenced: %+v", report)
	}
	if !strings.Contains(report.ActivationCommand, "prepared-release.json") {
		t.Fatalf("without a prepared record the activation command must name the record route: %q", report.ActivationCommand)
	}

	exact := "python3 /downloads/concord-installer.py activate --version v9.1.0"
	record := map[string]any{
		"schema":             "concord-prepared-release-v1",
		"version":            "v9.1.0",
		"activation_command": exact,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preparedReleasePath(root), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runUpgradeStdin(t, `{"plan":true}`)
	if code != 0 {
		t.Fatalf("plan with a prepared record = %d: %s", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("plan output is not JSON: %v", err)
	}
	if report.ActivationCommand != exact {
		t.Fatalf("activation command = %q, want the prepared record's exact command", report.ActivationCommand)
	}
}

// Session admission is the fence's excluded surface: while the boundary is
// open the host-lease verb refuses with the fence's notice, and admission
// resumes once the activation closes the boundary (CON-807).
func TestHostLeaseAdmissionIsExcludedUnderTheFence(t *testing.T) {
	_, root := cliStoreRoot(t)
	lease := `{"pid":` + pidJSON(os.Getpid()) + `}`
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"host-lease"}, strings.NewReader(lease), &out, &errOut); code != 0 {
		t.Fatalf("admission without a fence = %d: %s", code, errOut.String())
	}
	fence, err := hostlease.EnsureFence(root, hostlease.Fence{
		CoreBinary: "/releases/v9.1.0/bin/concord",
		Notice:     "session admission reopens when v9.1.0 activates",
	})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"host-lease"}, strings.NewReader(lease), &out, &errOut); code != 1 {
		t.Fatalf("admission under an open fence = %d, want a refusal: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), fence.Notice) || !strings.Contains(errOut.String(), "maintenance") {
		t.Fatalf("the admission refusal must carry the fence notice: %s", errOut.String())
	}
	if err := hostlease.RemoveFenceOwned(root, fence.FenceID); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"host-lease"}, strings.NewReader(lease), &out, &errOut); code != 0 {
		t.Fatalf("admission after the boundary closed = %d: %s", code, errOut.String())
	}
}

// The JSON descriptor route is the installer's side-effect-free capability
// probe (CON-807): --version --json answers before any stdin read, store
// open, or lease write, so probing a staged core changes nothing — not the
// store, not the lease directory, not the fence — while plain --version
// output stays exactly the version string.
func TestVersionJSONDescriptorIsSideEffectFree(t *testing.T) {
	path, root := cliStoreRoot(t)
	var out, errOut bytes.Buffer
	stdin := &countingStdin{}
	if code := runWithInput([]string{"--version", "--json"}, stdin, &out, &errOut); code != 0 {
		t.Fatalf("--version --json exit code = %d, want 0: %s", code, errOut.String())
	}
	if stdin.reads != 0 {
		t.Fatalf("the descriptor route consumed %d stdin reads", stdin.reads)
	}
	var descriptor struct {
		Version        string `json:"version"`
		SchemaVersion  int    `json:"schema_version"`
		FenceProtocol  int    `json:"fence_protocol"`
		ManifestDigest string `json:"manifest_digest"`
		ReleaseRoot    string `json:"release_root"`
		CoreBinary     string `json:"core_binary"`
	}
	if err := json.Unmarshal(out.Bytes(), &descriptor); err != nil {
		t.Fatalf("the descriptor is not JSON: %v: %s", err, out.String())
	}
	if descriptor.Version != version.Value {
		t.Fatalf("the descriptor version = %q, want the build stamp %q", descriptor.Version, version.Value)
	}
	if descriptor.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("the descriptor schema version = %d, want %d", descriptor.SchemaVersion, store.CurrentSchemaVersion())
	}
	if descriptor.FenceProtocol != hostlease.CurrentFenceProtocol {
		t.Fatalf("the descriptor fence protocol = %d, want %d", descriptor.FenceProtocol, hostlease.CurrentFenceProtocol)
	}
	if descriptor.ManifestDigest == "" || descriptor.ReleaseRoot == "" || descriptor.CoreBinary == "" {
		t.Fatalf("the descriptor must carry its pairing identity: %+v", descriptor)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the descriptor route created files beside the store %s: %v", path, names)
	}
	var plain bytes.Buffer
	if code := run([]string{"--version"}, &plain, &errOut); code != 0 || plain.String() != version.Value+"\n" {
		t.Fatalf("plain --version changed: code=%d output=%q", code, plain.String())
	}
}

func pidJSON(value int) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// pendingBreakingStore builds a real store whose manifest tail, breaking
// step included, is pending again. With unpoison set, the objects the tail
// created are dropped so the migration reapplies cleanly; without it, the
// first colliding object makes the migration fail deterministically.
func pendingBreakingStore(t *testing.T, path string, unpoison bool) {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open and migrate: %v", err)
	}
	_ = s.Close()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := rewindMigrationTail(db, 111, unpoison); err != nil {
		t.Fatal(err)
	}
}

// A zero floor selects only the newest manifest row. Poisoned fixtures keep
// schema objects; clean reapplication requires an explicit entry for every step.
func rewindMigrationTail(db *sql.DB, from int, unpoison bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if from == 0 {
		if err := tx.QueryRow(`SELECT coalesce(max(version),0) FROM schema_migrations`).Scan(&from); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT version FROM schema_migrations WHERE version >= ? ORDER BY version DESC`, from)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return err
		}
		versions = append(versions, version)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if unpoison {
		for _, version := range versions {
			if _, ok := migrationTailReversals[version]; !ok {
				return fmt.Errorf("missing fixture reversal for migration %d", version)
			}
		}
		for _, version := range versions {
			for _, statement := range migrationTailReversals[version] {
				if _, err := tx.Exec(statement); err != nil {
					return fmt.Errorf("cannot unpoison migration %d with %q: %w", version, statement, err)
				}
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version >= ?`, from); err != nil {
		return fmt.Errorf("cannot remove the manifest tail: %w", err)
	}
	return tx.Commit()
}

var migrationTailReversals = map[int][]string{
	// Rebuild-only steps replace their own tables when reapplied.
	111: nil,
	119: nil,
	126: nil,
	// Migration 134's overlap-resolution rebuild also drops and recreates
	// its own table, backup TEMP table included.
	134: nil,
	129: {
		`ALTER TABLE law_relations DROP COLUMN declarer_law_id`,
	},
	130: {
		`DROP TABLE knowledge_package_domains`,
		`DROP TABLE law_coverage_anchors`,
		`DROP TABLE law_evidence_paths`,
	},
	// Migrations 131 and 132 create only the tables migration 136's
	// retirement drops, so the rewind restores nothing and the reapplied
	// tail recreates them fresh.
	131: nil,
	132: nil,
	133: {
		`ALTER TABLE workflow_contracts DROP COLUMN decision_domains`,
	},
	135: {
		`ALTER TABLE workflow_contracts DROP COLUMN law_change`,
	},
	// Migration 136's retirement drops only retired tables; reapplying it
	// after the reversal recreates nothing the fixture must restore.
	136: nil,
	128: {
		`ALTER TABLE project_locators DROP COLUMN normalization_version`,
	},
	127: {
		`ALTER TABLE worktree_verify_leases DROP COLUMN event_watermark`,
	},
	125: {
		`DROP TRIGGER native_oracle_plan_pair_insert`,
		`DROP TRIGGER native_oracle_plan_pair_update`,
		`ALTER TABLE worktree_verify_leases DROP COLUMN native_plan_json`,
		`ALTER TABLE worktree_verify_leases DROP COLUMN native_plan_sha256`,
		`ALTER TABLE worktree_verify_leases DROP COLUMN stdout_blob`,
		`ALTER TABLE worktree_verify_leases DROP COLUMN stderr_blob`,
	},
	121: {
		`DROP TABLE outside_repair_reconciliations`,
		`DROP TABLE outside_repair_dispositions`,
		// Restore the pre-121 column set and CHECK while retaining every row.
		// DROP COLUMN alone leaves the widened instance_state constraint.
		`CREATE TABLE workflow_instances_before_outside_repair (
    work_id TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE RESTRICT,
    definition_ref TEXT NOT NULL,
    definition_version INTEGER NOT NULL,
    definition_digest TEXT NOT NULL,
    current_step TEXT NOT NULL,
    instance_state TEXT NOT NULL CHECK(instance_state IN ('planned','ready','running','blocked','awaiting_condition','verifying','completed','cancelled','superseded')),
    execution_actor_ref TEXT REFERENCES workflow_actors(actor_ref) ON DELETE RESTRICT,
    execution_model TEXT NOT NULL DEFAULT '' CHECK(length(execution_model) <= 128),
    started_at TEXT,
    completed_at TEXT,
    last_checkpoint_at TEXT,
    execution_started_at TEXT,
    CHECK(definition_version > 0 AND definition_version <= 2147483647),
    CHECK(length(definition_ref) BETWEEN 2 AND 128),
    CHECK(length(definition_digest) = 71 AND substr(definition_digest,1,7) = 'sha256:'),
    CHECK(length(current_step) BETWEEN 2 AND 128)
);
INSERT INTO workflow_instances_before_outside_repair
    (work_id, definition_ref, definition_version, definition_digest, current_step, instance_state,
     execution_actor_ref, execution_model, started_at, completed_at, last_checkpoint_at, execution_started_at)
SELECT work_id, definition_ref, definition_version, definition_digest, current_step, instance_state,
       execution_actor_ref, execution_model, started_at, completed_at, last_checkpoint_at, execution_started_at
  FROM workflow_instances;
DROP TABLE workflow_instances;
ALTER TABLE workflow_instances_before_outside_repair RENAME TO workflow_instances;
CREATE INDEX workflow_instances_state ON workflow_instances(instance_state, work_id);
CREATE TRIGGER workflow_instances_guard_insert BEFORE INSERT ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_update BEFORE UPDATE ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER workflow_instances_guard_delete BEFORE DELETE ON workflow_instances FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'workflow_instances is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;`,
	},
	112: {
		`DROP TABLE product_knowledge_sources`,
	},
	113: {
		`DROP TABLE law_cross_source_relations`,
	},
	114: {
		`ALTER TABLE workflow_proposal_records DROP COLUMN out_of_scope`,
	},
	115: {
		`DROP TABLE project_handoffs`,
	},
	118: {
		`DROP TABLE worker_job_revisions`,
	},
	116: {
		`DROP TABLE durability_commits`,
	},
	117: {
		`DROP TABLE runtime_state_writers`,
	},
	120: {
		// Migration 120 attaches its maintenance triggers to
		// pre-existing tables, so they drop by name before its tables.
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
	},
	124: {
		// Migration 124's retirement dropped the planning mirror objects
		// that the re-applied tail reads and drops again: migration 120
		// attaches its maintenance triggers to initiative_entries, and
		// migration 124 drops the mirror tables, the products planning
		// mode, and the phase-0 issue-link shape it rebuilds. Restore
		// each object's pre-124 shape from the migration that last
		// created it (8, 76, 102), carrying any current rows forward.
		`CREATE TABLE initiative_entries (
    initiative_work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    child_work_id  TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    position       INTEGER NOT NULL CHECK(position >= 0),
    required       INTEGER NOT NULL CHECK(required IN (0,1)),
    PRIMARY KEY(initiative_work_id, child_work_id),
    UNIQUE(initiative_work_id, position),
    CHECK(initiative_work_id <> child_work_id)
)`,
		`CREATE INDEX initiative_entries_by_child ON initiative_entries(child_work_id, initiative_work_id)`,
		`ALTER TABLE products ADD COLUMN planning_mode TEXT NOT NULL DEFAULT 'local_only'
    CHECK (planning_mode IN ('local_only','linear_enabled'))`,
		`ALTER TABLE linear_issue_links RENAME TO linear_issue_links_v124_stage`,
		`CREATE TABLE linear_issue_links (
    work_id            TEXT PRIMARY KEY CHECK(length(work_id) BETWEEN 2 AND 128),
    remote_issue_uuid  TEXT NOT NULL CHECK(length(remote_issue_uuid) BETWEEN 2 AND 128),
    human_key          TEXT NOT NULL DEFAULT '' CHECK(length(human_key) <= 64),
    url                TEXT NOT NULL DEFAULT '' CHECK(length(url) <= 2048),
    remote_updated_at  TEXT NOT NULL DEFAULT '',
    content_hash       TEXT NOT NULL DEFAULT '' CHECK(content_hash = '' OR (length(content_hash) = 71 AND substr(content_hash,1,7) = 'sha256:')),
    link_state         TEXT NOT NULL DEFAULT 'unpublished' CHECK(link_state IN ('unpublished','pending','confirmed','degraded')),
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
)`,
		`INSERT INTO linear_issue_links
    (work_id, remote_issue_uuid, human_key, url, created_at, updated_at)
    SELECT work_id, remote_issue_uuid, human_key, url, created_at, updated_at FROM linear_issue_links_v124_stage`,
		`DROP TABLE linear_issue_links_v124_stage`,
		`CREATE INDEX linear_issue_links_state ON linear_issue_links(link_state)`,
		`CREATE TRIGGER linear_issue_links_guard_insert BEFORE INSERT ON linear_issue_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_issue_links is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_issue_links_guard_update BEFORE UPDATE ON linear_issue_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_issue_links is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_issue_links_guard_delete BEFORE DELETE ON linear_issue_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_issue_links is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;`,
		`CREATE TABLE linear_project_links (
    work_id             TEXT PRIMARY KEY CHECK(length(work_id) BETWEEN 2 AND 128),
    remote_project_uuid TEXT NOT NULL UNIQUE CHECK(length(remote_project_uuid) BETWEEN 2 AND 128),
    name                TEXT NOT NULL DEFAULT '',
    url                 TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
)`,
		`CREATE INDEX linear_project_links_uuid ON linear_project_links(remote_project_uuid)`,
		`CREATE TABLE linear_outbox (
    operation_id     TEXT PRIMARY KEY CHECK(length(operation_id) BETWEEN 2 AND 128),
    work_id          TEXT NOT NULL CHECK(length(work_id) BETWEEN 2 AND 128),
    op_kind          TEXT NOT NULL CHECK(op_kind IN ('issue_create','issue_update','issue_adopt','issue_audit_comment','project_create','project_update')),
    idempotency_key  TEXT NOT NULL UNIQUE CHECK(length(idempotency_key) BETWEEN 2 AND 128),
    payload          TEXT NOT NULL CHECK(json_valid(payload) AND json_type(payload) = 'object'),
    state            TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','in_flight','done','failed')),
    attempts         INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    CHECK((state = 'queued' AND attempts = 0) OR attempts > 0)
)`,
		`CREATE INDEX linear_outbox_state ON linear_outbox(state, created_at)`,
		`CREATE TABLE linear_outbox_dispositions (
    operation_id TEXT PRIMARY KEY REFERENCES linear_outbox(operation_id) ON DELETE RESTRICT,
    work_id      TEXT NOT NULL REFERENCES work_items(id) ON DELETE RESTRICT,
    disposition  TEXT NOT NULL CHECK(disposition IN ('acknowledged')),
    reason       TEXT NOT NULL CHECK(length(reason) BETWEEN 1 AND 4096),
    created_at   TEXT NOT NULL,
    CHECK(length(operation_id) BETWEEN 2 AND 128),
    CHECK(length(work_id) BETWEEN 2 AND 128)
)`,
		`CREATE INDEX linear_outbox_dispositions_work ON linear_outbox_dispositions(work_id)`,
		`CREATE TRIGGER linear_project_links_guard_insert BEFORE INSERT ON linear_project_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_project_links is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_project_links_guard_update BEFORE UPDATE ON linear_project_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_project_links is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_project_links_guard_delete BEFORE DELETE ON linear_project_links FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_project_links is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;`,
		`CREATE TRIGGER linear_outbox_guard_insert BEFORE INSERT ON linear_outbox FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_outbox is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_outbox_guard_update BEFORE UPDATE ON linear_outbox FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_outbox is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_outbox_guard_delete BEFORE DELETE ON linear_outbox FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_outbox is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;`,
		`CREATE TRIGGER linear_outbox_dispositions_guard_insert BEFORE INSERT ON linear_outbox_dispositions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_outbox_dispositions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_outbox_dispositions_guard_update BEFORE UPDATE ON linear_outbox_dispositions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_outbox_dispositions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;
CREATE TRIGGER linear_outbox_dispositions_guard_delete BEFORE DELETE ON linear_outbox_dispositions FOR EACH ROW BEGIN SELECT RAISE(ABORT, 'linear_outbox_dispositions is fold-only') WHERE NOT EXISTS (SELECT 1 FROM fold_guard WHERE active=1); END;`,
	},
	122: {
		// Migration 122's guarded projection is one plain CREATE TABLE:
		// its guard triggers and index are owned by the table and drop
		// with it.
		`DROP TABLE IF EXISTS worktree_ref_outcomes`,
	},
	123: {
		`DROP TRIGGER IF EXISTS active_research_packs_retirement_delete_guard`,
	},
}

// A refused or failed migration closes the boundary it opened and commits
// nothing: the store keeps its pending tail and the active release stays
// usable (CON-807 recovery before an incompatible migration).
func TestUpgradeFailureClosesItsOwnFenceAndCommitsNothing(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, false)

	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("a colliding migration must fail the upgrade: %d %s", code, errOut)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence != nil {
		t.Fatalf("a failed upgrade must close the fence it opened: %+v %v", fence, err)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("the store must still read after the failed upgrade: %v", err)
	}
	blocked := false
	for _, pending := range plan.PendingBreaking {
		if pending.Version == 111 {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("the failed upgrade changed the pending breaking tail: %+v", plan)
	}
}

// A committed breaking step keeps the boundary open — the older release is
// now unusable — and the report names the activation route; a later no-op
// run adopts the same open boundary instead of closing it (CON-807
// candidate-forward recovery).
func TestUpgradeKeepsTheFenceAfterABreakingCommit(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)

	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("the repaired breaking tail must apply: %d %s", code, errOut)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence == nil {
		t.Fatalf("a committed breaking step must keep the boundary open: %+v %v", fence, err)
	}
	// The boundary the migration opens is attributed to the release root
	// that opened it — the ownership proof the installer's activation and
	// superseding install adopt and close by. An unattributed boundary is
	// nobody's to adopt (CON-807).
	selfRoot, selfBinary, err := selfRelease()
	if err != nil {
		t.Fatal(err)
	}
	if fence.ReleaseRoot != selfRoot || fence.CoreBinary != selfBinary {
		t.Fatalf("the migration's boundary must be attributed to its own release: root %q binary %q, want %q and %q",
			fence.ReleaseRoot, fence.CoreBinary, selfRoot, selfBinary)
	}
	var report struct {
		Applied     []int          `json:"applied"`
		Maintenance map[string]any `json:"maintenance"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("upgrade output is not JSON: %v: %s", err, out)
	}
	appliedBreaking := false
	for _, applied := range report.Applied {
		if applied == 111 {
			appliedBreaking = true
		}
	}
	if !appliedBreaking || report.Maintenance == nil {
		t.Fatalf("a breaking commit must report the open boundary: %+v", report)
	}
	command, _ := report.Maintenance["activation_command"].(string)
	if !strings.Contains(command, "prepared-release.json") {
		t.Fatalf("the boundary report must name the activation route: %+v", report.Maintenance)
	}

	// The operator's recorded activation command lands in the prepared
	// record; a no-op re-run adopts the open boundary and repeats it.
	exact := "python3 /downloads/concord-installer.py activate --version v9.1.0"
	if err := os.WriteFile(preparedReleasePath(root), []byte(`{"schema":"concord-prepared-release-v1","version":"v9.1.0","activation_command":"`+exact+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("a no-op re-run must succeed: %d %s", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("re-run output is not JSON: %v", err)
	}
	if command, _ := report.Maintenance["activation_command"].(string); command != exact {
		t.Fatalf("the adopted boundary must repeat the recorded command: %+v", report.Maintenance)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence == nil {
		t.Fatalf("only the activation closes the boundary: %+v %v", fence, err)
	}
}

// A post-commit failure keeps the boundary open (CON-807): Upgrade applies
// every migration and only then finishes the open, so a finishOpen refusal
// after a committed breaking step must not reopen session admission. The
// store's breaking tail is gone, the fence stays, and the diagnostic names
// the activation route that alone may reopen admission.
func TestUpgradeKeepsTheFenceWhenFinishOpenFailsAfterACommit(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// The strand must survive the migration itself: the installation cursor
	// key is finishOpen-only authority state. Migration 9 created its table
	// and every recorded migration keeps it applied, so no pending step
	// recreates it, snapshot qualification never reads it (projection
	// digests, membership invariants, and staged inventory all skip it), and
	// the committed store refuses every open at the cursor-key inspection.
	if _, err := db.Exec(`DROP TABLE agent_installation_keys`); err != nil {
		t.Fatalf("cannot drop the installation cursor key table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("a poisoned finishOpen must refuse the upgrade: %d %s", code, errOut)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("readiness after the failed upgrade: %v", err)
	}
	if len(plan.PendingBreaking) != 0 {
		t.Fatalf("the breaking step did not commit before the failure: %+v", plan)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil {
		t.Fatal(err)
	}
	if fence == nil {
		t.Fatal("a committed incompatible migration removed admission exclusion after a post-commit error")
	}
	if !strings.Contains(errOut, "activation") {
		t.Fatalf("the refusal must name the activation route: %s", errOut)
	}
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pre-upgrade-") && !strings.Contains(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), "-wal") && !strings.HasSuffix(entry.Name(), "-shm") {
			snapshots = append(snapshots, filepath.Join(root, "backups", entry.Name()))
		}
	}
	if len(snapshots) != 1 {
		t.Fatalf("finish-open failure must retain one snapshot: %v", snapshots)
	}
	if _, err := store.VerifyBackup(context.Background(), snapshots[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, snapshots[0]) {
		t.Fatalf("finish-open failure must name retained snapshot: %s", errOut)
	}
}

// A lease written before the fence protocol existed names a core that
// admits sessions without reading the fence, so the boundary cannot
// exclude it: the migration refuses, names the unfenceable participant,
// and closes the boundary this run opened (CON-807 fail-closed rule).
func TestUpgradeRefusesAnUnfenceableLegacyParticipant(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)

	companion := exec.Command("sleep", "30")
	if err := companion.Start(); err != nil {
		t.Fatalf("cannot start a legacy session: %v", err)
	}
	defer func() { _ = companion.Process.Kill() }()
	start, err := hostlease.ProcessStart(companion.Process.Pid)
	if err != nil {
		t.Fatalf("cannot observe the legacy session's start: %v", err)
	}
	// Admission refuses a lease whose pinned core is gone, so the synthetic
	// legacy lease pins this test binary, which exists.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := hostlease.Write(root, hostlease.Lease{
		PID:           companion.Process.Pid,
		PidStart:      start,
		ReleaseRoot:   "/releases/v0.1.0",
		CoreBinary:    self,
		SchemaVersion: 110,
		RecordedAt:    "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("cannot record the legacy lease: %v", err)
	}

	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("an unfenceable legacy participant must refuse the upgrade: %d %s", code, errOut)
	}
	if !strings.Contains(errOut, "unfenceable legacy participant") || !strings.Contains(errOut, "fence protocol 0") {
		t.Fatalf("the refusal must name the unfenceable participant and its protocol: %s", errOut)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence != nil {
		t.Fatalf("the refused boundary must close again: %+v %v", fence, err)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("the store must still read after the refusal: %v", err)
	}
	if len(plan.PendingBreaking) == 0 {
		t.Fatalf("the refusal must leave the breaking tail pending: %+v", plan)
	}
}

// A supported compatible release pair coexists on one store (CON-807): the
// newer release applied an additive step this (older) binary does not ship,
// and the older release still opens the store, keeps its session admitted,
// sees the newer session's lease beside it, and reads an unblocked readiness
// plan — schema compatibility floor admission in action, not just schema
// equality. Activation of the newer pair needs no maintenance boundary.
// The additive row is synthetic: this test proves the floor mechanism at
// the library level; the distinct released source pair running the same
// contract is proved by TestDistinctReleasedCoresCoexistOnOneStore.
func TestSupportedReleasePairsCoexistWithOldSessionsAndNewAccess(t *testing.T) {
	previous := version.Value
	version.Value = "v9.0.0"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("the older release must open and migrate the store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The older release's session holds its lease.
	older := `{"pid":` + pidJSON(os.Getpid()) + `,"directory":"/srv/site"}`
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"host-lease"}, strings.NewReader(older), &out, &errOut); code != 0 {
		t.Fatalf("the older session's admission = %d: %s", code, errOut.String())
	}
	// The newer release applied one additive step this binary does not ship.
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	head := store.CurrentSchemaVersion()
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking)
		VALUES(?, 'newer_additive_step', 'synthetic-additive-probe', '2026-01-01T00:00:00Z', 0)`, head+1); err != nil {
		t.Fatalf("cannot record the newer release's additive step: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The older release still opens the advanced store: the additive step is
	// admitted at the compatibility floor, so the old session keeps working.
	s, err = store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("the older release must open the newer pair's store: %v", err)
	}
	_ = s.Close()
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("readiness across the pair: %v", err)
	}
	if plan.ActivationBlocked || len(plan.PendingBreaking) != 0 {
		t.Fatalf("a compatible pair must plan unblocked: %+v", plan)
	}
	// A new session on the newer release is admitted beside the old one, and
	// both leases are observable together.
	companion := exec.Command("sleep", "30")
	if err := companion.Start(); err != nil {
		t.Fatalf("cannot start a companion session: %v", err)
	}
	defer func() { _ = companion.Process.Kill() }()
	newer := `{"pid":` + pidJSON(companion.Process.Pid) + `}`
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"host-lease"}, strings.NewReader(newer), &out, &errOut); code != 0 {
		t.Fatalf("the newer session's admission = %d: %s", code, errOut.String())
	}
	live, err := hostlease.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("the pair's sessions must coexist in the lease set: %+v", live)
	}
	for _, lease := range live {
		if lease.FenceProtocol != hostlease.CurrentFenceProtocol {
			t.Fatalf("a coexisting lease must record its fence protocol: %+v", lease)
		}
	}
}

// The fixtures below rebuild, at the CLI package's own seam, the pin shapes
// the internal authoring-cutover witnesses seed: workflow initialization runs
// through the exported store transaction API over synthetic work created by
// the event log, and the fold-only projection rows (contracts, the
// outside-repair hold, the terminal lifecycle) are written under the same
// transient fold guard the store's own folds use.

const (
	cutoverArchivedPinWork        = "cutover-archived-pin"
	cutoverOldContractWork        = "cutover-old-contract-pin"
	cutoverCanonicalWork          = "cutover-canonical"
	cutoverTerminalHistoryWork    = "cutover-terminal-history"
	cutoverSupersededContractWork = "cutover-superseded-contract"
	cutoverHeldWork               = "cutover-held-pin"
)

// cutoverSelectedWorks names the two nonterminal items the authoring-cutover
// gate selects in the fixture store: one archived-family instance pin and one
// old-authoring pin carried only by the work's active contract.
// cutoverSparedWorks names the three look-alikes the gate must leave alone.
var cutoverSelectedWorks = []string{cutoverArchivedPinWork, cutoverOldContractWork}
var cutoverSparedWorks = []string{cutoverCanonicalWork, cutoverTerminalHistoryWork, cutoverSupersededContractWork}

// cutoverDispositionReport is the cutover_disposition object the upgrade
// surface reports when the operator names the cancel input.
type cutoverDispositionReport struct {
	WorkCount      int            `json:"work_count"`
	ByLifecycle    map[string]int `json:"by_lifecycle"`
	ByPinnedFamily map[string]int `json:"by_pinned_family"`
	WorkIDs        []string       `json:"work_ids"`
}

type cutoverWorkState struct {
	Lifecycle     string
	Version       int64
	InstanceState string
}

// cutoverTransitionPayload is the ordinary work.transitioned payload the
// disposition appends per selected item.
type cutoverTransitionPayload struct {
	From             string `json:"from"`
	To               string `json:"to"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

// execUnderFoldGuard runs fixture statements against the fold-only
// projections under the same transient fold the store's own folds use.
func execUnderFoldGuard(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			t.Fatalf("cannot seed the cutover fixture with %q: %v", statement, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// cutoverFixtureActor is the operator-identity actor tuple every fixture
// workflow initialization records.
func cutoverFixtureActor(workID string) store.WorkflowActor {
	return store.WorkflowActor{
		PrincipalRef: "principal/operator",
		ClientRef:    "client/concord-cli-test",
		AgentRef:     "agent/operator",
		SessionRef:   "session/" + workID,
		ActorClass:   store.ActorAgent,
	}
}

// cutoverFixtureContract inserts one contract pin row for a work item under
// the fold guard, the shape an approval leaves behind.
func cutoverFixtureContract(workID string, contractVersion int64, ref string, definitionVersion int64, digest string) string {
	return fmt.Sprintf(`INSERT INTO workflow_contracts
		(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,definition_ref,definition_version,definition_digest)
		VALUES('%s',%d,'cutover contract pin','internal_sqlite','[]','[]','2026-10-12T00:00:00Z',
		(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]','%s',%d,'%s')`,
		workID, contractVersion, ref, definitionVersion, digest)
}

// seedCutoverPinStore builds one fully migrated temporary store whose
// nonterminal work holds every pin shape the authoring-cutover gate
// distinguishes. The item identities and their expected selection are
// cutoverSelectedWorks and cutoverSparedWorks.
func seedCutoverPinStore(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := pm1fixture.SeedProductAndProject(ctx, s, "cutover-product", "cutover-project"); err != nil {
		t.Fatal(err)
	}
	for _, workID := range append(append([]string{}, cutoverSelectedWorks...), cutoverSparedWorks...) {
		if err := pm1fixture.SeedWorkItem(ctx, s, "cutover-project", workID, "cutover pin fixture "+workID, 0); err != nil {
			t.Fatal(err)
		}
	}
	archived, err := store.BuiltinWorkflowDefinitionForRef("workflow.generic_one_off")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	old, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.implementation", 29)
	if !ok {
		t.Fatal("implementation v29 left the registry")
	}
	initialize := func(workID string, registered store.RegisteredDefinition) {
		t.Helper()
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
				WorkID:     workID,
				Definition: registered,
				Actor:      cutoverFixtureActor(workID),
				Now:        time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
			})
		}); err != nil {
			t.Fatalf("cannot initialize the workflow for %s: %v", workID, err)
		}
	}
	initialize(cutoverArchivedPinWork, archived)
	initialize(cutoverOldContractWork, canonical)
	initialize(cutoverCanonicalWork, canonical)
	initialize(cutoverTerminalHistoryWork, archived)
	initialize(cutoverSupersededContractWork, canonical)

	execUnderFoldGuard(t, s.DatabaseForTesting(),
		// The old-authoring item is pinned only through its active contract:
		// the instance carries the canonical head while contract v1 still
		// pins v29.
		cutoverFixtureContract(cutoverOldContractWork, 1, "workflow.implementation", 29, old.Digest),
		// The superseded-contract item carries the closed history the gate
		// must ignore: v1 pins the old authoring and v2, the canonical
		// successor, supersedes it.
		cutoverFixtureContract(cutoverSupersededContractWork, 1, "workflow.implementation", 29, old.Digest),
		cutoverFixtureContract(cutoverSupersededContractWork, 2, "workflow.implementation", 30, canonical.Digest),
		fmt.Sprintf(`UPDATE workflow_contracts SET superseded_by=2 WHERE work_id='%s' AND contract_version=1`, cutoverSupersededContractWork),
	)
	// The terminal historical item closed while its archived pin stayed
	// recorded: terminal work opens, reads, and rebuilds. The cancellation is
	// a retained work.transitioned event, so replay rebuilds the same
	// terminal state and snapshot qualification accepts the fixture by
	// design; a direct lifecycle row would not survive the staged rebuild.
	terminalActor, err := store.WorkflowActorRef(cutoverFixtureActor(cutoverTerminalHistoryWork))
	if err != nil {
		t.Fatal(err)
	}
	var terminalVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, cutoverTerminalHistoryWork).Scan(&terminalVersion); err != nil {
		t.Fatal(err)
	}
	terminalPayload, err := json.Marshal(map[string]any{
		"from": "needed", "to": "cancelled",
		"reason":           "cutover pin fixture: the item closed while its archived pin stayed recorded",
		"expected_version": terminalVersion, "resulting_version": terminalVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{
		EventID:        "cutover-fixture:terminal-history-cancelled",
		Kind:           "work.transitioned",
		SubjectType:    store.SubjectWorkItem,
		SubjectID:      cutoverTerminalHistoryWork,
		Actor:          terminalActor,
		OccurredAt:     time.Date(2026, 10, 12, 0, 1, 0, 0, time.UTC),
		PayloadVersion: 1,
		Payload:        terminalPayload,
	}}}); err != nil {
		t.Fatalf("cannot retain the terminal item's cancellation: %v", err)
	}
}

// holdCutoverWorkUnderOutsideRepair adds one more archived-pin item and
// places it under an active outside-repair hold, the held disposition the
// cancellation must refuse instead of transitioning.
func holdCutoverWorkUnderOutsideRepair(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := pm1fixture.SeedWorkItem(ctx, s, "cutover-project", cutoverHeldWork, "cutover pin fixture "+cutoverHeldWork, 0); err != nil {
		t.Fatal(err)
	}
	archived, err := store.BuiltinWorkflowDefinitionForRef("workflow.generic_one_off")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
			WorkID:     cutoverHeldWork,
			Definition: archived,
			Actor:      cutoverFixtureActor(cutoverHeldWork),
			Now:        time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
		})
	}); err != nil {
		t.Fatal(err)
	}
	execUnderFoldGuard(t, s.DatabaseForTesting(),
		fmt.Sprintf(`UPDATE workflow_instances SET instance_state='outside_repair', outside_repair_state='active' WHERE work_id='%s'`, cutoverHeldWork),
		fmt.Sprintf(`INSERT INTO outside_repair_dispositions(work_id,approval_ref,state,reason,evidence_json,recorded_at)
			VALUES('%s','approval:cutover-fixture-hold','active','operator-held outside repair','{}','2026-10-12T00:00:00Z')`, cutoverHeldWork),
	)
}

// cutoverWorkStates reads every fixture work item's lifecycle, version, and
// workflow instance state, so assertions compare full state, not claims.
func cutoverWorkStates(t *testing.T, path string) map[string]cutoverWorkState {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	rows, err := db.Query(`SELECT w.id, w.lifecycle, w.version, COALESCE(i.instance_state,'')
		FROM work_items w LEFT JOIN workflow_instances i ON i.work_id=w.id ORDER BY w.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := map[string]cutoverWorkState{}
	for rows.Next() {
		var workID string
		var state cutoverWorkState
		if err := rows.Scan(&workID, &state.Lifecycle, &state.Version, &state.InstanceState); err != nil {
			t.Fatal(err)
		}
		states[workID] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return states
}

// cutoverEventCount counts the whole event log, so a read-only claim and a
// zero-effect refusal are checked against every event, not a filtered kind.
func cutoverEventCount(t *testing.T, path string) int64 {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	var count int64
	if err := db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// cutoverTransitionedEvents reads the ordinary transition events keyed by
// their work item.
func cutoverTransitionedEvents(t *testing.T, path string) map[string]cutoverTransitionPayload {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	rows, err := db.Query(`SELECT subject_id, payload FROM domain_events WHERE kind='work.transitioned' ORDER BY subject_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	events := map[string]cutoverTransitionPayload{}
	for rows.Next() {
		var workID, raw string
		if err := rows.Scan(&workID, &raw); err != nil {
			t.Fatal(err)
		}
		var payload cutoverTransitionPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("the transition payload for %s is not JSON: %v: %s", workID, err, raw)
		}
		events[workID] = payload
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

// preCutoverUpgradeSnapshots lists the pre-mutation snapshots the data root
// retains, the same population the finish-open failure witness reads.
func preCutoverUpgradeSnapshots(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pre-upgrade-") && !strings.Contains(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), "-wal") && !strings.HasSuffix(entry.Name(), "-shm") {
			snapshots = append(snapshots, filepath.Join(root, "backups", entry.Name()))
		}
	}
	return snapshots
}

// preCutoverUpgradeManifests lists the pre-mutation snapshot manifests the
// data root retains, so a refusal that must leave no snapshot at all also
// proves no manifest survived it.
func preCutoverUpgradeManifests(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var manifests []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pre-upgrade-") && strings.HasSuffix(entry.Name(), ".json") {
			manifests = append(manifests, filepath.Join(root, "backups", entry.Name()))
		}
	}
	return manifests
}

// cutoverCancellationProbe is the deterministic timing witness: the trigger
// aborts the first cancellation append, so a mutating run dies exactly at the
// disposition and whatever the run already wrote stays observable.
const cutoverCancellationProbe = "cutover-cancel timing probe"

func installCancellationProbe(t *testing.T, path string) {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	if _, err := db.Exec(`CREATE TRIGGER cutover_cancel_timing_probe
		BEFORE INSERT ON domain_events
		WHEN NEW.kind = 'work.transitioned'
		BEGIN
			SELECT RAISE(ABORT, '` + cutoverCancellationProbe + `');
		END`); err != nil {
		t.Fatal(err)
	}
}

func dropCancellationProbe(t *testing.T, path string) {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	if _, err := db.Exec(`DROP TRIGGER cutover_cancel_timing_probe`); err != nil {
		t.Fatal(err)
	}
}

// The plan refuses a store whose nonterminal work pins retired or old
// authoring, and the refusal names the one operator input that cancels the
// selected work. The refusal reads only.
func TestUpgradePlanRefusesCutoverPinnedWorkUntilTheOperatorNamesTheCancelInput(t *testing.T) {
	path, _ := cliStoreRoot(t)
	seedCutoverPinStore(t, path)
	eventsBefore := cutoverEventCount(t, path)

	code, out, errOut := runUpgradeStdin(t, `{"plan":true}`)
	if code != 1 {
		t.Fatalf("plan over pinned work without the cancel input = %d, want a refusal: %s", code, errOut)
	}
	if out != "" {
		t.Fatalf("the refused plan must print no report: %s", out)
	}
	if !strings.Contains(errOut, `{"cancel_cutover_pinned_work":true}`) {
		t.Fatalf("the cutover refusal must name the operator input that cancels the selected work: %s", errOut)
	}
	if !strings.Contains(errOut, "workflow.generic_one_off") {
		t.Fatalf("the cutover refusal must name the blocking pin: %s", errOut)
	}
	if after := cutoverEventCount(t, path); after != eventsBefore {
		t.Fatalf("the refused plan moved the event log %d -> %d; the refusal must write nothing", eventsBefore, after)
	}
}

// The plan with the cancel input skips only the cutover gate and reports the
// exact disposition read-only: the two selected items, and nothing else —
// no lifecycle moves, no events, no fence, no snapshot.
func TestUpgradePlanWithTheCancelInputReportsTheExactCutoverDisposition(t *testing.T) {
	path, root := cliStoreRoot(t)
	seedCutoverPinStore(t, path)
	statesBefore := cutoverWorkStates(t, path)
	eventsBefore := cutoverEventCount(t, path)

	code, out, errOut := runUpgradeStdin(t, `{"plan":true,"cancel_cutover_pinned_work":true}`)
	if code != 0 {
		t.Fatalf("plan with the cancel input = %d: %s", code, errOut)
	}
	var report struct {
		store.UpgradeReadiness
		SnapshotRequired   bool                      `json:"snapshot_required"`
		CutoverDisposition *cutoverDispositionReport `json:"cutover_disposition"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the plan output is not JSON: %v: %s", err, out)
	}
	if report.ActivationBlocked || len(report.PendingBreaking) != 0 || !report.SnapshotRequired {
		t.Fatalf("the cancel input clears the cutover gate alone; nothing else may block the plan: %+v", report.UpgradeReadiness)
	}
	disposition := report.CutoverDisposition
	if disposition == nil {
		t.Fatalf("the plan with the cancel input must report a cutover_disposition object: %s", out)
	}
	if disposition.WorkCount != len(cutoverSelectedWorks) {
		t.Fatalf("cutover_disposition.work_count = %d, want %d: %+v", disposition.WorkCount, len(cutoverSelectedWorks), disposition)
	}
	if want := []string{cutoverArchivedPinWork, cutoverOldContractWork}; !reflect.DeepEqual(disposition.WorkIDs, want) {
		t.Fatalf("cutover_disposition.work_ids = %v, want the exactly selected %v", disposition.WorkIDs, want)
	}
	if want := map[string]int{"needed": 2}; !reflect.DeepEqual(disposition.ByLifecycle, want) {
		t.Fatalf("cutover_disposition.by_lifecycle = %v, want %v", disposition.ByLifecycle, want)
	}
	if want := map[string]int{"workflow.generic_one_off": 1, "workflow.implementation": 1}; !reflect.DeepEqual(disposition.ByPinnedFamily, want) {
		t.Fatalf("cutover_disposition.by_pinned_family = %v, want %v", disposition.ByPinnedFamily, want)
	}
	if after := cutoverEventCount(t, path); after != eventsBefore {
		t.Fatalf("the planned disposition moved the event log %d -> %d; the plan must read only", eventsBefore, after)
	}
	if after := cutoverWorkStates(t, path); !reflect.DeepEqual(after, statesBefore) {
		t.Fatalf("the planned disposition moved work state: %+v", after)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("a plan must not touch the maintenance boundary: %+v %v", fence, err)
	}
	if snapshots := preCutoverUpgradeSnapshots(t, root); len(snapshots) != 0 {
		t.Fatalf("a plan must not write a snapshot: %v", snapshots)
	}
}

// The migrating run with the cancel input and no pending breaking migration
// is still a mutation: it takes the maintenance boundary and writes a
// verified snapshot before the cancellation runs. Phase one proves the order
// deterministically — a probe trigger aborts the first cancellation append,
// so the verified snapshot the failed run leaves behind can only have been
// written before it — and phase two lets the same run complete and checks the
// disposition cancels exactly the selected items through ordinary
// work.transitioned events.
func TestUpgradeWithTheCancelInputFencesAndSnapshotsBeforeItCancels(t *testing.T) {
	path, root := cliStoreRoot(t)
	seedCutoverPinStore(t, path)
	statesBefore := cutoverWorkStates(t, path)
	eventsBefore := cutoverEventCount(t, path)

	installCancellationProbe(t, path)
	code, _, errOut := runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
	if code != 1 {
		t.Fatalf("the probe-aborted run = %d, want a refusal at the first cancellation: %s", code, errOut)
	}
	if !strings.Contains(errOut, cutoverCancellationProbe) {
		t.Fatalf("the run must fail at the cancellation append the probe aborts: %s", errOut)
	}
	snapshots := preCutoverUpgradeSnapshots(t, root)
	if len(snapshots) != 1 {
		t.Fatalf("the mutating run must write its verified snapshot before the cancellation it attempts: %v", snapshots)
	}
	if _, err := store.VerifyBackup(context.Background(), snapshots[0]); err != nil {
		t.Fatalf("the pre-cancellation snapshot must verify: %v", err)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("the refused run must close the boundary it opened: %+v %v", fence, err)
	}
	if after := cutoverEventCount(t, path); after != eventsBefore {
		t.Fatalf("the aborted disposition committed %d event(s); the refusal must be atomic", after-eventsBefore)
	}
	if after := cutoverWorkStates(t, path); !reflect.DeepEqual(after, statesBefore) {
		t.Fatalf("the aborted run moved work state: %+v", after)
	}
	dropCancellationProbe(t, path)

	code, out, errOut := runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
	if code != 0 {
		t.Fatalf("the canceling run = %d: %s", code, errOut)
	}
	var report struct {
		Applied       []int  `json:"applied"`
		SchemaVersion int    `json:"schema_version"`
		Backup        string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the upgrade output is not JSON: %v: %s", err, out)
	}
	if len(report.Applied) != 0 || report.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("a run with no pending migration must apply nothing: %+v", report)
	}
	if report.Backup == "" {
		t.Fatalf("the mutating run must report its pre-mutation snapshot: %s", out)
	}
	if _, err := store.VerifyBackup(context.Background(), report.Backup); err != nil {
		t.Fatalf("the reported snapshot must verify: %v", err)
	}
	if snapshots := preCutoverUpgradeSnapshots(t, root); len(snapshots) != 2 {
		t.Fatalf("each mutating attempt retains its verified snapshot: %v", snapshots)
	}

	events := cutoverTransitionedEvents(t, path)
	// The log holds the disposition's one transition per selected item plus
	// the fixture's own retained terminal cancellation, nothing else.
	if len(events) != len(cutoverSelectedWorks)+1 {
		t.Fatalf("the log holds %+v, want one transition per selected item %v plus the fixture's terminal cancellation", events, cutoverSelectedWorks)
	}
	if terminal := events[cutoverTerminalHistoryWork]; terminal.From != "needed" || terminal.To != "cancelled" {
		t.Fatalf("the fixture's terminal cancellation = %+v, want the retained needed->cancelled event", terminal)
	}
	for _, workID := range cutoverSelectedWorks {
		transition, ok := events[workID]
		if !ok {
			t.Fatalf("the selected item %s has no work.transitioned event: %+v", workID, events)
		}
		if transition.From != statesBefore[workID].Lifecycle || transition.To != "cancelled" {
			t.Fatalf("%s transitioned %+v, want an ordinary cancellation from %s", workID, transition, statesBefore[workID].Lifecycle)
		}
		if !strings.Contains(transition.Reason, "cutover") {
			t.Fatalf("%s transition reason %q must name the cutover disposition", workID, transition.Reason)
		}
		if transition.ExpectedVersion != statesBefore[workID].Version || transition.ResultingVersion != statesBefore[workID].Version+1 {
			t.Fatalf("%s transition versions = %+v, want %d -> %d", workID, transition, statesBefore[workID].Version, statesBefore[workID].Version+1)
		}
	}
	statesAfter := cutoverWorkStates(t, path)
	for _, workID := range cutoverSelectedWorks {
		state := statesAfter[workID]
		if state.Lifecycle != "cancelled" || state.Version != statesBefore[workID].Version+1 || state.InstanceState != "cancelled" {
			t.Fatalf("%s after the disposition = %+v, want the cancelled lifecycle, the bumped version, and the closed instance", workID, state)
		}
	}
	for _, workID := range cutoverSparedWorks {
		if statesAfter[workID] != statesBefore[workID] {
			t.Fatalf("the spared item %s moved: %+v -> %+v", workID, statesBefore[workID], statesAfter[workID])
		}
	}
	if after := cutoverEventCount(t, path); after != eventsBefore+int64(len(cutoverSelectedWorks)) {
		t.Fatalf("the canceling run moved the event log %d -> %d, want exactly the %d transitions", eventsBefore, after, len(cutoverSelectedWorks))
	}
	if _, err := store.PlanUpgradeReadiness(context.Background(), path); err != nil {
		t.Fatalf("readiness must pass once the selected work is cancelled: %v", err)
	}
}

// The maintenance boundary admits the canceling run like every other
// mutation: with a foreign fence open, the run refuses at the boundary before
// any effect — no snapshot, no events, no lifecycle moves — and the foreign
// boundary stays untouched.
func TestUpgradeWithTheCancelInputRefusesAtTheMaintenanceBoundaryBeforeAnyEffect(t *testing.T) {
	path, root := cliStoreRoot(t)
	seedCutoverPinStore(t, path)
	statesBefore := cutoverWorkStates(t, path)
	eventsBefore := cutoverEventCount(t, path)

	fenceID, err := hostlease.NewFenceID()
	if err != nil {
		t.Fatal(err)
	}
	foreign := hostlease.Fence{
		FenceID:       fenceID,
		Operation:     "operator-foreign-hold",
		ReleaseRoot:   "/releases/cutover-foreign",
		CoreBinary:    "/releases/cutover-foreign/bin/concord",
		SchemaVersion: 1,
		Notice:        "operator-held boundary for the cancel-input refusal probe",
	}
	if _, err := hostlease.EnsureFence(root, foreign); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
	if code != 1 {
		t.Fatalf("a run that cannot claim the boundary must refuse: %d %s", code, errOut)
	}
	if out != "" {
		t.Fatalf("the refused run must print no report: %s", out)
	}
	if !strings.Contains(errOut, "not this binary's own upgrade boundary") {
		t.Fatalf("the refusal must name the boundary admission failure: %s", errOut)
	}
	kept, err := hostlease.ReadFence(root)
	if err != nil || kept == nil || kept.FenceID != fenceID || kept.Operation != foreign.Operation {
		t.Fatalf("the foreign boundary must stay untouched: %+v %v", kept, err)
	}
	if snapshots := preCutoverUpgradeSnapshots(t, root); len(snapshots) != 0 {
		t.Fatalf("no snapshot may precede the boundary admission: %v", snapshots)
	}
	if after := cutoverEventCount(t, path); after != eventsBefore {
		t.Fatalf("the boundary-refused run moved the event log %d -> %d", eventsBefore, after)
	}
	if after := cutoverWorkStates(t, path); !reflect.DeepEqual(after, statesBefore) {
		t.Fatalf("the boundary-refused run moved work state: %+v", after)
	}
}

// A selected item under an active outside-repair hold is a refusal the plan
// can determine read-only: the plan names it as a blocker, and the migrating
// run refuses before fence or snapshot with zero events and zero lifecycle
// change on every item.
func TestUpgradeWithTheCancelInputRefusesAHeldOutsideRepairItemBeforeFenceOrSnapshot(t *testing.T) {
	path, root := cliStoreRoot(t)
	seedCutoverPinStore(t, path)
	holdCutoverWorkUnderOutsideRepair(t, path)
	statesBefore := cutoverWorkStates(t, path)
	eventsBefore := cutoverEventCount(t, path)

	code, out, errOut := runUpgradeStdin(t, `{"plan":true,"cancel_cutover_pinned_work":true}`)
	if code != 0 {
		t.Fatalf("the held-item plan must report its blockers as JSON: %d %s %s", code, out, errOut)
	}
	var report struct {
		store.UpgradeReadiness
		CutoverDisposition *cutoverDispositionReport `json:"cutover_disposition"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the held-item plan output is not JSON: %v: %s", err, out)
	}
	if !report.ActivationBlocked {
		t.Fatalf("a determinable cancellation refusal must block the plan: %+v", report.UpgradeReadiness)
	}
	if !strings.Contains(strings.Join(report.Blockers, "\n"), cutoverHeldWork) {
		t.Fatalf("the plan blockers must name the held item: %+v", report.Blockers)
	}
	if report.CutoverDisposition == nil || report.CutoverDisposition.WorkCount != len(cutoverSelectedWorks)+1 {
		t.Fatalf("the selected held item must remain in the disposition count: %+v", report.CutoverDisposition)
	}

	code, out, errOut = runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
	if code != 1 {
		t.Fatalf("the held item must refuse the migrating run: %d %s", code, errOut)
	}
	if out != "" {
		t.Fatalf("the refused run must print no report: %s", out)
	}
	if !strings.Contains(errOut, cutoverHeldWork) {
		t.Fatalf("the refusal must name the held item and its refusal: %s", errOut)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("the refused run must leave no boundary behind: %+v %v", fence, err)
	}
	if snapshots := preCutoverUpgradeSnapshots(t, root); len(snapshots) != 0 {
		t.Fatalf("the held-item refusal must come before the snapshot: %v", snapshots)
	}
	if after := cutoverEventCount(t, path); after != eventsBefore {
		t.Fatalf("the held-item refusal moved the event log %d -> %d; it must write nothing", eventsBefore, after)
	}
	if after := cutoverWorkStates(t, path); !reflect.DeepEqual(after, statesBefore) {
		t.Fatalf("the held-item refusal moved work state: %+v", after)
	}
}

// genuinePreTailCutoverStore builds the test's source store from the pinned
// released baseline. The v13.4.0 core initializes the real target at its own
// schema 122 — actual physical schema and manifest, no copied DDL — and the
// current-schema cutover fixture is seeded in a separate store, then only
// its rows and events move into the baseline's existing tables.
func genuinePreTailCutoverStore(t *testing.T, path string) {
	t.Helper()
	seedPath := filepath.Join(t.TempDir(), "cutover-seed.db")
	seedCutoverPinStore(t, seedPath)
	baseline := buildCoreFrom(t, extractReleasedSource(t, cutoverBaselineTag), "v0.0.0-con953-baseline-fixture")
	code, out, errText := runRelease(t, baseline, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the released baseline must initialize the fixture store: %d %s %s", code, out, errText)
	}
	var initialized struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(out), &initialized); err != nil {
		t.Fatalf("the baseline upgrade report is not JSON: %v: %s", err, out)
	}
	if initialized.SchemaVersion != cutoverBaselineSchema {
		t.Fatalf("the baseline fixture store's schema = %d, want %d", initialized.SchemaVersion, cutoverBaselineSchema)
	}
	transferCutoverFixture(t, seedPath, path)
}

// transferCutoverFixture moves the seeded fixture's population into the
// baseline store's existing tables: the fixture's own tables in foreign-key
// order, through the columns both schemas share. The baseline's extra
// members, such as products.planning_mode, keep their defaults, and its
// manifest and physical schema stay untouched.
func transferCutoverFixture(t *testing.T, seedPath, targetPath string) {
	t.Helper()
	tables := []string{
		"products", "projects", "product_projects", "work_items", "work_projects",
		"workflow_actors", "workflow_instances", "workflow_contracts", "domain_events",
	}
	seed, err := sql.Open("sqlite", store.SQLiteFileURI(seedPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seed.Close() }()
	seed.SetMaxOpenConns(1)
	target, err := sql.Open("sqlite", store.SQLiteFileURI(targetPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	target.SetMaxOpenConns(1)
	ctx := context.Background()
	readTx, err := seed.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readTx.Rollback() }()
	writeTx, err := target.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writeTx.Rollback() }()
	// The baseline's projections are fold-only, so the transfer writes under
	// the same transient fold guard every fixture writer uses and leaves no
	// guard row behind.
	if _, err := writeTx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		common := sharedColumns(t, readTx, writeTx, table)
		rows, err := readTx.QueryContext(ctx, `SELECT `+strings.Join(common, ",")+` FROM `+table)
		if err != nil {
			t.Fatalf("cannot read the seeded %s rows: %v", table, err)
		}
		insert, err := writeTx.PrepareContext(ctx, `INSERT INTO `+table+`(`+strings.Join(common, ",")+`) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", len(common)), ",")+`)`)
		if err != nil {
			t.Fatalf("cannot prepare the %s transfer: %v", table, err)
		}
		for rows.Next() {
			values := make([]any, len(common))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatalf("cannot scan the seeded %s rows: %v", table, err)
			}
			if _, err := insert.ExecContext(ctx, values...); err != nil {
				t.Fatalf("cannot transfer the seeded %s rows: %v", table, err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("cannot read the seeded %s rows: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if err := insert.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeTx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := writeTx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// sharedColumns returns the columns both schemas hold, in the target's
// order: the transfer writes only values the historical table stores, so
// the baseline's own members keep their recorded shapes and defaults.
func sharedColumns(t *testing.T, readTx, writeTx *sql.Tx, table string) []string {
	t.Helper()
	columns := func(tx *sql.Tx) []string {
		rows, err := tx.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("cannot inspect %s columns: %v", table, err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return names
	}
	target := columns(writeTx)
	held := make(map[string]bool)
	for _, name := range columns(readTx) {
		held[name] = true
	}
	var common []string
	for _, name := range target {
		if held[name] {
			common = append(common, name)
		}
	}
	return common
}

func TestUpgradeCutoverPreCommitFailureClosesFenceWithoutClaimingCommit(t *testing.T) {
	for _, stamp := range []string{version.Development, "v-test"} {
		t.Run(stamp, func(t *testing.T) {
			previous := version.Value
			version.Value = stamp
			t.Cleanup(func() { version.Value = previous })
			path, root := cliStoreRoot(t)
			genuinePreTailCutoverStore(t, path)
			if stamp != version.Development {
				// The stamped case's static fixture state is the source's own
				// file mode: an owner-read-only database leaves qualification
				// staging (a separately created writable copy) untouched while
				// the live store's own migration refuses to write. The mode
				// restores before the temporary tree goes away.
				if err := os.Chmod(path, 0o400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			}
			statesBefore := cutoverWorkStates(t, path)
			eventsBefore := cutoverEventCount(t, path)
			options := store.UpgradeOptions{CancelCutoverPinnedWork: true}
			before, err := store.PlanUpgradeReadiness(context.Background(), path, options)
			if err != nil || before.SchemaVersion != 122 || len(before.PendingBreaking) == 0 {
				t.Fatalf("pending breaking fixture = %+v, %v", before, err)
			}
			code, out, errOut := runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
			if code != 1 || out != "" {
				t.Fatalf("pre-commit failure = %d, %s, %s", code, out, errOut)
			}
			if strings.Contains(errOut, "the incompatible migration committed before this failure") {
				t.Fatalf("a pre-commit failure must not claim a breaking commit: %s", errOut)
			}
			if stamp != version.Development {
				// The refusal must come from the source's own migration, not
				// from snapshot qualification of the staged copy.
				if strings.Contains(errOut, "database snapshot or verification failed") {
					t.Fatalf("the read-only source must refuse the live migration, not the snapshot: %s", errOut)
				}
				if !strings.Contains(strings.ToLower(errOut), "readonly") {
					t.Fatalf("the stamped refusal must name the read-only source migration: %s", errOut)
				}
			}
			if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
				t.Fatalf("pre-commit failure must close its own fence: %+v, %v", fence, err)
			}
			if stamp == version.Development {
				// An unstamped build's pending tail refuses snapshot
				// qualification and leaves no artifacts.
				if !strings.Contains(errOut, "unstamped development build refuses schema migration for an existing store") {
					t.Fatalf("the dev run must refuse at the unstamped qualification: %s", errOut)
				}
				if !strings.Contains(errOut, unstampedRecoveryLine) {
					t.Fatalf("the snapshot refusal must name the recovery: %s", errOut)
				}
				if snapshots := preCutoverUpgradeSnapshots(t, root); len(snapshots) != 0 {
					t.Fatalf("the unstamped qualification refusal retains no snapshot: %v", snapshots)
				}
				if manifests := preCutoverUpgradeManifests(t, root); len(manifests) != 0 {
					t.Fatalf("the unstamped qualification refusal retains no manifest: %v", manifests)
				}
			} else {
				if snapshots := preCutoverUpgradeSnapshots(t, root); len(snapshots) != 1 {
					t.Fatalf("pre-commit failure must retain the pre-mutation snapshot: %v", snapshots)
				} else if _, err := store.VerifyBackup(context.Background(), snapshots[0]); err != nil {
					t.Fatal(err)
				}
			}
			after, err := store.PlanUpgradeReadiness(context.Background(), path, options)
			if err != nil || after.SchemaVersion != before.SchemaVersion || !reflect.DeepEqual(after.AppliedVersions, before.AppliedVersions) {
				t.Fatalf("pre-commit failure moved schema: %+v -> %+v, %v", before, after, err)
			}
			if cutoverEventCount(t, path) != eventsBefore || !reflect.DeepEqual(cutoverWorkStates(t, path), statesBefore) {
				t.Fatal("pre-commit failure moved the event log or work state")
			}
		})
	}
}
