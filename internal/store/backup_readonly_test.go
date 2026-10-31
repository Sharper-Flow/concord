package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Compile-time proof that the generalized read owner accepts both a pool and
// one held connection without any caller change.
var (
	_ beginTxSource = (*sql.DB)(nil)
	_ beginTxSource = (*sql.Conn)(nil)
)

// TestBeginReadTxPinsSnapshotOnHeldConnection proves the read-transaction
// owner pins a deferred snapshot through one held connection of a
// one-connection pool: a concurrent writer commits, the open transaction
// keeps its first snapshot, and reads after rollback see the commit.
func TestBeginReadTxPinsSnapshotOnHeldConnection(t *testing.T) {
	fixture := copyTestDatabase(t)
	ctx := context.Background()
	source, err := sql.Open(driverName, readOnlyDataSource(fixture))
	if err != nil {
		t.Fatal(err)
	}
	source.SetMaxOpenConns(1)
	defer func() { _ = source.Close() }()
	conn, err := source.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := beginReadTx(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var pinned int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&pinned); err != nil {
		t.Fatal(err)
	}

	// A separate writer commits while the pool's single connection is held
	// by the pinned read transaction.
	writer, err := sql.Open(driverName, dataSourceName(fixture))
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	defer func() { _ = writer.Close() }()
	writeTx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writeTx.Rollback()
	probe := testEvent()
	probe.EventID = "begin-read-probe"
	if _, err := AppendEvent(ctx, writeTx, probe); err != nil {
		t.Fatal(err)
	}
	if err := writeTx.Commit(); err != nil {
		t.Fatalf("writer commit under a pinned read snapshot: %v", err)
	}
	var still int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&still); err != nil {
		t.Fatal(err)
	}
	if still != pinned {
		t.Fatalf("pinned snapshot moved: %d events, want %d", still, pinned)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var released int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released != pinned+1 {
		t.Fatalf("read after rollback = %d events, want %d with the committed probe event", released, pinned+1)
	}
}

