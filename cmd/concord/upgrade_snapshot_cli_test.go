package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func snapshotUpgradeFixture(t *testing.T) (string, string, store.UpgradeReadiness) {
	t.Helper()
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || len(plan.PendingBreaking) == 0 {
		t.Fatalf("fixture must have pending breaking steps: %+v %v", plan, err)
	}
	return path, root, plan
}

func TestUpgradeSnapshotPrecedesBreakingMigration(t *testing.T) {
	path, root, before := snapshotUpgradeFixture(t)
	backup := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = backup })
	observed := false
	snapshotDatabase = func(ctx context.Context, source, destination string) (store.BackupManifest, error) {
		observed = true
		if fence, err := hostlease.ReadFence(root); err != nil || fence == nil {
			t.Errorf("snapshot must follow admission exclusion: %+v %v", fence, err)
		}
		current, err := store.PlanUpgradeReadiness(ctx, source)
		if err != nil || !reflect.DeepEqual(current.AppliedVersions, before.AppliedVersions) {
			t.Errorf("snapshot started after a migration: %+v %v", current, err)
		}
		return backup(ctx, source, destination)
	}
	code, out, errOut := runUpgradeStdin(t, `{}`)
	if code != 0 || !observed {
		t.Fatalf("upgrade must invoke snapshot owner before migrating: %d observed=%v %s", code, observed, errOut)
	}
	var report struct {
		Backup string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("upgrade must emit one JSON report: %v: %s", err, out)
	}
	manifest, err := store.VerifyBackup(context.Background(), report.Backup)
	if err != nil || manifest.SchemaVersion != before.SchemaVersion {
		t.Fatalf("snapshot must preserve pre-upgrade schema: %+v %v", manifest, err)
	}
	after, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || len(after.PendingBreaking) != 0 {
		t.Fatalf("upgrade did not apply the breaking tail: %+v %v", after, err)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence == nil {
		t.Fatalf("breaking commit must retain admission exclusion: %+v %v", fence, err)
	}
}

func TestUpgradeSnapshotFailureCommitsNothingAndClosesOwnedFence(t *testing.T) {
	for _, failure := range []string{"destination", "verification"} {
		t.Run(failure, func(t *testing.T) {
			path, root, before := snapshotUpgradeFixture(t)
			var failures []string
			if failure == "destination" {
				if err := os.WriteFile(filepath.Join(root, "backups"), []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				// Readiness accepts the schema, but backup verification rejects its foreign keys.
				failures = seedSnapshotForeignKeyCorruption(t, path, "fold_guard")
			}
			code, _, errOut := runUpgradeStdin(t, `{}`)
			if code != 1 {
				t.Fatalf("snapshot failure must refuse upgrade: code=%d error=%s", code, errOut)
			}
			if !strings.Contains(errOut, "snapshot") || !strings.Contains(errOut, filepath.Join(root, "backups", "pre-upgrade-")) {
				t.Errorf("refusal must name the snapshot cause and path: %s", errOut)
			}
			if failure == "verification" {
				assertSnapshotIntegrityAttribution(t, errOut, failures)
				assertNoQualifiedSnapshot(t, root)
			}
			after, err := store.PlanUpgradeReadiness(context.Background(), path)
			if err != nil || !reflect.DeepEqual(after.AppliedVersions, before.AppliedVersions) {
				t.Errorf("snapshot failure changed schema: before=%+v after=%+v error=%v", before, after, err)
			}
			if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
				t.Errorf("snapshot failure must close the fence this run opened: %+v %v", fence, err)
			}
		})
	}
}

