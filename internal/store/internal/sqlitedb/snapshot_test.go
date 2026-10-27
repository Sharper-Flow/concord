package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

func snapshotTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source%?#.db")
	db, err := Open(DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE sample(value); INSERT INTO sample VALUES('state')`); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func assertSnapshotFailure(t *testing.T, err error, op, detail string, retry bool, recovery string, cause error) {
	t.Helper()
	f, ok := err.(*Error)
	if !ok || f.Kind != Unavailable || f.Op != op || f.Detail != detail || f.RetrySafe != retry || f.RecoveryAction != recovery || f.EffectPossible {
		t.Fatalf("mechanism error = %#v, want unavailable/%s/%s/%t/%s with no possible effect", err, op, detail, retry, recovery)
	}
	if cause == nil {
		if f.Err != nil {
			t.Fatalf("unexpected cause: %v", f.Err)
		}
	} else if !errors.Is(err, cause) {
		t.Fatalf("cause = %v, want %v", err, cause)
	}
}

func assertSnapshotConnectionReleased(t *testing.T, db *sql.DB, conn *sql.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("consumed connection Ping = %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("source pool remains held: %v", err)
	}
	// Add later WAL frames without changing the final state being fingerprinted.
	if _, err := db.ExecContext(ctx, `INSERT INTO sample VALUES('release probe'); DELETE FROM sample WHERE value='release probe'`); err != nil {
		t.Fatal(err)
	}
	var busy, log, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil || busy != 0 || log != 0 || checkpointed != 0 {
		t.Fatalf("source snapshot remains pinned: %d/%d/%d, %v", busy, log, checkpointed, err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	db, source := snapshotTestDB(t)
	// More than one batch exercises the held snapshot across incremental steps.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sample VALUES(zeroblob(512000))`); err != nil {
		t.Fatal(err)
	}
	var pages int32
	if err := db.QueryRowContext(t.Context(), `PRAGMA page_count`).Scan(&pages); err != nil || pages <= BackupStepPages || BackupStepPages != 100 {
		t.Fatalf("page count = %d, batch = %d, err = %v", pages, BackupStepPages, err)
	}
	want, err := CurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	notifications := 0
	ctx := context.WithValue(t.Context(), BackupStartedContextKey{}, func() { notifications++ })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot%?#.db")
	if err := BackupSnapshot(ctx, conn, snapshot); err != nil {
		t.Fatal(err)
	}
	assertSnapshotConnectionReleased(t, db, conn)
	stage := filepath.Join(t.TempDir(), "stage%?#.db")
	if err := RunOnlineRestore(ctx, snapshot, stage); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(stage + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore left destination handle sidecar %s: %v", suffix, err)
		}
	}
	if notifications != 1 {
		t.Fatalf("live backup notifications = %d, want one and none from restore", notifications)
	}
	// The image carries the source WAL header even though backup uses a bare URI.
	for _, tc := range []struct{ path, journal string }{{source, "wal"}, {snapshot, "wal"}, {stage, "wal"}} {
		t.Run(tc.journal+filepath.Base(tc.path), func(t *testing.T) {
			copyDB, err := Open(ReadOnlyDataSource(tc.path))
			if err != nil {
				t.Fatal(err)
			}
			defer copyDB.Close()
			if got, err := CurrentStateDigest(t.Context(), copyDB); err != nil || got != want {
				t.Fatalf("state = %q, %v; want %q", got, err, want)
			}
			integrity, quick, foreign, err := VerifySQLiteTriple(t.Context(), copyDB)
			if err != nil || integrity != "ok" || quick != "ok" || foreign == nil || len(foreign) != 0 {
				t.Fatalf("verification = %q/%q/%v, %v", integrity, quick, foreign, err)
			}
			var journal string
			if err := copyDB.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != tc.journal {
				t.Fatalf("journal = %q, %v; want %q", journal, err, tc.journal)
			}
		})
	}
}

