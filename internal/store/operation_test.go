package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func operationEvent(id, kind string, subjectType SubjectType, subjectID string, payload any) Event {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return Event{
		EventID:        id,
		Kind:           kind,
		SubjectType:    subjectType,
		SubjectID:      subjectID,
		Actor:          "operator",
		OccurredAt:     time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		PayloadVersion: 1,
		Payload:        encoded,
	}
}

func productCreatedEvent(id, eventID string) Event {
	return operationEvent(eventID, "product.created", SubjectProduct, id, map[string]string{
		"display_name":              "Concord",
		"stage_maturity":            "prototype",
		"stage_audience_commitment": "operator_only",
	})
}

func projectCreatedEvent(id, eventID string) Event {
	return operationEvent(eventID, "project.created", SubjectProject, id, map[string]string{
		"display_name": "Core",
	})
}

func projectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	ctx := context.Background()
	var snapshot string
	rows, err := s.DB().QueryContext(ctx, `
		SELECT 'product', id, display_name, stage_maturity, stage_audience_commitment,
		       version, created_at, updated_at
		FROM products
		UNION ALL
		SELECT 'project', id, display_name, '', '', version, created_at, updated_at
		FROM projects
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("snapshot projections: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, id, displayName, maturity, audience, createdAt, updatedAt string
		var version int64
		if err := rows.Scan(&kind, &id, &displayName, &maturity, &audience, &version, &createdAt, &updatedAt); err != nil {
			t.Fatalf("scan projection snapshot: %v", err)
		}
		snapshot += fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s|%s\n", kind, id, displayName, maturity, audience, version, createdAt, updatedAt)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate projection snapshot: %v", err)
	}
	return snapshot
}

func TestRebuildFromLogPreservesCompleteProjectionContent(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	operations := []Operation{
		{Events: []Event{productCreatedEvent("product-1", "event-1")}, ExpectedVersions: map[string]int64{"product-1": 0}},
		{Events: []Event{projectCreatedEvent("project-1", "event-2")}, ExpectedVersions: map[string]int64{"project-1": 0}},
		{Events: []Event{operationEvent("event-3", "product.renamed", SubjectProduct, "product-1", map[string]string{"display_name": "Concord Core"})}, ExpectedVersions: map[string]int64{"product-1": 1}},
		{Events: []Event{operationEvent("event-4", "product.stage_changed", SubjectProduct, "product-1", map[string]string{"stage_maturity": "alpha", "stage_audience_commitment": "limited"})}, ExpectedVersions: map[string]int64{"product-1": 2}},
		{Events: []Event{operationEvent("event-5", "project.renamed", SubjectProject, "project-1", map[string]string{"display_name": "Core Runtime"})}, ExpectedVersions: map[string]int64{"project-1": 1}},
	}
	for _, operation := range operations {
		if err := ApplyOperation(ctx, s, operation); err != nil {
			t.Fatalf("ApplyOperation() error = %v", err)
		}
	}

	want := projectionSnapshot(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	if got := projectionSnapshot(t, s); got != want {
		t.Errorf("projection snapshot after rebuild =\n%s\nwant\n%s", got, want)
	}
	assertFoldGuardEmpty(t, s)
}

func TestProjectionTablesRejectIndependentWrites(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{productCreatedEvent("product-1", "event-1")},
		ExpectedVersions: map[string]int64{"product-1": 0},
	}); err != nil {
		t.Fatalf("seed Product: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{projectCreatedEvent("project-1", "event-2")},
		ExpectedVersions: map[string]int64{"project-1": 0},
	}); err != nil {
		t.Fatalf("seed Project: %v", err)
	}

	for _, tc := range []struct {
		name  string
		table string
		stmt  string
	}{
		{"product insert", "products", `INSERT INTO products (id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at) VALUES ('p', 'P', 'prototype', 'operator_only', 1, 'now', 'now')`},
		{"product update", "products", `UPDATE products SET display_name = 'changed' WHERE id = 'product-1'`},
		{"product delete", "products", `DELETE FROM products`},
		{"project insert", "projects", `INSERT INTO projects (id, display_name, version, created_at, updated_at) VALUES ('p', 'P', 1, 'now', 'now')`},
		{"project update", "projects", `UPDATE projects SET display_name = 'changed' WHERE id = 'project-1'`},
		{"project delete", "projects", `DELETE FROM projects`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.DB().ExecContext(ctx, tc.stmt); err == nil {
				t.Fatalf("direct write to %s succeeded", tc.table)
			}
		})
	}
}

func TestFoldGuardIsEmptyAfterSuccessfulFolds(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent("product-1", "event-1")}, ExpectedVersions: map[string]int64{"product-1": 0}}); err != nil {
		t.Fatalf("ApplyOperation() error = %v", err)
	}
	assertFoldGuardEmpty(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	assertFoldGuardEmpty(t, s)
}

func TestFailedOperationRollsBackLogProjectionAndGuard(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	err := ApplyOperation(ctx, s, Operation{Events: []Event{
		productCreatedEvent("product-1", "event-1"),
		operationEvent("event-2", "product.renamed", SubjectProduct, "product-1", map[string]int{"display_name": 7}),
	}, ExpectedVersions: map[string]int64{"product-1": 0}})
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
	assertTableCount(t, s, "domain_events", 0)
	assertTableCount(t, s, "products", 0)
}

func TestStaleExpectedVersionRollsBackWithoutMutation(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{productCreatedEvent("product-1", "event-1")}, ExpectedVersions: map[string]int64{"product-1": 0}}); err != nil {
		t.Fatalf("seed ApplyOperation() error = %v", err)
	}

	err := ApplyOperation(ctx, s, Operation{Events: []Event{
		operationEvent("event-2", "product.renamed", SubjectProduct, "product-1", map[string]string{"display_name": "stale"}),
	}, ExpectedVersions: map[string]int64{"product-1": 0}})
	assertFailureKind(t, err, KindVersionConflict)
	assertFoldGuardEmpty(t, s)
	assertTableCount(t, s, "domain_events", 1)
	assertTableCount(t, s, "products", 1)
	var name string
	if err := s.DB().QueryRowContext(ctx, `SELECT display_name FROM products WHERE id = 'product-1'`).Scan(&name); err != nil {
		t.Fatalf("read product: %v", err)
	}
	if name != "Concord" {
		t.Errorf("display_name = %q, want Concord", name)
	}
}

func TestRebuildRejectsUnknownEventKind(t *testing.T) {
	s := openTemp(t)
	e := operationEvent("event-1", "future.created", SubjectProduct, "product-1", map[string]string{"display_name": "future"})
	if _, err := appendInTx(t, s, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	err := RebuildFromLog(context.Background(), s)
	assertFailureKind(t, err, KindUnknownEventKind)
	assertFoldGuardEmpty(t, s)
	assertTableCount(t, s, "products", 0)
}

func TestRebuildRejectsMalformedEventPayload(t *testing.T) {
	s := openTemp(t)
	e := operationEvent("event-1", "product.created", SubjectProduct, "product-1", map[string]int{"display_name": 7})
	if _, err := appendInTx(t, s, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	err := RebuildFromLog(context.Background(), s)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
	assertTableCount(t, s, "products", 0)
}

func TestMultiEventOperationRollsBackWhenSecondEventFails(t *testing.T) {
	s := openTemp(t)
	operation := Operation{Events: []Event{
		productCreatedEvent("product-1", "event-1"),
		operationEvent("event-2", "product.stage_changed", SubjectProduct, "product-1", map[string]string{"stage_maturity": "invalid", "stage_audience_commitment": "limited"}),
	}, ExpectedVersions: map[string]int64{"product-1": 0}}

	err := ApplyOperation(context.Background(), s, operation)
	assertFailureKind(t, err, KindInvalidPayload)
	assertFoldGuardEmpty(t, s)
	assertTableCount(t, s, "domain_events", 0)
	assertTableCount(t, s, "products", 0)
}

func assertFailureKind(t *testing.T, err error, want FailureKind) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want *Failure", err)
	}
	if failure.Kind != want {
		t.Fatalf("Failure.Kind = %q, want %q", failure.Kind, want)
	}
	if failure.RecoveryAction == "" {
		t.Error("Failure.RecoveryAction is empty")
	}
}

func assertFoldGuardEmpty(t *testing.T, s *Store) {
	t.Helper()
	assertTableCount(t, s, "fold_guard", 0)
}

func assertTableCount(t *testing.T, s *Store, table string, want int) {
	t.Helper()
	var got int
	if err := s.DB().QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("%s count = %d, want %d", table, got, want)
	}
}