func TestUpgradeSnapshotPlanReportsRequiredWithoutCreatingOne(t *testing.T) {
	path, root, before := snapshotUpgradeFixture(t)
	code, out, errOut := runUpgradeStdin(t, `{"plan":true}`)
	if code != 0 {
		t.Fatalf("plan = %d: %s", code, errOut)
	}
	var report struct {
		SnapshotRequired *bool `json:"snapshot_required"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.SnapshotRequired == nil || !*report.SnapshotRequired {
		t.Errorf("plan must report snapshot requirement: %+v %v", report, err)
	}
	assertNoUpgradeSnapshot(t, root)
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("plan must not open a fence: %+v %v", fence, err)
	}
	after, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || !reflect.DeepEqual(after.AppliedVersions, before.AppliedVersions) {
		t.Fatalf("plan changed schema: %+v %v", after, err)
	}
}

func TestUpgradeSnapshotSkippedWithoutPendingBreakingSteps(t *testing.T) {
	for _, state := range []string{"absent", "current"} {
		t.Run(state, func(t *testing.T) {
			path, root := cliStoreRoot(t)
			if state == "current" {
				s, err := store.Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			code, out, errOut := runUpgradeStdin(t, `{"plan":true}`)
			var plan struct {
				SnapshotRequired *bool `json:"snapshot_required"`
			}
			if code != 0 || json.Unmarshal([]byte(out), &plan) != nil || plan.SnapshotRequired == nil || *plan.SnapshotRequired {
				t.Errorf("non-breaking plan: code=%d report=%s error=%s", code, out, errOut)
			}
			code, out, errOut = runUpgradeStdin(t, `{}`)
			if code != 0 || strings.Contains(out, `"backup"`) {
				t.Fatalf("non-breaking upgrade: code=%d report=%s error=%s", code, out, errOut)
			}
			assertNoUpgradeSnapshot(t, root)
		})
	}
}

func assertNoUpgradeSnapshot(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
		t.Fatalf("refused or non-breaking run created a backup directory: %v", err)
	}
}

func TestUpgradeSnapshotFailureKeepsAdoptedFence(t *testing.T) {
	path, root, before := snapshotUpgradeFixture(t)
	releaseRoot, binary, err := selfRelease()
	if err != nil {
		t.Fatal(err)
	}
	fence, err := hostlease.EnsureFence(root, hostlease.Fence{
		Operation: hostlease.FenceOperationUpgrade, ReleaseRoot: releaseRoot,
		CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(), Notice: "activation required",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backups"), []byte("blocked snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 || !strings.Contains(errOut, "snapshot") {
		t.Errorf("snapshot failure = %d: %s", code, errOut)
	}
	afterFence, err := hostlease.ReadFence(root)
	if err != nil || !reflect.DeepEqual(afterFence, &fence) {
		t.Fatalf("snapshot failure changed an adopted fence: before=%+v after=%+v error=%v", fence, afterFence, err)
	}
	after, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || !reflect.DeepEqual(after.AppliedVersions, before.AppliedVersions) {
		t.Fatalf("snapshot failure changed schema: %+v %v", after, err)
	}
}

func TestUpgradeSnapshotWaitsForFinalSessionChecks(t *testing.T) {
	for _, protocol := range []int{0, hostlease.CurrentFenceProtocol} {
		t.Run(pidJSON(protocol), func(t *testing.T) {
			path, root, before := snapshotUpgradeFixture(t)
			_, binary, err := selfRelease()
			if err != nil {
				t.Fatal(err)
			}
			start, err := hostlease.ProcessStart(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if err := hostlease.Write(root, hostlease.Lease{
				PID: os.Getpid(), PidStart: start, CoreBinary: binary,
				ReleaseRoot: "/releases/older", SchemaVersion: before.SchemaVersion,
				FenceProtocol: protocol, RecordedAt: "2026-01-01T00:00:00Z",
			}); err != nil {
				t.Fatal(err)
			}
			code, _, errOut := runUpgradeStdin(t, `{}`)
			if code != 1 || !strings.Contains(errOut, "session") {
				t.Fatalf("older session must refuse before snapshot: %d %s", code, errOut)
			}
			assertNoUpgradeSnapshot(t, root)
			after, err := store.PlanUpgradeReadiness(context.Background(), path)
			if err != nil || !reflect.DeepEqual(after.AppliedVersions, before.AppliedVersions) {
				t.Fatalf("session refusal changed schema: %+v %v", after, err)
			}
		})
	}
}

func TestUpgradeSnapshotRefusesExistingEmptyDatabase(t *testing.T) {
	path, root := cliStoreRoot(t)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 || !strings.Contains(errOut, "snapshot") {
		t.Errorf("only an absent source is exempt from snapshot verification: %d %s", code, errOut)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || len(plan.AppliedVersions) != 0 {
		t.Errorf("empty database snapshot refusal applied migrations: %+v %v", plan, err)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("empty database refusal retained its owned fence: %+v %v", fence, err)
	}
}
