package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
	"github.com/sharper-flow/concord/internal/version"
)

const unstampedRecoveryLine = "recovery: set CONCORD_DB_PATH to a fresh path outside any repository, then retry"

func useUnstampedBuild(t *testing.T) {
	t.Helper()
	previous := version.Value
	version.Value = version.Development
	t.Cleanup(func() { version.Value = previous })
}

// unstampedTailStore writes a migrated store into dir and removes the newest
// manifest row, so an unstamped build finds one pending additive migration.
// The refusal fires before any migration, so the objects that row created
// never matter.
func unstampedTailStore(t *testing.T, dir, name string) string {
	t.Helper()
	if err := storetest.WriteMigratedDatabase(dir, name); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	removeNewestManifestRow(t, path)
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || len(plan.Pending) != 1 {
		t.Fatalf("tail fixture plan = %+v, %v; want one pending migration", plan, err)
	}
	if len(plan.PendingBreaking) != 0 {
		t.Skip("the newest migration is breaking; the additive-tail fixture does not exist")
	}
	return path
}

func removeNewestManifestRow(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = (SELECT max(version) FROM schema_migrations)`); err != nil {
		t.Fatalf("cannot remove the newest manifest row: %v", err)
	}
}

// installRelease links dataRoot/current to dataRoot/<release>, the layout the
// installer activates.
func installRelease(t *testing.T, dataRoot, release string) {
	t.Helper()
	target := filepath.Join(dataRoot, release)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dataRoot, "current")); err != nil {
		t.Fatal(err)
	}
}

const unstampedRefusalLead = "unstamped development build refuses schema migration for an existing store"

func assertOwnedRefusal(t *testing.T, message, lead, dataRoot, release string) {
	t.Helper()
	for _, want := range []string{
		lead,
		"owning release: " + release + " (installed release at " + filepath.Join(dataRoot, "current") + "; installer record, not a migration-writer record)",
		unstampedRecoveryLine,
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("refusal %q must contain %q", message, want)
		}
	}
}

func unstampedRefusal() *store.Failure {
	return &store.Failure{Kind: store.KindInvalidOperation, Op: "open", RetrySafe: true,
		Detail:             "unstamped development build refuses schema migration for an existing store at schema version 126",
		RecoveryAction:     "set CONCORD_DB_PATH to a fresh path outside any repository, then retry",
		UnstampedMigration: &store.UnstampedMigrationRefusal{SchemaVersion: 126, FirstPending: 127, Pending: 1}}
}

// The owner is the installer's record for the data root, labeled as such;
// a missing link is "none found", and anything the installer layout does not
// explain is "unknown" with its cause. The recovery line is always present.
func TestUnstampedRefusalDiagnosticNamesTheInstalledRelease(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, root string)
		want    string
	}{
		{"installed", func(t *testing.T, root string) { installRelease(t, root, "v9.9.9") },
			"owning release: v9.9.9 (installed release at %s/current; installer record, not a migration-writer record)"},
		{"absent", func(*testing.T, string) {},
			"owning release: none found (%s/current does not exist; no installed release manages this data root)"},
		{"regular file", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "current"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "owning release: unknown (%s/current: not a symlink)"},
		{"outside the layout", func(t *testing.T, root string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
		}, "owning release: unknown (%s/current: points outside the installer layout: "},
		{"dangling", func(t *testing.T, root string) {
			if err := os.Symlink(filepath.Join(root, "v0.0.0"), filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
		}, "owning release: unknown (%s/current: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.prepare(t, root)
			refusal := unstampedRefusal()
			err := unstampedRefusalDiagnostic(refusal, root)
			message := err.Error()
			for _, want := range []string{refusal.Error(), fmt.Sprintf(tc.want, root), unstampedRecoveryLine} {
				if !strings.Contains(message, want) {
					t.Fatalf("diagnostic %q must contain %q", message, want)
				}
			}
			var failure *store.Failure
			if !errors.As(err, &failure) || failure != refusal {
				t.Fatalf("the diagnostic lost the typed refusal: %v", err)
			}
		})
	}
}

// Live sessions are observations beside the owner, never the owner, and an
// unreadable lease directory never suppresses the recovery line.
func TestUnstampedRefusalDiagnosticListsLiveSessions(t *testing.T) {
	root := t.TempDir()
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	// Admission refuses a lease whose pinned core is gone, so the lease pins
	// this test binary, which exists.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := hostlease.Write(root, hostlease.Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: "/releases/v9.9.8", CoreBinary: self,
		SchemaVersion: 125, Directory: "/workspace/example", RecordedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	message := unstampedRefusalDiagnostic(unstampedRefusal(), root).Error()
	want := fmt.Sprintf("live session(s) on this data root: pid %d holds /releases/v9.9.8 at schema version 125, directory /workspace/example", os.Getpid())
	if !strings.Contains(message, want) || !strings.Contains(message, "owning release: none found") {
		t.Fatalf("diagnostic %q must list the live session beside the owner", message)
	}

	if err := os.WriteFile(filepath.Join(hostlease.Directory(root), "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	message = unstampedRefusalDiagnostic(unstampedRefusal(), root).Error()
	if !strings.Contains(message, "live session(s) on this data root: unknown (") || !strings.Contains(message, unstampedRecoveryLine) {
		t.Fatalf("an unreadable lease directory must keep the recovery line: %q", message)
	}
}

// A refusal about a store outside the data root names no owner: restore
// refuses a backup file, which no installed release manages.
func TestUnstampedRefusalDiagnosticWithoutDataRootNamesRecoveryOnly(t *testing.T) {
	message := unstampedRefusalDiagnostic(unstampedRefusal(), "").Error()
	if strings.Contains(message, "owning release") || strings.Contains(message, "live session") || !strings.Contains(message, unstampedRecoveryLine) {
		t.Fatalf("diagnostic %q must carry the recovery line only", message)
	}
}

func TestUnstampedRefusalDiagnosticPassesOtherFailuresThrough(t *testing.T) {
	plain := errors.New("store: cannot open the database")
	other := &store.Failure{Kind: store.KindInvalidOperation, Op: "open", Detail: "other", RecoveryAction: "retry"}
	for _, err := range []error{plain, other} {
		if got := unstampedRefusalDiagnostic(err, t.TempDir()); got != err {
			t.Fatalf("a failure without the unstamped carrier was rewritten: %v", got)
		}
	}
}

// Every command route that prints the refusal names the owner of the refused
// store's data root, and writes nothing to the store. The readiness route
// blocks on the same rule before any open, so it leads with its blocker.
func TestUnstampedRefusalNamesTheOwnerOnEveryRoute(t *testing.T) {
	routes := []struct {
		name  string
		lead  string
		route func(t *testing.T, path string) string
	}{
		{"open", unstampedRefusalLead, func(t *testing.T, path string) string {
			_, err := openStoreForCommand(context.Background(), path)
			if err == nil {
				t.Fatal("unstamped open of a release-owned store succeeded")
			}
			return err.Error()
		}},
		{"upgrade", unstampedRefusalLead, func(t *testing.T, _ string) string {
			code, _, errOut := runUpgradeStdin(t, `{}`)
			if code != 1 {
				t.Fatalf("unstamped upgrade = %d, want a refusal", code)
			}
			return errOut
		}},
		{"readiness blocker", "an unstamped development build applies no migration to an existing store (CD-0139)", func(t *testing.T, _ string) string {
			code, _, errOut := runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
			if code != 1 {
				t.Fatalf("unstamped cancellation upgrade = %d %q, want the readiness blocker", code, errOut)
			}
			return errOut
		}},
		{"repair snapshot", unstampedRefusalLead, func(t *testing.T, path string) string {
			var out, errOut bytes.Buffer
			if code := backupBeforeRepair(&out, &errOut, filepath.Dir(path), "v-test"); code != 1 {
				t.Fatalf("unstamped repair snapshot = %d %s, want a refusal", code, &errOut)
			}
			return errOut.String()
		}},
		{"recover-fold-guard", unstampedRefusalLead, func(t *testing.T, path string) string {
			// The explicit path's directory is the data root, not the override's.
			t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "elsewhere.db"))
			var out, errOut bytes.Buffer
			input := fmt.Sprintf(`{"path":%q}`, path)
			if code := runWithInput([]string{"recover-fold-guard"}, strings.NewReader(input), &out, &errOut); code != 1 {
				t.Fatalf("unstamped recover-fold-guard = %d %s, want a refusal", code, &errOut)
			}
			return errOut.String()
		}},
	}
	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			useUnstampedBuild(t)
			root := t.TempDir()
			path := unstampedTailStore(t, root, "concord.db")
			t.Setenv(dbOverrideEnv, path)
			installRelease(t, root, "v9.9.9")
			before, err := store.PlanUpgradeReadiness(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}

			assertOwnedRefusal(t, tc.route(t, path), tc.lead, root, "v9.9.9")

			after, err := store.PlanUpgradeReadiness(context.Background(), path)
			if err != nil || after.SchemaVersion != before.SchemaVersion || len(after.Pending) != len(before.Pending) {
				t.Fatalf("the refused route changed the store: before %+v, after %+v, %v", before, after, err)
			}
		})
	}
}

// A relative store path names the same owner as its absolute form: the
// installer's link target is absolute, so the data root is resolved before
// the two are compared.
func TestUnstampedRefusalNamesTheOwnerForARelativeStorePath(t *testing.T) {
	useUnstampedBuild(t)
	root := t.TempDir()
	unstampedTailStore(t, root, "concord.db")
	installRelease(t, root, "v9.9.7")
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "elsewhere.db"))
	t.Chdir(filepath.Dir(root))
	relativeRoot := filepath.Base(root)

	var out, errOut bytes.Buffer
	input := fmt.Sprintf(`{"path":%q}`, filepath.Join(relativeRoot, "concord.db"))
	if code := runWithInput([]string{"recover-fold-guard"}, strings.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("unstamped recover-fold-guard of a relative path = %d %s, want a refusal", code, &errOut)
	}
	assertOwnedRefusal(t, errOut.String(), unstampedRefusalLead, relativeRoot, "v9.9.7")
}

// restore refuses a backup file, not the data root's store, so it names the
// recovery and no owner, even when the backup sits beside an installed link.
func TestUnstampedRestoreRefusalNamesRecoveryOnly(t *testing.T) {
	useUnstampedBuild(t)
	freshMigratedCLIDatabase(t)
	backups := t.TempDir()
	snapshot := filepath.Join(backups, "snapshot.db")
	runCLIJSON(t, []string{"backup"}, map[string]any{"destination": snapshot})
	manifest, err := store.VerifyBackup(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// Model a verified snapshot taken before the newest migration: the
	// manifest inventories the edited bytes, so only the restore's forward
	// migration of its staging copy can refuse.
	removeNewestManifestRow(t, snapshot)
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	older, err := store.PlanUpgradeReadiness(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.DBChecksum = fmt.Sprintf("%x", sha256.Sum256(raw))
	manifest.SchemaVersion = older.SchemaVersion
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot+".manifest.json", encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyBackup(context.Background(), snapshot); err != nil {
		t.Fatalf("fixture must pass snapshot inventory verification: %v", err)
	}
	installRelease(t, backups, "v9.9.9")

	input := fmt.Sprintf(`{"source":%q,"destination":%q}`, snapshot, filepath.Join(t.TempDir(), "restored.db"))
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, strings.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("unstamped restore of an older snapshot = %d %s, want a refusal", code, &errOut)
	}
	message := errOut.String()
	if !strings.Contains(message, unstampedRefusalLead) || !strings.Contains(message, unstampedRecoveryLine) || strings.Contains(message, "owning release") {
		t.Fatalf("restore refusal %q must carry the recovery line and no owner", message)
	}
}
