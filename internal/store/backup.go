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
// Keeping both methods in the assertion prevents accidentally using a wrapper
// that cannot also restore a verified snapshot.
type backupper interface {
	NewBackup(string) (*sqlite.Backup, error)
	NewRestore(string) (*sqlite.Backup, error)
}

// BackupOptions supplies the non-SQLite authorities recorded in the manifest.
// GitCommitIDs is inventory metadata only; it is never used while folding the
// event log.
type BackupOptions struct {
	SnapshotID   string
	GitCommitIDs map[string]string
}

// BackupManifest is verification metadata beside a SQLite snapshot. It is an
// inventory, never an authority or an input to RebuildFromLog.
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
}

// Backup creates a self-contained SQLite snapshot with the Online Backup API.
// The destination and its manifest must not already exist; a live database or
// WAL copy is never used.
func Backup(ctx context.Context, s *Store, destination string) (BackupManifest, error) {
	return BackupWithOptions(ctx, s, destination, BackupOptions{})
}

// BackupWithOptions is Backup with explicit snapshot and Git inventory data.
func BackupWithOptions(ctx context.Context, s *Store, destination string, options BackupOptions) (BackupManifest, error) {
	var manifest BackupManifest
	if s == nil || s.db == nil {
		return manifest, newFailure(KindUnavailable, "backup", "store is not open", false, "open a store before backing it up")
	}
	if err := validateBackupDestination(s.Path(), destination); err != nil {
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

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
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
	backupErr := conn.Raw(func(raw any) error {
		driverConn, ok := raw.(backupper)
		if !ok {
			return newFailure(KindUnavailable, "backup", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver")
		}
		operation, err := driverConn.NewBackup(destination)
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
	})
	_ = conn.Close()
	if backupErr != nil {
		_ = os.Remove(destination)
		_ = os.Remove(manifestPath(destination))
		return manifest, backupErr
	}

	manifest, err = verifyBackupFile(ctx, destination, CurrentSchemaVersion())
	if err != nil {
		_ = os.Remove(destination)
		return BackupManifest{}, err
	}
	manifest.SnapshotID = snapshotID
	manifest.CreatedAt = createdAt
	manifest.BackupTime = createdAt
	manifest.GitCommitIDs = cloneStringMap(options.GitCommitIDs)
	if err := writeBackupManifest(destination, manifest); err != nil {
		_ = os.Remove(destination)
		return BackupManifest{}, err
	}
	return manifest, nil
}

// VerifyBackup validates the snapshot and its manifest. The optional maximum
// lets a test or older binary reject a newer schema before serving it.
func VerifyBackup(ctx context.Context, destination string, supportedMax ...int) (BackupManifest, error) {
	if err := validateBackupDestination("", destination); err != nil {
		return BackupManifest{}, err
	}
	max := CurrentSchemaVersion()
	if len(supportedMax) > 0 {
		max = supportedMax[0]
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
	verified, err := verifyBackupFile(ctx, destination, max)
	if err != nil {
		return BackupManifest{}, err
	}
	if verified.DBChecksum != manifest.DBChecksum || verified.EventWatermark != manifest.EventWatermark || verified.SchemaVersion != manifest.SchemaVersion || verified.IntegrityCheck != manifest.IntegrityCheck || verified.QuickCheck != manifest.QuickCheck || strings.Join(verified.ForeignKeyCheck, "\x00") != strings.Join(manifest.ForeignKeyCheck, "\x00") {
		return BackupManifest{}, newFailure(KindInvariantViolation, "verify_backup", "backup manifest does not match the snapshot", false, "discard the snapshot and create a fresh verified backup")
	}
	verified.SnapshotID = manifest.SnapshotID
	verified.CreatedAt = manifest.CreatedAt
	verified.BackupTime = manifest.CreatedAt
	verified.GitCommitIDs = cloneStringMap(manifest.GitCommitIDs)
	verified.BinaryVersion = manifest.BinaryVersion
	return verified, nil
}

// RestoreBackup verifies a snapshot, restores it into a clean staging file
// with SQLite's Online Backup API, rebuilds and verifies its projections, then
// promotes it without replacing an existing destination. The source snapshot
// and manifest are never copied as bytes.
func RestoreBackup(ctx context.Context, source, destination string) (BackupManifest, error) {
	if err := validateBackupDestination("", destination); err != nil {
		return BackupManifest{}, err
	}
	if err := validateBackupDestination(source, destination); err != nil {
		return BackupManifest{}, err
	}
	manifest, err := VerifyBackup(ctx, source)
	if err != nil {
		return BackupManifest{}, err
	}
	if _, err := os.Stat(destination); err == nil {
		return BackupManifest{}, newFailure(KindInvalidOperation, "restore_backup", "restore destination already exists", false, "choose a clean destination path")
	} else if !os.IsNotExist(err) {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot inspect restore destination", true, "check the destination path", err)
	}
	if _, err := os.Stat(manifestPath(destination)); err == nil {
		return BackupManifest{}, newFailure(KindInvalidOperation, "restore_backup", "restore destination manifest already exists", false, "choose a clean destination path")
	} else if !os.IsNotExist(err) {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot inspect restore manifest destination", true, "check the destination path", err)
	}
	if err := contextErr(ctx); err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "restore was cancelled before it started", true, "retry the restore", err)
	}

	stageFile, err := os.CreateTemp(filepath.Dir(destination), ".concord-restore-*")
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot create restore staging file", true, "check destination permissions", err)
	}
	stage := stageFile.Name()
	cleanupStage := func() {
		_ = os.Remove(stage)
		_ = os.Remove(stage + "-wal")
		_ = os.Remove(stage + "-shm")
	}
	defer cleanupStage()
	if err := stageFile.Close(); err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot close restore staging file", true, "check destination permissions", err)
	}
	notifyRestoreStageReady(ctx, stage)

	stageDB, err := sql.Open(driverName, dataSourceName(stage))
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot open restore staging database", true, "check destination permissions", err)
	}
	stageDB.SetMaxOpenConns(1)
	if err := stageDB.PingContext(ctx); err != nil {
		_ = stageDB.Close()
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot initialize restore staging database", true, "check destination permissions", err)
	}
	conn, err := stageDB.Conn(ctx)
	if err != nil {
		_ = stageDB.Close()
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot acquire restore staging connection", true, "retry the restore", err)
	}
	restoreErr := conn.Raw(func(raw any) error {
		driverConn, ok := raw.(backupper)
		if !ok {
			return newFailure(KindUnavailable, "restore_backup", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver")
		}
		operation, err := driverConn.NewRestore(readOnlyDataSource(source))
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
		for retries := 0; ; {
			if err := contextErr(ctx); err != nil {
				_ = finish()
				return wrapFailure(KindUnavailable, "restore_backup", "online restore was interrupted", true, "discard the staging database and retry", err)
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
				return wrapFailure(KindUnavailable, "restore_backup", "online restore step failed", true, "discard the staging database and retry", stepErr)
			}
			retries++
			if err := waitBackupRetry(ctx); err != nil {
				_ = finish()
				return wrapFailure(KindUnavailable, "restore_backup", "online restore retry was interrupted", true, "discard the staging database and retry", err)
			}
		}
		if err := finish(); err != nil {
			return wrapFailure(KindUnavailable, "restore_backup", "cannot finish SQLite online restore", true, "discard the staging database and retry", err)
		}
		return nil
	})
	_ = conn.Close()
	if closeErr := stageDB.Close(); restoreErr == nil {
		restoreErr = closeErr
	}
	if restoreErr != nil {
		return BackupManifest{}, restoreErr
	}

	verifiedStage, err := verifyBackupFile(ctx, stage, manifest.SchemaVersion)
	if err != nil {
		return BackupManifest{}, err
	}
	if verifiedStage.SchemaVersion != manifest.SchemaVersion || verifiedStage.EventWatermark != manifest.EventWatermark || verifiedStage.IntegrityCheck != manifest.IntegrityCheck || verifiedStage.QuickCheck != manifest.QuickCheck || len(verifiedStage.ForeignKeyCheck) != 0 {
		return BackupManifest{}, newFailure(KindInvariantViolation, "restore_backup", "restored staging database does not match the verified source", false, "discard the staging database and restore a verified snapshot")
	}

	stageDB, err = sql.Open(driverName, dataSourceName(stage))
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot reopen restored staging database", true, "discard the staging database and retry", err)
	}
	stageDB.SetMaxOpenConns(1)
	stageStore := &Store{db: stageDB, path: stage}
	if err := stageStore.verifyPragmas(ctx); err != nil {
		_ = stageDB.Close()
		return BackupManifest{}, err
	}
	before, err := projectionDigest(ctx, stageDB)
	if err == nil {
		err = RebuildFromLog(ctx, stageStore)
	}
	if err == nil {
		var after string
		after, err = projectionDigest(ctx, stageDB)
		if err == nil && after != before {
			err = newFailure(KindInvariantViolation, "restore_backup", "restored projection rebuild changed projection content", false, "restore a consistent snapshot")
		}
	}
	if err == nil {
		err = validateMembershipInvariants(ctx, stageDB)
	}
	if closeErr := stageDB.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return BackupManifest{}, err
	}

	// A hard link followed by unlink is an atomic no-replace promotion on the
	// same directory. Unlike os.Rename, it cannot overwrite a destination that
	// appeared after the existence check.
	if err := os.Link(stage, destination); err != nil {
		return BackupManifest{}, wrapFailure(KindInvalidOperation, "restore_backup", "cannot atomically promote restore without replacing the destination", false, "choose a clean destination path", err)
	}
	if err := os.Remove(stage); err != nil {
		_ = os.Remove(destination)
		return BackupManifest{}, wrapFailure(KindUnavailable, "restore_backup", "cannot remove promoted restore staging name", true, "retry the restore", err)
	}
	return manifest, nil
}

