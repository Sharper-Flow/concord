package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

// commitErrorConnector delegates to real SQLite and loses the acknowledgement
// after its FULL commit. This tests uncertain effects, not a physical fsync fault.
type commitErrorConnector struct {
	driver driver.Driver
	path   string
	err    error
}

func (c *commitErrorConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return &commitErrorConn{Conn: conn, err: c.err}, nil
}

func (c *commitErrorConnector) Driver() driver.Driver { return c.driver }

type commitErrorConn struct {
	driver.Conn
	err error
}

func (c *commitErrorConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &commitErrorTx{Tx: tx, err: c.err}, nil
}

type commitErrorTx struct {
	driver.Tx
	err error
}

func (tx *commitErrorTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	return tx.err
}

func TestDurableCommitErrorPreservesPossibleEffect(t *testing.T) {
	s := openTemp(t)
	cause := errors.New("synthetic lost commit acknowledgement")
	connector := &commitErrorConnector{driver: s.db.Driver(), path: s.Path(), err: cause}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = sql.OpenDB(connector)
	s.db.SetMaxOpenConns(1)
	ctx := context.Background()
	err := s.TransactDurable(ctx, func(tx *Transaction) error {
		if got := readSynchronous(t, ctx, tx.tx); got != 2 {
			t.Fatalf("transaction synchronous=%d, want FULL", got)
		}
		_, err := tx.tx.ExecContext(ctx, "CREATE TABLE uncertain_commit_probe (id INTEGER PRIMARY KEY)")
		return err
	})
	var failure *Failure
	if !errors.As(err, &failure) || !errors.Is(err, cause) || !failure.EffectPossible || failure.Op != "durable_transaction" || !failure.RetrySafe {
		t.Fatalf("uncertain durable commit=%+v, want typed possible effect retaining cause", err)
	}
	wrapped := wrapFailure(KindUnavailable, "authority", "outer authority boundary", true, "reconcile operation", err)
	if !wrapped.EffectPossible || !errors.Is(wrapped, cause) {
		t.Fatalf("outer failure lost commit uncertainty: %+v", wrapped)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='uncertain_commit_probe'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("committed effect count=%d err=%v", count, err)
	}
	if s.DurableCommits() != 0 || readSynchronous(t, ctx, s.db) != 1 {
		t.Fatal("uncertain commit acknowledged durability or did not restore NORMAL")
	}
	refusal := newFailure(KindInvalidInput, "callback", "synthetic precommit refusal", false, "correct request")
	err = s.TransactDurable(ctx, func(tx *Transaction) error {
		if _, err := tx.tx.ExecContext(ctx, "CREATE TABLE rolled_back_probe (id INTEGER PRIMARY KEY)"); err != nil {
			return err
		}
		return refusal
	})
	if err != refusal || refusal.EffectPossible {
		t.Fatalf("precommit refusal=%v, want original no-effect refusal", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='rolled_back_probe'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back effect count=%d err=%v", count, err)
	}
}
