package store

import (
	"context"
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
	t.Logf("Backup() error with a foreign destination file: %v", err)
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatalf("failed Backup() deleted a foreign destination file it never owned: %v", readErr)
	}
	if string(got) != foreign {
		t.Fatalf("foreign destination content = %q, want %q", got, foreign)
	}
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
	t.Logf("Backup() error with a foreign manifest file: %v", backupErr)
	got, readErr := os.ReadFile(manifestPath(destination))
	if readErr != nil {
		t.Fatalf("Backup() removed a foreign manifest file it never owned: %v", readErr)
	}
	if string(got) != foreign {
		t.Fatalf("foreign manifest content = %q, want %q", got, foreign)
	}
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
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = context.WithValue(ctx, backupStartedContextKey{}, func() {
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
					cancel()
				}
				if err != nil {
					t.Error(err)
				}
			})
			if _, err := Backup(ctx, s, destination); err == nil {
				t.Error("Backup succeeded")
			}
			switch kind {
			case "empty destination":
				data, err := os.ReadFile(destination)
				if err != nil || len(data) != 0 {
					t.Errorf("empty destination changed: %q, %v", data, err)
				}
			case "destination symlink", "manifest symlink":
				path := destination
				if kind == "manifest symlink" {
					path = manifestPath(destination)
				}
				link, err := os.Readlink(path)
				if err != nil || link != target {
					t.Errorf("symlink changed: %q, %v", link, err)
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "foreign" {
					t.Errorf("target changed: %q, %v", data, err)
				}
			case "public sidecars":
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					data, err := os.ReadFile(destination + suffix)
					if err != nil || string(data) != "sidecar" {
						t.Errorf("sidecar %s changed: %q, %v", suffix, data, err)
					}
				}
			}
		})
	}
}
