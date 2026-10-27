package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

const (
	BackupStepPages    = int32(100)
	backupRetryLimit   = 100
	backupRetryBackoff = 50 * time.Millisecond
)

// BackupStartedContextKey carries a notification after live backup initialization.
type BackupStartedContextKey struct{}

func notifyBackupStarted(ctx context.Context) {
	if notify, ok := ctx.Value(BackupStartedContextKey{}).(func()); ok {
		notify()
	}
}

// backupper is the small driver surface required by the Online Backup API.
// The source-owned NewBackup direction lets Concord pin the source read
// snapshot; the driver owns the destination handle the DSN names.
type backupper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

// BackupSnapshot consumes and closes an acquired source connection. It pins one
// source snapshot for the entire copy; the caller selects the destination URI
// and owns destination cleanup.
func BackupSnapshot(ctx context.Context, conn *sql.Conn, destinationURI string) error {
	tx, err := BeginRead(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return failure(Unavailable, "backup", "cannot pin a backup source snapshot", true, "retry once the database is readable", err)
	}
	// A deferred BEGIN pins no snapshot; consume one read through the
	// transaction so the copy observes one source state instead of restarting
	// between batches.
	var schemaObjects int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema`).Scan(&schemaObjects); err != nil {
		pinErr := failure(Unavailable, "backup", "cannot pin the backup source snapshot", true, "retry once the database is readable", err)
		_ = tx.Rollback()
		_ = conn.Close()
		return pinErr
	}
	backupErr := conn.Raw(func(raw any) error {
		return copyOnlineBackup(ctx, raw, destinationURI)
	})
	// Exit Raw before releasing the snapshot: a rollback during the copy
	// would reset the source pager and restart the backup's progress. The
	// rollback can report sql.ErrTxDone after a context cancellation.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) && backupErr == nil {
		backupErr = failure(Unavailable, "backup", "cannot release the backup source snapshot", true, "retry once the database is readable", err)
	}
	_ = conn.Close()
	return backupErr
}

func copyOnlineBackup(ctx context.Context, raw any, destinationURI string) error {
	driverConn, ok := raw.(backupper)
	if !ok {
		return failure(Unavailable, "backup", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver", nil)
	}
	operation, err := driverConn.NewBackup(destinationURI)
	if err != nil {
		return failure(Unavailable, "backup", "cannot initialize SQLite online backup", true, "retry from a writable local filesystem", err)
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
		if err := ContextErr(ctx); err != nil {
			_ = finish()
			return failure(Unavailable, "backup", "online backup was interrupted", true, "discard the partial snapshot and retry", err)
		}
		more, stepErr := operation.Step(BackupStepPages)
		if stepErr == nil {
			if !more {
				break
			}
			retries = 0
			continue
		}
		if !isRetryableBackupError(stepErr) || retries >= backupRetryLimit {
			_ = finish()
			return failure(Unavailable, "backup", "online backup step failed", true, "discard the partial snapshot and retry", stepErr)
		}
		retries++
		if err := waitBackupRetry(ctx); err != nil {
			_ = finish()
			return failure(Unavailable, "backup", "online backup retry was interrupted", true, "discard the partial snapshot and retry", err)
		}
	}
	if err := finish(); err != nil {
		return failure(Unavailable, "backup", "cannot finish SQLite online backup", true, "discard the partial snapshot and retry", err)
	}
	return nil
}

// RunOnlineRestore copies the verified source into the staging database
// through SQLite's Online Backup API. The copy runs on one source connection
// whose read-only snapshot stays pinned for the whole copy, so an incremental
// step cannot reopen the source transaction and restart its progress. The
// driver owns the staging destination handle; no destination pool is opened.
func RunOnlineRestore(ctx context.Context, source, stage string) error {
	db, err := Open(ReadOnlyDataSource(source))
	if err != nil {
		return failure(Unavailable, "restore_backup", "cannot open restore source database read-only", false, "supply a readable verified snapshot", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return failure(Unavailable, "restore_backup", "cannot acquire restore source connection", true, "retry the restore", err)
	}
	tx, err := BeginRead(ctx, conn)
	if err != nil {
		_ = conn.Close()
		_ = db.Close()
		return failure(Unavailable, "restore_backup", "cannot pin a restore source snapshot", true, "retry the restore", err)
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
		restoreErr = failure(Unavailable, "restore_backup", "cannot release the restore source snapshot", true, "retry the restore", err)
	}
	if err := conn.Close(); err != nil && restoreErr == nil {
		restoreErr = failure(Unavailable, "restore_backup", "cannot release the restore source connection", true, "retry the restore", err)
	}
	if err := db.Close(); err != nil && restoreErr == nil {
		restoreErr = failure(Unavailable, "restore_backup", "cannot close the restore source pool", true, "retry the restore", err)
	}
	return restoreErr
}

func copyOnlineRestore(ctx context.Context, raw any, stage string) error {
	driverConn, ok := raw.(backupper)
	if !ok {
		return failure(Unavailable, "restore_backup", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver", nil)
	}
	operation, err := driverConn.NewBackup(DSN(stage))
	if err != nil {
		return failure(Unavailable, "restore_backup", "cannot initialize SQLite online restore", true, "retry from a readable verified snapshot", err)
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
		if err := ContextErr(ctx); err != nil {
			_ = finish()
			return failure(Unavailable, "restore_backup", "online restore was interrupted", true, "discard the staging database and retry", err)
		}
		more, stepErr := operation.Step(BackupStepPages)
		if stepErr == nil {
			if !more {
				break
			}
			continue
		}
		_ = finish()
		return failure(Unavailable, "restore_backup", "online restore step failed", true, "discard the staging database and retry", stepErr)
	}
	if err := finish(); err != nil {
		return failure(Unavailable, "restore_backup", "cannot finish SQLite online restore", true, "discard the staging database and retry", err)
	}
	return nil
}

// consumeSourceSnapshot reads one row through the open read transaction. A
// deferred transaction pins no snapshot until its first read completes.
func consumeSourceSnapshot(ctx context.Context, tx *sql.Tx) error {
	var schemaObjects int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema`).Scan(&schemaObjects); err != nil {
		return failure(Unavailable, "restore_backup", "cannot pin the restore source snapshot", true, "retry the restore", err)
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

// ContextErr consults Done before Err, including contexts that observe that read.
func ContextErr(ctx context.Context) error {
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
