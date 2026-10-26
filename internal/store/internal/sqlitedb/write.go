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
func (w *WriteTx) Rollback() error {
	err := w.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		err = nil
	}
	if err != nil {
		f := failure(Unavailable, "transaction_rollback", "cannot prove the transaction rolled back", false, "reconcile the requested effect before retrying", errors.Join(err, w.release()))
		f.EffectPossible = true
		return f
	}
	return errors.Join(err, w.release())
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
