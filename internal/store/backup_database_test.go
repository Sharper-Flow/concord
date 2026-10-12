package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupDatabasePreservesPendingAdditiveAndBreakingSchema(t *testing.T) {
	useStampedBuild(t)
	_, applied := breakingWindow(t)
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture.db")
	if err := openMigratedTo(t, fixture, applied).Close(); err != nil {
		t.Fatal(err)
	}
	before := manifestMax(t, fixture)
	path := filepath.Join(root, "source ?#%.db")
	if err := os.Rename(fixture, path); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := BackupDatabase(context.Background(), path, destination)
	if err != nil {
		t.Fatal(err)
	}
	after, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != before || after.SchemaVersion != before {
		t.Fatalf("backup migrated its source or snapshot: %+v, want schema %d", manifest, before)
	}
	if _, err := VerifyBackup(context.Background(), destination); err != nil {
		t.Fatalf("snapshot failed verification: %v", err)
	}
}

func TestBackupDatabaseRefusesMissingSourceWithoutCreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(context.Background(), path, destination); err == nil {
		t.Error("missing source produced a snapshot")
	}
	for _, candidate := range []string{path, destination, manifestPath(destination)} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Errorf("missing-source backup created %s: %v", candidate, err)
		}
	}
}
