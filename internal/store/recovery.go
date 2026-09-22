package store

import (
	"context"
	"os"
	"syscall"
)

// recoveryLockSuffix names the sidecar lock file one recovery holds at a
// time. The lock is advisory and offline: it serializes recovery invocations
// against each other, while SQLite's write lock serializes the recovery
// transaction against any writer.
const recoveryLockSuffix = ".recover-lock"

// RecoverFoldGuardReport states what one offline recovery did.
type RecoverFoldGuardReport struct {
	// Events is the number of log events the rebuild replayed.
	Events int64 `json:"events"`
	// Rebuilt is true when the cleared guard and the projection rebuild
	// committed together.
	Rebuilt bool `json:"rebuilt"`
}

// RecoverFoldGuard is the offline recovery route for a database that ordinary
// Open refuses because a fold_guard row committed without a closing scope. It
// clears the stranded guard and rebuilds every projection through the same
// transaction-scoped rebuild RebuildFromLog runs, so any projection row the
// event log does not restore is gone afterwards. The event log is never
// touched: the log is the authority the rebuild replays.
//
// The clear and the rebuild share one transaction. A failed recovery rolls
// that transaction back, which restores the stranded row and keeps ordinary
// writes refused; only a committed recovery leaves the database admittable.
// A sidecar lock refuses a second recovery while one is running.
func RecoverFoldGuard(ctx context.Context, path string) (RecoverFoldGuardReport, error) {
	var report RecoverFoldGuardReport
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return report, err
		}
	}
	lock, err := acquireRecoveryLock(path)
	if err != nil {
		return report, err
	}
	defer func() { _ = lock.close() }()
	s, err := openUnmigrated(ctx, path)
	if err != nil {
		return report, err
	}
	defer func() { _ = s.db.Close() }()
	// Migrations run because the stranded database may predate the current
	// schema; the stranded-guard refusal in finishOpen is deliberately not
	// applied, because clearing it is this command's whole purpose.
	if err := s.migrateOpen(ctx); err != nil {
		return report, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, wrapFailure(KindUnavailable, "recover_fold_guard", "cannot begin the recovery transaction", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) (RecoverFoldGuardReport, error) {
		_ = tx.Rollback()
		return report, cause
	}
	var stranded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM fold_guard`).Scan(&stranded); err != nil {
		return rollback(wrapFailure(KindUnavailable, "recover_fold_guard", "cannot inspect the fold guard", true,
			"confirm the database is readable", err))
	}
	if stranded == 0 {
		return rollback(newFailure(KindInvalidOperation, "recover_fold_guard",
			"the database carries no stranded fold_guard row, so there is nothing to recover", false,
			"open the store normally"))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		return rollback(wrapFailure(KindUnavailable, "recover_fold_guard", "cannot clear the stranded fold guard", true,
			"retry once the database is writable", err))
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&report.Events); err != nil {
		return rollback(wrapFailure(KindUnavailable, "recover_fold_guard", "cannot count the event log", true,
			"retry once the database is readable", err))
	}
	if err := rebuildFromLogTx(ctx, tx); err != nil {
		// The rollback restores the stranded row, so a failed recovery leaves
		// ordinary writes exactly as refused as before it ran.
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return rollback(wrapFailure(KindUnavailable, "recover_fold_guard", "cannot commit the recovery", true,
			"retry once the database is writable", err))
	}
	report.Rebuilt = true
	// committed; the durability barrier must hold before acknowledging
	if err := s.SyncDurable(ctx); err != nil {
		return report, err
	}
	return report, nil
}

// recoveryLock is the held advisory lock. close releases it and removes the
// sidecar file.
type recoveryLock struct {
	file *os.File
}

// acquireRecoveryLock takes the one-recovery-at-a-time lock beside the
// database. A held lock refuses immediately instead of queueing a second
// recovery behind an unknown one.
func acquireRecoveryLock(path string) (*recoveryLock, error) {
	lockPath := path + recoveryLockSuffix
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // lockPath joins the authority path with a fixed suffix inside the private data directory.
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "recover_fold_guard", "cannot create the recovery lock", true,
			"check directory permissions", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, newFailure(KindOperationConflict, "recover_fold_guard",
			"another fold-guard recovery already holds the recovery lock", false,
			"wait for the running recovery to finish, then retry")
	}
	return &recoveryLock{file: file}, nil
}

func (l *recoveryLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	path := l.file.Name()
	err := l.file.Close()
	if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		if err == nil {
			err = removeErr
		}
	}
	l.file = nil
	return err
}
