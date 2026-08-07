package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupUsesOnlineSnapshotAndPM10Manifest(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := BackupWithOptions(ctx, s, destination, BackupOptions{
		SnapshotID:   "snapshot-test-1",
		GitCommitIDs: map[string]string{"repo": "abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SnapshotID != "snapshot-test-1" || manifest.DBChecksum == "" || manifest.EventWatermark < 0 || manifest.BinaryVersion == "" {
		t.Fatalf("manifest missing PM10 fields: %+v", manifest)
	}
	if manifest.GitCommitIDs["repo"] != "abc123" {
		t.Fatalf("manifest git inventory = %+v", manifest.GitCommitIDs)
	}
	verified, err := VerifyBackup(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	if verified.DBChecksum != manifest.DBChecksum || verified.EventWatermark != manifest.EventWatermark || verified.SnapshotID != manifest.SnapshotID {
		t.Fatalf("verified manifest = %+v, original = %+v", verified, manifest)
	}
}

func TestInterruptedBackupRemovesPartialSnapshotAndManifest(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	destination := filepath.Join(t.TempDir(), "interrupted.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Backup(ctx, s, destination); err == nil {
		t.Fatal("Backup() with a cancelled context succeeded")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial snapshot stat error = %v", err)
	}
	if _, err := os.Stat(manifestPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial manifest stat error = %v", err)
	}
}

func TestRestoreBackupUsesCleanOnlineRestoreAndPromotesVerifiedDatabase(t *testing.T) {
	ctx := context.Background()
	source := openTemp(t)
	defer source.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	want, err := Backup(ctx, source, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	got, err := RestoreBackup(ctx, snapshot, destination)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.EventWatermark != want.EventWatermark || got.DBChecksum != want.DBChecksum {
		t.Fatalf("restored manifest = %+v, want %+v", got, want)
	}
	restored, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	integrity, quick, foreign, err := verifySQLiteTriple(ctx, restored.DB())
	if err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" || quick != "ok" || len(foreign) != 0 {
		t.Fatalf("restored triple = integrity=%q quick=%q foreign=%v", integrity, quick, foreign)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".concord-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("restore staging files = %v (err=%v)", matches, err)
	}
}

func TestRestoreBackupRefusesExistingDestinationUntouched(t *testing.T) {
	ctx := context.Background()
	source := openTemp(t)
	defer source.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, source, snapshot); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "live.db")
	want := []byte("existing live destination")
	if err := os.WriteFile(destination, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackup(ctx, snapshot, destination); err == nil {
		t.Fatal("RestoreBackup() replaced an existing destination")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("existing destination = %q, want %q", got, want)
	}
}

func TestInterruptedRestoreRemovesStagingAndLeavesDestinationAbsent(t *testing.T) {
	source := openTemp(t)
	defer source.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(context.Background(), source, snapshot); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "restored.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, restoreStageReadyContextKey{}, func(string) { cancel() })
	if _, err := RestoreBackup(ctx, snapshot, destination); err == nil {
		t.Fatal("interrupted RestoreBackup() succeeded")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted destination stat error = %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".concord-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("interrupted restore staging files = %v (err=%v)", matches, err)
	}
}

func TestBackupRejectsForeignKeyOnlyCorruption(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defer s.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, s, snapshot); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(manifestPath(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath(corrupt), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, "file:"+corrupt+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('missing-from','missing-to','blocks','2026-01-01T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifestData, err = os.ReadFile(manifestPath(corrupt))
	if err != nil {
		t.Fatal(err)
	}
	var corruptManifest BackupManifest
	if err := json.Unmarshal(manifestData, &corruptManifest); err != nil {
		t.Fatal(err)
	}
	corruptManifest.DBChecksum, err = fileSHA256(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err = json.Marshal(corruptManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath(corrupt), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, corrupt); err == nil {
		t.Fatal("VerifyBackup() accepted a foreign-key-only corrupt snapshot")
	}
	if _, err := RestoreBackup(ctx, corrupt, filepath.Join(t.TempDir(), "restored.db")); err == nil {
		t.Fatal("RestoreBackup() accepted a foreign-key-only corrupt snapshot")
	}
}
