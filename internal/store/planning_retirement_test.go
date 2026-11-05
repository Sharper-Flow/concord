package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func insertHistoricalEvents(t *testing.T, db *sql.DB, events []Event) {
	t.Helper()
	for _, event := range events {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatalf("seed historical %s: %v", event.Kind, err)
		}
	}
}

func assertRetiredPlanningRefusal(t *testing.T, err error) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "CD-0213") {
		t.Fatalf("error = %v, want an invalid_operation refusal naming CD-0213", err)
	}
}

func TestRetiredInitiativeWorkCreatedRefusedBeforeReceipt(t *testing.T) {
	s := openTemp(t)
	event := operationEvent("retired-create", "work.created", SubjectWorkItem, "retired-work", map[string]any{
		"work_kind": "initiative", "title": "Retired", 
	})
	event.PayloadVersion = CurrentEventPayloadVersion("work.created")
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	assertRetiredPlanningRefusal(t, err)
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired create receipts=%d err=%v", count, err)
	}
}
