package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
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
			return 0, wrapFailure(KindDuplicateEvent, "append_event",
				"event "+e.EventID+" is already recorded", true,
				"treat the existing event as the durable effect", err)
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

// isUniqueViolation reports whether err is a uniqueness conflict. The driver
// does not export a typed constraint error, so the message is matched here and
// translated into a typed failure once, at this boundary.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed")
}
