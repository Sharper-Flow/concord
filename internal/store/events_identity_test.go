package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAppendEventRejectsDivergentReuseOfEventID covers the divergent
// dimensions that issue #190 requires the append seam to reject as
// KindIdempotencyConflict. Each case inserts an event with one targeted
// field changed; the test asserts the rejection kind and that the recorded
// row is byte-identical (no update, no delete, no second row).
func TestAppendEventRejectsDivergentReuseOfEventID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Event)
		expect string
	}{
		{
			"different subject_type",
			func(e *Event) { e.SubjectType = SubjectProject },
			"subject_type",
		},
		{
			"different subject_id",
			func(e *Event) { e.SubjectID = "project-different" },
			"subject_id",
		},
		{
			"different actor",
			func(e *Event) { e.Actor = "another-operator" },
			"actor",
		},
		{
			"different canonical payload",
			func(e *Event) {
				e.Payload = []byte(`{"display_name":"Different","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)
			},
			"payload",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			ctx := context.Background()

			first := divergentIdentityBaseEvent()
			if _, err := appendInTx(t, s, first); err != nil {
				t.Fatalf("first AppendEvent() error = %v", err)
			}
			original, err := readRawEventRow(t, ctx, s, first.EventID)
			if err != nil {
				t.Fatalf("read original event: %v", err)
			}

			second := divergentIdentityBaseEvent()
			tc.mutate(&second)
			_, err = appendInTx(t, s, second)
			assertIdempotencyConflict(t, err, tc.expect)

			after, err := readRawEventRow(t, ctx, s, first.EventID)
			if err != nil {
				t.Fatalf("read post-rejection event: %v", err)
			}
			if original != after {
				t.Errorf("recorded event mutated after rejected divergent append:\nbefore=%s\nafter=%s", original, after)
			}
			assertEventRowCount(t, ctx, s, first.EventID, 1)
		})
	}
}

// TestAppendEventRejectsDivergentKind covers the kind dimension on its own
// because product.created and project.renamed accept different payload shapes;
// the test asserts the rejection kind and that the Detail names the kind.
func TestAppendEventRejectsDivergentKind(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	first := divergentIdentityBaseEvent()
	if _, err := appendInTx(t, s, first); err != nil {
		t.Fatalf("first AppendEvent() error = %v", err)
	}
	original, err := readRawEventRow(t, ctx, s, first.EventID)
	if err != nil {
		t.Fatalf("read original event: %v", err)
	}

	second := divergentIdentityBaseEvent()
	second.Kind = "project.renamed"
	second.Payload = []byte(`{"display_name":"Renamed"}`)
	_, err = appendInTx(t, s, second)
	assertIdempotencyConflict(t, err, "kind")

	after, err := readRawEventRow(t, ctx, s, first.EventID)
	if err != nil {
		t.Fatalf("read post-rejection event: %v", err)
	}
	if original != after {
		t.Errorf("recorded event mutated after rejected divergent append:\nbefore=%s\nafter=%s", original, after)
	}
	assertEventRowCount(t, ctx, s, first.EventID, 1)
}

// divergentIdentityBaseEvent returns an event that is registered, validates,
// and folds through a kind whose payload accepts the canonical comparison we
// need. product.created has a JSON-object payload with only the three required
// fields; every divergent case mutates exactly one of those fields or the
// header columns.
func divergentIdentityBaseEvent() Event {
	return Event{
		EventID:        "divergent-evt",
		Kind:           "product.created",
		SubjectType:    SubjectProduct,
		SubjectID:      "product-identity",
		Actor:          "operator",
		OccurredAt:     time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		PayloadVersion: 1,
		Payload:        []byte(`{"display_name":"Original","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`),
	}
}

// assertIdempotencyConflict fails the test unless err is a typed
// KindIdempotencyConflict that names the expected field in its Detail and
// carries the recovery wording that advises choosing a new event_id.
func assertIdempotencyConflict(t *testing.T, err error, expectField string) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("AppendEvent() error = %v, want *Failure", err)
	}
	if f.Kind != KindIdempotencyConflict {
		t.Errorf("Kind = %q, want %q", f.Kind, KindIdempotencyConflict)
	}
	if f.RetrySafe {
		t.Error("RetrySafe = true; a divergent reuse must not be retried")
	}
	if !strings.Contains(f.RecoveryAction, "event_id") {
		t.Errorf("RecoveryAction = %q, want it to advise choosing a new event_id", f.RecoveryAction)
	}
	if !strings.Contains(f.Detail, expectField) {
		t.Errorf("Detail = %q, want it to name %s", f.Detail, expectField)
	}
}

// assertEventRowCount fails the test unless exactly count rows hold eventID.
func assertEventRowCount(t *testing.T, ctx context.Context, s *Store, eventID string, count int) {
	t.Helper()
	var got int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE event_id = ?`, eventID).Scan(&got); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if got != count {
		t.Errorf("event count for %s = %d, want %d", eventID, got, count)
	}
}

// TestAppendEventRejectsDifferentPayloadVersion covers the payload_version
// dimension explicitly. project.created accepts both v1 and v2, so two
// attempts at the same event_id with different versions reach the conflict
// path rather than failing payload-version validation. The stored values are
// the caller-supplied payload_version and payload, so the readback exposes the
// difference directly.
func TestAppendEventRejectsDifferentPayloadVersion(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	first := projectIdentityEvent("divergent-version-evt", 1)
	if _, err := appendInTx(t, s, first); err != nil {
		t.Fatalf("first AppendEvent() error = %v", err)
	}
	original, err := readRawEventRow(t, ctx, s, first.EventID)
	if err != nil {
		t.Fatalf("read original event: %v", err)
	}

	second := projectIdentityEvent("divergent-version-evt", 2)
	_, err = appendInTx(t, s, second)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("second AppendEvent() error = %v, want *Failure", err)
	}
	if f.Kind != KindIdempotencyConflict {
		t.Errorf("Kind = %q, want %q", f.Kind, KindIdempotencyConflict)
	}
	if !strings.Contains(f.Detail, "payload_version") {
		t.Errorf("Detail = %q, want it to name payload_version", f.Detail)
	}

	after, err := readRawEventRow(t, ctx, s, first.EventID)
	if err != nil {
		t.Fatalf("read post-rejection event: %v", err)
	}
	if original != after {
		t.Errorf("recorded event mutated after rejected divergent append:\nbefore=%s\nafter=%s", original, after)
	}
}

// TestAppendEventRejectsLargeIntegerDifference is the regression test for the
// canonicalJSON float64 defect. Two integers beyond float64's exact range that
// used to collapse to the same float must still be classified as divergent.
func TestAppendEventRejectsLargeIntegerDifference(t *testing.T) {
	s := openTemp(t)

	first := largeIntegerEvent("large-int-evt", 9007199254740993)
	if _, err := appendInTx(t, s, first); err != nil {
		t.Fatalf("first AppendEvent() error = %v", err)
	}

	second := largeIntegerEvent("large-int-evt", 9007199254740994)
	_, err := appendInTx(t, s, second)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("second AppendEvent() error = %v, want *Failure", err)
	}
	if f.Kind != KindIdempotencyConflict {
		t.Errorf("Kind = %q, want %q", f.Kind, KindIdempotencyConflict)
	}
	if !strings.Contains(f.Detail, "payload") {
		t.Errorf("Detail = %q, want it to name payload", f.Detail)
	}
}

// TestAppendEventAcceptsEquivalentRetry covers the cases that must still be
// reported as KindDuplicateEvent: byte-identical retry, re-ordered object keys,
// insignificant whitespace, and a fresh occurred_at clock reading.
func TestAppendEventAcceptsEquivalentRetry(t *testing.T) {
	t.Run("byte identical", func(t *testing.T) {
		assertEquivalentRetry(t, testEvent(), testEvent())
	})
	t.Run("different object key order", func(t *testing.T) {
		second := testEvent()
		second.Payload = []byte(`{"stage_audience_commitment":"operator_only","stage_maturity":"prototype","display_name":"First Product"}`)
		assertEquivalentRetry(t, testEvent(), second)
	})
	t.Run("different whitespace", func(t *testing.T) {
		second := testEvent()
		second.Payload = []byte(`{ "display_name" : "First Product" , "stage_maturity": "prototype" , "stage_audience_commitment" : "operator_only" }`)
		assertEquivalentRetry(t, testEvent(), second)
	})
	t.Run("different occurred_at", func(t *testing.T) {
		second := testEvent()
		second.OccurredAt = time.Date(2026, 8, 7, 12, 0, 1, 0, time.UTC)
		assertEquivalentRetry(t, testEvent(), second)
	})
}

// assertEquivalentRetry inserts first, then attempts second at the same
// event_id, and asserts the rejection is KindDuplicateEvent with a retry-safe
// outcome that names the existing sequence.
func assertEquivalentRetry(t *testing.T, first, second Event) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()

	if _, err := appendInTx(t, s, first); err != nil {
		t.Fatalf("first AppendEvent() error = %v", err)
	}
	original, err := readRawEventRow(t, ctx, s, first.EventID)
	if err != nil {
		t.Fatalf("read original event: %v", err)
	}

	_, err = appendInTx(t, s, second)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("second AppendEvent() error = %v, want *Failure", err)
	}
	if f.Kind != KindDuplicateEvent {
		t.Errorf("Kind = %q, want %q", f.Kind, KindDuplicateEvent)
	}
	if !f.RetrySafe {
		t.Error("RetrySafe = false; an equivalent retry is the durable effect itself")
	}
	if !strings.Contains(f.Detail, "sequence") {
		t.Errorf("Detail = %q, want it to name the existing sequence", f.Detail)
	}

	after, err := readRawEventRow(t, ctx, s, first.EventID)
	if err != nil {
		t.Fatalf("read post-rejection event: %v", err)
	}
	if original != after {
		t.Errorf("recorded event mutated after duplicate append:\nbefore=%s\nafter=%s", original, after)
	}
}

// TestAppendEventConcurrentEquivalentAppends shows the same single row is the
// durable effect regardless of how many goroutines race to append an
// equivalent retry.
func TestAppendEventConcurrentEquivalentAppends(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	const workers = 8
	results := make([]error, workers)
	events := make([]Event, workers)
	for i := 0; i < workers; i++ {
		e := testEvent()
		e.OccurredAt = time.Date(2026, 8, 7, 12, 0, 0, i, time.UTC)
		events[i] = e
	}

	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				results[i] = err
				done <- struct{}{}
				return
			}
			_, err = AppendEvent(ctx, tx, events[i])
			if err != nil {
				_ = tx.Rollback()
				results[i] = err
				done <- struct{}{}
				return
			}
			if err := tx.Commit(); err != nil {
				results[i] = err
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}

	var accepted int
	var duplicates int
	for _, err := range results {
		if err == nil {
			accepted++
			continue
		}
		var f *Failure
		if errors.As(err, &f) && f.Kind == KindDuplicateEvent {
			duplicates++
			continue
		}
		t.Errorf("unexpected concurrent outcome: %v", err)
	}
	if accepted != 1 {
		t.Errorf("accepted = %d, want exactly one writer to win", accepted)
	}
	if duplicates != workers-1 {
		t.Errorf("duplicates = %d, want %d", duplicates, workers-1)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE event_id = ?`, testEvent().EventID).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("event count = %d, want 1", count)
	}
}

