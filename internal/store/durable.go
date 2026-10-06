package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// writeTx is a store write transaction. An ordinary writeTx commits under the
// store's synchronous=NORMAL, which does not sync the write-ahead log per
// commit: the commit stays consistent but may roll back after power loss or an
// operating-system crash.
//
// A durable writeTx carries a consequential operation (CD-0050 D3). It runs on
// the pool's one connection, pinned and set to synchronous=FULL before BEGIN,
// because SQLite refuses a safety-level change inside an open transaction.
// Under FULL, SQLite syncs the WAL before COMMIT returns, so a durable commit
// that returns nil is durable. Because fsync flushes the whole dirty tail of
// the append-only WAL, the sync also makes every earlier commit on the store
// durable (CD-0050 D2).
//
// The sync waits for no reader. A WAL read mark held by another process cannot
// delay or fail a durable commit; only the writer lock, under the connection's
// busy_timeout, can.
//
// Commit and Rollback restore synchronous=NORMAL and release the pinned
// connection. The pool holds one connection, so no other store call may run
// between beginning a durable transaction and its Commit or Rollback, the same
// rule that holds for every open transaction. Both methods are safe to call
// again after either has run.
type writeTx struct {
	*sql.Tx
	ctx     context.Context
	conn    *sql.Conn
	durable *atomic.Uint64
}

// beginWriteTx opens a write transaction on the store, durable when durable is
// true.
func beginWriteTx(ctx context.Context, s *Store, durable bool) (*writeTx, error) {
	if !durable {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		return &writeTx{Tx: tx, ctx: ctx}, nil
	}
	conn, err := s.db.Conn(ctx)
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
	return &writeTx{Tx: tx, ctx: ctx, conn: conn, durable: &s.durableCommits}, nil
}

// Commit commits the transaction. For a durable transaction a nil error means
// SQLite synced the WAL; there is no committed-but-not-durable result.
func (w *writeTx) Commit() error {
	if w.durable != nil {
		// FULL syncs a write commit, not a read-only replay. Toggle storage
		// metadata so every acknowledgement flushes earlier NORMAL commits.
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

func durableCommitFailure(detail string, err error) *Failure {
	failure := wrapFailure(KindUnavailable, "durable_transaction", detail, true, "reconcile the requested effect before retrying", err)
	// Commit failure may follow persisted frames; marker failure may follow
	// an older effect being replayed. Neither proves the effect absent.
	failure.EffectPossible = true
	return failure
}

// Rollback rolls the transaction back and releases a pinned connection.
func (w *writeTx) Rollback() error {
	err := w.Tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		err = nil
	}
	if err != nil {
		failure := wrapFailure(KindUnavailable, "transaction_rollback", "cannot prove the transaction rolled back", false, "reconcile the requested effect before retrying", errors.Join(err, w.release()))
		failure.EffectPossible = true
		return failure
	}
	return errors.Join(err, w.release())
}

func (w *writeTx) finish(resultErr *error) {
	if err := w.Rollback(); err != nil {
		failure := wrapFailure(KindUnavailable, "transaction_cleanup", "cannot clean up the transaction", false, "inspect the transaction cleanup failure", errors.Join(*resultErr, err))
		*resultErr = failure
	}
}

func (w *writeTx) release() error {
	if w.conn == nil {
		return nil
	}
	conn := w.conn
	w.conn = nil
	return releaseDurableConn(conn)
}

// releaseDurableConn restores synchronous=NORMAL and returns the connection to
// the pool. A connection that cannot be restored is discarded, so the pool
// opens a fresh connection with the store's DSN pragmas instead of reusing one
// left at FULL.
func releaseDurableConn(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		discardConn(conn)
		return err
	}
	return conn.Close()
}

// discardConn closes conn and tells the pool not to reuse its driver
// connection.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// beginDurableTx opens a durable write transaction on the store.
func (s *Store) beginDurableTx(ctx context.Context) (*writeTx, error) {
	return beginWriteTx(ctx, s, true)
}

// DurableCommits reports how many durable transactions this store handle has
// committed since it opened.
func (s *Store) DurableCommits() uint64 {
	if s == nil {
		return 0
	}
	return s.durableCommits.Load()
}
