package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
	"github.com/sharper-flow/concord/internal/version"
	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

const (
	backupStepPages    = int32(100)
	backupRetryLimit   = 100
	backupRetryBackoff = 50 * time.Millisecond
)

type backupStartedContextKey struct{}

type restoreStageReadyContextKey struct{}

func notifyBackupStarted(ctx context.Context) {
	if notify, ok := ctx.Value(backupStartedContextKey{}).(func()); ok {
		notify()
	}
}

func notifyRestoreStageReady(ctx context.Context, path string) {
	if notify, ok := ctx.Value(restoreStageReadyContextKey{}).(func(string)); ok {
		notify(path)
	}
}

// backupper is the small driver surface required by the Online Backup API.
// The source-owned NewBackup direction lets Concord pin the source read
// snapshot; the driver owns the destination handle the DSN names.
type backupper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

// BackupOptions supplies the non-SQLite authorities recorded in the manifest.
// GitCommitIDs is inventory metadata only.
type BackupOptions struct {
	SnapshotID   string
	GitCommitIDs map[string]string
}

// BackupManifest is verification metadata beside a SQLite snapshot. It is an
// inventory, never an authority.
type BackupManifest struct {
	SchemaVersion   int               `json:"schema_version"`
	SnapshotID      string            `json:"snapshot_id"`
	CreatedAt       string            `json:"created_at"`
	DBChecksum      string            `json:"db_checksum"`
	GitCommitIDs    map[string]string `json:"git_commit_ids"`
	EventWatermark  int64             `json:"event_watermark"`
	BinaryVersion   string            `json:"binary_version"`
	IntegrityCheck  string            `json:"integrity_check"`
	ForeignKeyCheck []string          `json:"foreign_key_check"`
	QuickCheck      string            `json:"quick_check"`

	// These aliases preserve the original in-package names for callers while
	// the manifest uses PM10's explicit vocabulary above.
	SourceEventMaxSeq int64  `json:"-"`
	FileSHA256        string `json:"-"`
	BackupTime        string `json:"-"`

	// Recomputed from the checksum-verified snapshot, never supplied by a manifest.
	currentStateDigest string
}

// BackupDatabase snapshots an existing database without migrating or repairing
// its schema. The read-only source open cannot create a missing database. The
// snapshot and manifest must pass the backup verb's verification rules.
func BackupDatabase(ctx context.Context, path, destination string) (BackupManifest, error) {
	if strings.TrimSpace(path) == "" {
		return BackupManifest{}, newFailure(KindInvalidOperation, "backup", "source database path is empty", false, "supply an existing database path")
	}
	if err := AdmitStorePath(path); err != nil {
		return BackupManifest{}, err
	}
	db, err := sqlitedb.Open(readOnlyDataSource(path))
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "backup", "cannot open the source database read-only", false, "supply a readable database", err)
	}
	s := &Store{db: db, path: path}
	defer func() { _ = s.Close() }()
	if _, err := Backup(ctx, s, destination); err != nil {
		return BackupManifest{}, err
	}
	return VerifyBackup(ctx, destination)
}

// Backup creates a self-contained SQLite snapshot with the Online Backup API.
// The destination and its manifest must not already exist; a live database or
// WAL copy is never used.
func Backup(ctx context.Context, s *Store, destination string) (BackupManifest, error) {
	return BackupWithOptions(ctx, s, destination, BackupOptions{})
}

