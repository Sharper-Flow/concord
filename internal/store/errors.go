package store

import "fmt"

// FailureKind classifies a storage failure. Callers branch on the kind rather
// than matching message text, so failure handling stays stable as wording
// changes.
type FailureKind string

const (
	// KindInvalidEvent marks an event whose required header fields are missing
	// or empty.
	KindInvalidEvent FailureKind = "invalid_event"
	// KindInvalidSubject marks an event whose subject type is not one this
	// binary recognizes. The event subject is the one application-validated
	// referential seam, so it fails closed.
	KindInvalidSubject FailureKind = "invalid_subject"
	// KindInvalidPayload marks a payload that is not a JSON object.
	KindInvalidPayload FailureKind = "invalid_payload"
	// KindUnsupportedPayloadVersion marks a payload version this binary cannot
	// interpret.
	KindUnsupportedPayloadVersion FailureKind = "unsupported_payload_version"
	// KindDuplicateEvent marks a repeated append of an event identifier that is
	// already durable. The prior effect stands.
	KindDuplicateEvent FailureKind = "duplicate_event"
	// KindSchemaDrift marks a recorded migration whose checksum no longer
	// matches this binary's definition of it.
	KindSchemaDrift FailureKind = "schema_drift"
	// KindSchemaUnsupported marks a database written by a newer binary.
	KindSchemaUnsupported FailureKind = "schema_unsupported"
	// KindUnavailable marks a database that cannot be opened or prepared.
	KindUnavailable FailureKind = "unavailable"
)

// Failure is a typed storage failure. The fields mirror the query contract's
// typed error envelope so higher layers can surface a failure without
// reclassifying it.
type Failure struct {
	// Kind is the closed classification.
	Kind FailureKind
	// Op names the operation that failed.
	Op string
	// Detail explains the specific cause.
	Detail string
	// RetrySafe reports whether repeating the same call is safe.
	RetrySafe bool
	// RecoveryAction states what resolves the failure.
	RecoveryAction string
	// Err is the underlying cause, when one exists.
	Err error
}

func (f *Failure) Error() string {
	if f.Err != nil {
		return fmt.Sprintf("store: %s: %s: %s: %v", f.Op, f.Kind, f.Detail, f.Err)
	}
	return fmt.Sprintf("store: %s: %s: %s", f.Op, f.Kind, f.Detail)
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (f *Failure) Unwrap() error { return f.Err }

func newFailure(kind FailureKind, op, detail string, retrySafe bool, recovery string) *Failure {
	return &Failure{
		Kind:           kind,
		Op:             op,
		Detail:         detail,
		RetrySafe:      retrySafe,
		RecoveryAction: recovery,
	}
}

func wrapFailure(kind FailureKind, op, detail string, retrySafe bool, recovery string, err error) *Failure {
	f := newFailure(kind, op, detail, retrySafe, recovery)
	f.Err = err
	return f
}
