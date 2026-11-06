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

// boundaryConnector runs real SQLite statements and records driver settlement.
// With rollbackFault set, the cancellation also arms the base connector's
// uncertain rollback, which commits the writes and reports an error that
// database/sql's context watchdog discards.
type boundaryConnector struct {
	base          *restoreFaultConnector
	cancel        context.CancelFunc
	cancelSQL     string
	rollbackFault bool
	commits       atomic.Int32
	canceled      atomic.Bool
}

func (c *boundaryConnector) cancelAtBoundary() {
	c.base.failRollback.Store(c.rollbackFault)
	c.canceled.Store(true)
	c.cancel()
}

func (c *boundaryConnector) Driver() driver.Driver { return c.base.Driver() }
func (c *boundaryConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &boundaryConn{restoreFaultConn: conn.(*restoreFaultConn), owner: c}, nil
}

type boundaryConn struct {
	*restoreFaultConn
	owner *boundaryConnector
}

func (c *boundaryConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.restoreFaultConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &boundaryTx{Tx: tx, owner: c.owner}, nil
}
func (c *boundaryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.restoreFaultConn.ExecContext(ctx, query, args)
	if err == nil && c.owner.cancel != nil && c.owner.cancelSQL != "" && strings.Contains(query, c.owner.cancelSQL) {
		c.owner.cancelAtBoundary()
	}
	return result, err
}

type boundaryTx struct {
	driver.Tx
	owner *boundaryConnector
}

func (tx *boundaryTx) Commit() error {
	tx.owner.commits.Add(1)
	return tx.Tx.Commit()
}

func commitBoundaryDB(t *testing.T, path string, landed bool) (*sql.DB, *boundaryConnector) {
	t.Helper()
	c := &boundaryConnector{base: &restoreFaultConnector{dsn: dataSourceName(path)}}
	c.base.failCommit.Store(landed)
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, c
}

func commitBoundaryFailure(t *testing.T, err error, landed, unprovedRollback, durable bool, op, detail string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Errorf("missing typed commit failure: %v", err)
		return
	}
	if durable {
		owner := findFailureByDetail(err, "cannot finish the durable commit")
		if owner == nil || owner.Op != "durable_transaction" || !owner.EffectPossible || !owner.RetrySafe || owner.RecoveryAction != "reconcile the requested effect before retrying" {
			t.Errorf("durable commit owner not preserved: %+v; error=%v", owner, err)
		} else if failure != owner && failure.Detail != "cannot clean up the transaction" {
			t.Errorf("classified durable commit owner was shadowed: %v", err)
		}
	} else {
		owner := findFailureByDetail(err, detail)
		if owner == nil || owner.Op != op {
			t.Errorf("missing boundary commit failure %s/%s: %v", op, detail, err)
		}
	}
	if landed {
		if !strings.Contains(err.Error(), "injected failure after SQLite commit") {
			t.Errorf("missing landed driver cause: %v", err)
		}
		if !durable && (!failure.EffectPossible || failure.RetrySafe || !strings.Contains(strings.ToLower(failure.RecoveryAction), "reconcile")) {
			t.Errorf("landed commit envelope: effect=%t retry=%t recovery=%q error=%v", failure.EffectPossible, failure.RetrySafe, failure.RecoveryAction, err)
		}
		return
	}
	if durable {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("durable commit lost canceled cause: %v", err)
		}
		return
	}
	if unprovedRollback && (!failure.EffectPossible || failure.RetrySafe) {
		t.Errorf("refused commit with an unproved rollback claimed a clean retry: %+v; error=%v", failure, err)
		return
	}
	if failure.Detail == "cannot clean up the transaction" {
		if !failure.EffectPossible || failure.RetrySafe {
			t.Errorf("unproved cleanup envelope: %+v", failure)
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("cleanup lost canceled commit cause: %v", err)
		}
		return
	}
	switch {
	case errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone):
		if failure.EffectPossible || !failure.RetrySafe {
			t.Errorf("clean refusal envelope: %+v", failure)
		}
	case errors.Is(err, sql.ErrTxDone):
		if !failure.EffectPossible || failure.RetrySafe {
			t.Errorf("unproved settlement envelope: %+v", failure)
		}
	default:
		t.Errorf("cancel did not reach commit: %v", err)
	}
}

