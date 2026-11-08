package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	sqlite "modernc.org/sqlite"
)

// Exclusive is one connection that holds the database file's exclusive lock
// until Close. The locking mode precedes every other setting, so the first read
// takes the exclusive lock and keeps the WAL index in process memory instead of
// shared memory. Every WAL connection keeps a shared lock on the database file
// once it has read, so acquisition fails while any other connection is open
// on the file, and no connection can read or write while the lock is held.
type Exclusive struct {
	db   *sql.DB
	Conn *sql.Conn
}

// OpenExclusive opens path in EXCLUSIVE locking mode and takes the lock. It
// changes no data and refuses a database whose journal mode is not WAL.
func OpenExclusive(ctx context.Context, path string) (*Exclusive, error) {
	dsn := FileURI(path) + "?_pragma=" + url.QueryEscape("locking_mode(EXCLUSIVE)") +
		"&_pragma=" + url.QueryEscape("busy_timeout("+BusyTimeout+")") +
		"&_pragma=" + url.QueryEscape("synchronous(FULL)") +
		"&_pragma=" + url.QueryEscape("foreign_keys(1)")
	db, err := Open(dsn)
	if err != nil {
		return nil, failure(Unavailable, "compact", "cannot open the store exclusively", true, "stop every process that has the store open and retry", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, failure(Unavailable, "compact", "cannot acquire the exclusive store connection", true, "stop every process that has the store open and retry", err)
	}
	exclusive := &Exclusive{db: db, Conn: conn}
	var lockingMode, journal string
	var objects int
	err = conn.QueryRowContext(ctx, `PRAGMA locking_mode`).Scan(&lockingMode)
	if err == nil {
		err = conn.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema`).Scan(&objects)
	}
	if err == nil {
		err = conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal)
	}
	if err != nil {
		_ = exclusive.Close()
		return nil, failure(Unavailable, "compact", "cannot take the exclusive store lock; another connection may have the store open", true, "stop every process that has the store open and retry", err)
	}
	if lockingMode != "exclusive" || journal != journalModeWAL {
		_ = exclusive.Close()
		return nil, failure(Unavailable, "compact", fmt.Sprintf("store locking mode %q and journal mode %q are not exclusive WAL", lockingMode, journal), false, "inspect the store settings", nil)
	}
	return exclusive, nil
}

// Close releases the lock. SQLite checkpoints the WAL when the last
// connection closes.
func (e *Exclusive) Close() error {
	err := e.Conn.Close()
	if closeErr := e.db.Close(); err == nil {
		err = closeErr
	}
	return err
}

type restorer interface {
	NewRestore(string) (*sqlite.Backup, error)
}

// RestoreFrom copies source over the locked database with the Online Backup
// API. The destination write transaction stays open across steps and commits
// only when the final step completes, so an interrupted copy leaves the prior
// database. Source must not change during the copy.
func (e *Exclusive) RestoreFrom(ctx context.Context, source string) error {
	return e.Conn.Raw(func(raw any) error {
		driverConn, ok := raw.(restorer)
		if !ok {
			return failure(Unavailable, "compact", "SQLite driver does not expose the Online Backup API", false, "use the supported modernc.org/sqlite driver", nil)
		}
		operation, err := driverConn.NewRestore(ReadOnlyDataSource(source))
		if err != nil {
			return failure(Unavailable, "compact", "cannot initialize the compact write-back", true, "recover using the held verified snapshot", err)
		}
		return stepWriteBack(ctx, operation)
	})
}

func stepWriteBack(ctx context.Context, operation *sqlite.Backup) error {
	for {
		if err := ContextErr(ctx); err != nil {
			_ = operation.Finish()
			return failure(Unavailable, "compact", "compact write-back was interrupted", true, "recover using the held verified snapshot", err)
		}
		more, err := operation.Step(BackupStepPages)
		if err != nil {
			_ = operation.Finish()
			return failure(Unavailable, "compact", "compact write-back step failed", true, "recover using the held verified snapshot", err)
		}
		if !more {
			break
		}
	}
	if err := operation.Finish(); err != nil {
		return failure(Unavailable, "compact", "cannot finish the compact write-back", true, "recover using the held verified snapshot", err)
	}
	return nil
}
