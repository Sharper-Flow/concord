package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// managedDataRoot writes a fresh store into a data root and then lays the
// installer's current link beside it, so the store predates the link the way
// an installed store does.
func managedDataRoot(t *testing.T, release string) (root, dbPath string) {
	t.Helper()
	root = t.TempDir()
	dbPath = filepath.Join(root, "concord.db")
	s, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, release, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, release), filepath.Join(root, installedReleaseLink)); err != nil {
		t.Fatal(err)
	}
	return root, dbPath
}

func fixedExecutable(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func assertReleaseManagedRefusal(t *testing.T, err error, want string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.RecoveryAction != ReleaseManagedRecovery || !failure.RetrySafe {
		t.Fatalf("error = %#v, want the release-managed refusal", err)
	}
	if !strings.Contains(failure.Detail, "a binary the installer did not place refuses the release-managed store") || !strings.Contains(failure.Detail, want) {
		t.Fatalf("refusal detail = %q, want it to contain %q", failure.Detail, want)
	}
}

// Only a binary under the data root opens a store the installer manages; the
// version stamp plays no part. A store without the installer's link is not
// managed, and anything the check cannot resolve refuses.
func TestAdmitStorePathAdmitsOnlyBinariesUnderAManagedDataRoot(t *testing.T) {
	root, dbPath := managedDataRoot(t, "v9.9.9")
	installed := filepath.Join(root, "v9.9.9", "bin", "concord")
	if err := os.WriteFile(installed, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(t.TempDir(), "concord")
	if err := os.Symlink(installed, launcher); err != nil {
		t.Fatal(err)
	}
	aliasedRoot := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(root, aliasedRoot); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(root, ".transactions", "tx", "stage", "version", "bin", "concord")
	outside := filepath.Join(t.TempDir(), "concord")
	for _, binary := range []string{staged, outside} {
		if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name, path, binary string
	}{
		{"installed release", dbPath, installed},
		{"symlinked launcher", dbPath, launcher},
		{"transaction stage", dbPath, staged},
		{"aliased data root", filepath.Join(aliasedRoot, "concord.db"), installed},
		{"unmanaged store", filepath.Join(t.TempDir(), "concord.db"), outside},
		{"path under a regular file", filepath.Join(outside, "concord.db"), outside},
	} {
		t.Run("admits "+tc.name, func(t *testing.T) {
			if err := admitStorePath(tc.path, fixedExecutable(tc.binary)); err != nil {
				t.Fatalf("admitStorePath(%s, %s) = %v, want admitted", tc.path, tc.binary, err)
			}
		})
	}

	t.Run("refuses a binary outside the data root", func(t *testing.T) {
		resolved, err := filepath.EvalSymlinks(outside)
		if err != nil {
			t.Fatal(err)
		}
		assertReleaseManagedRefusal(t, admitStorePath(dbPath, fixedExecutable(outside)),
			"the running binary "+resolved+" is not under the data root")
		assertReleaseManagedRefusal(t, admitStorePath(dbPath, fixedExecutable(outside)), "whose installed release is v9.9.9")
	})
	t.Run("refuses a sibling whose name extends the data root", func(t *testing.T) {
		sibling := root + "-branch"
		if err := os.MkdirAll(sibling, 0o700); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(sibling, "concord")
		if err := os.WriteFile(binary, nil, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(sibling) })
		assertReleaseManagedRefusal(t, admitStorePath(dbPath, fixedExecutable(binary)), "is not under the data root")
	})
	t.Run("refuses a relative path to the managed store", func(t *testing.T) {
		t.Chdir(filepath.Dir(root))
		assertReleaseManagedRefusal(t, admitStorePath(filepath.Join(filepath.Base(root), "concord.db"), fixedExecutable(outside)), "is not under the data root")
	})
	t.Run("refuses an unlocatable binary", func(t *testing.T) {
		assertReleaseManagedRefusal(t, admitStorePath(dbPath, func() (string, error) { return "", errors.New("no executable") }),
			"the running binary cannot be located: no executable")
	})
}

// Any current entry marks the data root as managed, so a link the installer
// layout does not explain refuses instead of admitting.
func TestAdmitStorePathFailsClosedOnAnUnexplainedCurrentEntry(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "concord")
	if err := os.WriteFile(outside, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, link string)
		want    string
	}{
		{"regular file", func(t *testing.T, link string) {
			if err := os.WriteFile(link, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "whose installed release is unknown"},
		{"dangling link", func(t *testing.T, link string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(link), "v0.0.0"), link); err != nil {
				t.Fatal(err)
			}
		}, "whose installed release is v0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.prepare(t, filepath.Join(root, installedReleaseLink))
			assertReleaseManagedRefusal(t, admitStorePath(filepath.Join(root, "concord.db"), fixedExecutable(outside)), tc.want)
		})
	}
}

// dataRootState records every entry of the data root with the bytes of each
// regular file, so a refused route that created a WAL, a lock, or a staging
// file, or wrote the store, is visible.
func dataRootState(t *testing.T, root string) []string {
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
	return state
}

// Every store entry point that takes a path refuses a managed store from this
// test binary, which the installer never placed, and leaves the data root
// byte-for-byte unchanged: no open, no migration, no lock, no WAL.
func TestReleaseManagedStoreRefusesEveryPathEntryPoint(t *testing.T) {
	ctx := context.Background()
	for _, stamp := range []string{"development", "stamped"} {
		t.Run(stamp, func(t *testing.T) {
			if stamp == "stamped" {
				useStampedBuild(t)
			} else {
				useDevelopmentBuild(t)
			}
			snapshotRoot := t.TempDir()
			source, err := Open(ctx, filepath.Join(snapshotRoot, "source.db"))
			if err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(snapshotRoot, "snapshot.db")
			if _, err := Backup(ctx, source, snapshot); err != nil {
				t.Fatal(err)
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}

			for _, route := range []struct {
				name string
				run  func(root, dbPath string) error
			}{
				{"Open", func(_, dbPath string) error {
					s, err := Open(ctx, dbPath)
					if err == nil {
						_ = s.Close()
					}
					return err
				}},
				{"Upgrade", func(_, dbPath string) error { _, err := Upgrade(ctx, dbPath, nil); return err }},
				{"PlanUpgradeReadiness", func(_, dbPath string) error { _, err := PlanUpgradeReadiness(ctx, dbPath); return err }},
				{"RecoverFoldGuard", func(_, dbPath string) error { _, err := RecoverFoldGuard(ctx, dbPath); return err }},
				{"BackupDatabase source", func(_, dbPath string) error {
					_, err := BackupDatabase(ctx, dbPath, filepath.Join(t.TempDir(), "copy.db"))
					return err
				}},
				{"VerifyBackup", func(_, dbPath string) error { _, err := VerifyBackup(ctx, dbPath); return err }},
				{"RestoreBackup destination", func(root, _ string) error {
					_, err := RestoreBackup(ctx, snapshot, filepath.Join(root, "restored.db"))
					return err
				}},
			} {
				t.Run(route.name, func(t *testing.T) {
					root, dbPath := managedDataRoot(t, "v9.9.9")
					before := dataRootState(t, root)
					assertReleaseManagedRefusal(t, route.run(root, dbPath), "whose installed release is v9.9.9")
					if after := dataRootState(t, root); strings.Join(after, "\n") != strings.Join(before, "\n") {
						t.Fatalf("the refused route changed the data root:\nbefore %v\nafter  %v", before, after)
					}
				})
			}
		})
	}
}