// TestRunOnlineRestoreInterruptedReleasesSourceSnapshot proves an interrupted
// restore fails typed, never reaches the staging file, and releases every
// source handle: a later TRUNCATE checkpoint finds no WAL read mark held.
func TestRunOnlineRestoreInterruptedReleasesSourceSnapshot(t *testing.T) {
	fixture := copyTestDatabase(t)
	source := copyClosedTestDatabase(t, fixture)
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := os.WriteFile(stage, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runOnlineRestore(ctx, source, stage)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable || !failure.RetrySafe {
		t.Fatalf("runOnlineRestore() error = %v, want typed retry-safe KindUnavailable interruption", err)
	}
	// The driver never reached the staging file: it stays the empty file the
	// caller staged, with no sidecars.
	info, statErr := os.Stat(stage)
	if statErr != nil || info.Size() != 0 {
		t.Fatalf("interrupted staging file = %v (stat err=%v), want the empty staged file", info, statErr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(stage + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("interrupted restore created stage%s: %v", suffix, err)
		}
	}
	assertNoSourceReaderHolds(t, source)
}

// stageWatchCancelContext cancels the underlying context the first time its
// Done is consulted after the driver created the staging file. Synchronous
// driver calls ignore the context, so the staging file's existence is the
// deterministic signal that NewBackup initialized the destination; the
// cancellation then fires at the copy loop's first check.
type stageWatchCancelContext struct {
	context.Context
	cancel context.CancelFunc
	stage  string
}

func (c *stageWatchCancelContext) Done() <-chan struct{} {
	if _, err := os.Stat(c.stage); err == nil {
		c.cancel()
	}
	return c.Context.Done()
}

// TestRunOnlineRestoreCancelsAfterDriverInitializesStage proves cancellation
// during the active copy lifetime, after driver initialization but before a
// page batch: the interruption is typed and wraps context.Canceled, the
// initialized stage stays behind with its destination handle closed, and the
// source read transaction is released.
func TestRunOnlineRestoreCancelsAfterDriverInitializesStage(t *testing.T) {
	fixture := copyTestDatabase(t)
	source := copyClosedTestDatabase(t, fixture)
	stage := filepath.Join(t.TempDir(), "stage.db")
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &stageWatchCancelContext{Context: parent, cancel: cancel, stage: stage}
	err := runOnlineRestore(ctx, source, stage)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Detail != "online restore was interrupted" {
		t.Fatalf("runOnlineRestore() error = %v, want the typed interruption after driver initialization", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runOnlineRestore() error = %v, want a wrapped context.Canceled", err)
	}
	// The driver initialized the destination before the cancellation fired.
	if _, statErr := os.Stat(stage); statErr != nil {
		t.Fatalf("staging file after interrupted restore: %v", statErr)
	}
	// Finish-once closed the driver-owned destination: no WAL sidecars remain.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(stage + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("interrupted restore left stage%s behind: %v", suffix, err)
		}
	}
	// The source read transaction is released despite the cancellation.
	assertNoSourceReaderHolds(t, source)
}

// TestRunOnlineRestoreEndsSnapshotBeforeStageVerification proves a direct
// source-owned copy succeeds, closes the driver destination and releases the
// source snapshot before any stage verification work, and leaves a staging
// copy equal to its source.
func TestRunOnlineRestoreEndsSnapshotBeforeStageVerification(t *testing.T) {
	fixture := copyTestDatabase(t)
	source := copyClosedTestDatabase(t, fixture)
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := runOnlineRestore(context.Background(), source, stage); err != nil {
		t.Fatalf("runOnlineRestore() error = %v", err)
	}
	// The driver-owned destination must be closed and clean when the helper
	// returns, before any stage open or probe.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(stage + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("runOnlineRestore left stage%s behind: %v", suffix, err)
		}
	}
	// Source identity before any intentionally mutating probe.
	if after, err := fileSHA256(source); err != nil {
		t.Fatalf("source checksum after restore: %v", err)
	} else if before, err := fileSHA256(fixture); err != nil || after != before {
		t.Fatalf("source bytes changed during restore: after=%q before=%q (err=%v)", after, before, err)
	}
	// Capture the source manifest before the release probe, which writes.
	sourceManifest, err := verifyBackupFile(context.Background(), source, CurrentSchemaVersion(), nil)
	if err != nil {
		t.Fatalf("verify source: %v", err)
	}
	// The source read transaction ended before any stage verification work:
	// a still-open snapshot would hold a WAL read mark and report busy.
	assertNoSourceReaderHolds(t, source)
	verified, err := verifyBackupFile(context.Background(), stage, CurrentSchemaVersion(), nil)
	if err != nil {
		t.Fatalf("verify restored stage: %v", err)
	}
	// A backup-finished destination carries its own header change counter,
	// so the stage is not byte-identical. Assert the production equality
	// fields that verifyRestoredStage compares, against the pre-probe
	// source manifest.
	if verified.SchemaVersion != sourceManifest.SchemaVersion || verified.EventWatermark != sourceManifest.EventWatermark || verified.IntegrityCheck != sourceManifest.IntegrityCheck || verified.QuickCheck != sourceManifest.QuickCheck || len(verified.ForeignKeyCheck) != 0 {
		t.Fatalf("restored stage = %+v, want source %+v", verified, sourceManifest)
	}
}

