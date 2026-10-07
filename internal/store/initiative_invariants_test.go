package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countingQueryer struct {
	*sql.Tx
	queries   atomic.Int64
	queryRows atomic.Int64
}

func (q *countingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries.Add(1)
	return q.Tx.QueryContext(ctx, query, args...)
}

func (q *countingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.queryRows.Add(1)
	return q.Tx.QueryRowContext(ctx, query, args...)
}

// runValidator opens a write transaction, opens the fold scope, and runs
// validateInitiativeInvariantsTx under an optional counter wrapper.
func runValidator(ctx context.Context, t testing.TB, s *Store, wrap func(*sql.Tx) queryer) (queries, queryRows int64, err error) {
	t.Helper()
	terr := s.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginFold(ctx, tx.tx)
		if err != nil {
			return err
		}
		var q queryer = tx.tx
		if wrap != nil {
			q = wrap(tx.tx)
		}
		err = validateInitiativeInvariantsTx(ctx, q)
		if c, ok := q.(*countingQueryer); ok {
			queries = c.queries.Load()
			queryRows = c.queryRows.Load()
		}
		return errors.Join(err, scope.close(ctx))
	})
	return queries, queryRows, terr
}

// newBootstrapStore opens a fresh store with the canonical bootstrap
// authority. Every fixture in this file starts from here.
func newBootstrapStore(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, initBootstrapStoreRepo(t))
	return s
}

// seedForeignScope creates one extra product with its own primary
// project. Foreign-scope cases derive a work in the foreign product.
func seedForeignScope(t *testing.T, s *Store, projectID, productID string) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "far-" + productID, Kind: "product.created", SubjectType: SubjectProduct, SubjectID: productID, Actor: "test", OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Foreign","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "far-" + projectID, Kind: "project.created", SubjectType: SubjectProject, SubjectID: projectID, Actor: "test", OccurredAt: time.Unix(11, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Foreign"}`)},
		{EventID: "far-mem-" + projectID, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: productID, Actor: "test", OccurredAt: time.Unix(12, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"` + productID + `","project_id":"` + projectID + `","role":"primary","reason":"foreign","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, productID): 0, VersionRef(SubjectProject, projectID): 0}}); err != nil {
		t.Fatalf("seed foreign scope: %v", err)
	}
}

// seedSiblingProjectInProduct adds one extra Project that joins
// product-bootstrap in the secondary role. A work can then derive
// product-bootstrap through either project.
func seedSiblingProjectInProduct(t *testing.T, s *Store, projectID string) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "dup-" + projectID, Kind: "project.created", SubjectType: SubjectProject, SubjectID: projectID, Actor: "test", OccurredAt: time.Unix(13, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Sibling"}`)},
		{EventID: "dup-link-" + projectID, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product-bootstrap", Actor: "test", OccurredAt: time.Unix(14, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product-bootstrap","project_id":"` + projectID + `","role":"secondary","reason":"sibling","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, projectID): 0, VersionRef(SubjectProduct, "product-bootstrap"): 2}}); err != nil {
		t.Fatalf("seed sibling project: %v", err)
	}
}

// addWork creates one work item and one project membership. role is
// "primary" or "secondary" (the latter leaves the work's primary
// scope empty for the "child no product" case).
func addWork(t *testing.T, s *Store, workID, kind, projectID, role string) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "create-" + workID, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"` + kind + `","title":"` + workID + `","priority":1}`)},
		{EventID: "add-" + workID, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "test", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"` + workID + `","project_id":"` + projectID + `","role":"` + role + `","reason":"test","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatalf("add work %q in %q: %v", workID, projectID, err)
	}
}

// addSecondaryProject adds one secondary project membership on an
// existing work.
func addSecondaryProject(t *testing.T, s *Store, workID, projectID string, expected int64) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "secondary-" + workID + "-" + projectID, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "test", OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"` + workID + `","project_id":"` + projectID + `","role":"secondary","reason":"test","expected_version":` + strconv.FormatInt(expected, 10) + `,"resulting_version":` + strconv.FormatInt(expected+1, 10) + `}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}); err != nil {
		t.Fatalf("add secondary: %v", err)
	}
}

// readInt reads a scalar from the committed projection.
func readInt[T any](t *testing.T, s *Store, query string) T {
	t.Helper()
	var v T
	if err := s.db.QueryRowContext(context.Background(), query).Scan(&v); err != nil {
		t.Fatalf("read int: %v\n%s", err, query)
	}
	return v
}

// addEntry folds an initiative_entry.added event.
func addEntry(t *testing.T, ctx context.Context, s *Store, initiativeID, childID string, expected int64) {
	t.Helper()
	event, err := InitiativeEntryEvent("entry-"+initiativeID+"-"+childID, "initiative_entry.added", initiativeID, InitiativeEntry{ChildWorkID: childID, Position: 0, Required: true}, "test", time.Unix(30, 0).UTC(), expected)
	if err != nil {
		t.Fatalf("build entry event: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, initiativeID): expected}}); err != nil {
		t.Fatalf("apply entry: %v", err)
	}
}

// synthEntryRelation returns the SQL pair that inserts one initiative
// entry and one includes relation. The negative relation id reserves
// the positive autoincrement space.
func synthEntryRelation(initID, childID string, relID int64) (string, string) {
	return `INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('` + initID + `','` + childID + `',0,1)`,
		`INSERT INTO relations(id, work_id_from, work_id_to, kind, created_at) VALUES(` + strconv.FormatInt(relID, 10) + `, '` + initID + `', '` + childID + `', 'includes', '2026-01-01T00:00:00Z')`
}

// synthEntryOnly returns the entry insert alone, used by the missing-
// relation and absent-child cases.
func synthEntryOnly(initID, childID string) string {
	return `INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('` + initID + `','` + childID + `',0,1)`
}

// synthSecondPrimary returns the SQL that inserts one more primary
// project membership for a work, bypassing the partial unique index.
func synthSecondPrimary(workID, projectID string) string {
	return `INSERT INTO work_projects(work_id, project_id, role) VALUES('` + workID + `', '` + projectID + `', 'primary')`
}

// synthExec runs the given statements on a separate connection that
// drops the work_projects partial unique index, disables foreign keys,
// and runs under a fold guard. The dropped index and synthetic
// relation IDs stay confined to the throwaway test store.
func synthExec(t *testing.T, s *Store, queries ...string) {
	t.Helper()
	db, err := sql.Open(driverName, "file:"+s.Path()+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatalf("open synthetic connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	for _, pre := range []string{
		`DROP INDEX work_projects_one_primary`,
		`INSERT INTO fold_guard(active) VALUES(1)`,
	} {
		if _, err := db.ExecContext(ctx, pre); err != nil {
			t.Fatalf("synthetic setup: %v\n%s", err, pre)
		}
	}
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM fold_guard`) }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin synthetic tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			t.Fatalf("synthetic query: %v\n%s", err, q)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit synthetic tx: %v", err)
	}
}

