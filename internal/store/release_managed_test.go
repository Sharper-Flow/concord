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

// writeBinary writes an empty file at path, creating its directories.
func writeBinary(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Only a core the installer placed opens a store it manages: a release tree
// or a staged transaction candidate. The version stamp plays no part, and a
// binary elsewhere under the data root, such as one built in a managed
// worktree, refuses. A store without the installer's link is not managed,
// and anything the check cannot resolve refuses.
func TestAdmitStorePathAdmitsOnlyInstallerPlacedBinaries(t *testing.T) {
	root, dbPath := managedDataRoot(t, "v9.9.9")
	installed := writeBinary(t, filepath.Join(root, "v9.9.9", "bin", "concord"))
	older := writeBinary(t, filepath.Join(root, "v9.8.0", "bin", "concord"))
	staged := writeBinary(t, filepath.Join(root, ".concord-transactions", "0123abcd", "stage", "version", "bin", "concord"))
	outside := writeBinary(t, filepath.Join(t.TempDir(), "concord"))
	launcher := filepath.Join(t.TempDir(), "concord")
	if err := os.Symlink(installed, launcher); err != nil {
		t.Fatal(err)
	}
	aliasedRoot := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(root, aliasedRoot); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, binary string
	}{
		{"installed release", dbPath, installed},
		{"retained older release", dbPath, older},
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

	for _, tc := range []struct {
		name, binary string
	}{
		{"a binary outside the data root", outside},
		{"a build in a managed worktree", filepath.Join(root, "worktrees", "concord", "work-1", "concord")},
		{"a build in a worktree's bin directory", filepath.Join(root, "worktrees", "concord", "work-1", "bin", "concord")},
		{"a tree whose name is not a release", filepath.Join(root, "worktrees", "bin", "concord")},
		{"a prerelease-named tree", filepath.Join(root, "v9.9.9-dev", "bin", "concord")},
		{"another file in a release tree", filepath.Join(root, "v9.9.9", "bin", "concord-dev")},
		{"a transaction backup", filepath.Join(root, ".concord-transactions", "0123abcd", "backup", "version", "bin", "concord")},
		{"a sibling whose name extends the data root", filepath.Join(root+"-branch", "v9.9.9", "bin", "concord")},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			binary := writeBinary(t, tc.binary)
			resolved, err := filepath.EvalSymlinks(binary)
			if err != nil {
				t.Fatal(err)
			}
			assertReleaseManagedRefusal(t, admitStorePath(dbPath, fixedExecutable(binary)),
				"the running binary "+resolved+" is not a release the installer placed in the data root")
			assertReleaseManagedRefusal(t, admitStorePath(dbPath, fixedExecutable(binary)), "whose installed release is v9.9.9")
		})
	}
	t.Run("refuses a relative path to the managed store", func(t *testing.T) {
		t.Chdir(filepath.Dir(root))
		assertReleaseManagedRefusal(t, admitStorePath(filepath.Join(filepath.Base(root), "concord.db"), fixedExecutable(outside)), "is not a release the installer placed")
	})
	t.Run("refuses a database symlink into the managed root", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "concord.db")
		if err := os.Symlink(dbPath, link); err != nil {
			t.Fatal(err)
		}
		assertReleaseManagedRefusal(t, admitStorePath(link, fixedExecutable(outside)), "is not a release the installer placed")
	})
	t.Run("refuses a dangling database symlink into the managed root", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "concord.db")
		if err := os.Symlink(filepath.Join(root, "absent.db"), link); err != nil {
			t.Fatal(err)
		}
		assertReleaseManagedRefusal(t, admitStorePath(link, fixedExecutable(outside)), "is not a release the installer placed")
	})
	t.Run("refuses an unlocatable binary", func(t *testing.T) {
		assertReleaseManagedRefusal(t, admitStorePath(dbPath, func() (string, error) { return "", errors.New("no executable") }),
			"the running binary cannot be located: no executable")
	})
}

// Any current entry marks the data root as managed, so a link the installer
// layout does not explain refuses instead of admitting.
func TestAdmitStorePathFailsClosedOnAnUnexplainedCurrentEntry(t *testing.T) {
	outside := writeBinary(t, filepath.Join(t.TempDir(), "concord"))
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
				{"BackupDatabase source", func(_, dbPath string) error {
					_, err := BackupDatabase(ctx, dbPath, filepath.Join(t.TempDir(), "copy.db"))
					return err
				}},
				{"Backup destination", func(root, _ string) error {
					s, err := Open(ctx, filepath.Join(t.TempDir(), "unmanaged.db"))
					if err != nil {
						return err
					}
					defer func() { _ = s.Close() }()
					_, err = Backup(ctx, s, filepath.Join(root, "copy.db"))
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
