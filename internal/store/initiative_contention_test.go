package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func withFold(ctx context.Context, s *Store, fn func(tx *sql.Tx, scope *foldScope) error) error {
	return s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginFold(ctx, tx.tx)
		if err != nil {
			return err
		}
		return errors.Join(fn(tx.tx, scope), scope.close(ctx))
	})
}

// The Product and Project must exist before seeding the projection.
func seedInitiativeProjection(tb testing.TB, s *Store, productID, projectID string, n, m int) {
	tb.Helper()
	if n <= 0 || m <= 0 {
		tb.Fatalf("seed: n=%d m=%d", n, m)
	}
	now := "2026-01-01T00:00:00Z"
	cols := `id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at`
	ph := func(limit int, name string) string {
		return fmt.Sprintf(`%s(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM %s WHERE n<%d)`, name, name, limit)
	}
	si, sc := ph(n-1, "si"), ph(m-1, "sc")
	stmts := []struct {
		sql  string
		args []any
	}{
		{fmt.Sprintf(`WITH RECURSIVE %s INSERT INTO work_items(%s) SELECT 'init-'||printf('%%05d',n),'initiative','T','needed',0,'standard',1,?,? FROM si`, si, cols), []any{now, now}},
		{fmt.Sprintf(`WITH RECURSIVE %s,%s INSERT INTO work_items(%s) SELECT 'init-'||printf('%%05d',i.n)||'-task-'||printf('%%05d',c.n),'task','T','needed',0,'standard',1,?,? FROM si i, sc c`, si, sc, cols), []any{now, now}},
		{`INSERT INTO work_projects(work_id,project_id,role) SELECT id,?,'primary' FROM work_items WHERE kind IN ('initiative','task')`, []any{projectID}},
		{`INSERT INTO product_projects(product_id,project_id,role) VALUES(?,?,'primary') ON CONFLICT DO NOTHING`, []any{productID, projectID}},
		{fmt.Sprintf(`WITH RECURSIVE %s,%s INSERT INTO initiative_entries(initiative_work_id,child_work_id,position,required) SELECT 'init-'||printf('%%05d',i.n),'init-'||printf('%%05d',i.n)||'-task-'||printf('%%05d',c.n),c.n,1 FROM si i, sc c`, si, sc), nil},
		// Fixture-only negative identities leave positive event-derived relation
		// identities available to the ordinary writer being exercised.
		{fmt.Sprintf(`WITH RECURSIVE %s,%s INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) SELECT -(i.n*%d+c.n+1),'init-'||printf('%%05d',i.n),'init-'||printf('%%05d',i.n)||'-task-'||printf('%%05d',c.n),'includes',? FROM si i, sc c`, si, sc, m), []any{now}},
	}
	if err := withFold(context.Background(), s, func(tx *sql.Tx, _ *foldScope) error {
		for _, s := range stmts {
			if _, err := tx.ExecContext(context.Background(), s.sql, s.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
}

func openWaiterPair(t *testing.T, s *Store) (boot, ord *Store) {
	t.Helper()
	boot, err := Open(context.Background(), s.Path())
	if err != nil {
		t.Fatal(err)
	}
	ord, err = Open(context.Background(), s.Path())
	if err != nil {
		_ = boot.Close()
		t.Fatal(err)
	}
	return boot, ord
}

func TestInitiativeValidatorDoesNotBlockConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	repo := initBootstrapStoreRepo(t)
	seedBootstrapStoreAuthority(t, s, repo)
	seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 150, 100)
	boot, ord := openWaiterPair(t, s)
	defer boot.Close()
	defer ord.Close()
	for _, st := range []*Store{s, boot, ord} {
		if _, err := st.db.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", testContentionBusyTimeoutMs)); err != nil {
			t.Fatal(err)
		}
	}
	bootReq := bootstrapStoreRequest()
	bootReq.IdempotencyKey = "con852-bootstrap"
	operationID, wantWork, digest, err := CanonicalBootstrapIdentity(bootReq)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.resolveFreshCreationBase(ctx, ExecGitRunner{}, bootReq.ProjectID, wantWork, bootReq.Ref)
	if err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repo)
	initVersion := readWorkVersion(t, ord, "init-00000")
	newChild := "con852-child"
	var bootResult BootstrapResult
	cases := []struct {
		name   string
		waiter func() error
		assert func(*testing.T)
	}{
		{
			name: "bootstrap",
			waiter: func() error {
				// Preflight resolves the location before contention. The existing
				// in-memory runner keeps the journal's absence probe off native Git.
				prepared, err := boot.prepareBootstrapMode(ctx, bootReq, operationID, wantWork, digest, false, bootReq, location, git)
				if err != nil {
					return err
				}
				if prepared.State != "pending" || prepared.Result.WorkID != wantWork || prepared.Location != location {
					return fmt.Errorf("prepared bootstrap = %+v, want pending journal for %s at %+v", prepared, wantWork, location)
				}
				bootResult, err = boot.BootstrapWorktree(ctx, bootReq, nil)
				return err
			},
			assert: func(t *testing.T) {
				var n int
				if err := s.db.QueryRow(`SELECT count(*) FROM bootstrap_operations WHERE operation_id=? AND work_id=? AND state='completed'`, operationID, wantWork).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 || bootResult.OperationID != operationID || bootResult.WorkID != wantWork {
					t.Errorf("bootstrap journal rows=%d result=%+v want completed %s/%s", n, bootResult, operationID, wantWork)
				}
				if err := s.db.QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, wantWork).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Errorf("bootstrap work rows=%d want 1 for %s", n, wantWork)
				}
			},
		},
		{
			name: "ordinary",
			waiter: func() error {
				return withFold(ctx, ord, func(tx *sql.Tx, scope *foldScope) error {
					if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at) VALUES(?, 'task', 'T', 'needed', 0, 'standard', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, newChild); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?, 'project-bootstrap', 'primary')`, newChild); err != nil {
						return err
					}
					event, err := InitiativeEntryEvent("con852-entry", "initiative_entry.added", "init-00000",
						InitiativeEntry{ChildWorkID: newChild, Position: 100, Required: true}, "test", time.Unix(40, 0).UTC(), initVersion)
					if err != nil {
						return err
					}
					_, err = applyOperationTx(ctx, tx, Operation{
						Events:           []Event{event},
						ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "init-00000"): initVersion},
					}, scope, false)
					return err
				})
			},
			assert: func(t *testing.T) {
				var n int
				if err := s.db.QueryRow(`SELECT count(*) FROM initiative_entries WHERE initiative_work_id='init-00000' AND child_work_id=? AND position=100 AND required=1`, newChild).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Errorf("entry rows=%d want 1 for child=%s", n, newChild)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type holderResult struct {
				err                   error
				validateDur, totalDur time.Duration
				released              time.Time
				queries, queryRows    int64
			}
			holderReady := make(chan struct{})
			holderDone := make(chan holderResult, 1)
			go func() {
				var result holderResult
				var start time.Time
				result.err = s.TransactDurable(ctx, func(tx *Transaction) error {
					start = time.Now()
					scope, err := beginFold(ctx, tx.tx)
					if err != nil {
						return err
					}
					close(holderReady)
					counter := &countingQueryer{Tx: tx.tx}
					v0 := time.Now()
					err = validateInitiativeInvariantsTx(ctx, counter)
					result.validateDur = time.Since(v0)
					result.queries, result.queryRows = counter.queries.Load(), counter.queryRows.Load()
					return errors.Join(err, scope.close(ctx))
				})
				result.totalDur = time.Since(start)
				result.released = time.Now()
				holderDone <- result
			}()
			select {
			case <-holderReady:
			case result := <-holderDone:
				t.Fatalf("holder did not acquire its transaction: %v", result.err)
			}
			waiterStart := time.Now()
			waiterErr := tc.waiter()
			waiterDur := time.Since(waiterStart)
			holder := <-holderDone
			t.Logf("holder: validate=%s total=%s busy_timeout=%dms reads: queryContext=%d queryRow=%d; waiter: dur=%s err=%v",
				holder.validateDur, holder.totalDur, testContentionBusyTimeoutMs, holder.queries, holder.queryRows, waiterDur, waiterErr)
			if holder.err != nil {
				t.Fatalf("holder: %v", holder.err)
			}
			if !waiterStart.Before(holder.released) {
				t.Fatal("writer request must start before its holder releases the write lock")
			}
			if waiterErr != nil {
				t.Fatalf("waiter failed while holder validator held the write lock for %s: %v", holder.validateDur, waiterErr)
			}
			// The validator must stay set-based over the whole population: query
			// count is the deterministic regression guard for a per-entry loop,
			// independent of the busy_timeout budget (CD-0045 D1 bounded hold).
			if holder.queries != 0 || holder.queryRows != 2 {
				t.Errorf("holder validator read counts=(%d queryContext, %d queryRow), want (0, 2) for the 150x100 entry population",
					holder.queries, holder.queryRows)
			}
			tc.assert(t)
		})
	}
}

// BenchmarkInitiativeValidator measures validateInitiativeInvariantsTx over
// finite projection fixtures at 150x100 (15000 entries), 300x100 (30000), and
// 300x300 (90000). Each subbenchmark names its dimensions and reports its entry
// population; the three points bound the measured range only and claim no
// scaling law beyond them. Run with `-bench=BenchmarkInitiativeValidator -benchtime=5x`.
func BenchmarkInitiativeValidator(b *testing.B) {
	for _, tc := range []struct {
		name string
		n, m int
	}{
		{"150x100_15000entries", 150, 100},
		{"300x100_30000entries", 300, 100},
		{"300x300_90000entries", 300, 300},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(b.TempDir(), "concord.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			if err := withFold(ctx, s, func(tx *sql.Tx, _ *foldScope) error {
				for _, stmt := range []string{
					`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p-bench','C','prototype','operator_only',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
					`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('pr-bench','P',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
				} {
					if _, err := tx.ExecContext(ctx, stmt); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			seedInitiativeProjection(b, s, "p-bench", "pr-bench", tc.n, tc.m)
			b.ResetTimer()
			b.ReportMetric(float64(tc.n*tc.m), "entries/op")
			for i := 0; i < b.N; i++ {
				if err := s.Transact(ctx, func(tx *Transaction) error {
					scope, err := beginFold(ctx, tx.tx)
					if err != nil {
						return err
					}
					defer func() { _ = scope.close(ctx) }()
					return validateInitiativeInvariantsTx(ctx, tx.tx)
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
