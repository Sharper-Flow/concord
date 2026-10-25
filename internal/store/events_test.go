package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

func assertEventLogAbsent(t *testing.T, s *Store) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("event-log schema objects = %d, err = %v, want none", count, err)
	}
}

// appendInTx runs one append inside its own transaction and commits.
func appendInTx(t *testing.T, s *Store, e Event) (int64, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
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
	t.Parallel()
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

// A rolled-back append must leave neither a receipt nor an allocated sequence.
func TestAppendEventIsTransactional(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
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
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Errorf("events after rollback = %d, want 0", count)
	}
	var sequence int64
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&sequence); err != nil || sequence != 0 {
		t.Fatalf("effect sequence after rollback = %d, err = %v", sequence, err)
	}
}

func TestAppendEventRejectsDuplicateEventID(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
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
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil {
				t.Fatalf("count events in rejected transaction: %v", err)
			}
			if count != 0 {
				t.Errorf("events after rejected append = %d, want 0", count)
			}
		})
	}
}

func TestAppendEventRoundTripsEveryField(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()

	want := testEvent()
	seq, err := appendInTx(t, s, want)
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	var got Event
	var digest string
	err = s.DatabaseForTesting().QueryRowContext(ctx, `
		SELECT effect_seq, event_id, kind, subject_type, subject_id, actor, payload_version, payload_digest
		FROM effect_receipts WHERE effect_seq = ?`, seq).
		Scan(&got.Seq, &got.EventID, &got.Kind, &got.SubjectType, &got.SubjectID, &got.Actor,
			&got.PayloadVersion, &digest)
	if err != nil {
		t.Fatalf("read back event: %v", err)
	}

	if got.Seq != seq || got.EventID != want.EventID || got.Kind != want.Kind ||
		got.SubjectType != want.SubjectType || got.SubjectID != want.SubjectID ||
		got.Actor != want.Actor || got.PayloadVersion != want.PayloadVersion {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	canonical := `{"display_name":"First Product","stage_audience_commitment":"operator_only","stage_maturity":"prototype"}`
	sum := sha256.Sum256([]byte(canonical))
	if wantDigest := "sha256:" + hex.EncodeToString(sum[:]); digest != wantDigest {
		t.Errorf("payload digest = %q, want %q", digest, wantDigest)
	}
}

// The Product writer owns UTC storage, not the receipt append seam.
func TestAppendEventNormalizesTimeZone(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()

	zone := time.FixedZone("UTC+7", 7*60*60)
	e := testEvent()
	e.OccurredAt = time.Date(2026, 8, 7, 19, 0, 0, 0, zone)

	project := projectCreatedEvent("time-project", "time-project-created")
	project.OccurredAt = e.OccurredAt
	membership := operationEvent("time-membership", "product_project.added", SubjectProduct, e.SubjectID, map[string]any{
		"product_id": e.SubjectID, "project_id": project.SubjectID, "role": "primary", "reason": "test",
		"expected_version": 1, "resulting_version": 2,
	})
	membership.OccurredAt = e.OccurredAt
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{e, project, membership}}); err != nil {
		t.Fatalf("ApplyOperation() error = %v", err)
	}

	var createdAt, updatedAt string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT created_at, updated_at FROM products WHERE id = ?`, e.SubjectID).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read back time: %v", err)
	}
	if want := "2026-08-07T12:00:00Z"; createdAt != want || updatedAt != want {
		t.Errorf("Product times = (%q, %q), want (%q, %q)", createdAt, updatedAt, want, want)
	}
}

func TestSubjectTypeValidation(t *testing.T) {
	t.Parallel()
	for st := range subjectTypes {
		if !st.valid() {
			t.Errorf("registered subject type %q reports invalid", st)
		}
	}
	if SubjectType("nope").valid() {
		t.Error("unregistered subject type reports valid")
	}
}
