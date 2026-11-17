package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func plantForeignFileAt(t *testing.T, ctx context.Context, path, content string) context.Context {
	t.Helper()
	return context.WithValue(ctx, backupStartedContextKey{}, func() {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Errorf("cannot plant the foreign file %s: %v", path, err)
		}
	})
}

// assertBackupLeftOnlyForeignEntries fails when a failed backup left any
// staging file or a destination name the foreign writer did not create.
func assertBackupLeftOnlyForeignEntries(t *testing.T, dir string, foreign ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())
		owned := true
		for _, path := range foreign {
			if name == path {
				owned = false
			}
		}
		if owned {
			t.Errorf("failed Backup() left %s", entry.Name())
		}
	}
}

func TestBackupLeavesForeignDestinationFileThatAppearedAfterValidation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	const foreign = "foreign destination content owned by another writer"
	ctx := plantForeignFileAt(t, context.Background(), destination, foreign)
	_, err := Backup(ctx, s, destination)
	if err == nil {
		t.Fatal("Backup() succeeded although a foreign file occupied the destination")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "backup destination already exists" {
		t.Errorf("Backup() error = %v, want the destination refusal", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatalf("failed Backup() deleted a foreign destination file it never owned: %v", readErr)
	}
	if string(got) != foreign {
		t.Fatalf("foreign destination content = %q, want %q", got, foreign)
	}
	assertBackupLeftOnlyForeignEntries(t, dir, destination)
}

func TestBackupLeavesForeignManifestFileThatAppearedAfterValidation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	const foreign = "foreign manifest content owned by another writer"
	ctx := plantForeignFileAt(t, context.Background(), manifestPath(destination), foreign)
	_, backupErr := Backup(ctx, s, destination)
	if backupErr == nil {
		t.Error("Backup() succeeded with a foreign manifest")
	}
	var failure *Failure
	if !errors.As(backupErr, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "backup manifest already exists" {
		t.Errorf("Backup() error = %v, want the manifest refusal", backupErr)
	}
	got, readErr := os.ReadFile(manifestPath(destination))
	if readErr != nil {
		t.Fatalf("Backup() removed a foreign manifest file it never owned: %v", readErr)
	}
	if string(got) != foreign {
		t.Fatalf("foreign manifest content = %q, want %q", got, foreign)
	}
	assertBackupLeftOnlyForeignEntries(t, dir, manifestPath(destination))
}

func TestInterruptedBackupLeavesForeignManifestFileThatAppearedAfterValidation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	const foreign = "foreign manifest content owned by another writer"
	backupCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backupCtx = plantForeignFileAt(t, backupCtx, manifestPath(destination), foreign)
	backupCtx = context.WithValue(backupCtx, restoreStageReadyContextKey{}, func(string) { cancel() })
	_, err := Backup(backupCtx, s, destination)
	if err == nil {
		t.Fatal("interrupted Backup() succeeded")
	}
	t.Logf("interrupted Backup() error: %v", err)
	got, readErr := os.ReadFile(manifestPath(destination))
	if readErr != nil {
		t.Fatalf("interrupted Backup() deleted a foreign manifest file it never owned: %v", readErr)
	}
	if string(got) != foreign {
		t.Fatalf("foreign manifest content = %q, want %q", got, foreign)
	}
	assertBackupLeftOnlyForeignEntries(t, dir, manifestPath(destination))
}

// A snapshot published beside foreign SQLite sidecars would be read through
// them, so Backup refuses the destination and leaves the sidecars untouched.
func TestBackupRefusesExistingDestinationSidecars(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			defer s.Close()
			dir := t.TempDir()
			destination := filepath.Join(dir, "snapshot.db")
			sidecar := destination + suffix
			if err := os.WriteFile(sidecar, []byte("stale sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Backup(context.Background(), s, destination)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || failure.Detail != "backup destination SQLite sidecar already exists" {
				t.Fatalf("Backup() error = %v, want the sidecar refusal", err)
			}
			if data, err := os.ReadFile(sidecar); err != nil || string(data) != "stale sidecar" {
				t.Fatalf("sidecar changed: %q, %v", data, err)
			}
			assertBackupLeftOnlyForeignEntries(t, dir, sidecar)
		})
	}
}

func TestBackupPreservesForeignEntriesAfterValidation(t *testing.T) {
	for _, kind := range []string{"empty destination", "destination symlink", "manifest symlink", "public sidecars"} {
		t.Run(kind, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			dir := t.TempDir()
			destination := filepath.Join(dir, "snapshot.db")
			target := filepath.Join(dir, "foreign")
			if err := os.WriteFile(target, []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), backupStartedContextKey{}, func() {
				var err error
				switch kind {
				case "empty destination":
					err = os.WriteFile(destination, nil, 0o600)
				case "destination symlink":
					err = os.Symlink(target, destination)
				case "manifest symlink":
					err = os.Symlink(target, manifestPath(destination))
				case "public sidecars":
					for _, suffix := range []string{"-wal", "-shm", "-journal"} {
						if e := os.WriteFile(destination+suffix, []byte("sidecar"), 0o600); e != nil {
							t.Error(e)
						}
					}
				}
				if err != nil {
					t.Error(err)
				}
			})
			_, backupErr := Backup(ctx, s, destination)
			if backupErr == nil {
				t.Error("Backup succeeded")
			}
			foreign := []string{target}
			switch kind {
			case "empty destination":
				foreign = append(foreign, destination)
				data, err := os.ReadFile(destination)
				if err != nil || len(data) != 0 {
					t.Errorf("empty destination changed: %q, %v", data, err)
				}
			case "destination symlink", "manifest symlink":
				path := destination
				if kind == "manifest symlink" {
					path = manifestPath(destination)
				}
				foreign = append(foreign, path)
				link, err := os.Readlink(path)
				if err != nil || link != target {
					t.Errorf("symlink changed: %q, %v", link, err)
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "foreign" {
					t.Errorf("target changed: %q, %v", data, err)
				}
			case "public sidecars":
				var failure *Failure
				if !errors.As(backupErr, &failure) || failure.Detail != "backup destination SQLite sidecar already exists" {
					t.Errorf("Backup() error = %v, want the sidecar refusal before publication", backupErr)
				}
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					foreign = append(foreign, destination+suffix)
					data, err := os.ReadFile(destination + suffix)
					if err != nil || string(data) != "sidecar" {
						t.Errorf("sidecar %s changed: %q, %v", suffix, data, err)
					}
				}
			}
			assertBackupLeftOnlyForeignEntries(t, dir, foreign...)
		})
	}
}