// BackupWithOptions is Backup with explicit snapshot and Git inventory data.
func BackupWithOptions(ctx context.Context, s *Store, destination string, options BackupOptions) (manifest BackupManifest, err error) {
	if s == nil || s.db == nil {
		return manifest, newFailure(KindUnavailable, "backup", "store is not open", false, "open a store before backing it up")
	}
	if err := validateBackupDestination(s.Path(), destination); err != nil {
		return manifest, err
	}
	if err := AdmitStorePath(destination); err != nil {
		return manifest, err
	}
	if _, err := os.Stat(destination); err == nil {
		return manifest, newFailure(KindInvalidOperation, "backup", "backup destination already exists", false, "choose a new explicit destination")
	} else if !os.IsNotExist(err) {
		return manifest, wrapFailure(KindUnavailable, "backup", "cannot inspect backup destination", true, "check the destination path", err)
	}
	if _, err := os.Stat(manifestPath(destination)); err == nil {
		return manifest, newFailure(KindInvalidOperation, "backup", "backup manifest already exists", false, "choose a new explicit destination")
	} else if !os.IsNotExist(err) {
		return manifest, wrapFailure(KindUnavailable, "backup", "cannot inspect backup manifest destination", true, "check the destination path", err)
	}
	if err := contextErr(ctx); err != nil {
		return manifest, wrapFailure(KindUnavailable, "backup", "backup was cancelled before it started", true, "retry the backup", err)
	}

	createdAt := s.now().Format(time.RFC3339Nano)
	snapshotID := options.SnapshotID
	if snapshotID == "" {
		snapshotID = newSnapshotID()
	}
	if strings.TrimSpace(snapshotID) == "" {
		return manifest, newFailure(KindInvalidOperation, "backup", "snapshot ID is empty", false, "supply a non-empty snapshot ID")
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return manifest, wrapFailure(KindUnavailable, "backup", "cannot acquire a SQLite connection", true, "retry once the database is readable", err)
	}
	defer func() {
		if err != nil {
			for _, path := range []string{destination, destination + "-wal", destination + "-shm", manifestPath(destination)} {
				_ = os.Remove(path)
			}
		}
	}()
	tx, err := beginReadTx(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return manifest, wrapFailure(KindUnavailable, "backup", "cannot pin a backup source snapshot", true, "retry once the database is readable", err)
	}
	// A deferred BEGIN pins no snapshot; consume one read through the
	// transaction so the copy observes one source state instead of restarting
	// between batches.
	var schemaObjects int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema`).Scan(&schemaObjects); err != nil {
		pinErr := wrapFailure(KindUnavailable, "backup", "cannot pin the backup source snapshot", true, "retry once the database is readable", err)
		_ = tx.Rollback()
		_ = conn.Close()
		return manifest, pinErr
	}
	backupErr := conn.Raw(func(raw any) error {
		return copyOnlineBackup(ctx, raw, destination)
	})
	// Exit Raw before releasing the snapshot: a rollback during the copy
	// would reset the source pager and restart the backup's progress. The
	// rollback can report sql.ErrTxDone after a context cancellation.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) && backupErr == nil {
		backupErr = wrapFailure(KindUnavailable, "backup", "cannot release the backup source snapshot", true, "retry once the database is readable", err)
	}
	_ = conn.Close()
	if backupErr != nil {
		return manifest, backupErr
	}

	manifest, err = verifyBackupFile(ctx, destination, CurrentSchemaVersion(), s.Clock)
	if err != nil {
		return BackupManifest{}, err
	}
	manifest.SnapshotID = snapshotID
	manifest.CreatedAt = createdAt
	manifest.BackupTime = createdAt
	manifest.GitCommitIDs = cloneStringMap(options.GitCommitIDs)
	// The promotion gate clones the verified snapshot to a disposable staging
	// database and checks the whole current state before applying migrations.
	// The retained snapshot, manifest, and live store are never written.
	if err := verifyBackupRestore(ctx, destination, manifest); err != nil {
		return BackupManifest{}, err
	}
	if err := writeBackupManifest(destination, manifest); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

func copyOnlineBackup(ctx context.Context, raw any, destination string) error {
	driverConn, ok := raw.(backupper)
	if !ok {
		return newFailure(KindUnavailable, "backup", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver")
	}
	operation, err := driverConn.NewBackup(SQLiteFileURI(destination))
	if err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot initialize SQLite online backup", true, "retry from a writable local filesystem", err)
	}
	notifyBackupStarted(ctx)
	finished := false
	finish := func() error {
		if finished {
			return nil
		}
		finished = true
		return operation.Finish()
	}
	for retries := 0; ; {
		if err := contextErr(ctx); err != nil {
			_ = finish()
			return wrapFailure(KindUnavailable, "backup", "online backup was interrupted", true, "discard the partial snapshot and retry", err)
		}
		more, stepErr := operation.Step(backupStepPages)
		if stepErr == nil {
			if !more {
				break
			}
			retries = 0
			continue
		}
		if !isRetryableBackupError(stepErr) || retries >= backupRetryLimit {
			_ = finish()
			return wrapFailure(KindUnavailable, "backup", "online backup step failed", true, "discard the partial snapshot and retry", stepErr)
		}
		retries++
		if err := waitBackupRetry(ctx); err != nil {
			_ = finish()
			return wrapFailure(KindUnavailable, "backup", "online backup retry was interrupted", true, "discard the partial snapshot and retry", err)
		}
	}
	if err := finish(); err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot finish SQLite online backup", true, "discard the partial snapshot and retry", err)
	}
	return nil
}

// VerifyBackup validates the snapshot and its manifest. The optional maximum
// lets a test or older binary reject a newer schema before serving it.
func VerifyBackup(ctx context.Context, destination string, supportedMax ...int) (BackupManifest, error) {
	if err := validateBackupDestination("", destination); err != nil {
		return BackupManifest{}, err
	}
	if err := AdmitStorePath(destination); err != nil {
		return BackupManifest{}, err
	}
	maxSupported := CurrentSchemaVersion()
	if len(supportedMax) > 0 {
		maxSupported = supportedMax[0]
	}
	manifest, err := readBackupManifest(destination)
	if err != nil {
		return BackupManifest{}, err
	}
	if manifest.SchemaVersion < 1 || manifest.SnapshotID == "" || manifest.CreatedAt == "" {
		return BackupManifest{}, newFailure(KindInvalidPayload, "verify_backup", "backup manifest is missing PM10 identity fields", false, "create a fresh verified backup")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return BackupManifest{}, newFailure(KindInvalidPayload, "verify_backup", "backup manifest creation time is invalid", false, "create a fresh verified backup")
	}
	verified, err := verifyBackupFile(ctx, destination, maxSupported, nil)
	if err != nil {
		return BackupManifest{}, err
	}
	if verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != manifest.SchemaVersion || verified.IntegrityCheck != manifest.IntegrityCheck || verified.QuickCheck != manifest.QuickCheck || strings.Join(verified.ForeignKeyCheck, "\x00") != strings.Join(manifest.ForeignKeyCheck, "\x00") {
		return BackupManifest{}, newFailure(KindInvariantViolation, "verify_backup", "backup manifest does not match the snapshot", false, "discard the snapshot and create a fresh verified backup")
	}
	verified.SnapshotID = manifest.SnapshotID
	verified.CreatedAt = manifest.CreatedAt
	verified.BackupTime = manifest.CreatedAt
	verified.GitCommitIDs = cloneStringMap(manifest.GitCommitIDs)
	verified.BinaryVersion = manifest.BinaryVersion
	return verified, nil
}

// RestoredDatabaseInventory is the verified inventory of the promoted restore
// destination, computed from the staging database after its forward migrations.
// Like the manifest, it is an inventory, never an authority.
type RestoredDatabaseInventory struct {
	SchemaVersion  int    `json:"schema_version"`
	DBChecksum     string `json:"db_checksum"`
	EventWatermark int64  `json:"event_watermark"`
}

// RestoreReport separates the verified source snapshot from the promoted
// restored database. A migrated destination never inherits the source inventory
// values, and no manifest is written for the destination.
type RestoreReport struct {
	SourceSnapshot   BackupManifest            `json:"source_snapshot"`
	RestoredDatabase RestoredDatabaseInventory `json:"restored_database"`
}

// RestoreBackup verifies a snapshot, restores it into a clean staging file
// with SQLite's Online Backup API, verifies its current state and applies accepted
// migrations, then promotes it without replacing an existing destination. The
// source snapshot and manifest are never copied as bytes and never modified.
func RestoreBackup(ctx context.Context, source, destination string) (RestoreReport, error) {
	if err := validateBackupDestination("", destination); err != nil {
		return RestoreReport{}, err
	}
	if err := validateBackupDestination(source, destination); err != nil {
		return RestoreReport{}, err
	}
	if err := AdmitStorePath(destination); err != nil {
		return RestoreReport{}, err
	}
	manifest, err := VerifyBackup(ctx, source)
	if err != nil {
		return RestoreReport{}, err
	}
	if _, err := os.Stat(destination); err == nil {
		return RestoreReport{}, newFailure(KindInvalidOperation, "restore_backup", "restore destination already exists", false, "choose a clean destination path")
	} else if !os.IsNotExist(err) {
		return RestoreReport{}, wrapFailure(KindUnavailable, "restore_backup", "cannot inspect restore destination", true, "check the destination path", err)
	}
	if _, err := os.Stat(manifestPath(destination)); err == nil {
		return RestoreReport{}, newFailure(KindInvalidOperation, "restore_backup", "restore destination manifest already exists", false, "choose a clean destination path")
	} else if !os.IsNotExist(err) {
		return RestoreReport{}, wrapFailure(KindUnavailable, "restore_backup", "cannot inspect restore manifest destination", true, "check the destination path", err)
	}
	if err := contextErr(ctx); err != nil {
		return RestoreReport{}, wrapFailure(KindUnavailable, "restore_backup", "restore was cancelled before it started", true, "retry the restore", err)
	}

	stageFile, err := os.CreateTemp(filepath.Dir(destination), ".concord-restore-*")
	if err != nil {
		return RestoreReport{}, wrapFailure(KindUnavailable, "restore_backup", "cannot create restore staging file", true, "check destination permissions", err)
	}
	stage := stageFile.Name()
	cleanupStage := func() {
		_ = os.Remove(stage)
		_ = os.Remove(stage + "-wal")
		_ = os.Remove(stage + "-shm")
	}
	defer cleanupStage()
	if err := stageFile.Close(); err != nil {
		return RestoreReport{}, wrapFailure(KindUnavailable, "restore_backup", "cannot close restore staging file", true, "check destination permissions", err)
	}
	notifyRestoreStageReady(ctx, stage)

	if err := runOnlineRestore(ctx, source, stage); err != nil {
		return RestoreReport{}, err
	}

	inventory, err := verifyRestoredStage(ctx, stage, manifest)
	if err != nil {
		return RestoreReport{}, err
	}

	// A hard link followed by unlink is an atomic no-replace promotion on the
	// same directory. Unlike os.Rename, it cannot overwrite a destination that
	// appeared after the existence check.
	if err := os.Link(stage, destination); err != nil {
		return RestoreReport{}, wrapFailure(KindInvalidOperation, "restore_backup", "cannot atomically promote restore without replacing the destination", false, "choose a clean destination path", err)
	}
	if err := os.Remove(stage); err != nil {
		_ = os.Remove(destination)
		return RestoreReport{}, wrapFailure(KindUnavailable, "restore_backup", "cannot remove promoted restore staging name", true, "retry the restore", err)
	}
	return RestoreReport{SourceSnapshot: manifest, RestoredDatabase: inventory}, nil
}

// verifyBackupRestore proves a snapshot promotes only when this binary can
// restore it. It clones the snapshot to a disposable staging database through
// SQLite's Online Backup API and runs the shared restore verifier on the
// clone, then discards the clone. The snapshot itself is only ever read.
func verifyBackupRestore(ctx context.Context, snapshot string, manifest BackupManifest) error {
	stageFile, err := os.CreateTemp(filepath.Dir(snapshot), ".concord-backup-verify-*")
	if err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot create backup verification staging file", true, "check snapshot permissions", err)
	}
	stage := stageFile.Name()
	cleanupStage := func() {
		_ = os.Remove(stage)
		_ = os.Remove(stage + "-wal")
		_ = os.Remove(stage + "-shm")
	}
	defer cleanupStage()
	if err := stageFile.Close(); err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot close backup verification staging file", true, "check snapshot permissions", err)
	}
	if err := runOnlineRestore(ctx, snapshot, stage); err != nil {
		return err
	}
	// The staging clone now holds the snapshot bytes; the stage-ready seam
	// fires before verification so a cancelled backup discards a complete
	// clone and leaves the retained snapshot untouched.
	notifyRestoreStageReady(ctx, stage)
	_, err = verifyRestoredStage(ctx, stage, manifest)
	return err
}

// runOnlineRestore copies the verified source into the staging database
// through SQLite's Online Backup API. The copy runs on one source connection
// whose read-only snapshot stays pinned for the whole copy, so an incremental
// step cannot reopen the source transaction and restart its progress. The
// driver owns the staging destination handle; no destination pool is opened.
func runOnlineRestore(ctx context.Context, source, stage string) error {
	db, err := sqlitedb.Open(readOnlyDataSource(source))
	if err != nil {
		return wrapFailure(KindUnavailable, "restore_backup", "cannot open restore source database read-only", false, "supply a readable verified snapshot", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return wrapFailure(KindUnavailable, "restore_backup", "cannot acquire restore source connection", true, "retry the restore", err)
	}
	tx, err := beginReadTx(ctx, conn)
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return wrapFailure(KindUnavailable, "restore_backup", "cannot pin a restore source snapshot", true, "retry the restore", err)
	}
	restoreErr := func() error {
		if err := consumeSourceSnapshot(ctx, tx); err != nil {
			return err
		}
		return conn.Raw(func(raw any) error {
			return copyOnlineRestore(ctx, raw, stage)
		})
	}()
	// Exit Raw before releasing the snapshot: a rollback during the copy
	// would reset the source pager and restart the backup's progress. The
	// rollback can report sql.ErrTxDone when a context cancellation already
	// ended the transaction.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) && restoreErr == nil {
		restoreErr = wrapFailure(KindUnavailable, "restore_backup", "cannot release the restore source snapshot", true, "retry the restore", err)
	}
	if err := conn.Close(); err != nil && restoreErr == nil {
		restoreErr = wrapFailure(KindUnavailable, "restore_backup", "cannot release the restore source connection", true, "retry the restore", err)
	}
	if err := db.Close(); err != nil && restoreErr == nil {
		restoreErr = wrapFailure(KindUnavailable, "restore_backup", "cannot close the restore source pool", true, "retry the restore", err)
	}
	return restoreErr
}

func copyOnlineRestore(ctx context.Context, raw any, stage string) error {
	driverConn, ok := raw.(backupper)
	if !ok {
		return newFailure(KindUnavailable, "restore_backup", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver")
	}
	operation, err := driverConn.NewBackup(dataSourceName(stage))
	if err != nil {
		return wrapFailure(KindUnavailable, "restore_backup", "cannot initialize SQLite online restore", true, "retry from a readable verified snapshot", err)
	}
	finished := false
	finish := func() error {
		if finished {
			return nil
		}
		finished = true
		return operation.Finish()
	}
	for {
		if err := contextErr(ctx); err != nil {
			_ = finish()
			return wrapFailure(KindUnavailable, "restore_backup", "online restore was interrupted", true, "discard the staging database and retry", err)
		}
		more, stepErr := operation.Step(backupStepPages)
		if stepErr == nil {
			if !more {
				break
			}
			continue
		}
		_ = finish()
		return wrapFailure(KindUnavailable, "restore_backup", "online restore step failed", true, "discard the staging database and retry", stepErr)
	}
	if err := finish(); err != nil {
		return wrapFailure(KindUnavailable, "restore_backup", "cannot finish SQLite online restore", true, "discard the staging database and retry", err)
	}
	return nil
}

// consumeSourceSnapshot reads one row through the open read transaction. A
// deferred transaction pins no snapshot until its first read completes.
func consumeSourceSnapshot(ctx context.Context, tx *sql.Tx) error {
	var schemaObjects int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema`).Scan(&schemaObjects); err != nil {
		return wrapFailure(KindUnavailable, "restore_backup", "cannot pin the restore source snapshot", true, "retry the restore", err)
	}
	return nil
}

