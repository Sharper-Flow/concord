// CD-0061 D4: a launcher-started session records the orchestrator identity
// it asserted as a domain event with subject_type='session'. The event is
// evidence, not configuration — no projection consumes it, so its fold is
// the no-op projectionMutation. Anything that tries to authorize behaviour
// from this event would re-introduce the persona definition CD-0049 Invariant
// 4 disclaims.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"time"
)

// EventSessionOrchestratorIdentityAsserted names the kind Concord records at
// session start once it has asserted the orchestrator identity the host
// resolved. CD-0061 D4 fixes this as a domain event subject_type='session'.
const EventSessionOrchestratorIdentityAsserted = "session.orchestrator_identity_asserted"

// orchestratorIdentityAssertedPayload is the closed payload shape recorded
// once per session start. It carries the asserted type and version (both
// Concord-owned constants) and the SHA-256 over the resolved-artifact
// manifest the digest derives from. The sources list is the evidence the
// digest was computed over; a reader recomputes the digest from it and
// compares to detect any drift between what was asserted and what the host
// would resolve today.
type orchestratorIdentityAssertedPayload struct {
	Type          string                       `json:"type"`
	Version       string                       `json:"version"`
	RulesetDigest string                       `json:"ruleset_digest"`
	Sources       []orchestratorArtifactSource `json:"sources"`
}

type orchestratorArtifactSource struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// OrchestratorIdentityAssertion is what an orchestrator session records.
// Type and Version are Concord-owned constants; the digest and source list
// are host-derived, computed from the artifacts the assertion actually
// resolved. The remaining fields carry the session identity the event is
// attributed to.
type OrchestratorIdentityAssertion struct {
	Type          string
	Version       string
	RulesetDigest string
	Sources       []OrchestratorArtifactSource
	// ProductID and WorkID name the session the assertion is for. ProductID
	// is the launcher-provided product scope; WorkID is the optional work
	// scope and is empty for product-only sessions.
	ProductID string
	WorkID    string
	// Actor identity fields, formatted the same way a worker attempt
	// reports them. They flow into the canonical actor_ref the event
	// appendEvent carries.
	PrincipalRef string
	ClientRef    string
	AgentRef     string
	SessionRef   string
}

// OrchestratorArtifactSource records one host-supplied artifact the
// orchestrator assertion resolved. Kind names the surface (the orchestrator
// definition file, the AGENTS.md chain, or a host-declared instruction
// file), Path the resolved filesystem location, SHA256 the bare hex digest
// of its bytes.
type OrchestratorArtifactSource struct {
	Kind   string
	Path   string
	SHA256 string
}

// SubjectID returns the canonical session identifier the assertion is
// recorded against: the work id when one is in scope, otherwise the
// product id. A session is always scoped to one product.
func (a OrchestratorIdentityAssertion) SubjectID() string {
	if a.WorkID != "" {
		return a.WorkID
	}
	return a.ProductID
}

// ActorRef derives the canonical session actor the assertion is attributed
// to. The actor ref is derived from the launcher-provided identity so a
// session run later can identify the originating context.
func (a OrchestratorIdentityAssertion) ActorRef() string {
	return DeriveWorkflowActorRef(a.PrincipalRef, a.ClientRef, a.AgentRef, a.SessionRef)
}

// orchestratorDigestPattern is the closed shape of a ruleset digest this
// binary records. Other shapes are rejected at the payload boundary so a
// recorded value is provably the same construction on read.
var orchestratorDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// sha256HexPattern matches the bare hex form of a SHA-256 digest (no
// "sha256:" prefix). The per-source digests are recorded unprefixed so the
// manifest concatenation the ruleset digest hashes over is stable.
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// foldSessionOrchestratorIdentityAsserted is the no-op projection fold. The
// event is evidence of what was asserted at a moment; nothing reads it back
// to decide what a session may do, so the projection fold has no work.
func foldSessionOrchestratorIdentityAsserted(ctx context.Context, tx *sql.Tx, event Event) error {
	return nil
}