// TestAppendEventConcurrentDivergentAppends shows that racing callers reusing
// the same event_id with different content each surface a typed conflict and
// never overwrite the recorded row.
func TestAppendEventConcurrentDivergentAppends(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	const workers = 6
	results := make([]error, workers)
	events := make([]Event, workers)
	for i := 0; i < workers; i++ {
		e := testEvent()
		e.Actor = "operator-" + string(rune('a'+i))
		events[i] = e
	}

	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				results[i] = err
				done <- struct{}{}
				return
			}
			_, err = AppendEvent(ctx, tx, events[i])
			if err != nil {
				_ = tx.Rollback()
				results[i] = err
				done <- struct{}{}
				return
			}
			if err := tx.Commit(); err != nil {
				results[i] = err
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}

	var accepted int
	var conflicts int
	for _, err := range results {
		if err == nil {
			accepted++
			continue
		}
		var f *Failure
		if errors.As(err, &f) && f.Kind == KindIdempotencyConflict {
			conflicts++
			continue
		}
		t.Errorf("unexpected concurrent outcome: %v", err)
	}
	if accepted != 1 {
		t.Errorf("accepted = %d, want exactly one writer to win", accepted)
	}
	if conflicts != workers-1 {
		t.Errorf("conflicts = %d, want %d", conflicts, workers-1)
	}
	var actor string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT actor FROM domain_events WHERE event_id = ?`, testEvent().EventID).Scan(&actor); err != nil {
		t.Fatalf("read winning actor: %v", err)
	}
	if !strings.HasPrefix(actor, "operator-") {
		t.Errorf("winning actor = %q, want one of the racing actors", actor)
	}
}

// TestAppendEventUniqueViolationLeavesTransactionUsable verifies the SQLite
// statement-level rollback claim the design relies on. After a UNIQUE INSERT
// fails inside a transaction, a follow-up SELECT on the same tx must still
// succeed and read the row that already holds the event_id.
func TestAppendEventUniqueViolationLeavesTransactionUsable(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if _, err := appendInTx(t, s, testEvent()); err != nil {
		t.Fatalf("seed AppendEvent() error = %v", err)
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			t.Fatalf("Rollback() error = %v", rbErr)
		}
	}()

	if _, err := AppendEvent(ctx, tx, testEvent()); err == nil {
		t.Fatal("second AppendEvent() succeeded; a UNIQUE violation was expected")
	}

	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM domain_events WHERE event_id = ?`, testEvent().EventID).Scan(&seq); err != nil {
		t.Fatalf("SELECT after UNIQUE violation: %v (statement-level rollback did not hold)", err)
	}
	if seq <= 0 {
		t.Errorf("seq = %d, want positive", seq)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

// TestCanonicalJSONPreservesLargeIntegers is the focused regression test for
// the canonicalJSON UseNumber fix. The two integers are equal as float64 and
// must still compare as distinct strings after canonicalization.
func TestCanonicalJSONPreservesLargeIntegers(t *testing.T) {
	a := json.RawMessage(`{"n":9007199254740993}`)
	b := json.RawMessage(`{"n":9007199254740994}`)

	aCanonical, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON(a) error = %v", err)
	}
	bCanonical, err := canonicalJSON(b)
	if err != nil {
		t.Fatalf("canonicalJSON(b) error = %v", err)
	}
	if string(aCanonical) == string(bCanonical) {
		t.Errorf("canonicalJSON collapsed distinct large integers to the same form:\na=%s\nb=%s", aCanonical, bCanonical)
	}
}