// sourceReaderBlocksCheckpoint appends a synthetic WAL frame to the source
// and then attempts a TRUNCATE checkpoint with busy_timeout=0. An empty WAL
// cannot distinguish a retained reader, so the write is what makes the
// checkpoint sensitive: an old read view below the new frames reports busy.
func sourceReaderBlocksCheckpoint(t *testing.T, source string) bool {
	t.Helper()
	probe, err := sql.Open(driverName, SQLiteFileURI(source)+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	probe.SetMaxOpenConns(1)
	defer func() { _ = probe.Close() }()
	if _, err := probe.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS store_probe_reader (n INTEGER); INSERT INTO store_probe_reader VALUES (1)`); err != nil {
		t.Fatalf("probe write: %v", err)
	}
	var busy, logPages, checkpointed int
	if err := probe.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logPages, &checkpointed); err != nil {
		t.Fatalf("probe checkpoint: %v", err)
	}
	return busy != 0
}

// assertNoSourceReaderHolds proves no restore handle still pins the source
// through an old WAL read view.
func assertNoSourceReaderHolds(t *testing.T, source string) {
	t.Helper()
	if sourceReaderBlocksCheckpoint(t, source) {
		t.Fatal("restore left a source reader behind: TRUNCATE checkpoint stayed busy after a synthetic WAL write")
	}
}

// TestSourceReaderProbeDetectsPinnedReader proves the release probe detects
// a retained transaction instead of passing vacuously: a pinned read
// transaction blocks the checkpoint until rollback releases it.
func TestSourceReaderProbeDetectsPinnedReader(t *testing.T) {
	fixture := copyTestDatabase(t)
	source := copyClosedTestDatabase(t, fixture)
	ctx := context.Background()
	reader, err := sql.Open(driverName, readOnlyDataSource(source))
	if err != nil {
		t.Fatal(err)
	}
	reader.SetMaxOpenConns(1)
	defer func() { _ = reader.Close() }()
	conn, err := reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := beginReadTx(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	// Release the snapshot even when a probe step below fails the test, so
	// the deferred connection close cannot outlive an open transaction.
	defer func() { _ = tx.Rollback() }()
	var events int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if !sourceReaderBlocksCheckpoint(t, source) {
		t.Fatal("probe reported no reader while a read transaction held the source")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if sourceReaderBlocksCheckpoint(t, source) {
		t.Fatal("probe reported a retained reader after rollback released the source")
	}
}

type backupVerificationMark struct {
	ordinal    int
	path       string
	elapsed    time.Duration
	remaining  time.Duration
	contextErr error
}

type backupVerificationTrace struct {
	started  time.Time
	deadline time.Time
	marks    []backupVerificationMark
}

func (trace *backupVerificationTrace) record(ctx context.Context, path string, now time.Time) {
	trace.marks = append(trace.marks, backupVerificationMark{
		ordinal: len(trace.marks) + 1, path: path,
		elapsed: now.Sub(trace.started), remaining: trace.deadline.Sub(now), contextErr: ctx.Err(),
	})
}

func (trace *backupVerificationTrace) report() string {
	if len(trace.marks) == 0 {
		return "no backup verifier entries\n"
	}
	var report strings.Builder
	for _, mark := range trace.marks {
		fmt.Fprintf(&report, "backup verifier entry %d: path=%q elapsed=%s remaining=%s context_error=%v\n",
			mark.ordinal, mark.path, mark.elapsed, mark.remaining, mark.contextErr)
	}
	return report.String()
}

func TestBackupDatabaseVerificationEntryObserver(t *testing.T) {
	source := copyTestDatabase(t)
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	var entries []string
	ctx := context.WithValue(context.Background(), backupVerificationEntryContextKey{}, func(path string) {
		entries = append(entries, path)
	})
	if _, err := BackupDatabase(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("verifier entries = %q, want destination, disposable clone, destination", entries)
	}
	if entries[0] != destination || entries[2] != destination {
		t.Fatalf("verifier entries = %q, want destination at entries 1 and 3", entries)
	}
	clone := entries[1]
	if clone == destination || filepath.Dir(clone) != filepath.Dir(destination) || !strings.HasPrefix(filepath.Base(clone), ".concord-backup-verify-") {
		t.Fatalf("clone entry = %q, want a disposable backup-verification clone beside %q", clone, destination)
	}
	if _, err := os.Stat(clone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disposable clone after backup: %v", err)
	}
	if _, err := VerifyBackup(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("plain-context verification added an entry: %q", entries)
	}
}

func TestBackupVerificationTraceReport(t *testing.T) {
	started := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	deadline := started.Add(2 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	trace := backupVerificationTrace{started: started, deadline: deadline}
	if got := trace.report(); got != "no backup verifier entries\n" {
		t.Fatalf("empty report = %q", got)
	}
	trace.record(ctx, "snapshot.db", started.Add(2250*time.Millisecond))
	trace.record(ctx, "clone.db", started.Add(2500*time.Millisecond))
	want := "backup verifier entry 1: path=\"snapshot.db\" elapsed=2.25s remaining=-250ms context_error=context deadline exceeded\n" +
		"backup verifier entry 2: path=\"clone.db\" elapsed=2.5s remaining=-500ms context_error=context deadline exceeded\n"
	if got := trace.report(); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}

// TestBackupDatabaseReadOnlyWALBounded holds the backup path to the same
// source-snapshot contract as the restore path: BackupDatabase from a
// WAL-mode 0444 source larger than one batch must complete within a
// two-second context, with or without pre-existing read-only sidecars, and a
// failure must clean the snapshot and manifest. The child-process bound
// mirrors the restore regression because synchronous driver calls ignore the
// caller context.
func TestBackupDatabaseReadOnlyWALBounded(t *testing.T) {
	const childModeEnv = "TEST_CONCORD_BACKUP_WAL_MODE"
	mode := os.Getenv(childModeEnv)
	if mode == "" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"0444", "0444-sidecars"} {
			t.Run(mode, func(t *testing.T) {
				// Raw driver calls need a process bound if they ignore the context.
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestBackupDatabaseReadOnlyWALBounded$", "-test.v", "-test.timeout=10s")
				cmd.Env = append(os.Environ(), childModeEnv+"="+mode)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("backup of WAL source mode %s: %v (process context: %v)\n%s", mode, err, ctx.Err(), output)
				}
				t.Logf("%s", output)
			})
		}
		return
	}

	fixture := copyTestDatabase(t)
	ctx := context.Background()
	manifest, err := verifyBackupFile(ctx, fixture, CurrentSchemaVersion(), nil)
	if err != nil {
		t.Fatal(err)
	}
	source := copyClosedTestDatabase(t, fixture)
	header, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) < 32 || header[18] != 2 || header[19] != 2 {
		t.Fatal("source fixture is not a WAL-mode database")
	}
	if pages := binary.BigEndian.Uint32(header[28:32]); pages <= uint32(backupStepPages) {
		t.Fatalf("source has %d pages, need more than %d to exercise incremental backup", pages, backupStepPages)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(source + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source%s must be absent before backup: %v", suffix, err)
		}
	}
	if err := os.Chmod(source, 0o444); err != nil {
		t.Fatal(err)
	}
	if mode == "0444-sidecars" {
		// Recreate the restore setup: the read-only verification open leaves
		// mode-0444 sidecars behind, so the backup meets a source whose WAL
		// index was already opened read-only rather than a fresh read.
		if _, err := verifyBackupFile(ctx, source, CurrentSchemaVersion(), nil); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if info, err := os.Stat(source + suffix); err != nil || info.Mode().Perm() != 0o444 {
				t.Fatalf("source%s must exist with mode 0444: info=%v err=%v", suffix, info, err)
			}
		}
	}

	dir := t.TempDir()
	destination := filepath.Join(dir, "snapshot.db")
	backupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	deadline, _ := backupCtx.Deadline()
	trace := backupVerificationTrace{started: deadline.Add(-2 * time.Second), deadline: deadline}
	backupCtx = context.WithValue(backupCtx, backupVerificationEntryContextKey{}, func(path string) {
		trace.record(backupCtx, path, time.Now())
	})
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("backup verifier entries (copy/verification boundaries):\n%s", trace.report())
		}
	})
	got, err := BackupDatabase(backupCtx, source, destination)
	// Source identity holds in either outcome.
	if after, statErr := fileSHA256(source); statErr != nil || after != manifest.DBChecksum {
		t.Fatalf("source checksum after backup = %q (err=%v), want %q", after, statErr, manifest.DBChecksum)
	}
	if info, statErr := os.Stat(source); statErr != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("source mode changed during backup: info=%v err=%v", info, statErr)
	}
	if err != nil {
		// A failed backup leaves neither the snapshot nor its manifest.
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed backup destination must be absent: %v", statErr)
		}
		if _, statErr := os.Stat(manifestPath(destination)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed backup manifest must be absent: %v", statErr)
		}
		t.Fatalf("backup source mode 0444 (%s): %v", mode, err)
	}
	// A completed backup must pass the backup verb's verification rules.
	verified, err := VerifyBackup(ctx, destination)
	if err != nil {
		t.Fatalf("verify backup destination: %v", err)
	}
	if verified.DBChecksum != got.DBChecksum || verified.SchemaVersion != got.SchemaVersion || verified.EventWatermark != got.EventWatermark || verified.SnapshotID != got.SnapshotID {
		t.Fatalf("verified manifest = %+v, original = %+v", verified, got)
	}
}

// TestInterruptedBackupAfterInitReleasesSourceSnapshot proves cancellation
// during the active copy lifetime: the initialization seam cancels the
// caller context once the operation exists, the typed interruption returns,
// the partial snapshot and manifest are cleaned, and the pinned source
// transaction releases the store's connection before the store serves
// another read.
func TestInterruptedBackupAfterInitReleasesSourceSnapshot(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	destination := filepath.Join(t.TempDir(), "interrupted-active.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	ctx = context.WithValue(ctx, backupStartedContextKey{}, func() {
		close(started)
		cancel()
	})
	_, err := Backup(ctx, s, destination)
	select {
	case <-started:
	default:
		t.Fatal("backup initialization never ran; the cancellation did not come from the active copy")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable || failure.Detail != "online backup was interrupted" {
		t.Fatalf("interrupted Backup() error = %v, want the typed interruption", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("interrupted snapshot stat error = %v", statErr)
	}
	if _, statErr := os.Stat(manifestPath(destination)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("interrupted manifest stat error = %v", statErr)
	}
	// The pinned source transaction released the store connection: the
	// sensitive probe must find no retained WAL reader on the store file.
	if sourceReaderBlocksCheckpoint(t, s.Path()) {
		t.Fatal("backup left a source reader behind on the store database")
	}
	// The store still serves reads on its pool.
	var events int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM effect_receipts`).Scan(&events); err != nil {
		t.Fatalf("store read after interrupted backup: %v", err)
	}
}