// RecordOrchestratorIdentityAssertion appends the assertion as a domain
// event with subject_type='session'. The event_id is supplied by the caller
// (the session command derives it from the launcher-provided product and
// work identity so concurrent sessions do not collide). OccurredAt is the
// wall-clock instant Concord asserts the identity.
//
// The write owns its transaction; the only state in scope is the assertion
// and the caller-supplied event id and timestamp.
func (s *Store) RecordOrchestratorIdentityAssertion(ctx context.Context, eventID string, occurredAt time.Time, assertion OrchestratorIdentityAssertion) (Sequence, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "record_orchestrator_identity",
			"store is not open", false, "open the authority database before recording an assertion")
	}
	if eventID == "" {
		return 0, newFailure(KindInvalidOperation, "record_orchestrator_identity",
			"event id is required", false, "supply an event id")
	}
	if occurredAt.IsZero() {
		return 0, newFailure(KindInvalidOperation, "record_orchestrator_identity",
			"occurred_at is required", false, "supply the wall-clock instant of the assertion")
	}
	var seq Sequence
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return appendOrchestratorIdentityAssertionTx(ctx, transaction, eventID, occurredAt, assertion, &seq)
	})
	if err != nil {
		return 0, err
	}
	if err := s.SyncDurable(ctx); err != nil {
		return 0, err
	}
	return seq, nil
}

func appendOrchestratorIdentityAssertionTx(ctx context.Context, transaction *Transaction, eventID string, occurredAt time.Time, assertion OrchestratorIdentityAssertion, seqOut *Sequence) error {
	sources := make([]orchestratorArtifactSource, len(assertion.Sources))
	for i, src := range assertion.Sources {
		sources[i] = orchestratorArtifactSource{Kind: src.Kind, Path: src.Path, SHA256: src.SHA256}
	}
	payload, err := json.Marshal(orchestratorIdentityAssertedPayload{
		Type:          assertion.Type,
		Version:       assertion.Version,
		RulesetDigest: assertion.RulesetDigest,
		Sources:       sources,
	})
	if err != nil {
		return wrapFailure(KindUnavailable, "record_orchestrator_identity",
			"cannot encode the assertion payload", false, "inspect the assertion fields", err)
	}
	tx, err := transactionSQL(transaction, "record_orchestrator_identity")
	if err != nil {
		return err
	}
	seq, err := AppendEvent(ctx, tx, Event{
		EventID:        eventID,
		Kind:           EventSessionOrchestratorIdentityAsserted,
		SubjectType:    SubjectSession,
		SubjectID:      assertion.SubjectID(),
		Actor:          assertion.ActorRef(),
		OccurredAt:     occurredAt,
		PayloadVersion: 1,
		Payload:        payload,
	})
	if err != nil {
		return err
	}
	*seqOut = seq
	return nil
}

// validateSessionOrchestratorIdentityAssertedPayload enforces the closed
// shape on write. A malformed payload cannot quietly enter the log; the
// recorded value must be the Concord-owned type constant, the Concord-owned
// version constant, a sha256:<hex> digest, and at least the orchestrator
// definition file in its sources list.
func validateSessionOrchestratorIdentityAssertedPayload(_ Event, p orchestratorIdentityAssertedPayload) error {
	if p.Type == "" {
		return newFailure(KindInvalidPayload, "validate_session_orchestrator_identity",
			"event is missing the orchestrator type", false,
			"set type to the Concord-owned orchestrator type constant")
	}
	if p.Version == "" {
		return newFailure(KindInvalidPayload, "validate_session_orchestrator_identity",
			"event is missing the orchestrator version", false,
			"set version to the Concord-owned orchestrator version constant")
	}
	if !orchestratorDigestPattern.MatchString(p.RulesetDigest) {
		return newFailure(KindInvalidPayload, "validate_session_orchestrator_identity",
			"ruleset_digest must match sha256:<64-hex>", false,
			"compute the digest from the resolved artifacts and prefix it sha256:")
	}
	if len(p.Sources) == 0 {
		return newFailure(KindInvalidPayload, "validate_session_orchestrator_identity",
			"sources must enumerate at least the orchestrator definition file", false,
			"include the resolved definition file in sources")
	}
	for _, src := range p.Sources {
		if src.Kind == "" || src.Path == "" {
			return newFailure(KindInvalidPayload, "validate_session_orchestrator_identity",
				"every source must name its kind and path", false,
				"supply kind and path for every resolved artifact")
		}
		if !sha256HexPattern.MatchString(src.SHA256) {
			return newFailure(KindInvalidPayload, "validate_session_orchestrator_identity",
				"source sha256 must be 64 lowercase hex characters", false,
				"hash the artifact bytes and record the hex digest")
		}
	}
	return nil
}