// assertValidatorOK runs the validator and fails the test if the result
// is anything other than nil. Used for the accepts table.
func assertValidatorOK(t *testing.T, s *Store) {
	t.Helper()
	if _, _, err := runValidator(context.Background(), t, s, nil); err != nil {
		t.Fatalf("validator rejected the projection: %v", err)
	}
}

// assertValidatorKind checks the refusal's kind and detail.
func assertValidatorKind(t *testing.T, s *Store, wantKind FailureKind, wantDetail string) {
	t.Helper()
	_, _, err := runValidator(context.Background(), t, s, nil)
	if wantKind == "" {
		if err != nil {
			t.Fatalf("validator returned %v, want nil", err)
		}
		return
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("validator returned %v, want typed *Failure with kind %q", err, wantKind)
	}
	if failure.Kind != wantKind {
		t.Errorf("Kind = %q, want %q", failure.Kind, wantKind)
	}
	if wantDetail != "" && !strings.Contains(failure.Detail, wantDetail) {
		t.Errorf("Detail = %q, want substring %q", failure.Detail, wantDetail)
	}
}

func TestInitiativeInvariants_Accepts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(t *testing.T) *Store
	}{
		{"small 2x2 projection", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 2, 2)
			return s
		}},
		{"duplicate memberships same product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedSiblingProjectInProduct(t, s, "project-sibling")
			addWork(t, s, "init-dup", "initiative", "project-bootstrap", "primary")
			synthExec(t, s, synthSecondPrimary("init-dup", "project-sibling"))
			return s
		}},
		{"child duplicate primary projects same product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedSiblingProjectInProduct(t, s, "project-sibling")
			addWork(t, s, "init-child-dup", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-dup", "task", "project-bootstrap", "primary")
			addEntry(t, context.Background(), s, "init-child-dup", "child-dup", 2)
			synthExec(t, s, synthSecondPrimary("child-dup", "project-sibling"))
			return s
		}},
		{"secondary foreign project does not widen scope", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedForeignScope(t, s, "project-far", "product-far")
			addWork(t, s, "init-scope", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-scope", "task", "project-bootstrap", "primary")
			addSecondaryProject(t, s, "init-scope", "project-far", 2)
			addSecondaryProject(t, s, "child-scope", "project-far", 2)
			addEntry(t, context.Background(), s, "init-scope", "child-scope", 3)
			return s
		}},
		{"noise work and cross-scope secondaries", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedForeignScope(t, s, "project-noise", "product-noise")
			addWork(t, s, "init-noise", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-noise", "task", "project-bootstrap", "primary")
			addWork(t, s, "lone-noise", "task", "project-bootstrap", "primary")
			addWork(t, s, "cross-noise", "task", "project-noise", "primary")
			addEntry(t, context.Background(), s, "init-noise", "child-noise", 2)
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidatorOK(t, tc.setup(t))
		})
	}
}

