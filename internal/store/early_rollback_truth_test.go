package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type earlyRollbackConnector struct {
	base     *restoreFaultConnector
	query    string
	version  int64
	cause    error
	fault    bool
	postExec bool
	reached  atomic.Bool
}

func (c *earlyRollbackConnector) Driver() driver.Driver { return c.base.Driver() }
func (c *earlyRollbackConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &earlyRollbackConn{restoreFaultConn: conn.(*restoreFaultConn), owner: c}, nil
}

type earlyRollbackConn struct {
	*restoreFaultConn
	owner *earlyRollbackConnector
}

func (c *earlyRollbackConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == c.owner.query && (c.owner.version == 0 || len(args) > 0 && args[0].Value == c.owner.version) {
		if c.owner.postExec {
			if _, err := c.restoreFaultConn.ExecContext(ctx, query, args); err != nil {
				return nil, err
			}
		}
		c.owner.reached.Store(true)
		c.owner.base.failRollback.Store(c.owner.fault)
		return nil, c.owner.cause
	}
	return c.restoreFaultConn.ExecContext(ctx, query, args)
}

func earlyRollbackDB(t *testing.T, path string, query string, version int64, cause error, fault bool) (*sql.DB, *earlyRollbackConnector) {
	t.Helper()
	c := &earlyRollbackConnector{base: &restoreFaultConnector{dsn: dataSourceName(path)}, query: query, version: version, cause: cause, fault: fault}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, c
}

func earlyRollbackEffect(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Errorf("original cause lost: %v", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Errorf("uncertain rollback has no typed failure: %v", err)
	} else if !failure.EffectPossible || failure.RetrySafe {
		t.Errorf("uncertain rollback envelope: effect=%t retry_safe=%t recovery=%q; error=%v", failure.EffectPossible, failure.RetrySafe, failure.RecoveryAction, err)
	}
	rollback := findFailureByDetail(err, "cannot prove the transaction rolled back")
	if rollback == nil || !strings.Contains(rollback.RecoveryAction, "reconcile") {
		t.Errorf("uncertain rollback lost reconcile information: %v", err)
	}
}

