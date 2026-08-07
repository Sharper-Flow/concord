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
	// KindInvalidOperation marks an operation that cannot be applied as a unit.
	KindInvalidOperation FailureKind = "invalid_operation"
	// KindVersionConflict marks an optimistic-concurrency precondition that no
	// longer matches the projection version.
	KindVersionConflict FailureKind = "version_conflict"
	// KindUnknownEventKind marks a log event for which no fold handler exists.
	KindUnknownEventKind FailureKind = "unknown_event_kind"
	// KindProjectionNotFound marks an event whose subject has no current row.
	KindProjectionNotFound FailureKind = "projection_not_found"
	// KindProjectionConflict marks an event that cannot establish the requested
	// projection identity because a row already exists.
	KindProjectionConflict FailureKind = "projection_conflict"
	// KindIllegalLifecycleTransition marks a state change outside PM4's closed
	// transition table. Keeping this distinct prevents callers from retrying a
	// permanently invalid workflow action.
	KindIllegalLifecycleTransition FailureKind = "illegal_lifecycle_transition"
	// KindCycleDetected marks an edge that would make a governing relation graph
	// cyclic. The graph check is performed before the edge is inserted.
	KindCycleDetected FailureKind = "cycle_detected"
	// KindSupersessionTargetAlreadySuperseded marks a supersession whose target
	// has already reached the terminal superseded state.
	KindSupersessionTargetAlreadySuperseded FailureKind = "supersession_target_already_superseded"
	// KindSupersessionSecondSuccessor marks a second direct successor for one
	// predecessor, which would make canonical replacement ambiguous.
	KindSupersessionSecondSuccessor FailureKind = "supersession_second_successor"
	// KindRelationContractViolation marks a relation event that bypasses the
	// composite operation responsible for preserving lifecycle invariants.
	KindRelationContractViolation FailureKind = "relation_contract_violation"
	// KindRelationConflict marks a relation that violates a database-enforced
	// structural rule such as uniqueness or the no-self-edge check.
	KindRelationConflict FailureKind = "relation_conflict"
	// KindRelationNotFound marks a removal that cannot be explained by the
	// preceding relation history.
	KindRelationNotFound FailureKind = "relation_not_found"
	// KindMembershipInvariant marks a final projection state that has lost a
	// required Product, Project, or work membership.
	KindMembershipInvariant FailureKind = "membership_invariant"
	// KindMembershipConflict marks a duplicate or invalid primary membership.
	KindMembershipConflict FailureKind = "membership_conflict"
	// KindMembershipMigrationRequired marks a pre-PM5 database that needs an
	// explicit operator-supplied membership mapping before migration 4.
	KindMembershipMigrationRequired FailureKind = "membership_migration_required"
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
