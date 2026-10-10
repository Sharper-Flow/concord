package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 111`); err != nil {
		t.Fatalf("cannot remove the manifest tail: %v", err)
	}
	if unpoison {
		for _, statement := range []string{
			`DROP TABLE outside_repair_reconciliations`,
			`DROP TABLE outside_repair_dispositions`,
			// Restore the pre-117 column set and CHECK while retaining every row.
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
			`DROP TABLE product_knowledge_sources`,
			`DROP TABLE law_cross_source_relations`,
			`ALTER TABLE workflow_proposal_records DROP COLUMN out_of_scope`,
			`DROP TABLE project_handoffs`,
			`DROP TABLE worker_job_revisions`,
			`DROP TABLE durability_commits`,
			`DROP TABLE runtime_state_writers`,
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
			// Migration 122's guarded projection is one plain CREATE TABLE:
			// its guard triggers and index are owned by the table and drop
			// with it.
			`DROP TABLE IF EXISTS worktree_ref_outcomes`,
			`DROP TRIGGER IF EXISTS active_research_packs_retirement_delete_guard`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("cannot unpoison %q: %v", statement, err)
			}
		}
	}
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// The strand must survive the migration itself: migration 124 ends by
	// clearing fold_guard, so a work item without its required membership
	// strands finishOpen instead — no tail step repairs a membership
	// invariant, and the committed store still refuses every open. The
	// fold-only guard needs a transiently active fold to accept the row,
	// and the strand keeps no fold_guard row behind.
	for _, statement := range []string{
		`INSERT INTO fold_guard(active) VALUES(1)`,
		`INSERT INTO work_items (id, kind, title, lifecycle, priority, version, created_at, updated_at) VALUES ('finish-open-strand-probe', 'task', 'finishOpen refusal probe', 'needed', 0, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		`DELETE FROM fold_guard`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("cannot strand the membership invariants with %q: %v", statement, err)
		}
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
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