// TestInitiativeInvariants_Rejects groups every post-fold case the
// validator must refuse with a typed kind. Synthetic fixtures bypass
// the fold path through synthExec, which drops the partial unique
// index, disables foreign keys, and runs the inserts under a fold
// guard. Synthetic relation IDs are negative to reserve the positive
// autoincrement space.
func TestInitiativeInvariants_Rejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		setup      func(t *testing.T) *Store
		wantKind   FailureKind
		wantDetail string
	}{
		{"initiative zero primary product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			synthExec(t, s,
				`INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at) VALUES('init-zero','initiative','Zero','needed',0,'standard',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
				`INSERT INTO work_projects(work_id, project_id, role) VALUES('init-zero', 'project-bootstrap', 'secondary')`)
			return s
		}, KindInitiativeScopeViolation, "init-zero"},
		{"initiative ambiguous primary product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedForeignScope(t, s, "project-amb", "product-amb")
			addWork(t, s, "init-amb", "initiative", "project-bootstrap", "primary")
			synthExec(t, s, synthSecondPrimary("init-amb", "project-amb"))
			return s
		}, KindInitiativeScopeViolation, "init-amb"},
		{"child no product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			addWork(t, s, "init-nochild", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-no", "task", "project-bootstrap", "secondary")
			entry, rel := synthEntryRelation("init-nochild", "child-no", -1)
			synthExec(t, s, entry, rel)
			return s
		}, KindInitiativeScopeViolation, "exactly one Product"},
		{"child ambiguous product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedForeignScope(t, s, "project-amb", "product-amb")
			addWork(t, s, "init-ambc", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-amb", "task", "project-bootstrap", "primary")
			entry, rel := synthEntryRelation("init-ambc", "child-amb", -2)
			synthExec(t, s, synthSecondPrimary("child-amb", "project-amb"), entry, rel)
			return s
		}, KindInitiativeScopeViolation, "exactly one Product"},
		{"child foreign product", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			seedForeignScope(t, s, "project-foreign", "product-foreign")
			addWork(t, s, "init-foreign", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-foreign", "task", "project-bootstrap", "primary")
			entry, rel := synthEntryRelation("init-foreign", "child-foreign", -3)
			synthExec(t, s, `UPDATE work_projects SET project_id='project-foreign' WHERE work_id='child-foreign' AND role='primary'`, entry, rel)
			return s
		}, KindInitiativeScopeViolation, "different Product"},
		{"nested initiative child", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			addWork(t, s, "init-outer", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "init-inner", "initiative", "project-bootstrap", "primary")
			entry, rel := synthEntryRelation("init-outer", "init-inner", -4)
			synthExec(t, s, entry, rel)
			return s
		}, KindInitiativeScopeViolation, "nested Initiative entry"},
		{"missing includes relation", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			addWork(t, s, "init-norel", "initiative", "project-bootstrap", "primary")
			addWork(t, s, "child-norel", "task", "project-bootstrap", "primary")
			synthExec(t, s, synthEntryOnly("init-norel", "child-norel"))
			return s
		}, KindInitiativeScopeViolation, "diverged"},
		{"absent child rejected", func(t *testing.T) *Store {
			s := newBootstrapStore(t)
			addWork(t, s, "init-absent", "initiative", "project-bootstrap", "primary")
			synthExec(t, s, synthEntryOnly("init-absent", "child-vanished"))
			return s
		}, KindProjectionNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidatorKind(t, tc.setup(t), tc.wantKind, tc.wantDetail)
		})
	}
}

// TestInitiativeInvariants_NoInitiatives pins the read count for the
// empty projection. Both validator reads are QueryRowContext, so the
// counter records zero QueryContext and two QueryRowContext.
func TestInitiativeInvariants_NoInitiatives(t *testing.T) {
	t.Parallel()
	s := newBootstrapStore(t)
	queries, queryRows, err := runValidator(context.Background(), t, s, func(tx *sql.Tx) queryer {
		return &countingQueryer{Tx: tx}
	})
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	if queries != 0 || queryRows != 2 {
		t.Errorf("read counts = (%d, %d), want (0, 2)", queries, queryRows)
	}
}

func TestInitiativeInvariants_QueryerCounterIndependentOfPopulation(t *testing.T) {
	t.Parallel()
	s := newBootstrapStore(t)
	seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 100, 100)
	queries, queryRows, err := runValidator(context.Background(), t, s, func(tx *sql.Tx) queryer {
		return &countingQueryer{Tx: tx}
	})
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	if queries != 0 || queryRows != 2 {
		t.Errorf("read counts = (%d, %d), want (0, 2) for 100x100 population", queries, queryRows)
	}
	if stats := s.db.Stats(); stats.MaxOpenConnections != 1 {
		t.Errorf("pool MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestInitiativeInvariants_UnrelatedWorkDoesNotIncreaseReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newBootstrapStore(t)
	seedInitiativeProjection(t, s, "product-bootstrap", "project-bootstrap", 2, 2)
	if err := withFold(ctx, s, func(tx *sql.Tx, _ *foldScope) error {
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE noise(n) AS (
			SELECT 0 UNION ALL SELECT n+1 FROM noise WHERE n<99999
		) INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at)
		SELECT 'unrelated-'||printf('%06d',n),'task','Unrelated','needed',0,'standard',1,
			'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z' FROM noise`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id,project_id,role)
			SELECT id,'project-bootstrap','primary' FROM work_items WHERE id LIKE 'unrelated-%'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := readInt[int64](t, s, `SELECT count(*) FROM work_items WHERE id LIKE 'unrelated-%'`); got != 100000 {
		t.Fatalf("unrelated work rows=%d want 100000", got)
	}
	queries, queryRows, err := runValidator(ctx, t, s, func(tx *sql.Tx) queryer {
		return &countingQueryer{Tx: tx}
	})
	if err != nil {
		t.Fatalf("validator rejected projection with 100000 unrelated work items: %v", err)
	}
	if queries != 0 || queryRows != 2 {
		t.Errorf("read counts=(%d, %d) want (0, 2) with 100000 unrelated work items", queries, queryRows)
	}
}

