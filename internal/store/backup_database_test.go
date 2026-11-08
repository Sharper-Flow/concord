package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestBackupDatabaseCompactsFreePagesWithoutChangingSource(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	path := s.Path()
	for _, statement := range []string{
		// No INTEGER PRIMARY KEY: VACUUM may renumber implicit rowids, and
		// the current-state digest does not cover them.
		`CREATE TABLE backup_compaction_fixture (payload BLOB NOT NULL)`,
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<200) INSERT INTO backup_compaction_fixture SELECT randomblob(4096) FROM n`,
		`DELETE FROM backup_compaction_fixture WHERE rowid NOT IN (3, 7, 150)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	freePages := func(path string) int {
		t.Helper()
		db, err := sqlitedb.Open(readOnlyDataSource(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var count int
		if err := db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	fixtureRows := func(path string) string {
		t.Helper()
		db, err := sqlitedb.Open(readOnlyDataSource(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var rows string
		if err := db.QueryRowContext(ctx, `SELECT group_concat(rowid || ':' || hex(payload), ',') FROM (SELECT rowid, payload FROM backup_compaction_fixture ORDER BY rowid)`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	sourceRows := fixtureRows(path)
	beforeFree := freePages(path)
	if beforeFree == 0 {
		t.Fatal("fixture has no free pages")
	}
	before, err := verifyBackupFile(ctx, path, CurrentSchemaVersion(), nil)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	manifest, err := BackupDatabase(ctx, path, destination)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".concord-backup-copy-") {
			t.Fatalf("backup left its full-size copy %q", entry.Name())
		}
	}
	verified, err := VerifyBackup(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	after, err := verifyBackupFile(ctx, path, CurrentSchemaVersion(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.DBChecksum != after.DBChecksum || beforeFree != freePages(path) || before.currentStateDigest != after.currentStateDigest || before.SchemaVersion != after.SchemaVersion {
		t.Fatal("backup changed the source database")
	}
	if verified.currentStateDigest != before.currentStateDigest || verified.SchemaVersion != before.SchemaVersion || verified.DBChecksum != manifest.DBChecksum {
		t.Fatal("snapshot state, schema, or checksum differs from its source or manifest")
	}
	if got := freePages(destination); got != 0 {
		t.Fatalf("snapshot contains %d free pages; want 0 (source has %d)", got, beforeFree)
	}
	if got := fixtureRows(destination); got != sourceRows {
		t.Fatal("snapshot changed implicit rowids or row content")
	}
	snapshot, err := sqlitedb.Open(readOnlyDataSource(destination))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var mode string
	if err := snapshot.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("snapshot journal mode = %q (err=%v); want the source's wal", mode, err)
	}
}

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
