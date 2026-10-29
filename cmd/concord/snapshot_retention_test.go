package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

type retentionFixture struct {
	root, source string
	database     []byte
	manifest     store.BackupManifest
	now          time.Time
}

func newRetentionFixture(t *testing.T) retentionFixture {
	t.Helper()
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	source, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	manifest, err := store.BackupDatabase(context.Background(), source, seed)
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	return retentionFixture{root, source, database, manifest, time.Now().UTC()}
}

func (f retentionFixture) snapshot(t *testing.T, ordinal int, age time.Duration) string {
	t.Helper()
	created := f.now.Add(-age)
	name := fmt.Sprintf("pre-repair-v-test-%s", created.Format("20060102T150405.000000000Z"))
	path := filepath.Join(f.root, "backups", name)
	manifest := f.manifest
	manifest.CreatedAt = created.Format(time.RFC3339Nano)
	manifest.SnapshotID = fmt.Sprintf("snapshot-%d", ordinal)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f.database, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".manifest.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireSnapshot(t *testing.T, path string, present bool) {
	t.Helper()
	for _, member := range []string{path, path + ".manifest.json"} {
		_, err := os.Lstat(member)
		if present && err != nil {
			t.Errorf("retained %s: %v", member, err)
		}
		if !present && !os.IsNotExist(err) {
			t.Errorf("deleted %s still exists: %v", member, err)
		}
	}
}