func TestSnapshotBackupPinsSourceAcrossWriterCommit(t *testing.T) {
	db, source := snapshotTestDB(t)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sample VALUES(zeroblob(512000))`); err != nil {
		t.Fatal(err)
	}
	want, err := CurrentStateDigest(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(DSN(source))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var writeErr error
	ctx := context.WithValue(t.Context(), BackupStartedContextKey{}, func() {
		_, writeErr = writer.ExecContext(t.Context(), `INSERT INTO sample VALUES('later commit')`)
	})
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "pinned.db")
	if err := BackupSnapshot(ctx, conn, snapshot); err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatalf("writer under pinned read: %v", writeErr)
	}
	if got, err := CurrentStateDigest(t.Context(), db); err != nil || got == want {
		t.Fatalf("source did not advance: %q, %v", got, err)
	}
	copyDB, err := Open(ReadOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	if got, err := CurrentStateDigest(t.Context(), copyDB); err != nil || got != want {
		t.Fatalf("copy moved beyond pinned state: %q, %v; want %q", got, err, want)
	}
}

func TestSnapshotBackupCancellationReleasesConnection(t *testing.T) {
	db, _ := snapshotTestDB(t)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications := 0
	ctx = context.WithValue(ctx, BackupStartedContextKey{}, func() { notifications++; cancel() })
	err = BackupSnapshot(ctx, conn, filepath.Join(t.TempDir(), "partial.db"))
	assertSnapshotFailure(t, err, "backup", "online backup was interrupted", true, "discard the partial snapshot and retry", context.Canceled)
	if notifications != 1 {
		t.Fatalf("notifications = %d, want one after initialization", notifications)
	}
	assertSnapshotConnectionReleased(t, db, conn)
}

func TestSnapshotBackupPinFailureFields(t *testing.T) {
	db, _ := snapshotTestDB(t)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	err = BackupSnapshot(t.Context(), conn, filepath.Join(t.TempDir(), "snapshot.db"))
	assertSnapshotFailure(t, err, "backup", "cannot pin a backup source snapshot", true, "retry once the database is readable", sql.ErrConnDone)
	assertSnapshotConnectionReleased(t, db, conn)
}

type snapshotInitFailure struct {
	dsn string
	err error
}

func (s *snapshotInitFailure) NewBackup(dsn string) (*sqlite.Backup, error) {
	s.dsn = dsn
	return nil, s.err
}

func TestSnapshotCopyFailureFieldsAndDSNs(t *testing.T) {
	cause := errors.New("synthetic initialization failure")
	path := "/synthetic/snapshot%?#.db"
	notifications := 0
	ctx := context.WithValue(t.Context(), BackupStartedContextKey{}, func() { notifications++ })
	for _, tc := range []struct {
		op, dsn, detail, recovery string
		copy                      func(context.Context, any, string) error
	}{
		{"backup", FileURI(path), "cannot initialize SQLite online backup", "retry from a writable local filesystem", copyOnlineBackup},
		{"restore_backup", DSN(path), "cannot initialize SQLite online restore", "retry from a readable verified snapshot", copyOnlineRestore},
	} {
		t.Run(tc.op, func(t *testing.T) {
			err := tc.copy(ctx, struct{}{}, path)
			assertSnapshotFailure(t, err, tc.op, "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver", nil)
			driver := &snapshotInitFailure{err: cause}
			err = tc.copy(ctx, driver, path)
			assertSnapshotFailure(t, err, tc.op, tc.detail, true, tc.recovery, cause)
			if driver.dsn != tc.dsn {
				t.Fatalf("DSN = %q, want %q", driver.dsn, tc.dsn)
			}
		})
	}
	if notifications != 0 {
		t.Fatalf("failed initialization fired %d notifications", notifications)
	}
}

func TestSnapshotRestoreCancellationAndInitFailure(t *testing.T) {
	db, source := snapshotTestDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := RunOnlineRestore(ctx, source, filepath.Join(t.TempDir(), "cancelled.db"))
	assertSnapshotFailure(t, err, "restore_backup", "cannot acquire restore source connection", true, "retry the restore", context.Canceled)
	err = RunOnlineRestore(t.Context(), source, filepath.Join(t.TempDir(), "missing", "stage.db"))
	var driverErr *sqlite.Error
	if !errors.As(err, &driverErr) {
		t.Fatalf("missing SQLite cause: %v", err)
	}
	assertSnapshotFailure(t, err, "restore_backup", "cannot initialize SQLite online restore", true, "retry from a readable verified snapshot", driverErr)
	notifications := 0
	partial := filepath.Join(t.TempDir(), "partial.db")
	parent, stop := context.WithCancel(t.Context())
	defer stop()
	ctx = &snapshotStageCancelContext{Context: parent, stage: partial, cancel: stop}
	ctx = context.WithValue(ctx, BackupStartedContextKey{}, func() { notifications++ })
	err = RunOnlineRestore(ctx, source, partial)
	assertSnapshotFailure(t, err, "restore_backup", "online restore was interrupted", true, "discard the staging database and retry", context.Canceled)
	if notifications != 0 {
		t.Fatal("restore fired the live-backup notification")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(partial + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("interrupted restore left destination handle sidecar %s: %v", suffix, err)
		}
	}
	// A later WAL write makes the checkpoint sensitive to a retained read mark.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sample VALUES('release probe')`); err != nil {
		t.Fatal(err)
	}
	var busy, log, checkpointed int
	if err := db.QueryRowContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil || busy != 0 || log != 0 || checkpointed != 0 {
		t.Fatalf("restore retained a reader: %d/%d/%d, %v", busy, log, checkpointed, err)
	}
}

// Observe driver initialization without adding a restore notification mechanism.
type snapshotStageCancelContext struct {
	context.Context
	stage  string
	cancel context.CancelFunc
}

func (c *snapshotStageCancelContext) Done() <-chan struct{} {
	if _, err := os.Stat(c.stage); err == nil {
		c.cancel()
	}
	return c.Context.Done()
}

type snapshotObservedContext struct {
	context.Context
	done                chan struct{}
	doneCalls, errCalls int
}

func (c *snapshotObservedContext) Done() <-chan struct{} { c.doneCalls++; return c.done }
func (c *snapshotObservedContext) Err() error            { c.errCalls++; return context.Canceled }

func TestSnapshotContextErrConsultsDone(t *testing.T) {
	if err := ContextErr(nil); err == nil || err.Error() != "nil context" {
		t.Fatalf("nil context = %v", err)
	}
	ctx := &snapshotObservedContext{Context: t.Context(), done: make(chan struct{})}
	if err := ContextErr(ctx); err != nil || ctx.doneCalls != 1 || ctx.errCalls != 0 {
		t.Fatalf("open context = %v, Done/Err calls = %d/%d", err, ctx.doneCalls, ctx.errCalls)
	}
	close(ctx.done)
	if err := ContextErr(ctx); !errors.Is(err, context.Canceled) || ctx.doneCalls != 2 || ctx.errCalls != 1 {
		t.Fatalf("closed context = %v, Done/Err calls = %d/%d", err, ctx.doneCalls, ctx.errCalls)
	}
}
