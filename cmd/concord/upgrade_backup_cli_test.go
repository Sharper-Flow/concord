package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

// A breaking step drops data the event log cannot rebuild, so the upgrade
// writes a verified snapshot of the unmigrated store before it applies and
// names it in the report.
func TestUpgradeSnapshotsTheStoreBeforeABreakingStep(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)

	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("the repaired breaking tail must apply: %d %s", code, errOut)
	}
	var report struct {
		Applied []int  `json:"applied"`
		Backup  string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("upgrade output is not JSON: %v: %s", err, out)
	}
	if !strings.HasPrefix(report.Backup, filepath.Join(root, "backups")+string(filepath.Separator)) {
		t.Fatalf("the report must name a snapshot under the data root: %+v", report)
	}
	manifest, err := store.VerifyBackup(context.Background(), report.Backup)
	if err != nil {
		t.Fatalf("the snapshot must verify: %v", err)
	}
	if manifest.SchemaVersion >= 111 {
		t.Fatalf("the snapshot must hold the unmigrated store, got schema %d", manifest.SchemaVersion)
	}
	assertSnapshotLacksMigration(t, report.Backup, 111)
}

// The snapshot precedes the first migration, so a migration that fails still
// leaves the pre-upgrade copy behind.
func TestUpgradeKeepsTheSnapshotWhenTheBreakingStepFails(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		manifest, err := backup(ctx, source, destination)
		if err != nil {
			return manifest, err
		}
		// The rollback snapshot must be restorable. A source-only collision
		// after verification exercises a later live migration failure.
		db, err := sql.Open("sqlite", "file:"+source)
		if err != nil {
			return manifest, err
		}
		_, err = db.ExecContext(ctx, `ALTER TABLE worktree_verify_leases ADD COLUMN native_plan_json TEXT NOT NULL DEFAULT ''`)
		if closeErr := db.Close(); err == nil {
			err = closeErr
		}
		return manifest, err
	}

	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("a colliding migration must fail the upgrade: %d %s", code, errOut)
	}
	if !strings.Contains(errOut, "duplicate column name: native_plan_json") {
		t.Fatalf("the verified snapshot must precede the source-only migration collision: %s", errOut)
	}
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil {
		t.Fatalf("the backup directory must exist: %v", err)
	}
	var snapshots []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "pre-upgrade-") && !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") {
			snapshots = append(snapshots, name)
		}
	}
	if len(snapshots) != 1 {
		t.Fatalf("exactly one pre-upgrade snapshot must remain: %v", entries)
	}
	snapshot := filepath.Join(root, "backups", snapshots[0])
	if !strings.Contains(errOut, snapshot) {
		t.Errorf("migration failure must report retained snapshot %s: %s", snapshot, errOut)
	}
	if _, err := store.VerifyBackup(context.Background(), snapshot); err != nil {
		t.Fatalf("the snapshot must verify: %v", err)
	}
	assertSnapshotLacksMigration(t, snapshot, 111)
}

func assertSnapshotLacksMigration(t *testing.T, snapshot string, migration int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+snapshot+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var applied int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=?`, migration).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("the snapshot already carries migration %d", migration)
	}
}
