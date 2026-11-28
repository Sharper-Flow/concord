package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRefusesStoreBelowSupportedFloor(t *testing.T) {
	useStampedBuild(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driverName, SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES(198,'old','old','time')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), path)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("Open() error = %v, want %s", err, KindSchemaUnsupported)
	}
	if !strings.Contains(failure.Detail+failure.RecoveryAction, "199") || !strings.Contains(failure.Detail+failure.RecoveryAction, "v67.0.0") {
		t.Fatalf("refusal must name the minimum and bridge release: %v", err)
	}
	db, err = sql.Open(driverName, SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('schema_migrations') WHERE name='breaking'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("refusal repaired the manifest")
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("refusal changed journal mode to %q", mode)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("refusal changed parent permissions to %o", info.Mode().Perm())
	}
}

func TestSchemaFloorFreshManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count, minimum, maximum int
	if err := s.db.QueryRow(`SELECT count(*),min(version),max(version) FROM schema_migrations`).Scan(&count, &minimum, &maximum); err != nil {
		t.Fatal(err)
	}
	if count != 4 || minimum != 199 || maximum != 202 {
		t.Fatalf("fresh manifest = (%d,%d,%d), want (4,199,202)", count, minimum, maximum)
	}
}

func TestSchemaFloorBaselineForward(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "baseline.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaBaseline198); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version >= 199 && m.Version <= 202 {
			if err := applyMigration(ctx, db, m); err != nil {
				t.Fatalf("migration %d: %v", m.Version, err)
			}
		}
	}
}

func schemaFloorFixture199(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema199.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, schemaBaseline198); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	var first migration
	for _, m := range migrations {
		if m.Version == 199 {
			first = m
			break
		}
	}
	if first.Version != 199 {
		t.Fatal("migration 199 is missing")
	}
	if err := applyMigration(ctx, db, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking) VALUES(?,?,?,?,?)`, first.Version, first.Name, first.checksum(), "2026-01-01T00:00:00Z", first.Breaking); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSchemaFloorUpgradeFrom199(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	path := schemaFloorFixture199(t)
	report, err := Upgrade(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Applied) != 3 || report.Applied[0] != 200 || report.Applied[1] != 201 || report.Applied[2] != 202 || report.SchemaVersion != 202 {
		t.Fatalf("upgrade report = %+v, want migrations 200, 201 and 202", report)
	}
}

func TestSchemaFloorUpgradeRollbackAndRecovery(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	path := schemaFloorFixture199(t)
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER floor_test_abort BEFORE INSERT ON schema_migrations WHEN NEW.version=201 BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(ctx, path, nil); err == nil {
		t.Fatal("upgrade unexpectedly succeeded")
	}
	db, err = sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('workflow_contract_law_modifications','runtime_state_writers')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rollback retained %d of 2 tables", count)
	}
	if got := manifestMax(t, path); got != 199 {
		t.Fatalf("rollback manifest = %d, want 199", got)
	}
	if _, err := db.Exec(`DROP TRIGGER floor_test_abort`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Upgrade(ctx, path, nil); err != nil {
		t.Fatalf("upgrade after repair: %v", err)
	}
}

func TestSchemaFloorRefusesBackupAndUpgrade(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES(198,'old','old','time')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnsupported := func(err error) {
		t.Helper()
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindSchemaUnsupported {
			t.Fatalf("error = %v, want %s", err, KindSchemaUnsupported)
		}
	}
	_, err = PlanUpgradeReadiness(ctx, path)
	assertUnsupported(err)
	_, err = Upgrade(ctx, path, nil)
	assertUnsupported(err)
	destination := filepath.Join(dir, "backup.db")
	started := false
	backupCtx := context.WithValue(ctx, backupStartedContextKey{}, func() { started = true })
	_, err = BackupDatabase(backupCtx, path, destination)
	assertUnsupported(err)
	if started {
		t.Fatal("backup copied the store before refusing it")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("backup destination exists or cannot be inspected: %v", err)
	}
}

func TestSchemaFloorRetainedChecksumsAndAdmission(t *testing.T) {
	applied := make(map[int]appliedMigration)
	for _, m := range migrations {
		if want, ok := migrationChecksumPins[m.Version]; !ok || m.checksum() != want {
			t.Fatalf("migration %d checksum = %s, pin = %s", m.Version, m.checksum(), want)
		}
		applied[m.Version] = appliedMigration{Checksum: m.checksum(), Breaking: m.Breaking}
	}
	applied[1] = appliedMigration{Checksum: "retired", Breaking: true}
	if err := checkManifest(applied); err != nil {
		t.Fatalf("retired manifest entry refused: %v", err)
	}
	applied[203] = appliedMigration{Checksum: "future", Breaking: false}
	if err := checkManifest(applied); err != nil {
		t.Fatalf("additive future entry refused: %v", err)
	}
	applied[203] = appliedMigration{Checksum: "future", Breaking: true}
	assertFailureKind(t, checkManifest(applied), KindSchemaUnsupported)
	delete(applied, 203)
	applied[200] = appliedMigration{Checksum: "forged", Breaking: true}
	assertFailureKind(t, checkManifest(applied), KindSchemaDrift)
	delete(applied, 200)
	assertFailureKind(t, checkManifest(applied), KindSchemaDrift)
}
