package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
)

type livenessCountingDriver struct {
	base    driver.Driver
	queries atomic.Int64
}

var livenessDriverSequence atomic.Int64

func (d *livenessCountingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &livenessCountingConn{Conn: c, counter: &d.queries}, nil
}

type livenessCountingConn struct {
	driver.Conn
	counter *atomic.Int64
}

func (c *livenessCountingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *livenessCountingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *livenessCountingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func TestBrowseLivenessStatementCount(t *testing.T) {
	path := copyTestDatabase(t)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 12; i++ {
		seedWork(t, s, fmt.Sprintf("count-work-%02d", i))
	}
	d := &livenessCountingDriver{base: s.db.Driver()}
	driverName := fmt.Sprintf("liveness-counting-%d", livenessDriverSequence.Add(1))
	sql.Register(driverName, d)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db, err = sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	s.db.SetMaxOpenConns(1)
	measure := func(limit int) int64 {
		t.Helper()
		d.queries.Store(0)
		result, err := s.QueryQ3(t.Context(), Q3Request{Product: "product", Limit: limit, Detail: "summary"})
		if err != nil || len(result.Items) != limit {
			t.Fatalf("limit %d: items=%d err=%v", limit, len(result.Items), err)
		}
		return d.queries.Load()
	}
	one, many := measure(1), measure(10)
	t.Logf("Q3 statements: one=%d ten=%d", one, many)
	if one != many {
		t.Errorf("statement count grows with page size: one=%d ten=%d", one, many)
	}
	measureQ2 := func(limit int) int64 {
		t.Helper()
		d.queries.Store(0)
		result, err := s.QueryQ2(t.Context(), Q2Request{Product: "product", PreviewLimit: limit})
		if err != nil || len(result.Items) != limit {
			t.Fatalf("Q2 limit %d: items=%d err=%v", limit, len(result.Items), err)
		}
		return d.queries.Load()
	}
	one, many = measureQ2(1), measureQ2(10)
	t.Logf("Q2 statements: one=%d ten=%d", one, many)
	if one != many {
		t.Errorf("Q2 statement count grows with page size: one=%d ten=%d", one, many)
	}
}

func TestBrowseLivenessParity(t *testing.T) {
	s := openTemp(t)
	for _, state := range []string{"needed", "in_progress", "completed", "cancelled"} {
		seedState(t, s, "matrix-"+state, state)
	}
	if err := applyWorkEvent(t, s, relationAddedEvent("matrix-blocks", "blocks", "matrix-in_progress", "matrix-needed", 3, 4), workVersion("matrix-in_progress", 3)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
		productCreatedEvent("other-product", "other-product-create"),
		projectCreatedEvent("other-project", "other-project-create"),
		operationEvent("other-membership", "product_project.added", SubjectProduct, "other-product", map[string]any{"product_id": "other-product", "project_id": "other-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "other-product"): 0, VersionRef(SubjectProject, "other-project"): 0}}); err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET last_progress_at=CASE WHEN id='matrix-needed' THEN '' ELSE '2026-01-02T03:04:05Z' END`); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"first", "second"} {
		if _, err := db.Exec(`INSERT INTO workflow_decision_records(work_id,question,options_considered,decision,rationale,consequences,inputs,poc_findings,recorded_at) VALUES('matrix-in_progress',?,'["option"]','accepted_decision','reason','["effect"]','["input"]','finding','2026-01-01T00:00:00Z')`, question); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workflow_decision_records(work_id,question,options_considered,decision,rationale,consequences,inputs,poc_findings,recorded_at) VALUES('matrix-completed','single','["option"]','accepted_decision','reason','["effect"]','["input"]','finding','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	assertItem := func(item WorkItem) {
		t.Helper()
		want := WorkLiveness{WorkID: item.ID, State: "unknown", Evidence: []string{"host liveness has no verified active or waiting evidence"}}
		if item.ID != "matrix-needed" {
			want.LastProgress = "2026-01-02T03:04:05Z"
		}
		if item.ID == "matrix-in_progress" {
			want.Evidence = append(want.Evidence, "2 operator decision(s)")
		}
		if item.ID == "matrix-completed" {
			want.Evidence = append(want.Evidence, "1 operator decision(s)")
		}
		if item.Liveness == nil || !reflect.DeepEqual(*item.Liveness, want) {
			t.Errorf("%s liveness=%+v want=%+v", item.ID, item.Liveness, want)
		}
		if item.Blocked != (item.ID == "matrix-needed") || item.Ready || item.Active != (item.Lifecycle == "in_progress") || item.Terminal != (item.Lifecycle == "completed" || item.Lifecycle == "cancelled") {
			t.Errorf("unexpected flags: %+v", item)
		}
	}
	for _, detail := range []string{"summary", "full"} {
		for _, state := range []string{"needed", "in_progress", "completed", "cancelled"} {
			r, err := s.QueryQ3(t.Context(), Q3Request{Product: "product", Detail: detail, LifecycleStates: []string{state}, ProjectIDs: []string{"project"}})
			if err != nil || len(r.Items) != 1 || r.Items[0].ID != "matrix-"+state {
				t.Fatalf("%s %s: %+v %v", detail, state, r.Items, err)
			}
			assertItem(r.Items[0])
		}
		req := Q3Request{Product: "product", Detail: detail, Limit: 1}
		seen := map[string]bool{}
		for {
			r, err := s.QueryQ3(t.Context(), req)
			if err != nil || len(r.Items) != 1 {
				t.Fatalf("page: %+v %v", r.Items, err)
			}
			item := r.Items[0]
			if seen[item.ID] {
				t.Fatalf("duplicate page item %s", item.ID)
			}
			seen[item.ID] = true
			assertItem(item)
			if r.NextCursor == nil {
				break
			}
			req.Cursor = *r.NextCursor
		}
		if len(seen) != 4 {
			t.Fatalf("page items: %v", seen)
		}
		empty, err := s.QueryQ3(t.Context(), Q3Request{Product: "product", ProjectIDs: []string{"not-owned"}, Detail: detail})
		if err != nil || len(empty.Items) != 0 {
			t.Fatalf("outside scope: %+v %v", empty.Items, err)
		}
		filtered, err := s.QueryQ3(t.Context(), Q3Request{Product: "product", Detail: detail, Kind: "decision"})
		if err != nil || len(filtered.Items) != 0 {
			t.Fatalf("kind filter: %+v %v", filtered.Items, err)
		}
		outside, err := s.QueryQ3(t.Context(), Q3Request{Product: "other-product", Detail: detail, WorkIDs: []string{"matrix-needed"}})
		if err != nil || len(outside.Items) != 0 {
			t.Fatalf("other Product: %+v %v", outside.Items, err)
		}
		all, err := s.QueryQ3(t.Context(), Q3Request{Product: "product", Detail: detail})
		if err != nil || len(all.Items) != 4 {
			t.Fatalf("all: %+v %v", all.Items, err)
		}
		for _, item := range all.Items {
			assertItem(item)
		}
	}
	preview, err := s.QueryQ2(t.Context(), Q2Request{Product: "product", PreviewLimit: 10})
	if err != nil || len(preview.Items) != 4 {
		t.Fatalf("preview: %+v %v", preview.Items, err)
	}
	for _, item := range preview.Items {
		assertItem(item)
	}
	outside, err := s.QueryQ2(t.Context(), Q2Request{Product: "other-product", ProjectIDs: []string{"project"}})
	if err != nil || len(outside.Items) != 0 {
		t.Fatalf("other Product preview: %+v %v", outside.Items, err)
	}
}
