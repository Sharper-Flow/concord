package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// SubjectType names the kind of thing an event is about.
//
// SQLite cannot express one foreign key that targets several typed projection
// tables, so the event subject is the one deliberate application-validated
// referential seam. It is a closed set and fails closed: an unrecognized
// subject is refused rather than stored for a later reader to interpret.
type SubjectType string

const (
	SubjectProduct  SubjectType = "product"
	SubjectProject  SubjectType = "project"
	SubjectWorkItem SubjectType = "work_item"
)

// subjectTypes is the closed set this binary recognizes.
var subjectTypes = map[SubjectType]struct{}{
	SubjectProduct:  {},
	SubjectProject:  {},
	SubjectWorkItem: {},
}

func (s SubjectType) valid() bool {
	_, ok := subjectTypes[s]
	return ok
}

func knownSubjectTypes() []SubjectType {
	out := make([]SubjectType, 0, len(subjectTypes))
	for s := range subjectTypes {
		out = append(out, s)
	}
	return out
}

// Event is one row of the append-only log.
type Event struct {
	// Seq is populated when an event is read from the log. It is not caller
	// supplied and is never persisted as part of the event payload.
	Seq Sequence
	// EventID is the caller-supplied stable identity. Repeating an append with
	// the same identity is reported as a duplicate rather than creating a
	// second effect.
	EventID string
	// Kind names the domain event.
	Kind string
	// SubjectType and SubjectID name what the event is about.
	SubjectType SubjectType
	SubjectID   string
	// Actor records who performed the operation.
	Actor string
	// OccurredAt is normalized to Coordinated Universal Time on write.
	OccurredAt time.Time
	// PayloadVersion selects the payload shape, and later selects the upcasters
	// a rebuild runs.
	PayloadVersion int
	// Payload is a JSON object.
	Payload []byte
}

// Sequence is the log's stable total order. It is assigned by the database on
// append.
type Sequence = int64

// AppendEvent writes one event inside the caller's transaction and returns its
// assigned sequence.
//
// The transaction is supplied rather than opened here so an event and every
// projection it affects commit together. There is no variant that commits on
// its own.
func AppendEvent(ctx context.Context, tx *sql.Tx, e Event) (Sequence, error) {
	return appendEvent(ctx, tx, e, false)
}

// appendEvent is the lower-level append seam. All workflow advancement events
// are reserved for an owning workflow route; the private authority bit is not
// exposed through the generic append APIs.
func appendEvent(ctx context.Context, tx *sql.Tx, e Event, allowCompletion bool) (Sequence, error) {
	if err := e.validate(); err != nil {
		return 0, err
	}
	prepared, err := prepareRegisteredEvent(e)
	if err != nil {
		return 0, err
	}
	if prepared.registration.Authority == EventAppendAuthorityWorkflow && !allowCompletion {
		return 0, workflowDispatcherRequired(e.Kind)
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO domain_events
			(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.EventID, e.Kind, string(e.SubjectType), e.SubjectID, e.Actor,
		e.OccurredAt.UTC().Format(time.RFC3339Nano), e.PayloadVersion, string(e.Payload))
	if err != nil {
		if isUniqueViolation(err) {
			return 0, classifyEventIDConflict(ctx, tx, e, err)
		}
		return 0, wrapFailure(KindUnavailable, "append_event", "cannot append the event", true,
			"retry once the database is writable", err)
	}

	seq, err := res.LastInsertId()
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "append_event", "cannot read the assigned sequence", true,
			"retry the operation", err)
	}
	return seq, nil
}

func (e Event) validate() error {
	const op = "append_event"

	for _, missing := range []struct {
		field string
		empty bool
	}{
		{"event id", e.EventID == ""},
		{"kind", e.Kind == ""},
		{"subject id", e.SubjectID == ""},
		{"actor", e.Actor == ""},
		{"occurrence time", e.OccurredAt.IsZero()},
	} {
		if missing.empty {
			return newFailure(KindInvalidEvent, op, "event is missing its "+missing.field, false,
				"supply every required event header field")
		}
	}

	if !e.SubjectType.valid() {
		return newFailure(KindInvalidSubject, op,
			"unrecognized subject type "+string(e.SubjectType), false,
			"use a subject type this binary recognizes")
	}

	if e.PayloadVersion < 1 {
		return newFailure(KindUnsupportedPayloadVersion, op,
			"payload version must be positive", false,
			"set a payload version of at least 1")
	}

	if !isJSONObject(e.Payload) {
		return newFailure(KindInvalidPayload, op, "payload is not a JSON object", false,
			"encode the payload as a JSON object")
	}

	return nil
}

// isJSONObject reports whether payload is a syntactically valid JSON object.
// Per-kind payload schemas arrive with the event kinds that need them.
func isJSONObject(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	var decoded map[string]json.RawMessage
	return json.Unmarshal(payload, &decoded) == nil && decoded != nil
}

