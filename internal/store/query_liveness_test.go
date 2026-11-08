package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"slices"
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
	if _, err := db.Exec(`INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES('matrix-in_progress','remote-matrix','SYN-7','https://example.invalid/issues/SYN-7','2026-01-01','2026-01-01')`); err != nil {
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
	lifecycles := []string{"needed", "in_progress", "completed", "cancelled"}
	since := "1970-01-01T00:00:00Z"
	filters := []struct {
		name  string
		req   Q3Request
		admit func(state string) bool
	}{
		{"unfiltered", Q3Request{Product: "product"}, func(string) bool { return true }},
		{"project", Q3Request{Product: "product", Project: "project"}, func(string) bool { return true }},
		{"project-ids", Q3Request{Product: "product", ProjectIDs: []string{"project"}}, func(string) bool { return true }},
		{"kind", Q3Request{Product: "product", Kind: "task"}, func(string) bool { return true }},
		{"other-kind", Q3Request{Product: "product", Kind: "decision"}, func(string) bool { return false }},
		{"work-ids", Q3Request{Product: "product", WorkIDs: []string{"matrix-needed", "matrix-completed"}}, func(state string) bool { return state == "needed" || state == "completed" }},
		{"linear-issue", Q3Request{Product: "product", LinearIssueKey: "SYN-7"}, func(state string) bool { return state == "in_progress" }},
		{"terminal-since", Q3Request{Product: "product", TerminalSince: &since}, func(state string) bool { return state == "completed" || state == "cancelled" }},
		{"not-owned-project", Q3Request{Product: "product", ProjectIDs: []string{"not-owned"}}, func(string) bool { return false }},
		{"other-product", Q3Request{Product: "other-product", WorkIDs: []string{"matrix-needed"}}, func(string) bool { return false }},
	}
	for _, detail := range []string{"summary", "full"} {
		for mask := 0; mask < 1<<len(lifecycles); mask++ {
			var states []string
			for i, state := range lifecycles {
				if mask&(1<<i) != 0 {
					states = append(states, state)
				}
			}
			for _, filter := range filters {
				want := map[string]bool{}
				for _, state := range lifecycles {
					if (len(states) == 0 || slices.Contains(states, state)) && filter.admit(state) {
						want["matrix-"+state] = true
					}
				}
				name := fmt.Sprintf("%s/%s/%v", detail, filter.name, states)
				req := filter.req
				req.Detail, req.LifecycleStates = detail, states
				whole, err := s.QueryQ3(t.Context(), req)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				got := map[string]bool{}
				for _, item := range whole.Items {
					got[item.ID] = true
					assertItem(item)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: items=%v want=%v", name, got, want)
				}
				req.Limit = 1
				paged := map[string]bool{}
				for {
					page, err := s.QueryQ3(t.Context(), req)
					if err != nil || len(page.Items) > 1 {
						t.Fatalf("%s page: %+v %v", name, page.Items, err)
					}
					for _, item := range page.Items {
						if paged[item.ID] {
							t.Fatalf("%s: duplicate page item %s", name, item.ID)
						}
						paged[item.ID] = true
						assertItem(item)
					}
					if page.NextCursor == nil {
						break
					}
					req.Cursor = *page.NextCursor
				}
				if !reflect.DeepEqual(paged, want) {
					t.Fatalf("%s: paged items=%v want=%v", name, paged, want)
				}
			}
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