// verifyRestoredStage compares every materialized table and schema object with
// the verified source before applying accepted forward migrations. It validates
// the migrated stage without reconstructing or repairing current rows.
func verifyRestoredStage(ctx context.Context, stage string, source BackupManifest) (RestoredDatabaseInventory, error) {
	verifiedStage, err := verifyBackupFile(ctx, stage, source.SchemaVersion, nil)
	if err != nil {
		return RestoredDatabaseInventory{}, err
	}
	if source.currentStateDigest == "" || verifiedStage.currentStateDigest != source.currentStateDigest || verifiedStage.SchemaVersion != source.SchemaVersion || verifiedStage.IntegrityCheck != source.IntegrityCheck || verifiedStage.QuickCheck != source.QuickCheck || len(verifiedStage.ForeignKeyCheck) != 0 {
		return RestoredDatabaseInventory{}, newFailure(KindInvariantViolation, "restore_backup", "restored staging database does not match the verified source current state", false, "discard the staging database and restore a verified snapshot")
	}
	if err := contextErr(ctx); err != nil {
		return RestoredDatabaseInventory{}, wrapFailure(KindUnavailable, "restore_backup", "restore verification was interrupted", true, "retry the restore", err)
	}
	stageDB, err := sqlitedb.Open(dataSourceName(stage))
	if err != nil {
		return RestoredDatabaseInventory{}, wrapFailure(KindUnavailable, "restore_backup", "cannot reopen restored staging database", true, "discard the staging database and retry", err)
	}
	stageStore := &Store{db: stageDB, path: stage}
	if err := stageStore.verifyPragmas(ctx); err != nil {
		_ = stageDB.Close()
		return RestoredDatabaseInventory{}, err
	}
	// Forward migrations run on the staging database only. The source
	// snapshot, its manifest, and the live store keep their recorded schemas.
	if err := migrateRestoredStage(ctx, stageDB); err != nil {
		_ = stageDB.Close()
		return RestoredDatabaseInventory{}, err
	}
	var inventory RestoredDatabaseInventory
	integrity, quick, foreign, err := verifySQLiteTriple(ctx, stageDB)
	if err == nil && (integrity != "ok" || quick != "ok" || len(foreign) != 0) {
		err = newFailure(KindInvariantViolation, "restore_backup", fmt.Sprintf("restored staging database failed SQLite integrity verification: integrity_check=%q quick_check=%q foreign_key_check=%v", integrity, quick, foreign), false, "restore a consistent snapshot")
	}
	if err == nil {
		err = validateMembershipInvariants(ctx, stageDB)
	}
	if err == nil {
		inventory.SchemaVersion, inventory.EventWatermark, err = stagedInventory(ctx, stageDB)
	}
	if closeErr := stageDB.Close(); err == nil {
		err = closeErr
	}
	// The checksum is taken after the close so it describes the checkpointed
	// staging bytes, which are the exact bytes a successful promotion links
	// into the destination.
	if err == nil {
		inventory.DBChecksum, err = fileSHA256(stage)
	}
	if err != nil {
		return RestoredDatabaseInventory{}, err
	}
	return inventory, nil
}