func TestManagedSnapshotRetentionCountAndAge(t *testing.T) {
	f := newRetentionFixture(t)
	if err := os.Mkdir(filepath.Join(f.root, ".concord-transactions"), 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 0; i < 10; i++ {
		paths = append(paths, f.snapshot(t, i, time.Duration(i)*time.Hour))
	}
	old := f.snapshot(t, 20, 31*24*time.Hour)
	report, err := pruneManagedSnapshots(f.root, paths[0], f.now)
	if err != nil || report.DeferredReason != "" || len(report.Deleted) != 3 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	for i, path := range paths {
		requireSnapshot(t, path, i < 8)
	}
	requireSnapshot(t, old, false)
}

func TestManagedSnapshotRetentionNewestExemptFromAge(t *testing.T) {
	f := newRetentionFixture(t)
	newest := f.snapshot(t, 1, 31*24*time.Hour)
	older := f.snapshot(t, 2, 32*24*time.Hour)
	report, err := pruneManagedSnapshots(f.root, newest, f.now)
	if err != nil || report.ProtectedExcess != 1 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	requireSnapshot(t, newest, true)
	requireSnapshot(t, older, false)
}

func TestManagedSnapshotRetentionRecoveryExclusions(t *testing.T) {
	for _, marker := range []string{"maintenance.json", "prepared-release.json", ".concord-transactions/pending/journal.json", ".concord-restore-interrupted", "backups/.concord-restore-interrupted"} {
		t.Run(marker, func(t *testing.T) {
			f := newRetentionFixture(t)
			newest := f.snapshot(t, 0, 0)
			var old []string
			for i := 1; i <= 10; i++ {
				old = append(old, f.snapshot(t, i, time.Duration(31+i)*24*time.Hour))
			}
			path := filepath.Join(f.root, marker)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			// Even malformed/unknown recovery records must exclude cleanup.
			if err := os.WriteFile(path, []byte("unfinished"), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := pruneManagedSnapshots(f.root, newest, f.now)
			if err != nil || report.DeferredReason == "" || len(report.Deleted) != 0 || report.ProtectedExcess != 10 {
				t.Fatalf("prune = %+v, %v", report, err)
			}
			for _, path := range append(old, newest) {
				requireSnapshot(t, path, true)
			}
		})
	}
}

func TestManagedSnapshotRetentionLeavesForeignAndUnverifiedPairs(t *testing.T) {
	f := newRetentionFixture(t)
	newest := f.snapshot(t, 0, 0)
	corrupt := f.snapshot(t, 1, 40*24*time.Hour)
	if err := os.WriteFile(corrupt, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(f.root, "backups", "exported-database")
	if _, err := store.BackupDatabase(context.Background(), f.source, foreign); err != nil {
		t.Fatal(err)
	}
	symlink := f.snapshot(t, 2, 41*24*time.Hour)
	if err := os.Remove(symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, symlink); err != nil {
		t.Fatal(err)
	}
	manifestLink := f.snapshot(t, 3, 42*24*time.Hour)
	if err := os.Remove(manifestLink + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign+".manifest.json", manifestLink+".manifest.json"); err != nil {
		t.Fatal(err)
	}
	orphan := f.snapshot(t, 4, 43*24*time.Hour)
	if err := os.Remove(orphan + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	sidecarLink := f.snapshot(t, 5, 44*24*time.Hour)
	if err := os.Symlink(foreign, sidecarLink+"-wal"); err != nil {
		t.Fatal(err)
	}
	pendingWAL := f.snapshot(t, 6, 45*24*time.Hour)
	if err := os.WriteFile(pendingWAL+"-wal", []byte("uncheckpointed"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := pruneManagedSnapshots(f.root, newest, f.now)
	if err != nil || len(report.Deleted) != 0 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	for _, path := range []string{newest, corrupt, foreign, symlink, manifestLink, sidecarLink, pendingWAL} {
		requireSnapshot(t, path, true)
	}
	if _, err := os.Lstat(orphan); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSnapshotRetentionRequiresVerifiedNewSnapshot(t *testing.T) {
	f := newRetentionFixture(t)
	old := f.snapshot(t, 1, 40*24*time.Hour)
	newest := f.snapshot(t, 2, 0)
	if err := os.WriteFile(newest, []byte("failed candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneManagedSnapshots(f.root, newest, f.now); err == nil {
		t.Fatal("unverified new snapshot authorized cleanup")
	}
	requireSnapshot(t, old, true)
}

func TestManagedSnapshotRetentionStableCreationOrder(t *testing.T) {
	f := newRetentionFixture(t)
	var paths []string
	for i := 0; i < 9; i++ {
		path := f.snapshot(t, i, time.Duration(i)*time.Hour)
		raw, err := os.ReadFile(path + ".manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		var manifest store.BackupManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.CreatedAt = f.now.Format(time.RFC3339Nano)
		raw, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".manifest.json", raw, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	report, err := pruneManagedSnapshots(f.root, paths[0], f.now)
	if err != nil || len(report.Deleted) != 1 || report.Deleted[0] != paths[8] {
		t.Fatalf("stable order = %+v, %v", report, err)
	}
}

func TestUpgradeSnapshotRetentionDefersUnderRetainedFence(t *testing.T) {
	_, root, _ := snapshotUpgradeFixture(t)
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 {
		t.Fatalf("upgrade = %d: %s", code, errOut)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence == nil {
		t.Fatalf("fence = %+v, %v", fence, err)
	}
	var report struct {
		SnapshotRetention struct {
			DeferredReason string `json:"deferred_reason"`
		} `json:"snapshot_retention"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || !strings.Contains(report.SnapshotRetention.DeferredReason, hostlease.FencePath(root)) {
		t.Fatalf("missing structured exclusion reason: %s; %v", out, err)
	}
}

func TestUpgradeSucceedsWhenSnapshotRetentionCannotRun(t *testing.T) {
	_, root, _ := snapshotUpgradeFixture(t)
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		manifest, err := backup(ctx, source, destination)
		if err != nil {
			return manifest, err
		}
		backups := filepath.Dir(destination)
		if err := os.Rename(backups, backups+"-held"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(backups+"-held", backups); err != nil {
			t.Fatal(err)
		}
		return manifest, nil
	}
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 || !strings.Contains(errOut, "upgrade finished; snapshot retention failed") {
		t.Fatalf("upgrade = %d: %s; %s", code, out, errOut)
	}
	var report struct {
		SnapshotRetention struct {
			Error string `json:"error"`
		} `json:"snapshot_retention"`
		Maintenance struct {
			FenceID           string `json:"fence_id"`
			ActivationCommand string `json:"activation_command"`
		} `json:"maintenance"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.SnapshotRetention.Error == "" || report.Maintenance.FenceID == "" || report.Maintenance.ActivationCommand == "" {
		t.Fatalf("missing retention error or maintenance block: %s; %v", out, err)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence == nil || fence.FenceID != report.Maintenance.FenceID {
		t.Fatalf("maintenance fence = %+v: %v", fence, err)
	}
}

func TestManagedSnapshotRetentionKeepsNewSnapshotUnderClockSkew(t *testing.T) {
	f := newRetentionFixture(t)
	var future []string
	for i := 1; i <= managedSnapshotCount; i++ {
		future = append(future, f.snapshot(t, i, -time.Duration(managedSnapshotCount+1-i)*time.Hour))
	}
	newest := f.snapshot(t, 0, 0)
	report, err := pruneManagedSnapshots(f.root, newest, f.now)
	if err != nil || len(report.Deleted) != 0 || report.ProtectedExcess != 1 {
		t.Fatalf("prune = %+v, %v", report, err)
	}
	for _, path := range append(future, newest) {
		requireSnapshot(t, path, true)
	}
}

func TestManagedSnapshotRestoreThroughSymlinkedAncestor(t *testing.T) {
	f := newRetentionFixture(t)
	source := f.snapshot(t, 1, 40*24*time.Hour)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(f.root), alias); err != nil {
		t.Fatal(err)
	}
	aliased := filepath.Join(alias, filepath.Base(f.root), "backups", filepath.Base(source))
	restore := func(destination string) (int, string) {
		raw, err := json.Marshal(map[string]string{"source": aliased, "destination": destination})
		if err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		return runWithInput([]string{"restore"}, bytes.NewReader(raw), &out, &errOut), errOut.String()
	}
	release, err := hostlease.AcquireMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if code, errOut := restore(filepath.Join(t.TempDir(), "held.db")); code != 1 || !strings.Contains(errOut, "held") {
		release()
		t.Fatalf("aliased restore under retention lock = %d: %s", code, errOut)
	}
	release()
	if code, errOut := restore(filepath.Join(t.TempDir(), "restored.db")); code != 0 {
		t.Fatalf("aliased restore = %d: %s", code, errOut)
	}
}

func TestManagedSnapshotRestoreSharesMaintenanceExclusion(t *testing.T) {
	f := newRetentionFixture(t)
	source := f.snapshot(t, 1, 40*24*time.Hour)
	release, err := hostlease.AcquireMaintenance(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	destination := filepath.Join(t.TempDir(), "restored.db")
	raw, err := json.Marshal(map[string]string{"source": source, "destination": destination})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"restore"}, bytes.NewReader(raw), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "held") {
		t.Fatalf("restore under retention lock = %d: %s", code, &errOut)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("restore raced cleanup: %v", err)
	}
}
