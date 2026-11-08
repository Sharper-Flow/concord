package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestCompactStateComparesRowsAndRowids(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		match        bool
	}{
		{"same", "", true},
		{"implicit rowid", `UPDATE naked SET rowid=19 WHERE rowid=7`, false},
		{"shadowed rowid", `UPDATE shadowed SET _rowid_=19 WHERE _rowid_=7`, false},
		{"declared key", `UPDATE keyed SET value='changed' WHERE id=7`, false},
		{"without rowid", `UPDATE keyless SET value='changed' WHERE id='a'`, false},
		{"extra row", `INSERT INTO naked(rowid,value) VALUES(30,'extra')`, false},
		{"sequence", `UPDATE sqlite_sequence SET seq=99 WHERE name='sequenced'`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			left, err := sqlitedb.Open(sqlitedb.DSN(filepath.Join(t.TempDir(), "left.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer left.Close()
			right, err := sqlitedb.Open(sqlitedb.DSN(filepath.Join(t.TempDir(), "right.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer right.Close()
			const fixture = `CREATE TABLE naked(value); INSERT INTO naked(rowid,value) VALUES(7,'same');
			CREATE TABLE shadowed(rowid TEXT, value TEXT); INSERT INTO shadowed(_rowid_,rowid,value) VALUES(7,'shadow','same');
			CREATE TABLE keyed(id INTEGER PRIMARY KEY,value); INSERT INTO keyed VALUES(7,'same');
			CREATE TABLE keyless(id TEXT PRIMARY KEY,value) WITHOUT ROWID; INSERT INTO keyless VALUES('a','same');
			CREATE TABLE sequenced(id INTEGER PRIMARY KEY AUTOINCREMENT, value); INSERT INTO sequenced(value) VALUES('same');`
			for _, db := range []*sql.DB{left, right} {
				if _, err := db.ExecContext(ctx, fixture); err != nil {
					t.Fatal(err)
				}
			}
			if tc.change != "" {
				if _, err := right.ExecContext(ctx, tc.change); err != nil {
					t.Fatal(err)
				}
			}
			err = compareCompactState(ctx, left, right)
			if (err == nil) != tc.match {
				t.Fatalf("comparison error = %v, match = %v", err, tc.match)
			}
		})
	}
}

func TestCompactReclaimsPagesAndPreservesRows(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	db, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE compact_fixture(value); INSERT INTO compact_fixture(rowid,value) VALUES(7,'keep'),(15,zeroblob(1500000)); DELETE FROM compact_fixture WHERE rowid=15; PRAGMA application_id=12345; PRAGMA user_version=54321`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Compact(ctx, path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.WriteBackStarted || report.RequiredBytes < uint64(report.After.Bytes) || report.AvailableBytes < report.RequiredBytes || report.Before.FreePages == 0 || report.After.FreePages != 0 || report.After.Bytes >= report.Before.Bytes || report.After.ApplicationID != 12345 || report.After.UserVersion != 54321 || report.After.PageSize != report.Before.PageSize || report.After.JournalMode != "wal" {
		t.Fatalf("compact report = %+v", report)
	}
	afterInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("target inode changed: %v", err)
	}
	db, err = sqlitedb.Open(sqlitedb.ReadOnlyDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRowContext(ctx, `SELECT value FROM compact_fixture WHERE rowid=7`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("rowid 7 = %q: %v", value, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Compact(ctx, path, snapshot)
	if err != nil || second.WriteBackStarted || second.Before.FreePages != 0 {
		t.Fatalf("already compact store = %+v: %v", second, err)
	}
}

func TestCompactRefusesUnverifiedOrChangedSnapshotBeforeMutation(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			ctx := context.Background()
			path := copyTestDatabase(t)
			snapshot := filepath.Join(t.TempDir(), "snapshot.db")
			if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if err := os.WriteFile(snapshot+".manifest.json", []byte("corrupt"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := sqlitedb.Open(sqlitedb.DSN(path))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `CREATE TABLE compact_changed(value)`); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			report, err := Compact(ctx, path, snapshot)
			if err == nil || report.WriteBackStarted {
				t.Fatalf("compact = %+v, %v", report, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("target changed before write-back: %v", err)
			}
		})
	}
}

func TestCompactRefusesWhenScratchRestoreProofFails(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	proved := false
	ctx = context.WithValue(ctx, restoreStageReadyContextKey{}, func(string) { proved = true; cancel() })
	report, err := Compact(ctx, path, snapshot)
	if err == nil || report.WriteBackStarted || !proved {
		t.Fatalf("compact = %+v, proof reached = %t, error = %v", report, proved, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("target changed after failed proof: %v", err)
	}
}

func TestCompactRefusesStoreOpenByAnotherConnection(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	db, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE compact_fixture(value); INSERT INTO compact_fixture(rowid,value) VALUES(7,'keep'),(15,zeroblob(1500000)); DELETE FROM compact_fixture WHERE rowid=15`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// An unleased process that has read the store keeps a shared lock on it.
	other, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var value string
	if err := other.QueryRowContext(ctx, `SELECT value FROM compact_fixture WHERE rowid=7`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	report, err := Compact(ctx, path, snapshot)
	if err == nil || report.WriteBackStarted {
		t.Fatalf("compact = %+v, %v", report, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("target changed while another connection held it: %v", err)
	}
}

func TestCompactRefusesChangedImplicitRowidBeforeMutation(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	db, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE compact_rowid(value); INSERT INTO compact_rowid(rowid,value) VALUES(7,'same')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
		t.Fatal(err)
	}
	db, err = sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE compact_rowid SET rowid=19 WHERE rowid=7`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Compact(ctx, path, snapshot)
	if err == nil || report.WriteBackStarted {
		t.Fatalf("compact = %+v, %v", report, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("target changed before write-back: %v", err)
	}
}

func TestCompactFullSizeRehearsal(t *testing.T) {
	if os.Getenv("OC_COMPACT_REHEARSAL") != "1" {
		t.Skip("full-size disposable rehearsal is opt-in")
	}
	ctx := context.Background()
	path := copyTestDatabase(t)
	db, err := sqlitedb.Open(sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE rehearsal(value); WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<630) INSERT INTO rehearsal(rowid,value) SELECT n,zeroblob(1000000) FROM seq; DELETE FROM rehearsal WHERE rowid>400`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	started := time.Now()
	release, err := hostlease.AcquireMaintenance(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fence, err := hostlease.EnsureFence(filepath.Dir(path), hostlease.Fence{Operation: hostlease.FenceOperationCompact})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := hostlease.RemoveFenceOwned(filepath.Dir(path), fence.FenceID); err != nil {
			t.Error(err)
		}
	}()
	if _, err := BackupDatabase(ctx, path, snapshot); err != nil {
		t.Fatal(err)
	}
	snapshotTime := time.Since(started)
	report, err := Compact(ctx, path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.Bytes < 630000000 || report.Before.FreePages == 0 || report.After.FreePages != 0 || report.After.Bytes >= report.Before.Bytes {
		t.Fatalf("rehearsal = %+v", report)
	}
	t.Logf("maintenance_ms=%d snapshot_ms=%d report=%+v", time.Since(started).Milliseconds(), snapshotTime.Milliseconds(), report)
}

func TestCompactStateComparesStoreToVerifiedSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := Backup(ctx, s, snapshot); err != nil {
		t.Fatal(err)
	}
	snapshotDB, err := sqlitedb.Open(sqlitedb.ReadOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshotDB.Close()
	if err := compareCompactState(ctx, s.db, snapshotDB); err != nil {
		t.Fatalf("verified snapshot differs from store: %v", err)
	}
}

func TestCompactStateRefusesAllShadowedRowidNames(t *testing.T) {
	ctx := context.Background()
	left, err := sqlitedb.Open(sqlitedb.DSN(filepath.Join(t.TempDir(), "left.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := sqlitedb.Open(sqlitedb.DSN(filepath.Join(t.TempDir(), "right.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	for _, db := range []*sql.DB{left, right} {
		if _, err := db.ExecContext(ctx, `CREATE TABLE shadowed(rowid, _rowid_, oid, value)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := compareCompactState(ctx, left, right); err == nil {
		t.Fatal("comparison accepted an inaccessible implicit rowid")
	}
}