func TestInitiativeInvariants_DurableTransactRollsBackInvalidScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newBootstrapStore(t)
	seedForeignScope(t, s, "project-far", "product-far")
	addWork(t, s, "init-keep", "initiative", "project-bootstrap", "primary")
	addWork(t, s, "child-keep", "task", "project-bootstrap", "primary")
	addEntry(t, ctx, s, "init-keep", "child-keep", 2)

	priorVersion := readInt[int64](t, s, `SELECT version FROM work_items WHERE id='init-keep'`)
	priorChildVersion := readInt[int64](t, s, `SELECT version FROM work_items WHERE id='child-keep'`)
	priorEntries, err := s.ReadInitiativeEntries(ctx, "init-keep")
	if err != nil {
		t.Fatal(err)
	}
	wantEntry := InitiativeEntry{InitiativeWorkID: "init-keep", ChildWorkID: "child-keep", Position: 0, Required: true}
	if len(priorEntries) != 1 || priorEntries[0] != wantEntry {
		t.Fatalf("prior entries=%+v want [%+v]", priorEntries, wantEntry)
	}
	assertValidatorOK(t, s)
	ordinary := Event{EventID: "dur-ordinary", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "dur-ordinary", Actor: "test", OccurredAt: time.Unix(81, 0).UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"Durable ordinary","priority":1}`)}
	membership := Event{EventID: "dur-child-memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "child-keep", Actor: "test", OccurredAt: time.Unix(82, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"memberships":[{"project_id":"project-far","role":"primary"}],"expected_version":` + strconv.FormatInt(priorChildVersion, 10) + `,"resulting_version":` + strconv.FormatInt(priorChildVersion+1, 10) + `}`)}
	ordinaryMembership := Event{EventID: "dur-ordinary-membership", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "dur-ordinary", Actor: "test", OccurredAt: time.Unix(83, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"dur-ordinary","project_id":"project-bootstrap","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)}
	postFoldValidationReached := false
	durableErr := s.TransactDurable(ctx, func(tx *Transaction) error {
		scope, err := beginFold(ctx, tx.tx)
		if err != nil {
			return err
		}
		for _, event := range []Event{ordinary, membership} {
			event.Seq, err = AppendEvent(ctx, tx.tx, event)
			if err != nil {
				return err
			}
			if err := foldRegisteredEvent(ctx, tx.tx, event); err != nil {
				return err
			}
		}
		postFoldValidationReached = true
		_, err = applyOperationTx(ctx, tx.tx, Operation{
			Events:           []Event{ordinaryMembership},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "dur-ordinary"): 1},
		}, scope, false)
		return errors.Join(err, scope.close(ctx))
	})
	if !postFoldValidationReached {
		t.Fatalf("event fold failed before global validation: %v", durableErr)
	}
	var failure *Failure
	if !errors.As(durableErr, &failure) || failure.Kind != KindInitiativeScopeViolation {
		t.Fatalf("durable err = %v, want KindInitiativeScopeViolation", durableErr)
	}
	if (failure.Op != "initiative_invariants" && failure.Op != "fold_event") || !strings.Contains(failure.Detail, "different Product") {
		t.Fatalf("durable failure=%+v want global initiative validation for different Product", failure)
	}
	checks := []struct {
		query string
		want  int64
		what  string
	}{
		{`SELECT count(*) FROM work_items WHERE id='dur-ordinary'`, 0, "ordinary work row committed"},
		{`SELECT count(*) FROM domain_events WHERE event_id='dur-ordinary'`, 0, "ordinary event committed"},
		{`SELECT count(*) FROM domain_events WHERE event_id IN ('dur-child-memberships','dur-ordinary-membership')`, 0, "membership events committed"},
		{`SELECT count(*) FROM work_projects WHERE work_id='dur-ordinary'`, 0, "ordinary membership committed"},
		{`SELECT version FROM work_items WHERE id='init-keep'`, priorVersion, "Initiative version changed"},
		{`SELECT version FROM work_items WHERE id='child-keep'`, priorChildVersion, "child version changed"},
		{`SELECT count(*) FROM work_projects WHERE work_id='init-keep' AND project_id='project-bootstrap' AND role='primary'`, 1, "Initiative primary membership"},
		{`SELECT count(*) FROM work_projects WHERE work_id='child-keep'`, 1, "child membership count"},
		{`SELECT count(*) FROM work_projects WHERE work_id='child-keep' AND project_id='project-bootstrap' AND role='primary'`, 1, "child primary membership"},
		{`SELECT count(*) FROM work_projects WHERE work_id='child-keep' AND project_id='project-far'`, 0, "foreign child membership committed"},
		{`SELECT count(*) FROM relations WHERE work_id_from='init-keep' AND work_id_to='child-keep' AND kind='includes'`, 1, "original includes relation"},
	}
	for _, c := range checks {
		got := readInt[int64](t, s, c.query)
		if got != c.want {
			t.Errorf("%s: got=%d want=%d", c.what, got, c.want)
		}
	}
	entries, err := s.ReadInitiativeEntries(ctx, "init-keep")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(priorEntries) || entries[0] != priorEntries[0] {
		t.Errorf("entries=%+v want unchanged %+v", entries, priorEntries)
	}
	assertValidatorOK(t, s)
}

// TestInitiativeInvariants_SchemaForeignKeyIsStructural documents that
// the absent-child path is normally unreachable: the schema's FK on
// initiative_entries.child_work_id refuses the insert before the
// validator ever sees the row. s.db has foreign_keys=ON by default,
// and the typed refusal proves the structural guard.
func TestInitiativeInvariants_SchemaForeignKeyIsStructural(t *testing.T) {
	t.Parallel()
	s := newBootstrapStore(t)
	addWork(t, s, "init-fk", "initiative", "project-bootstrap", "primary")
	ctx := context.Background()
	db := s.DatabaseForTesting()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("seed fold guard: %v", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM fold_guard`) }()
	_, err := db.ExecContext(ctx, `INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('init-fk','child-vanished',0,1)`)
	if err == nil {
		t.Fatal("foreign key did not refuse orphan insert")
	}
	if !isForeignKeyViolation(err) {
		t.Fatalf("refusal %v is not a typed foreign-key violation", err)
	}
}
