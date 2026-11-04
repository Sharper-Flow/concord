package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"modernc.org/sqlite"
)

// watchdogRollbackConnector delegates to the real driver, forwards the
// session interfaces that decide connection reuse (driver.SessionResetter
// and driver.Validator; hiding them would make database/sql discard the
// connection after a watchdog rollback instead of pooling it), and makes the
// watchdog's rollback of a canceled write transaction observably fail: the
// wrapped driver.Tx.Rollback blocks until the test releases it and then
// fails without running the real rollback, so the write transaction stays
// open on the pooled connection. That is the settlement sql.ErrTxDone alone
// cannot prove.
type watchdogRollbackConnector struct {
	dsn     string
	entered chan struct{}
	release chan struct{}
}

func (c *watchdogRollbackConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func (c *watchdogRollbackConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &watchdogRollbackConn{Conn: conn, owner: c}, nil
}

type watchdogRollbackConn struct {
	driver.Conn
	owner *watchdogRollbackConnector
}

func (c *watchdogRollbackConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &watchdogRollbackTx{Tx: tx, owner: c.owner}, nil
}

func (c *watchdogRollbackConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *watchdogRollbackConn) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

type watchdogRollbackTx struct {
	driver.Tx
	owner *watchdogRollbackConnector
}

func (tx *watchdogRollbackTx) Commit() error { return tx.Tx.Commit() }

// Rollback blocks until the test releases it, then fails without running the
// real rollback.
func (tx *watchdogRollbackTx) Rollback() error {
	close(tx.owner.entered)
	<-tx.owner.release
	return errors.New("injected automatic rollback failure")
}

// A canceled write transaction that database/sql's watchdog settles is not
// proved rolled back: the watchdog discards its rollback error, and when that
// rollback failed the write transaction stays open on a pooled connection.
// The test orders every step through channels: the write begins, the context
// is canceled, and only the watchdog can reach the wrapped driver rollback
// because no store rollback has been called yet; after it fails, the scope's
// own Rollback sees sql.ErrTxDone and must report the unproved outcome while
// the pooled connection still shows the uncommitted revision advance.
func TestWatchdogSettledRollbackReportsUnprovedOutcome(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	before := revisionValue(t, s.db)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	connector := &watchdogRollbackConnector{
		dsn:     dataSourceName(s.Path()),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	write, err := beginWriteTx(ctx, db, nil, stateWriteAdmission)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-connector.entered
	close(connector.release)
	err = write.Rollback()
	var failure *Failure
	if !errors.As(err, &failure) || !failure.EffectPossible || !containsUncertainRollbackFailure(err) {
		t.Fatalf("watchdog-settled rollback reported as proved: %v", err)
	}
	if after := revisionValue(t, db); after != before+1 {
		t.Fatalf("failed watchdog rollback reported nothing possible, but revision is %d want uncommitted %d", after, before+1)
	}
}
