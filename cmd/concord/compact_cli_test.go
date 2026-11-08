package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func TestCompactCompletesAndReleasesFence(t *testing.T) {
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE compact_fixture(value); INSERT INTO compact_fixture(rowid,value) VALUES(7,'keep'),(9,zeroblob(1000000)); DELETE FROM compact_fixture WHERE rowid=9`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	original := compactDatabase
	t.Cleanup(func() { compactDatabase = original })
	compactDatabase = func(ctx context.Context, path, snapshot string) (store.CompactReport, error) {
		if _, err := os.Stat(snapshot + compactHoldSuffix); err != nil {
			t.Errorf("hold absent before write-back: %v", err)
		}
		if fence, err := hostlease.ReadFence(root); err != nil || fence == nil || fence.Operation != hostlease.FenceOperationCompact {
			t.Errorf("fence absent before write-back: %+v, %v", fence, err)
		}
		return original(ctx, path, snapshot)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 0 {
		t.Fatalf("compact = %d, want success: %s", code, errOut.String())
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("compact must release its fence: %+v, %v", fence, err)
	}
	var report struct {
		Snapshot string              `json:"snapshot"`
		Hold     string              `json:"hold"`
		Compact  store.CompactReport `json:"compact"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Compact.Before.FreePages == 0 || report.Compact.After.FreePages != 0 || !report.Compact.WriteBackStarted {
		t.Fatalf("report = %+v", report)
	}
	if report.Hold != report.Snapshot+compactHoldSuffix {
		t.Fatalf("hold = %q snapshot = %q", report.Hold, report.Snapshot)
	}
	if _, err := os.Stat(report.Hold); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.VerifyBackup(context.Background(), report.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(report.Hold)
	if err != nil {
		t.Fatal(err)
	}
	var hold compactHold
	if err := json.Unmarshal(data, &hold); err != nil || hold.Snapshot != filepath.Base(report.Snapshot) || hold.SnapshotID != manifest.SnapshotID || hold.Checksum != manifest.DBChecksum {
		t.Fatalf("snapshot hold = %+v: %v", hold, err)
	}
}

func TestCompactRetainsFenceOnPostWriteFailure(t *testing.T) {
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	original := compactDatabase
	t.Cleanup(func() { compactDatabase = original })
	compactDatabase = func(ctx context.Context, path, snapshot string) (store.CompactReport, error) {
		report, err := original(ctx, path, snapshot)
		if err != nil {
			return report, err
		}
		report.WriteBackStarted = true
		return report, fmt.Errorf("injected post-write verification failure")
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 1 {
		t.Fatalf("compact = %d: %s", code, errOut.String())
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence == nil || fence.Operation != hostlease.FenceOperationCompact {
		t.Fatalf("retained fence = %+v: %v", fence, err)
	}
	if !strings.Contains(errOut.String(), "hold:") || !strings.Contains(errOut.String(), "fence retained: true") {
		t.Fatalf("diagnostic = %s", errOut.String())
	}
}

func TestCompactSnapshotFailureClosesFenceBeforeMutation(t *testing.T) {
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original := snapshotDatabase
	t.Cleanup(func() { snapshotDatabase = original })
	snapshotDatabase = func(context.Context, string, string) (store.BackupManifest, error) {
		return store.BackupManifest{}, fmt.Errorf("injected snapshot refusal")
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 1 {
		t.Fatalf("compact = %d: %s", code, errOut.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("source changed: %v", err)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence != nil {
		t.Fatalf("fence = %+v: %v", fence, err)
	}
}

func TestCompactDoesNotMigrateSource(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, _ := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	before, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 0 {
		t.Fatalf("compact = %d: %s", code, errOut.String())
	}
	after, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || !reflect.DeepEqual(before.AppliedVersions, after.AppliedVersions) {
		t.Fatalf("migrations changed: before=%v after=%v err=%v", before.AppliedVersions, after.AppliedVersions, err)
	}
}

func TestCompactRefusesBusyForeignFenceAndLiveLease(t *testing.T) {
	for _, reason := range []string{"busy", "fence", "lease", "legacy"} {
		t.Run(reason, func(t *testing.T) {
			path, root := cliStoreRoot(t)
			s, err := store.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "busy":
				release, err := hostlease.AcquireMaintenance(root)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "fence":
				_, err := hostlease.EnsureFence(root, hostlease.Fence{FenceID: "foreign", Operation: "foreign"})
				if err != nil {
					t.Fatal(err)
				}
			case "lease", "legacy":
				start, err := hostlease.ProcessStart(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				releaseRoot, binary, err := selfRelease()
				if err != nil {
					t.Fatal(err)
				}
				protocol := hostlease.CurrentFenceProtocol
				if reason == "legacy" {
					protocol = 0
				}
				if err := hostlease.Write(root, hostlease.Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: releaseRoot, CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(), FenceProtocol: protocol}); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 1 {
				t.Fatalf("%s compact = %d: %s", reason, code, errOut.String())
			}
			if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
				t.Fatalf("%s created snapshot: %v", reason, err)
			}
			fence, err := hostlease.ReadFence(root)
			if err != nil || (reason == "fence" && (fence == nil || fence.FenceID != "foreign")) || (reason != "fence" && fence != nil) {
				t.Fatalf("%s fence = %+v: %v", reason, fence, err)
			}
		})
	}
}

func TestCompactUsesResolvedStoreRootForLeaseFence(t *testing.T) {
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "linked.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, alias)
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	releaseRoot, binary, err := selfRelease()
	if err != nil {
		t.Fatal(err)
	}
	if err := hostlease.Write(root, hostlease.Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: releaseRoot, CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(), FenceProtocol: hostlease.CurrentFenceProtocol}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 1 {
		t.Fatalf("compact = %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
		t.Fatalf("snapshot created despite live lease: %v", err)
	}
}

func TestCompactRefusesUnfenceableReleaseTree(t *testing.T) {
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "legacy", "bin")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "concord"), []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"compact"}, strings.NewReader(`{}`), &out, &errOut); code != 1 {
		t.Fatalf("compact = %d: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "cannot be excluded") || strings.Contains(errOut.String(), "confirm_sessions_stopped") {
		t.Fatalf("diagnostic = %s", errOut.String())
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence != nil {
		t.Fatalf("fence = %+v: %v", fence, err)
	}
}