func TestEarlyRollbackTruth(t *testing.T) {
	ctx := context.Background()
	for _, fault := range []bool{false, true} {
		name := "clean"
		if fault {
			name = "uncertain"
		}
		t.Run("operation/"+name, func(t *testing.T) {
			path := copyTestDatabase(t)
			seed, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected operation refusal")
			db, connector := earlyRollbackDB(t, path, "", 0, cause, fault)
			s := &Store{db: db, path: path}
			operation := Operation{Events: []Event{
				productCreatedEvent("early-product", "early-event"),
				projectCreatedEvent("early-project", "early-project-event"),
				operationEvent("early-membership", "product_project.added", SubjectProduct, "early-product", map[string]any{"product_id": "early-product", "project_id": "early-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
			}}
			reached := false
			_, err = applyOperationWithResult(ctx, s, operation, func() error {
				reached = true
				connector.base.failRollback.Store(fault)
				return cause
			})
			if !reached {
				t.Fatalf("hook not reached: %v", err)
			}
			if fault {
				earlyRollbackEffect(t, err, cause)
			} else if err != cause {
				t.Errorf("clean rollback changed sentinel: %v", err)
			}
			var n int
			if e := db.QueryRow("SELECT count(*) FROM products WHERE id='early-product'").Scan(&n); e != nil {
				t.Fatal(e)
			}
			want := 0
			if fault {
				want = 1
			}
			if n != want {
				t.Errorf("product witness=%d want=%d", n, want)
			}
		})
		t.Run("migration/"+name, func(t *testing.T) {
			path := copyTestDatabase(t)
			seed, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := seed.db.Exec("DROP TABLE durability_commits; DELETE FROM schema_migrations WHERE version=116"); err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected migration manifest insert refusal")
			db, connector := earlyRollbackDB(t, path, "INSERT INTO schema_migrations (version, name, checksum, applied_at, breaking) VALUES (?, ?, ?, ?, ?)", 116, cause, fault)
			err = migrateOnce(ctx, db, migrateForOpen, nil, func() time.Time { return time.Unix(1, 0) })
			if !connector.reached.Load() {
				t.Fatalf("migration insert not reached: %v", err)
			}
			if fault {
				earlyRollbackEffect(t, err, cause)
			} else if !errors.Is(err, cause) {
				t.Errorf("clean rollback lost sentinel: %v", err)
			}
			var n int
			if e := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='durability_commits'").Scan(&n); e != nil {
				t.Fatal(e)
			}
			want := 0
			if fault {
				want = 1
			}
			if n != want {
				t.Errorf("migration DDL witness=%d want=%d", n, want)
			}
		})
		t.Run("manifest_repair/"+name, func(t *testing.T) {
			path := copyTestDatabase(t)
			seed, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := seed.db.Exec("ALTER TABLE schema_migrations DROP COLUMN breaking"); err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("injected post-execution ALTER error")
			db, connector := earlyRollbackDB(t, path, "ALTER TABLE schema_migrations ADD COLUMN breaking INTEGER NOT NULL DEFAULT 1", 0, cause, fault)
			connector.postExec = true
			err = repairManifestBreakingColumn(ctx, db)
			if !connector.reached.Load() {
				t.Fatalf("real ALTER did not complete: %v", err)
			}
			if fault {
				earlyRollbackEffect(t, err, cause)
			} else if !errors.Is(err, cause) {
				t.Errorf("clean rollback lost sentinel: %v", err)
			}
			var n int
			if e := db.QueryRow("SELECT count(*) FROM pragma_table_info('schema_migrations') WHERE name='breaking'").Scan(&n); e != nil {
				t.Fatal(e)
			}
			want := 0
			if fault {
				want = 1
			}
			if n != want {
				t.Errorf("manifest column witness=%d want=%d", n, want)
			}
		})
		t.Run("unstamped_stop/"+name, func(t *testing.T) {
			path := copyTestDatabase(t)
			seed, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			db, connector := earlyRollbackDB(t, path, "", 0, nil, fault)
			connector.base.failRollback.Store(fault)
			err = migrateOnce(ctx, db, migrateForUnstamped, nil)
			if connector.base.failRollback.Load() {
				t.Fatal("stop path did not reach rollback")
			}
			if !fault {
				if err != nil {
					t.Errorf("clean stop returned an error: %v", err)
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Op != "transaction_cleanup" || !failure.EffectPossible || failure.RetrySafe {
				t.Fatalf("unproved stop cleanup envelope: %v", err)
			}
			if rollback := findFailureByDetail(err, "cannot prove the transaction rolled back"); rollback == nil || !strings.Contains(rollback.RecoveryAction, "reconcile") {
				t.Errorf("unproved stop cleanup lost reconcile information: %v", err)
			}
			if migrationLockContended(err) {
				t.Errorf("unproved stop cleanup would be retried: %v", err)
			}
		})
	}
	t.Run("manifest_repair/no_write", func(t *testing.T) {
		path := copyTestDatabase(t)
		seed, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := seed.db.Exec("ALTER TABLE schema_migrations DROP COLUMN breaking"); err != nil {
			t.Fatal(err)
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
		cause := errors.New("injected ALTER refusal")
		db, connector := earlyRollbackDB(t, path, "ALTER TABLE schema_migrations ADD COLUMN breaking INTEGER NOT NULL DEFAULT 1", 0, cause, true)
		schema := func() string {
			var result string
			if e := db.QueryRow("SELECT coalesce(group_concat(sql, char(10)), '') FROM (SELECT sql FROM sqlite_schema ORDER BY type, name)").Scan(&result); e != nil {
				t.Fatal(e)
			}
			return result
		}
		before := schema()
		err = repairManifestBreakingColumn(ctx, db)
		if !connector.reached.Load() || !errors.Is(err, cause) {
			t.Errorf("ALTER refusal not reached or cause lost: %v", err)
		}
		if connector.base.failRollback.Load() {
			t.Error("rollback fault was not consumed")
		}
		if after := schema(); before != after {
			t.Errorf("schema changed despite refused ALTER: before=%q after=%q", before, after)
		}
	})
}