// migrateRestoredStage applies this binary's accepted forward migrations to
// the staging database. A stamped release migrates the way concord upgrade
// migrates the live store — every pending step, breaking ones included — so a
// pre-upgrade snapshot verifies against the schema this binary serves. An
// unstamped development build owns no migration for an existing store and
// refuses a pending tail instead of bypassing that rule (CD-0139).
func migrateRestoredStage(ctx context.Context, db *sql.DB) error {
	if version.Value == version.Development {
		return migrateUnstamped(ctx, db)
	}
	return Migrate(ctx, db)
}

// stagedInventory reads the restored inventory after migration. The schema must
// be this binary's: the migrations above brought it forward or refused.
func stagedInventory(ctx context.Context, stageDB *sql.DB) (int, int64, error) {
	var schema int
	if err := stageDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schema); err != nil {
		return 0, 0, wrapFailure(KindUnavailable, "restore_backup", "cannot read the restored schema version", true, "discard the staging database and retry", err)
	}
	if schema != CurrentSchemaVersion() {
		return 0, 0, newFailure(KindInvariantViolation, "restore_backup", fmt.Sprintf("restored staging database holds schema version %d, not this binary's schema version %d", schema, CurrentSchemaVersion()), false, "restore from a snapshot this binary can migrate")
	}
	watermark, err := backupEventInventory(ctx, stageDB)
	return schema, watermark, err
}