func projectionDigest(ctx context.Context, db *sql.DB) (string, error) {
	queries := []struct {
		name  string
		query string
	}{
		{"products", `SELECT id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at FROM products ORDER BY id`},
		{"projects", `SELECT id, display_name, stage_maturity_override, stage_audience_commitment_override, version, created_at, updated_at FROM projects ORDER BY id`},
		{"work_items", `SELECT id, kind, title, lifecycle, priority, urgency, version, created_at, updated_at, terminal_time FROM work_items ORDER BY id`},
		{"relations", `SELECT work_id_from, work_id_to, kind, created_at FROM relations ORDER BY work_id_from, work_id_to, kind`},
		{"initiative_entries", `SELECT initiative_work_id, child_work_id, position, required FROM initiative_entries ORDER BY initiative_work_id, position, child_work_id`},
		{"product_projects", `SELECT product_id, project_id, role FROM product_projects ORDER BY product_id, project_id`},
		{"work_projects", `SELECT work_id, project_id, role FROM work_projects ORDER BY work_id, project_id`},
	}
	hash := sha256.New()
	for _, item := range queries {
		rows, err := db.QueryContext(ctx, item.query)
		if err != nil {
			return "", wrapFailure(KindUnavailable, "restore_backup", "cannot read "+item.name+" projection", true, "retry the restore", err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return "", wrapFailure(KindUnavailable, "restore_backup", "cannot inspect "+item.name+" projection", true, "retry the restore", err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				return "", wrapFailure(KindUnavailable, "restore_backup", "cannot decode "+item.name+" projection", true, "retry the restore", err)
			}
			fmt.Fprintf(hash, "%s\x00", item.name)
			for _, value := range values {
				fmt.Fprintf(hash, "%T:%v\x00", value, value)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return "", wrapFailure(KindUnavailable, "restore_backup", "cannot read "+item.name+" projection", true, "retry the restore", err)
		}
		_ = rows.Close()
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyBackupFile(ctx context.Context, destination string, supportedMax int) (BackupManifest, error) {
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
	db, err := sql.Open(driverName, readOnlyDataSource(destination))
	if err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot open backup snapshot", false, "supply a readable SQLite snapshot", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
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
		return BackupManifest{}, newFailure(KindInvariantViolation, "verify_backup", "SQLite integrity verification failed", false, "discard the corrupt snapshot and restore a verified backup")
	}
	var watermark sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(seq) FROM domain_events`).Scan(&watermark); err != nil {
		return BackupManifest{}, wrapFailure(KindUnavailable, "verify_backup", "cannot read backup event watermark", false, "supply a Concord SQLite snapshot", err)
	}
	result := BackupManifest{
		SchemaVersion: schema, EventWatermark: watermark.Int64, DBChecksum: hash,
		BinaryVersion: version.Value, IntegrityCheck: integrity, ForeignKeyCheck: foreign, QuickCheck: quick,
	}
	result.SourceEventMaxSeq = result.EventWatermark
	result.FileSHA256 = result.DBChecksum
	result.BackupTime = time.Now().UTC().Format(time.RFC3339Nano)
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
	return "file:" + path + "?mode=ro&_pragma=foreign_keys(1)"
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
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
	defer os.Remove(tmpName)
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
