package store

import "fmt"

// FailureKind classifies a storage failure. Callers branch on the kind rather
// than matching message text, so failure handling stays stable as wording
// changes.
type FailureKind string

// FailureStage is the closed phase where replay rejected an event.
type FailureStage string

const (
	StageUpcast FailureStage = "upcast"
	StageDecode FailureStage = "decode"
	StageFold   FailureStage = "fold"
)

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
	KindLimitExceeded    FailureKind = "limit_exceeded"
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
	// KindInvalidTransition marks a semantically forbidden workflow mutation
	// whose subject remains otherwise valid.
	KindInvalidTransition FailureKind = "invalid_transition"
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
	// KindInvalidRelation marks a typed workflow relation whose family or
	// composition semantics are not allowed by the pinned definitions.
	KindInvalidRelation FailureKind = "invalid_relation"
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
	KindUnknownScope                FailureKind = "unknown_scope"
	KindAmbiguousScope              FailureKind = "ambiguous_scope"
	KindStaleContext                FailureKind = "stale_context"
	KindApprovalInvalid             FailureKind = "approval_invalid"
	KindDegradedNotAllowed          FailureKind = "degraded_not_allowed"
	KindBudgetRefused               FailureKind = "budget_refused"
	KindInvalidInput                FailureKind = "invalid_input"
	KindCancelled                   FailureKind = "cancelled"
	KindTimeout                     FailureKind = "timeout"
	KindTransportFailure            FailureKind = "transport_failure"
	KindMalformedResponse           FailureKind = "malformed_response"
	KindInternalError               FailureKind = "internal_error"
	KindInvalidFilter               FailureKind = "invalid_filter"
	KindInvalidCursor               FailureKind = "invalid_cursor"
	KindStaleRequiresReview         FailureKind = "stale_requires_review"
	KindInvariantViolation          FailureKind = "invariant_violation"
	KindUnreachable                 FailureKind = "unreachable"
	KindInvalidNoteProof            FailureKind = "invalid_note_proof"
	KindGitUnreachable              FailureKind = "git_unreachable"
	KindKnowledgeIndexIncomplete    FailureKind = "knowledge_index_incomplete"
	KindIndexDegraded               FailureKind = "index_degraded"
	KindKnowledgeAmbiguous          FailureKind = "knowledge_ambiguous"
	KindKnowledgeMissing            FailureKind = "knowledge_missing"
	KindKnowledgeUnavailable        FailureKind = "knowledge_unavailable"
	KindCompactionConflict          FailureKind = "compaction_conflict"
	KindStaleAttempt                FailureKind = "stale_attempt"
	KindIdempotencyConflict         FailureKind = "idempotency_conflict"
	KindTakeoverRequired            FailureKind = "takeover_required"
	KindResearchRevisionImmutable   FailureKind = "research_revision_immutable"
	KindResearchConsumerBlocked     FailureKind = "research_consumer_blocked"
	// KindResourceClaimHeld marks a claim on a resource another work item
	// already holds. The refusal names coordination, not authority.
	KindResourceClaimHeld           FailureKind = "resource_claim_held"
	KindInitiativeScopeViolation    FailureKind = "initiative_scope_violation"
	KindInitiativeEntryConflict     FailureKind = "initiative_entry_conflict"
	KindInitiativeCompletionBlocked FailureKind = "initiative_completion_blocked"
	// KindDomainRegistryAbsent marks a Domain read against a Product whose Git
	// knowledge home has not projected a Domain registry. Absent is a refusal,
	// never an empty page: authoritative-empty requires a registry watermark.
	KindDomainRegistryAbsent FailureKind = "domain_registry_absent"
	// KindUnknownDomain marks a Domain reference that does not resolve to a
	// current Domain in the Product's projected registry.
	KindUnknownDomain FailureKind = "unknown_domain"
	// KindDecisionRecordRequired marks the deliberate pre-workflow boundary for
	// architecture spikes. Generic storage cannot yet verify accepted decision
	// records, so completion fails closed rather than inferring proof from refs.
	KindDecisionRecordRequired FailureKind = "decision_record_required"
	// KindWorkflowCompletionRequired marks a completion event attempted outside
	// the ordered, single-transaction workflow completion entry point.
	KindWorkflowCompletionRequired FailureKind = "workflow_completion_required"
	// Completion-gate failures are closed contract values. They intentionally
	// mirror the public workflow error vocabulary rather than leaking evaluator
	// implementation details.
	KindMissingEvidence   FailureKind = "missing_evidence"
	KindNotTerminal       FailureKind = "not_terminal"
	KindApprovalRequired  FailureKind = "approval_required"
	KindOutcomeMismatch   FailureKind = "outcome_mismatch"
	KindOperationConflict FailureKind = "operation_conflict"
	// Definition failures are closed registry errors. They are deliberately
	// distinct from workflow action refusals so callers cannot mistake a binary
	// definition drift for a retryable action failure.
	KindInvalidDefinition             FailureKind = "invalid_definition"
	KindDefinitionVersionConflict     FailureKind = "definition_version_conflict"
	KindDefinitionVersionNotMonotonic FailureKind = "definition_version_not_monotonic"
	KindDefinitionDigestMismatch      FailureKind = "definition_digest_mismatch"
	KindDefinitionActionOrStepUnknown FailureKind = "definition_action_or_step_unknown"
	// Lane failures are closed registry errors. They fail before a worker can
	// create any workflow authority or external effect.
	KindLaneDefinitionNotRegistered  FailureKind = "lane_definition_not_registered"
	KindLaneDefinitionDigestMismatch FailureKind = "lane_definition_digest_mismatch"
	KindLaneDefinitionInvalid        FailureKind = "lane_definition_invalid"
	KindUnauthorized                 FailureKind = "unauthorized"
	KindStaleLawRevision             FailureKind = "stale_law_revision"
	KindDomainOverlap                FailureKind = "domain_overlap"
	// KindUnauthorizedDispatch marks a worker-dispatch evidence write that
	// names a work item with no authorized, unconsumed dispatch window at
	// the current step epoch. CD-0059 makes this refusal structural: one
	// authorization admits exactly one attempt, and a worker spawned
	// outside the registered action is visible as absent evidence.
	KindUnauthorizedDispatch FailureKind = "unauthorized_dispatch"
)

