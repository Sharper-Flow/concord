package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func testEvent() Event {
	return Event{
		EventID:        "evt-0001",
		Kind:           "product.created",
		SubjectType:    SubjectProduct,
		SubjectID:      "product-0001",
		Actor:          "operator",
		OccurredAt:     time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		PayloadVersion: 1,
		Payload:        []byte(`{"display_name":"First Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`),
	}
}

// appendInTx runs one append inside its own transaction and commits.
func appendInTx(t *testing.T, s *Store, e Event) (int64, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	seq, err := AppendEvent(ctx, tx, e)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			t.Fatalf("Rollback() error = %v", rbErr)
		}
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return seq, nil
}

func TestAppendEventAssignsMonotonicSequence(t *testing.T) {
	s := openTemp(t)

	first := testEvent()
	firstSeq, err := appendInTx(t, s, first)
	if err != nil {
		t.Fatalf("first AppendEvent() error = %v", err)
	}

	second := testEvent()
	second.EventID = "evt-0002"
	secondSeq, err := appendInTx(t, s, second)
	if err != nil {
		t.Fatalf("second AppendEvent() error = %v", err)
	}

	if firstSeq <= 0 {
		t.Errorf("first sequence = %d, want positive", firstSeq)
	}
	if secondSeq <= firstSeq {
		t.Errorf("sequence did not advance: %d then %d", firstSeq, secondSeq)
	}
}

// The event log is the sole authority, so an append that is part of a rolled
// back transaction must leave nothing behind.
func TestAppendEventIsTransactional(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if _, err := AppendEvent(ctx, tx, testEvent()); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Errorf("events after rollback = %d, want 0", count)
	}
}

func TestAppendEventRejectsDuplicateEventID(t *testing.T) {
	s := openTemp(t)

	if _, err := appendInTx(t, s, testEvent()); err != nil {
		t.Fatalf("first AppendEvent() error = %v", err)
	}

	_, err := appendInTx(t, s, testEvent())
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("duplicate AppendEvent() error = %v, want *Failure", err)
	}
	if f.Kind != KindDuplicateEvent {
		t.Errorf("Kind = %q, want %q", f.Kind, KindDuplicateEvent)
	}
	if !f.RetrySafe {
		t.Error("RetrySafe = false; a duplicate append means the effect already exists")
	}
}

func TestAppendEventRejectsInvalidEvents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Event)
		want   FailureKind
	}{
		{"empty event id", func(e *Event) { e.EventID = "" }, KindInvalidEvent},
		{"empty kind", func(e *Event) { e.Kind = "" }, KindInvalidEvent},
		{"empty subject id", func(e *Event) { e.SubjectID = "" }, KindInvalidEvent},
		{"empty actor", func(e *Event) { e.Actor = "" }, KindInvalidEvent},
		{"zero time", func(e *Event) { e.OccurredAt = time.Time{} }, KindInvalidEvent},
		{"unknown subject type", func(e *Event) { e.SubjectType = "galaxy" }, KindInvalidSubject},
		{"zero payload version", func(e *Event) { e.PayloadVersion = 0 }, KindUnsupportedPayloadVersion},
		{"negative payload version", func(e *Event) { e.PayloadVersion = -1 }, KindUnsupportedPayloadVersion},
		{"payload not json", func(e *Event) { e.Payload = []byte("not json") }, KindInvalidPayload},
		{"payload is array", func(e *Event) { e.Payload = []byte(`["a"]`) }, KindInvalidPayload},
		{"payload is null", func(e *Event) { e.Payload = nil }, KindInvalidPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			e := testEvent()
			tc.mutate(&e)

			_, err := appendInTx(t, s, e)
			var f *Failure
			if !errors.As(err, &f) {
				t.Fatalf("AppendEvent() error = %v, want *Failure", err)
			}
			if f.Kind != tc.want {
				t.Errorf("Kind = %q, want %q", f.Kind, tc.want)
			}
		})
	}
}

func TestAppendEventRejectsUnregisteredEventsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Event)
		want   FailureKind
	}{
		{"unknown event kind", func(e *Event) { e.Kind = "event.not_registered" }, KindUnknownEventKind},
		{"unsupported payload version", func(e *Event) {
			e.PayloadVersion = eventKindRegistry[e.Kind].CurrentVersion + 1
		}, KindUnsupportedPayloadVersion},
		{"invalid registered payload", func(e *Event) {
			e.Payload = []byte(`{"display_name":7,"stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)
		}, KindInvalidPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			ctx := context.Background()
			tx, err := s.DB().BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx() error = %v", err)
			}
			defer tx.Rollback()

			e := testEvent()
			tc.mutate(&e)
			_, appendErr := AppendEvent(ctx, tx, e)

			var f *Failure
			if !errors.As(appendErr, &f) {
				t.Fatalf("AppendEvent() error = %v, want *Failure", appendErr)
			}
			if f.Kind != tc.want {
				t.Errorf("Kind = %q, want %q", f.Kind, tc.want)
			}

			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
				t.Fatalf("count events in rejected transaction: %v", err)
			}
			if count != 0 {
				t.Errorf("events after rejected append = %d, want 0", count)
			}
		})
	}
}

// PM3 requires an append-only log. Convention is not enough: the database must
// refuse rewrites even when a caller reaches past the append boundary.
func TestDomainEventsRejectUpdateAndDelete(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if _, err := appendInTx(t, s, testEvent()); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	for _, tc := range []struct {
		name string
		stmt string
	}{
		{"update", `UPDATE domain_events SET actor = 'someone-else'`},
		{"delete", `DELETE FROM domain_events`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.DB().ExecContext(ctx, tc.stmt); err == nil {
				t.Fatalf("%s on domain_events succeeded; the log is not append-only", tc.name)
			}
		})
	}

	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("events = %d, want 1", count)
	}
}

func TestAppendEventRoundTripsEveryField(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	want := testEvent()
	seq, err := appendInTx(t, s, want)
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	var got Event
	var occurredAt string
	err = s.DB().QueryRowContext(ctx, `
		SELECT event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload
		FROM domain_events WHERE seq = ?`, seq).
		Scan(&got.EventID, &got.Kind, &got.SubjectType, &got.SubjectID, &got.Actor,
			&occurredAt, &got.PayloadVersion, &got.Payload)
	if err != nil {
		t.Fatalf("read back event: %v", err)
	}

	if got.EventID != want.EventID || got.Kind != want.Kind ||
		got.SubjectType != want.SubjectType || got.SubjectID != want.SubjectID ||
		got.Actor != want.Actor || got.PayloadVersion != want.PayloadVersion {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Errorf("payload = %s, want %s", got.Payload, want.Payload)
	}
	if occurredAt != want.OccurredAt.UTC().Format(time.RFC3339Nano) {
		t.Errorf("occurred_at = %q, want %q", occurredAt, want.OccurredAt.UTC().Format(time.RFC3339Nano))
	}
}

// Timestamps are stored in a single normalized form so ordering and comparison
// never depend on the caller's location.
func TestAppendEventNormalizesTimeZone(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	zone := time.FixedZone("UTC+7", 7*60*60)
	e := testEvent()
	e.OccurredAt = time.Date(2026, 8, 7, 19, 0, 0, 0, zone)

	seq, err := appendInTx(t, s, e)
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	var occurredAt string
	if err := s.DB().QueryRowContext(ctx, `SELECT occurred_at FROM domain_events WHERE seq = ?`, seq).Scan(&occurredAt); err != nil {
		t.Fatalf("read back time: %v", err)
	}
	if want := "2026-08-07T12:00:00Z"; occurredAt != want {
		t.Errorf("occurred_at = %q, want %q", occurredAt, want)
	}
}

func TestSubjectTypeValidation(t *testing.T) {
	for _, st := range knownSubjectTypes() {
		if !st.valid() {
			t.Errorf("registered subject type %q reports invalid", st)
		}
	}
	if SubjectType("nope").valid() {
		t.Error("unregistered subject type reports valid")
	}
}