// readRawEventRow returns the row bytes verbatim so a test can prove the
// stored row is byte-identical before and after a rejected append.
func readRawEventRow(t *testing.T, ctx context.Context, s *Store, eventID string) (string, error) {
	t.Helper()
	var row string
	if err := s.DatabaseForTesting().QueryRowContext(ctx,
		`SELECT seq || '|' || event_id || '|' || kind || '|' || subject_type || '|' || subject_id || '|' || actor || '|' || occurred_at || '|' || payload_version || '|' || payload FROM domain_events WHERE event_id = ?`,
		eventID,
	).Scan(&row); err != nil {
		return "", err
	}
	return row, nil
}

// projectIdentityEvent builds a project.created event at the requested
// payload version. The v1 form omits the stage override fields because the
// v1 upcaster refuses payloads that already carry them; the v2 form carries
// them as explicit nulls. Both forms share the same canonical content.
func projectIdentityEvent(eventID string, version int) Event {
	var payload string
	if version == 1 {
		payload = `{"display_name":"Project Identity"}`
	} else {
		payload = `{"display_name":"Project Identity","stage_maturity_override":null,"stage_audience_commitment_override":null}`
	}
	return Event{
		EventID:        eventID,
		Kind:           "project.created",
		SubjectType:    SubjectProject,
		SubjectID:      "project-identity",
		Actor:          "operator",
		OccurredAt:     time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		PayloadVersion: version,
		Payload:        []byte(payload),
	}
}