// Failure is a typed storage failure. The fields mirror the query contract's
// typed error envelope so higher layers can surface a failure without
// reclassifying it.
type Failure struct {
	// Kind is the closed classification.
	Kind FailureKind `json:"kind"`
	// Op names the operation that failed.
	Op string `json:"-"`
	// Detail explains the specific cause.
	Detail string `json:"detail,omitempty"`
	// RetrySafe reports whether repeating the same call is safe.
	RetrySafe bool `json:"retry_safe"`
	// RecoveryAction states what resolves the failure.
	RecoveryAction   string   `json:"recovery_action"`
	CandidateIDs     []string `json:"candidate_ids,omitempty"`
	UnavailableKinds []string `json:"unavailable_kinds,omitempty"`
	// CurrentVersions carries the typed current version for each subject whose
	// version conflict produced this failure. Higher layers surface this
	// structurally so callers do not have to regex the human detail string.
	CurrentVersions []SubjectCurrentVersion `json:"current_versions,omitempty"`
	// Violations carries the typed offending relation/edge identities for
	// failures that refuse a relation because of a structural violation
	// (cycle, supersession target already taken, etc.).
	Violations []string `json:"violations,omitempty"`
	// StaleLawRevision carries the old and accepted successor law proofs without
	// requiring callers to parse Detail.
	StaleLawRevision *StaleLawRevision `json:"stale_law_revision,omitempty"`
	// DomainOverlap carries both active contract identities and every derived
	// bounded intersection needed to choose one of the closed recovery paths.
	DomainOverlap *DomainOverlapFailure `json:"domain_overlap,omitempty"`
	// Clause identifies the ordered workflow completion clause that refused the
	// operation. Zero means the failure did not originate in that gate.
	Clause int `json:"clause,omitempty"`
	// Event attribution is populated for replay/reconstruction failures. It is
	// deliberately bounded to the event header and fold stage; payload bytes do
	// not belong in a durable diagnostic envelope.
	EventID        string       `json:"event_id,omitempty"`
	EventKind      string       `json:"event_kind,omitempty"`
	PayloadVersion int          `json:"payload_version,omitempty"`
	SubjectType    SubjectType  `json:"subject_type,omitempty"`
	SubjectID      string       `json:"subject_id,omitempty"`
	Sequence       int64        `json:"sequence,omitempty"`
	Stage          FailureStage `json:"stage,omitempty"`
	// Err is the underlying cause, when one exists.
	Err error `json:"-"`
}

// SubjectCurrentVersion is the typed current-version carrier for an optimistic
// concurrency refusal. Exists is false for projections the projectionVersion
// probe reported as missing; in that case Version is zero and callers should
// interpret the absence as "no projection exists yet".
type SubjectCurrentVersion struct {
	SubjectType SubjectType `json:"subject_type"`
	SubjectID   string      `json:"subject_id"`
	Version     int64       `json:"version"`
	Exists      bool        `json:"exists"`
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

func attributeFailure(err error, event Event, stage FailureStage) error {
	var failure *Failure
	if !failureAs(err, &failure) {
		failure = wrapFailure(KindInvalidPayload, "fold_event", "event fold failed", false,
			"repair the event payload or install a compatible binary", err)
	}
	failure.EventID = event.EventID
	failure.EventKind = event.Kind
	failure.PayloadVersion = event.PayloadVersion
	failure.SubjectType = event.SubjectType
	failure.SubjectID = event.SubjectID
	failure.Sequence = event.Seq
	if failure.Stage == "" {
		failure.Stage = stage
	}
	return failure
}

// failureAs is kept local so attribution can preserve wrapped typed failures
// without requiring callers to know the implementation of errors.As.
func failureAs(err error, target **Failure) bool {
	if err == nil {
		return false
	}
	for err != nil {
		if failure, ok := err.(*Failure); ok {
			*target = failure
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
