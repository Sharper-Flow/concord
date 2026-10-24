package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
	"github.com/sharper-flow/concord/internal/version"
)

const unstampedRecoveryLine = "recovery: set CONCORD_DB_PATH to a fresh path outside any repository, then retry"

const unstampedRefusalLead = "unstamped development build refuses schema migration for an existing store"

func useStamp(t *testing.T, stamp string) {
	t.Helper()
	previous := version.Value
	version.Value = stamp
	t.Cleanup(func() { version.Value = previous })
}

func removeNewestManifestRow(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := rewindMigrationTail(db, 0, false); err != nil {
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

// cliDataRootState records every entry under root with the digest of each
// regular file, so a refused command that wrote a store, WAL, lease, lock,
// fence, or snapshot is visible.
func cliDataRootState(t *testing.T, root string) string {
	t.Helper()
	var state []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		line := path
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path) //nolint:gosec // the walk stays inside the test's own temporary data root.
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" %x", sha256.Sum256(raw))
		}
		state = append(state, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(state)
	return strings.Join(state, "\n")
}

// A branch, development, or test build of concord never opens the store of a
// data root the installer manages, whatever its stamp (CD-0240). This test
// binary is such a build. Every command route refuses before it reads or
// writes the store, its leases, its locks, or its fences, names the installed
// release, and names the recovery.
func TestReleaseManagedStoreRefusesABinaryTheInstallerDidNotPlace(t *testing.T) {
	routes := []struct {
		name  string
		route func(t *testing.T, root, path string) (int, string)
	}{
		{"store command", func(t *testing.T, _, _ string) (int, string) {
			var out, errOut bytes.Buffer
			input := fmt.Sprintf(`{"destination":%q}`, filepath.Join(t.TempDir(), "snapshot.db"))
			return runWithInput([]string{"backup"}, strings.NewReader(input), &out, &errOut), errOut.String()
		}},
		{"upgrade plan", func(t *testing.T, _, _ string) (int, string) {
			code, _, errOut := runUpgradeStdin(t, `{"plan":true}`)
			return code, errOut
		}},
		{"upgrade", func(t *testing.T, _, _ string) (int, string) {
			code, _, errOut := runUpgradeStdin(t, `{}`)
			return code, errOut
		}},
		{"cutover cancellation upgrade", func(t *testing.T, _, _ string) (int, string) {
			code, _, errOut := runUpgradeStdin(t, `{"cancel_cutover_pinned_work":true}`)
			return code, errOut
		}},
		{"host leases", func(t *testing.T, _, _ string) (int, string) {
			var out, errOut bytes.Buffer
			return runWithInput([]string{"host-leases"}, strings.NewReader(`{}`), &out, &errOut), errOut.String()
		}},
		{"repair snapshot", func(t *testing.T, root, _ string) (int, string) {
			var out, errOut bytes.Buffer
			return backupBeforeRepair(&out, &errOut, root, "v-test"), errOut.String()
		}},
	}
	for _, stamp := range []string{version.Development, "v9.9.9"} {
		for _, tc := range routes {
			t.Run(stamp+"/"+tc.name, func(t *testing.T) {
				useStamp(t, stamp)
				root := t.TempDir()
				if err := storetest.WriteMigratedDatabase(root, "concord.db"); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "concord.db")
				t.Setenv(dbOverrideEnv, path)
				installRelease(t, root, "v9.9.9")
				before := cliDataRootState(t, root)

				code, message := tc.route(t, root, path)
				if code == 0 {
					t.Fatalf("the route admitted a binary the installer did not place: %s", message)
				}
				for _, want := range []string{
					"a binary the installer did not place refuses the release-managed store",
					"whose installed release is v9.9.9",
					"recovery: " + store.ReleaseManagedRecovery,
				} {
					if !strings.Contains(message, want) {
						t.Fatalf("refusal %q must contain %q", message, want)
					}
				}
				if after := cliDataRootState(t, root); after != before {
					t.Fatalf("the refused route changed the data root:\nbefore\n%s\nafter\n%s", before, after)
				}
			})
		}
	}
}

// restore cannot promote a snapshot into a managed data root from a binary
// the installer did not place, even under a fresh destination name.
func TestReleaseManagedRestoreDestinationRefuses(t *testing.T) {
	freshMigratedCLIDatabase(t)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	runCLIJSON(t, []string{"backup"}, map[string]any{"destination": snapshot})
	root := t.TempDir()
	installRelease(t, root, "v9.9.9")
	before := cliDataRootState(t, root)

	input := fmt.Sprintf(`{"source":%q,"destination":%q}`, snapshot, filepath.Join(root, "restored.db"))
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, strings.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("restore into a managed data root = %d %s, want a refusal", code, &errOut)
	}
	if !strings.Contains(errOut.String(), "refuses the release-managed store") {
		t.Fatalf("restore refusal %q must name the release-managed store", &errOut)
	}
	if after := cliDataRootState(t, root); after != before {
		t.Fatalf("the refused restore changed the data root:\nbefore\n%s\nafter\n%s", before, after)
	}
}

// restore refuses a backup file, not a data root's store, so its unstamped
// migration refusal names the one recovery line.
func TestUnstampedRestoreRefusalNamesTheRecovery(t *testing.T) {
	useStamp(t, version.Development)
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

	input := fmt.Sprintf(`{"source":%q,"destination":%q}`, snapshot, filepath.Join(t.TempDir(), "restored.db"))
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"restore"}, strings.NewReader(input), &out, &errOut); code != 1 {
		t.Fatalf("unstamped restore of an older snapshot = %d %s, want a refusal", code, &errOut)
	}
	message := errOut.String()
	if !strings.Contains(message, unstampedRefusalLead) || strings.Count(message, unstampedRecoveryLine) != 1 || strings.Count(message, "recovery: ") != 1 {
		t.Fatalf("restore refusal %q must carry one recovery line", message)
	}
}
