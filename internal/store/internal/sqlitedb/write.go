package sqlitedb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// WriteTx owns an ordinary NORMAL write or a durable FULL write. FULL is set
// before BEGIN on the pool's pinned connection, since SQLite refuses safety
// changes inside a transaction. Commit syncs the whole dirty WAL tail without
// waiting for readers. Commit and Rollback restore NORMAL or discard the
// connection, then release it. No pool call may run while this scope is open.
type WriteTx struct {
	*sql.Tx
	ctx     context.Context
	conn    *sql.Conn
	durable *atomic.Uint64
	// commitRan and rollbackRan record settlements this scope observed
	// through its own calls: the driver commit ran, or a driver-level
	// rollback ran. database/sql's context watchdog can also settle the
	// transaction between this scope's statements, and it discards its
	// rollback error (Tx.awaitDone), so only these observations prove what
	// ErrTxDone from a later call means.
	commitRan   bool
	rollbackRan bool
}

type WriteAdmissionKind uint8

const (
	StateWriteAdmission WriteAdmissionKind = iota + 1
	// Schema writes precede the revision table; migrations advance it later.
	SchemaWriteAdmission
)

// BeginWrite admits state or schema writes. A non-nil counter selects FULL
// durability and records successful commits on that counter.
func BeginWrite(ctx context.Context, db *sql.DB, durable *atomic.Uint64, kind WriteAdmissionKind) (*WriteTx, error) {
	if kind != StateWriteAdmission && kind != SchemaWriteAdmission {
		return nil, failure(InvalidOperation, "transaction", "write admission kind is required", false, "select state or schema write admission", nil)
	}
	var write *WriteTx
	if durable == nil {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		write = &WriteTx{Tx: tx, ctx: ctx}
	} else {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
			discardConn(conn)
			return nil, err
		}
		var level int
		if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&level); err != nil {
			return nil, errors.Join(err, releaseDurableConn(conn))
		}
		if level != 2 {
			return nil, errors.Join(fmt.Errorf("durable transaction requires synchronous=2 (FULL), connection reports %d", level), releaseDurableConn(conn))
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return nil, errors.Join(err, releaseDurableConn(conn))
		}
		write = &WriteTx{Tx: tx, ctx: ctx, conn: conn, durable: durable}
	}
	if kind == StateWriteAdmission {
		if err := AdvanceRevision(ctx, write.Tx); err != nil {
			return nil, errors.Join(err, write.Rollback())
		}
	}
	return write, nil
}

// Commit forces a marker write for durable acknowledgements, including replay
// callbacks. A nil durable result means SQLite synced the WAL.
func (w *WriteTx) Commit() error {
	if w.durable != nil {
		result, err := w.Tx.ExecContext(w.ctx, `INSERT INTO durability_commits(id,bit) VALUES(1,1)
			ON CONFLICT(id) DO UPDATE SET bit=1-durability_commits.bit`)
		if err != nil {
			return durableCommitFailure("cannot write the durability commit marker", errors.Join(err, w.Rollback()))
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return durableCommitFailure(fmt.Sprintf("durability commit marker changed %d rows, want one", rows), errors.Join(err, w.Rollback()))
		}
	}
	err := w.Tx.Commit()
	// ErrTxDone means the context watchdog settled the transaction before
	// the commit ran; that outcome is unobservable here, so Rollback must
	// report it as unproved. A context error with a done context means
	// Tx.Commit refused before attempting the driver commit, so no commit
	// ran either. Every other outcome, including a failed commit, proves
	// the driver commit call ran and the commit envelope owns the effect
	// truth from here on.
	switch {
	case errors.Is(err, sql.ErrTxDone):
	case contextCanceled(err) && w.ctx.Err() != nil:
	default:
		w.commitRan = true
	}
	if err == nil && w.durable != nil {
		w.durable.Add(1)
	}
	err = errors.Join(err, w.release())
	if err != nil && w.durable != nil {
		return durableCommitFailure("cannot finish the durable commit", err)
	}
	return err
}

func durableCommitFailure(detail string, err error) *Error {
	f := failure(Unavailable, "durable_transaction", detail, true, "reconcile the requested effect before retrying", err)
	// Persisted frames or an older replayed effect cannot be proved absent.
	f.EffectPossible = true
	return f
}

// Rollback releases a pinned connection even if rollback cannot be proved.
//
// ErrTxDone is proof of settlement only when this scope observed the
// settlement itself: its own commit call ran the driver commit (the commit
// envelope then owns the effect truth), or an earlier Rollback ran a
// driver-level rollback. Any other ErrTxDone means database/sql's context
// watchdog settled the transaction while this scope was between statements,
// and the watchdog unconditionally discards its rollback error, so the
// rollback outcome cannot be observed through the returned error. On this
// driver a failed watchdog rollback leaves the write transaction open on a
// connection database/sql may return to the pool (modernc.org/sqlite
// implements SessionResetter and Validator, whose checks do not reject it),
// where a later commit would commit those writes. Such a settlement reports
// the unproved-rollback failure instead of settled.
func (w *WriteTx) Rollback() error {
	err := w.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		if w.commitRan || w.rollbackRan {
			err = nil
		} else {
			f := failure(Unavailable, "transaction_rollback", "cannot prove the transaction rolled back", false, "reconcile the requested effect before retrying", w.release())
			f.EffectPossible = true
			return f
		}
	} else {
		w.rollbackRan = true
	}
	if err != nil {
		f := failure(Unavailable, "transaction_rollback", "cannot prove the transaction rolled back", false, "reconcile the requested effect before retrying", errors.Join(err, w.release()))
		f.EffectPossible = true
		return f
	}
	return errors.Join(err, w.release())
}

// contextCanceled reports whether err is a context cancellation, which
// Tx.Commit returns unchanged when it refuses to attempt the commit of a
// transaction whose context is already done.
func contextCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (w *WriteTx) release() error {
	if w.conn == nil {
		return nil
	}
	conn := w.conn
	w.conn = nil
	return releaseDurableConn(conn)
}

// Restore before reuse, or discard so the next borrower gets DSN settings.
func releaseDurableConn(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		discardConn(conn)
		return err
	}
	return conn.Close()
}

func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