// largeIntegerEvent builds a project.created v=2 event whose
// stage_maturity_override carries a large integer. The override is a
// json.RawMessage, so the literal survives the decode/canonicalize cycle
// without float64 precision loss.
func largeIntegerEvent(eventID string, big int64) Event {
	return Event{
		EventID:        eventID,
		Kind:           "project.created",
		SubjectType:    SubjectProject,
		SubjectID:      "project-large-int",
		Actor:          "operator",
		OccurredAt:     time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		PayloadVersion: 2,
		Payload:        []byte(`{"display_name":"Project Large Int","stage_maturity_override":` + jsonIntLiteral(big) + `,"stage_audience_commitment_override":null}`),
	}
}

// jsonIntLiteral returns n as a JSON integer literal, with no loss.
func jsonIntLiteral(n int64) string {
	return strconv.FormatInt(n, 10)
}

// TestCanonicalJSONRejectsTrailingContent pins the single-value rule. Decoding
// with a streaming decoder would otherwise stop at the first value and let two
// inputs differing only after it compare equal.
func TestCanonicalJSONRejectsTrailingContent(t *testing.T) {
	for _, raw := range []string{`{"a":1} {"b":2}`, `{"a":1}[]`, `{"a":1}garbage`} {
		if _, err := canonicalJSON([]byte(raw)); err == nil {
			t.Errorf("canonicalJSON(%q) = nil error, want rejection of trailing content", raw)
		}
	}
	if _, err := canonicalJSON([]byte(`{"a":1}`)); err != nil {
		t.Errorf("canonicalJSON on a single object returned %v, want nil", err)
	}
}