// The store revision is inventory only, never a verification input. A snapshot
// from before the revision migration reports zero inventory.
func backupEventInventory(ctx context.Context, db *sql.DB) (int64, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE type='table' AND name='store_revision')`).Scan(&exists); err != nil {
		return 0, backupStateReadFailure(err)
	}
	if !exists {
		return 0, nil
	}
	return storeRevision(ctx, db)
}

func verifyBackupFile(ctx context.Context, destination string, supportedMax int, clock func() time.Time) (BackupManifest, error) {
	info, err := os.Stat(destination)
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot stat backup snapshot", true, "restore the snapshot path and retry", err)
	}
	if !info.Mode().IsRegular() {
		return BackupManifest{}, newFailure(KindInvalidOperation, "verify_backup", "backup snapshot is not a regular file", false, "supply a regular SQLite snapshot")
	}
	hash, err := fileSHA256(destination)
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot hash backup snapshot", true, "check snapshot permissions", err)
	}
	db, err := sqlitedb.Open(readOnlyDataSource(destination))
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot open backup snapshot", false, "supply a readable SQLite snapshot", err)
	}
	defer func() { _ = db.Close() }()
	var schema int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schema); err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot read backup schema version", false, "supply a Concord SQLite snapshot", err)
	}
	if schema > supportedMax {
		return BackupManifest{}, newFailure(KindSchemaUnsupported, "verify_backup", fmt.Sprintf("backup schema %d exceeds supported maximum %d", schema, supportedMax), true, "upgrade the binary before restoring this snapshot")
	}
	integrity, quick, foreign, err := verifySQLiteTriple(ctx, db)
	if err != nil {
		return BackupManifest{}, err
	}
	if integrity != "ok" || quick != "ok" || len(foreign) != 0 {
		return BackupManifest{}, newFailure(KindInvariantViolation, "verify_backup", fmt.Sprintf("SQLite integrity verification failed: integrity_check=%q quick_check=%q foreign_key_check=%v", integrity, quick, foreign), false, "discard the corrupt snapshot and restore a verified backup")
	}
	stateDigest, err := backupCurrentStateDigest(ctx, db)
	if err != nil {
		return BackupManifest{}, err
	}
	watermark, err := backupEventInventory(ctx, db)
	if err != nil {
		return BackupManifest{}, err
	}
	result := BackupManifest{
		SchemaVersion: schema, DBChecksum: hash, EventWatermark: watermark,
		BinaryVersion: version.Value, IntegrityCheck: integrity, ForeignKeyCheck: foreign, QuickCheck: quick,
		currentStateDigest: stateDigest,
	}
	result.SourceEventMaxSeq = result.EventWatermark
	result.FileSHA256 = result.DBChecksum
	result.BackupTime = nowFromClock(clock).Format(time.RFC3339Nano)
	if err := db.Close(); err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot close backup snapshot", true, "retry the backup verification", err)
	}
	return result, nil
}

func verifySQLiteTriple(ctx context.Context, db *sql.DB) (string, string, []string, error) {
	integrity, err := pragmaSingle(ctx, db, `PRAGMA integrity_check`)
	if err != nil {
		return "", "", nil, err
	}
	quick, err := pragmaSingle(ctx, db, `PRAGMA quick_check`)
	if err != nil {
		return "", "", nil, err
	}
	foreign, err := foreignKeyCheck(ctx, db)
	if err != nil {
		return "", "", nil, err
	}
	return integrity, quick, foreign, nil
}

func validateBackupDestination(source, destination string) error {
	if strings.TrimSpace(destination) == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return newFailure(KindInvalidOperation, "backup", "destination must be an explicit absolute clean path", false, "supply a trusted absolute destination path")
	}
	if source != "" {
		sourceAbs, err := filepath.Abs(source)
		if err != nil {
			return wrapFailure(KindUnavailable, "backup", "cannot resolve source path", false, "use a readable database path", err)
		}
		if sourceAbs == destination {
			return newFailure(KindInvalidOperation, "backup", "backup destination is the live database", false, "choose a separate snapshot path")
		}
	}
	if info, err := os.Stat(filepath.Dir(destination)); err != nil || !info.IsDir() {
		return newFailure(KindInvalidOperation, "backup", "backup destination parent is not an existing directory", false, "create or select a trusted destination directory")
	}
	return nil
}

func isRetryableBackupError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlitelib.SQLITE_BUSY || code == sqlitelib.SQLITE_LOCKED
}

func waitBackupRetry(ctx context.Context) error {
	timer := time.NewTimer(backupRetryBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func newSnapshotID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().UTC().String()))
		return hex.EncodeToString(sum[:12])
	}
	return hex.EncodeToString(random[:])
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func manifestPath(destination string) string { return destination + ".manifest.json" }

func readOnlyDataSource(path string) string {
	return sqlitedb.ReadOnlyDataSource(path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // callers supply a validated backup path or an internally-created test fixture path.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func pragmaSingle(ctx context.Context, db *sql.DB, query string) (string, error) {
	var value string
	if err := db.QueryRowContext(ctx, query).Scan(&value); err != nil {
		return "", wrapFailure(KindUnavailable, "verify_backup", "cannot run "+query, false, "supply a readable SQLite snapshot", err)
	}
	return value, nil
}

func foreignKeyCheck(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "verify_backup", "cannot run foreign_key_check", false, "supply a readable SQLite snapshot", err)
	}
	defer rows.Close()
	failures := make([]string, 0)
	for rows.Next() {
		var table string
		var rowID, parent, fk any
		if err := rows.Scan(&table, &rowID, &parent, &fk); err != nil {
			return nil, wrapFailure(KindUnavailable, "verify_backup", "cannot decode foreign_key_check", false, "repair the snapshot", err)
		}
		failures = append(failures, fmt.Sprintf("%s:%v:%v:%v", table, rowID, parent, fk))
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "verify_backup", "cannot read foreign_key_check", false, "repair the snapshot", err)
	}
	return failures, nil
}

func writeBackupManifest(destination string, manifest BackupManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot encode backup manifest", false, "repair the manifest encoder", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".concord-backup-manifest-*")
	if err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot create temporary manifest", true, "check destination permissions", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return wrapFailure(KindUnavailable, "backup", "cannot protect temporary manifest", false, "check destination permissions", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return wrapFailure(KindUnavailable, "backup", "cannot write backup manifest", true, "check destination permissions", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return wrapFailure(KindUnavailable, "backup", "cannot sync backup manifest", true, "retry the backup", err)
	}
	if err := tmp.Close(); err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot close backup manifest", true, "retry the backup", err)
	}
	if err := os.Rename(tmpName, manifestPath(destination)); err != nil {
		return wrapFailure(KindUnavailable, "backup", "cannot atomically promote backup manifest", true, "retry the backup", err)
	}
	return nil
}

func readBackupManifest(destination string) (BackupManifest, error) {
	data, err := os.ReadFile(manifestPath(destination))
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot read backup manifest", true, "supply the snapshot and its manifest", err)
	}
	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, wrapFailure(KindInvalidPayload, "verify_backup", "backup manifest is not valid JSON", false, "discard the manifest and create a fresh backup", err)
	}
	return m, nil
}
