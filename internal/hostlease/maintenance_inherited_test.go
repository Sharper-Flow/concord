package hostlease

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInheritedMaintenanceRefusesSymlinkedChildGuard(t *testing.T) {
	root := t.TempDir()
	parent, err := acquireMaintenanceLock(root, openMaintenanceDirectory, syscall.Flock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	target := filepath.Join(t.TempDir(), "operator-file")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "native-upgrade.lock")); err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireMaintenanceInherited(root, int(parent.Fd())); err == nil {
		release()
		t.Fatal("a symlinked child guard admitted an inherited upgrade")
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "unchanged" {
		t.Fatalf("the guard changed an operator file: %q %v", content, err)
	}
}

func TestInheritedMaintenanceKeepsTheParentLockUntilItsDescriptorCloses(t *testing.T) {
	root := t.TempDir()
	parent, err := acquireMaintenanceLock(root, openMaintenanceDirectory, syscall.Flock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	release, err := AcquireMaintenanceInherited(root, int(parent.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if duplicate, err := AcquireMaintenanceInherited(root, int(parent.Fd())); !errors.Is(err, ErrMaintenanceBusy) {
		if duplicate != nil {
			duplicate()
		}
		t.Fatalf("a second child sharing the same description must refuse: %v", err)
	}
	release()
	if unexpected, err := AcquireMaintenance(root); !errors.Is(err, ErrMaintenanceBusy) {
		if unexpected != nil {
			unexpected()
		}
		t.Fatalf("child release unlocked the parent's description: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	final, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("closed parent still owns the lock: %v", err)
	}
	final()
}

func TestInheritedMaintenanceRefusesInvalidForeignAndContendedDescriptors(t *testing.T) {
	root := t.TempDir()
	foreignRoot := t.TempDir()
	foreign, err := os.Open(foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	contended, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contended.Close() }()
	parent, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent()
	for name, fd := range map[string]int{"invalid": -1, "foreign": int(foreign.Fd()), "contended": int(contended.Fd())} {
		t.Run(name, func(t *testing.T) {
			release, err := AcquireMaintenanceInherited(root, fd)
			if release != nil {
				release()
			}
			if err == nil {
				t.Fatal("invalid inherited descriptor admitted maintenance")
			}
		})
	}
	foreignRelease, err := AcquireMaintenance(foreignRoot)
	if err != nil {
		t.Fatalf("foreign descriptor refusal left a lock behind: %v", err)
	}
	foreignRelease()
}