func TestCommitBoundaryTruth(t *testing.T) {
	for _, boundary := range []string{"operation", "workflow_replay", "workflow_normal", "manifest_repair", "migration"} {
		for _, mode := range []string{"landed", "canceled", "unproved_rollback"} {
			landed, unprovedRollback := mode == "landed", mode == "unproved_rollback"
			t.Run(boundary+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var db *sql.DB
				var connector *boundaryConnector
				var err error
				var witness func() int
				durable := false
				var op, detail string
				reachedBoundary := false
				switch boundary {
				case "operation":
					op, detail = "apply_operation", "cannot commit domain operation"
					path := copyTestDatabase(t)
					seed, e := Open(ctx, path)
					if e != nil {
						t.Fatal(e)
					}
					_ = seed.Close()
					db, connector = commitBoundaryDB(t, path, landed)
					connector.cancel, connector.rollbackFault = cancel, unprovedRollback
					s := &Store{db: db, path: path}
					var hook preCommitHook
					if !landed {
						hook = func() error { reachedBoundary = true; connector.cancelAtBoundary(); return nil }
					}
					operation := Operation{Events: []Event{
						productCreatedEvent("boundary-product", "boundary-event"),
						projectCreatedEvent("boundary-project", "boundary-project-event"),
						operationEvent("boundary-membership", "product_project.added", SubjectProduct, "boundary-product", map[string]any{"product_id": "boundary-product", "project_id": "boundary-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
					}}
					_, err = applyOperationWithResult(ctx, s, operation, hook)
					witness = func() int {
						var n int
						if e := db.QueryRow("SELECT count(*) FROM products WHERE id='boundary-product'").Scan(&n); e != nil {
							t.Fatal(e)
						}
						return n
					}
				case "workflow_replay", "workflow_normal":
					durable = true
					s := openTemp(t)
					actor, version := seedTestWorkflow(t, s, "boundary-work")
					seedWorkflowLaw(t, s)
					version = issue31WorkflowActionWithPayload(t, s, "boundary-work", version, "record_plan", "boundary-plan", actor, []byte(testWorkflowPlanDocument))
					// approve first: CD-0203 D1 forbids closing the contract step without a contract
					version = issue31WorkflowActionWithPayload(t, s, "boundary-work", version, "approve_contract", "boundary-approve", actor, []byte(contractStepApprovalPayload))
					if _, e := s.db.Exec("CREATE TABLE boundary_witness (value INTEGER)"); e != nil {
						t.Fatal(e)
					}
					path := s.Path()
					if e := s.db.Close(); e != nil {
						t.Fatal(e)
					}
					db, connector = commitBoundaryDB(t, path, landed)
					s.db = db
					if !landed {
						connector.cancel, connector.rollbackFault = cancel, unprovedRollback
						connector.cancelSQL = "INSERT INTO durability_commits"
					}
					payload := []byte(`{"step_id":"plan","note":"Boundary witness."}`)
					request := WorkflowActionPreflightRequest{WorkID: "boundary-work", ExpectedVersion: version, ActionID: "complete_step", Payload: payload, Actor: actor}
					var before func(*Transaction) (bool, error)
					if boundary == "workflow_replay" {
						before = func(tx *Transaction) (bool, error) {
							_, e := tx.tx.ExecContext(ctx, "INSERT INTO boundary_witness VALUES(1)")
							reachedBoundary = e == nil
							return true, e
						}
					}
					err = authorizeWorkflowActionAtBoundaryCore(ctx, s, request, before, nil, func(tx *Transaction) error {
						_, e := tx.tx.ExecContext(ctx, "INSERT INTO boundary_witness VALUES(1)")
						reachedBoundary = e == nil
						return e
					})
					witness = func() int {
						var n int
						if e := db.QueryRow("SELECT count(*) FROM boundary_witness").Scan(&n); e != nil {
							t.Fatal(e)
						}
						return n
					}
				case "manifest_repair":
					op, detail = "upgrade", "cannot commit the manifest repair"
					path := copyTestDatabase(t)
					seed, e := Open(ctx, path)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = seed.db.Exec("ALTER TABLE schema_migrations DROP COLUMN breaking"); e != nil {
						t.Fatal(e)
					}
					_ = seed.Close()
					db, connector = commitBoundaryDB(t, path, landed)
					if !landed {
						connector.cancel, connector.rollbackFault = cancel, unprovedRollback
						connector.cancelSQL = "ALTER TABLE schema_migrations ADD COLUMN breaking"
					}
					err = repairManifestBreakingColumn(ctx, db)
					witness = func() int {
						var n int
						if e := db.QueryRow("SELECT count(*) FROM pragma_table_info('schema_migrations') WHERE name='breaking'").Scan(&n); e != nil {
							t.Fatal(e)
						}
						return n
					}
				case "migration":
					op, detail = "migrate", "cannot commit schema migration"
					path := copyTestDatabase(t)
					seed, e := Open(ctx, path)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = seed.db.Exec("DROP TABLE durability_commits; DELETE FROM schema_migrations WHERE version=116"); e != nil {
						t.Fatal(e)
					}
					_ = seed.Close()
					db, connector = commitBoundaryDB(t, path, landed)
					if !landed {
						connector.cancel, connector.rollbackFault = cancel, unprovedRollback
						connector.cancelSQL = "INSERT INTO schema_migrations (version, name, checksum, applied_at, breaking)"
					}
					err = migrateOnce(ctx, db, migrateForOpen, nil, func() time.Time { return time.Unix(1, 0) })
					witness = func() int {
						var n int
						if e := db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version=116").Scan(&n); e != nil {
							t.Fatal(e)
						}
						return n
					}
				}
				if landed && connector.commits.Load() != 1 {
					t.Errorf("driver commits=%d, want 1; error=%v", connector.commits.Load(), err)
				}
				if !landed && connector.commits.Load() != 0 {
					t.Errorf("canceled driver commits=%d", connector.commits.Load())
				}
				if !landed && !connector.canceled.Load() {
					t.Errorf("cancellation did not reach the boundary's commit fixture: %v", err)
				}
				if (boundary == "operation" && !landed || durable) && !reachedBoundary {
					t.Errorf("boundary callback was not reached: %v", err)
				}
				commitBoundaryFailure(t, err, landed, unprovedRollback, durable, op, detail)
				got := witness()
				want := 0
				if landed || unprovedRollback {
					want = 1
				}
				if got != want {
					t.Errorf("persisted witness=%d want=%d", got, want)
				}
			})
		}
	}
}
