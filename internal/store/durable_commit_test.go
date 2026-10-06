package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestConsequentialCommitDoesNotWaitForWALReaders(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	snapshot, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback()
	var before int
	if err := snapshot.QueryRowContext(ctx, "SELECT count(*) FROM agent_clients").Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Keep the reader open through acknowledgement. A checkpoint-coupled
	// acknowledgement fails even though SQLite can commit beside this reader.
	if err := s.RegisterTrustedClient(ctx,
		TrustedClientRecord{ClientRef: "reader-safe-client", Status: "active", PrincipalRef: "principal-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`},
		TrustedClientKeyRecord{ClientRef: "reader-safe-client", KeyID: "key-1", PublicKey: make([]byte, 32), Status: "active"},
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("consequential acknowledgement waited for the reader: %v", err)
	}
	var during int
	if err := snapshot.QueryRowContext(ctx, "SELECT count(*) FROM agent_clients").Scan(&during); err != nil {
		t.Fatal(err)
	}
	if during != before {
		t.Fatalf("reader snapshot changed: before=%d during=%d", before, during)
	}
	var mode int
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 1 {
		t.Fatalf("ordinary connection synchronous=%d, want NORMAL", mode)
	}
}

func TestDurableReplayForcesCommit(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.appendSeedEvent(ctx); err != nil {
		t.Fatal(err)
	}
	observer, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	conn, err := observer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var before, after int
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.TransactDurable(ctx, func(*Transaction) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("a no-write replay acknowledged without committing WAL frames")
	}
	before = after
	if err := s.TransactDurable(ctx, func(tx *Transaction) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE durability_commits SET bit=bit WHERE id=1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&after); err != nil || after == before {
		t.Fatalf("same-value callback acknowledged without a write commit: %d -> %d err=%v", before, after, err)
	}
	var bit int
	if err := s.db.QueryRow(`SELECT bit FROM durability_commits WHERE id=1`).Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("two durable commits did not toggle the bit twice: bit=%d err=%v", bit, err)
	}
	if err := s.Transact(ctx, func(*Transaction) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT bit FROM durability_commits WHERE id=1`).Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("ordinary transaction changed the durability marker: bit=%d err=%v", bit, err)
	}
}

func TestDurableTransactionRestoresNormal(t *testing.T) {
	for _, outcome := range []string{"commit", "error", "cancel", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := Open(ctx, copyTestDatabase(t))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var escaped *Transaction
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				err = s.TransactDurable(ctx, func(tx *Transaction) error {
					escaped = tx
					var mode int
					if err := tx.tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&mode); err != nil || mode != 2 {
						t.Fatalf("writer synchronous=%d err=%v, want FULL", mode, err)
					}
					if _, err := tx.tx.ExecContext(ctx, `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,agent_scope_json,created_at) VALUES('mode-client','active','principal-1','[]','[]','[]','[]','now')`); err != nil {
						return err
					}
					switch outcome {
					case "error":
						return errors.New("callback refused")
					case "cancel":
						cancel()
					case "panic":
						panic("callback panic")
					}
					return nil
				})
			}()
			if (outcome == "error" || outcome == "cancel") && err == nil {
				t.Fatal("failed callback or canceled transaction acknowledged")
			}
			if outcome == "commit" && err != nil {
				t.Fatal(err)
			}
			if (outcome == "panic") != (recovered != nil) {
				t.Fatalf("recovered=%v outcome=%s", recovered, outcome)
			}
			if escaped == nil || escaped.tx != nil {
				t.Fatal("callback retained an active transaction")
			}
			var mode, rows, bit int
			if err := s.db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil || mode != 1 {
				t.Fatalf("ordinary synchronous=%d err=%v, want NORMAL", mode, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM agent_clients WHERE client_ref='mode-client'`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT bit FROM durability_commits WHERE id=1`).Scan(&bit); err != nil {
				t.Fatal(err)
			}
			want := 0
			if outcome == "commit" {
				want = 1
			}
			if rows != want || bit != want {
				t.Fatalf("rows=%d bit=%d, want %d after %s", rows, bit, want, outcome)
			}
			if err := s.appendSeedEvent(context.Background()); err != nil {
				t.Fatalf("ordinary write after %s: %v", outcome, err)
			}
		})
	}
}

func TestDurableTransactionExclusivelyOwnsConnection(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, copyTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.TransactDurable(ctx, func(*Transaction) error {
		if stats := s.db.Stats(); stats.MaxOpenConnections != 1 || stats.InUse != 1 {
			t.Fatalf("pool ownership: %+v", stats)
		}
		waitCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		conn, err := s.db.Conn(waitCtx)
		if conn != nil {
			_ = conn.Close()
			t.Fatal("another borrower reached the FULL connection")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("borrower error=%v, want deadline exceeded", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDurableMarkerFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, copyTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.TransactDurable(ctx, func(tx *Transaction) error {
		_, err := tx.tx.ExecContext(ctx, "DROP TABLE durability_commits")
		return err
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable || !failure.EffectPossible {
		t.Fatalf("marker failure=%v, want typed possible-effect failure", err)
	}
	var bit, mode int
	if err := s.db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("failed marker did not roll back: bit=%d err=%v", bit, err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil || mode != 1 {
		t.Fatalf("failed marker leaked synchronous=%d err=%v", mode, err)
	}
}

func TestDurableMarkerRefusesASuppressedWrite(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, copyTestDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.TransactDurable(ctx, func(tx *Transaction) error {
		_, err := tx.tx.ExecContext(ctx, `CREATE TEMP TRIGGER suppress_marker BEFORE UPDATE ON durability_commits BEGIN SELECT RAISE(IGNORE); END`)
		return err
	})
	if err == nil {
		t.Fatal("acknowledged without the forcing write")
	}
	var bit int
	if err := s.db.QueryRow(`SELECT bit FROM durability_commits WHERE id=1`).Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("suppressed commit was not rolled back: bit=%d err=%v", bit, err)
	}
}

// restoreFaultConnector delegates to the real SQLite driver and fails one
// restoration statement or commit response, without a production test seam.
type restoreFaultConnector struct {
	dsn          string
	failRestore  atomic.Bool
	failCommit   atomic.Bool
	opened       atomic.Int32
	closed       atomic.Int32
	failRollback atomic.Bool
}

func (c *restoreFaultConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func (c *restoreFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	c.opened.Add(1)
	return &restoreFaultConn{Conn: conn, owner: c}, nil
}

type restoreFaultConn struct {
	driver.Conn
	owner *restoreFaultConnector
}

func (c *restoreFaultConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, owner: c.owner}, nil
}

type commitFaultTx struct {
	driver.Tx
	owner *restoreFaultConnector
}

func (tx *commitFaultTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.owner.failCommit.Swap(false) {
		return errors.New("injected failure after SQLite commit")
	}
	return nil
}

func (tx *commitFaultTx) Rollback() error {
	if tx.owner.failRollback.Swap(false) {
		if err := tx.Tx.Commit(); err != nil {
			return err
		}
		return errors.New("injected uncertain rollback outcome")
	}
	return tx.Tx.Rollback()
}

func TestDurableRollbackFailureReportsPossibleEffect(t *testing.T) {
	for _, typedRefusal := range []bool{false, true} {
		t.Run(fmt.Sprintf("typed-refusal=%t", typedRefusal), func(t *testing.T) {
			ctx := context.Background()
			path := copyTestDatabase(t)
			seed, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			connector := &restoreFaultConnector{dsn: dataSourceName(path)}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer db.Close()
			s := &Store{db: db, path: path}
			var refusal error = errors.New("injected callback refusal")
			if typedRefusal {
				refusal = newFailure(KindInvalidOperation, "test_callback", "injected callback refusal", false, "inspect the callback")
			}
			err = s.TransactDurable(ctx, func(tx *Transaction) error {
				if _, err := tx.tx.ExecContext(ctx, "UPDATE durability_commits SET bit=1 WHERE id=1"); err != nil {
					return err
				}
				connector.failRollback.Store(true)
				return refusal
			})
			var failure *Failure
			if !errors.Is(err, refusal) || !errors.As(err, &failure) || !failure.EffectPossible {
				t.Fatalf("uncertain rollback lost its cause or possible effect: %v", err)
			}
			var bit int
			if err := db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 1 {
				t.Fatalf("injected rollback outcome did not persist: bit=%d err=%v", bit, err)
			}
		})
	}
}

func (c *restoreFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if query == "PRAGMA synchronous=NORMAL" && c.owner.failRestore.Swap(false) {
		return nil, errors.New("injected NORMAL restoration failure")
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *restoreFaultConn) Close() error {
	c.owner.closed.Add(1)
	return c.Conn.Close()
}

func TestDurableRestoreFailureDiscardsConnection(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(path)}
	connector.failRestore.Store(true)
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	s := &Store{db: db, path: path}
	err = s.TransactDurable(ctx, func(*Transaction) error { return nil })
	var failure *Failure
	if !errors.As(err, &failure) || !failure.EffectPossible {
		t.Fatalf("post-commit restoration failure=%v", err)
	}
	wrapped := wrapFailure(KindUnavailable, "test_acknowledgement", "outer owner failed", true, "reconcile_operation", err)
	if !wrapped.EffectPossible {
		t.Fatal("outer owner discarded possible-effect metadata")
	}
	if connector.closed.Load() != 1 {
		t.Fatal("failed FULL connection was not discarded")
	}
	var bit, mode int
	if err := db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 1 {
		t.Fatalf("commit did not persist before restoration failed: bit=%d err=%v", bit, err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil || mode != 1 {
		t.Fatalf("replacement synchronous=%d err=%v, want NORMAL", mode, err)
	}
	if connector.opened.Load() != 2 {
		t.Fatal("ordinary borrower did not receive a new connection")
	}
}

func TestDurableCleanupPreservesPriorPossibleEffect(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(path)}
	connector.failRestore.Store(true)
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	s := &Store{db: db, path: path}
	cause := newFailure(KindUnavailable, "earlier_effect", "an earlier effect is uncertain", false, "reconcile_operation")
	cause.EffectPossible = true
	err = s.TransactDurable(ctx, func(*Transaction) error { return cause })
	var failure *Failure
	if !errors.Is(err, cause) || !errors.As(err, &failure) || !failure.EffectPossible {
		t.Fatalf("cleanup lost prior possible effect: %v", err)
	}
	if !strings.Contains(err.Error(), "injected NORMAL restoration failure") {
		t.Fatalf("cleanup lost restoration failure: %v", err)
	}
}

func TestDurableRefusalReportsRestoreFailure(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(path)}
	connector.failRestore.Store(true)
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	s := &Store{db: db, path: path}
	refusal := errors.New("injected callback refusal")
	err = s.TransactDurable(ctx, func(*Transaction) error { return refusal })
	if !errors.Is(err, refusal) || !strings.Contains(err.Error(), "injected NORMAL restoration failure") {
		t.Fatalf("callback refusal lost cleanup failure: %v", err)
	}
	var mode, bit int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil || mode != 1 {
		t.Fatalf("replacement synchronous=%d err=%v, want NORMAL", mode, err)
	}
	if err := db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("refused transaction committed: bit=%d err=%v", bit, err)
	}
	if connector.closed.Load() != 1 || connector.opened.Load() != 2 {
		t.Fatal("failed FULL connection was not replaced")
	}
}

func TestDurableCommitFailureReportsPersistedEffect(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(path)}
	connector.failCommit.Store(true)
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	s := &Store{db: db, path: path}
	err = s.TransactDurable(ctx, func(*Transaction) error { return nil })
	var failure *Failure
	if !errors.As(err, &failure) || !failure.EffectPossible || !strings.Contains(err.Error(), "injected failure after SQLite commit") {
		t.Fatalf("persisted commit failure lost its possible effect: %v", err)
	}
	var bit, mode int
	if err := db.QueryRow("SELECT bit FROM durability_commits WHERE id=1").Scan(&bit); err != nil || bit != 1 {
		t.Fatalf("commit effect did not persist: bit=%d err=%v", bit, err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil || mode != 1 {
		t.Fatalf("post-failure synchronous=%d err=%v, want NORMAL", mode, err)
	}
}

func TestDurableClaimCommitFailureKeepsPersistedNativeState(t *testing.T) {
	ctx := context.Background()
	s, git, _ := worktreeFixture(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	connector := &restoreFaultConnector{dsn: dataSourceName(s.Path())}
	connector.failCommit.Store(true)
	s.db = sql.OpenDB(connector)
	s.db.SetMaxOpenConns(1)
	_, err := s.ClaimWorktree(ctx, baseClaim(git))
	var failure *Failure
	if !errors.As(err, &failure) || !failure.EffectPossible {
		t.Fatalf("claim commit failure lost its possible effect: %v", err)
	}
	entries, err := s.WorktreeEntries(ctx, "work-w")
	if err != nil || len(entries) != 1 || entries[0].State != worktreeEntryActive {
		t.Fatalf("claim did not persist before the error: entries=%+v err=%v", entries, err)
	}
	if git.countCalls("worktree remove") != 0 {
		t.Fatal("compensation removed native state still named by persisted authority")
	}
}

func TestMigrateV115ToV116AddsDurabilityMarker(t *testing.T) {
	useStampedBuild(t)
	ctx := context.Background()
	path := copyTestDatabase(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE durability_commits; DELETE FROM schema_migrations WHERE version=116;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("additive open: %v", err)
	}
	defer s.Close()
	var bit, floor int
	if err := s.db.QueryRow(`SELECT bit FROM durability_commits WHERE id=1`).Scan(&bit); err != nil || bit != 0 {
		t.Fatalf("migrated marker=%d err=%v", bit, err)
	}
	if err := s.db.QueryRow(`SELECT max(version) FROM schema_migrations WHERE breaking=1`).Scan(&floor); err != nil || floor != 111 {
		t.Fatalf("compatibility floor=%d err=%v, want unchanged 111", floor, err)
	}
	for _, invalid := range []string{
		`INSERT INTO durability_commits(id,bit) VALUES(2,0)`,
		`UPDATE durability_commits SET bit=2 WHERE id=1`,
	} {
		if _, err := s.db.ExecContext(ctx, invalid); err == nil {
			t.Fatalf("marker constraint admitted %s", invalid)
		}
	}
}