func TestRestoreBackupReadOnlyWALBounded(t *testing.T) {
	const childModeEnv = "TEST_CONCORD_RESTORE_WAL_MODE"
	mode := os.Getenv(childModeEnv)
	if mode == "" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"0600", "0444", "0444-sidecars"} {
			t.Run(mode, func(t *testing.T) {
				// Raw driver calls need a process bound if they ignore the context.
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRestoreBackupReadOnlyWALBounded$", "-test.v", "-test.timeout=10s")
				cmd.Env = append(os.Environ(), childModeEnv+"="+mode)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("restore of WAL source mode %s: %v (process context: %v)\n%s", mode, err, ctx.Err(), output)
				}
				t.Logf("%s", output)
			})
		}
		return
	}

	fixture := copyTestDatabase(t)
	ctx := context.Background()
	manifest, err := verifyBackupFile(ctx, fixture, CurrentSchemaVersion(), nil)
	if err != nil {
		t.Fatal(err)
	}
	source := copyClosedTestDatabase(t, fixture)
	manifest.SnapshotID = "synthetic-readonly-wal"
	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeBackupManifest(source, manifest); err != nil {
		t.Fatal(err)
	}
	header, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) < 32 || header[18] != 2 || header[19] != 2 {
		t.Fatal("source fixture is not a WAL-mode database")
	}
	if pages := binary.BigEndian.Uint32(header[28:32]); pages <= uint32(backupStepPages) {
		t.Fatalf("source has %d pages, need more than %d to exercise incremental restore", pages, backupStepPages)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(source + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source%s must be absent before restore: %v", suffix, err)
		}
	}
	permission := os.FileMode(0o600)
	if mode != "0600" {
		permission = 0o444
	}
	if err := os.Chmod(source, permission); err != nil {
		t.Fatal(err)
	}
	if mode == "0444-sidecars" {
		if _, err := verifyBackupFile(ctx, source, CurrentSchemaVersion(), nil); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if info, err := os.Stat(source + suffix); err != nil || info.Mode().Perm() != 0o444 {
				t.Fatalf("source%s must exist with mode 0444: info=%v err=%v", suffix, info, err)
			}
		}
	}
	restoreCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	destination := filepath.Join(t.TempDir(), "restored.db")
	t.Logf("restore begin: source mode=%04o, WAL header=2/2, pre-existing sidecars=%t", permission, mode == "0444-sidecars")
	got, err := RestoreBackup(restoreCtx, source, destination)
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".concord-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("restore staging files = %v (err=%v)", matches, err)
	}
	if after, err := fileSHA256(source); err != nil || after != manifest.DBChecksum {
		t.Fatalf("source checksum after restore = %q (err=%v), want %q", after, err, manifest.DBChecksum)
	}
	if info, statErr := os.Stat(source); statErr != nil || info.Mode().Perm() != permission {
		t.Fatalf("source mode changed after restore: info=%v err=%v", info, statErr)
	}
	if err != nil {
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed restore destination must be absent: %v", statErr)
		}
		t.Fatalf("restore source mode %04o: %v", permission, err)
	}
	if got.SourceSnapshot.DBChecksum != manifest.DBChecksum || got.SourceSnapshot.SchemaVersion != manifest.SchemaVersion || got.SourceSnapshot.EventWatermark != manifest.EventWatermark {
		t.Fatalf("restored manifest = %+v, want %+v", got, manifest)
	}
	if _, err := verifyBackupFile(ctx, destination, CurrentSchemaVersion(), nil); err != nil {
		t.Fatalf("verify restored destination: %v", err)
	}
}