// constraintCode returns the typed SQLite result code carried by err, or
// 0 when err is not a driver error. Driver message text is not a stability
// contract; classification at this boundary uses the typed code.
func constraintCode(err error) int {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return 0
	}
	return sqliteErr.Code()
}

// isUniqueViolation reports whether err is a uniqueness conflict.
func isUniqueViolation(err error) bool {
	return constraintCode(err) == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}

// isForeignKeyViolation reports whether err is a foreign-key conflict.
func isForeignKeyViolation(err error) bool {
	return constraintCode(err) == sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY
}

// isCheckViolation reports whether err is a CHECK constraint conflict.
func isCheckViolation(err error) bool {
	return constraintCode(err) == sqlitelib.SQLITE_CONSTRAINT_CHECK
}

// isConstraintViolation reports whether err is any SQLite constraint
// conflict (unique, primary key, foreign key, check, not-null).
func isConstraintViolation(err error) bool {
	code := constraintCode(err)
	return code != 0 && code&0xff == sqlitelib.SQLITE_CONSTRAINT
}

// classifyEventIDConflict inspects the event that already holds the requested
// event_id and reports whether the second attempt is an equivalent retry or a
// divergent reuse. It runs inside the caller's transaction so the read observes
// the durable row from the same connection, never a stale snapshot from a
// separate pool member.
//
// The SQLite default ON CONFLICT resolution is ABORT, which rolls back only
// the failing statement and leaves the transaction usable for the follow-up
// SELECT. If that guarantee ever changes, callers will see a read failure
// rather than silently classify the wrong operation, and this function reports
// it as KindUnavailable rather than guessing.
//
// occurred_at is deliberately excluded from semantic identity: a legitimate
// retry carries a fresh clock reading and must not be misclassified as
// divergent. A future reader must not "fix" that omission.
func classifyEventIDConflict(ctx context.Context, tx *sql.Tx, attempted Event, insertErr error) error {
	var existing Event
	if err := tx.QueryRowContext(ctx,
		`SELECT seq, kind, subject_type, subject_id, actor, payload_version, payload FROM domain_events WHERE event_id = ?`,
		attempted.EventID,
	).Scan(&existing.Seq, &existing.Kind, &existing.SubjectType, &existing.SubjectID, &existing.Actor, &existing.PayloadVersion, &existing.Payload); err != nil {
		return wrapFailure(KindUnavailable, "append_event",
			"cannot read the conflicting event_id "+attempted.EventID, true,
			"retry once the database is readable", err)
	}
	if diffs := eventIdentityDifferences(attempted, existing); len(diffs) > 0 {
		return newFailure(KindIdempotencyConflict, "append_event",
			fmt.Sprintf("event_id %q is already recorded with a different %s", attempted.EventID, strings.Join(diffs, ", ")),
			false,
			"choose a new event_id; the recorded event is a different operation")
	}
	return wrapFailure(KindDuplicateEvent, "append_event",
		fmt.Sprintf("event %s is already recorded at sequence %d", attempted.EventID, existing.Seq),
		true,
		"treat the existing event as the durable effect", insertErr)
}

// eventIdentityDifferences returns the names of semantic-identity fields that
// differ between attempted and existing, in a stable order. occurred_at is
// deliberately omitted: a legitimate retry carries a fresh clock reading and
// must not be misclassified as divergent. The canonical payload comparison
// rules live on canonicalJSON.
func eventIdentityDifferences(attempted, existing Event) []string {
	var diffs []string
	if attempted.Kind != existing.Kind {
		diffs = append(diffs, "kind")
	}
	if attempted.SubjectType != existing.SubjectType {
		diffs = append(diffs, "subject_type")
	}
	if attempted.SubjectID != existing.SubjectID {
		diffs = append(diffs, "subject_id")
	}
	if attempted.Actor != existing.Actor {
		diffs = append(diffs, "actor")
	}
	if attempted.PayloadVersion != existing.PayloadVersion {
		diffs = append(diffs, "payload_version")
	}
	if !payloadsCanonicallyEqual(attempted.Payload, existing.Payload) {
		diffs = append(diffs, "payload")
	}
	return diffs
}

// payloadsCanonicallyEqual reports whether two JSON payloads are semantically
// equal under canonicalJSON. A canonicalization failure on either side is
// treated as divergent so a malformed payload never silently masks a conflict.
func payloadsCanonicallyEqual(a, b []byte) bool {
	aCanonical, errA := canonicalJSON(a)
	if errA != nil {
		return false
	}
	bCanonical, errB := canonicalJSON(b)
	if errB != nil {
		return false
	}
	return string(aCanonical) == string(bCanonical)
}
