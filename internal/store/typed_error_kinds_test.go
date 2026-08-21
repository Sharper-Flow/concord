package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The generated vocabulary must equal the enum it is projected from. A Go-side
// edit that drifts from the schema fails here as well as in the generator's
// --check mode, so the drift is caught by `go test` and not only by the
// contract validator.
func TestTypedErrorKindsMatchEnvelopeSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-envelope.schema.json"))
	if err != nil {
		t.Fatalf("cannot read the envelope schema: %v", err)
	}
	var document struct {
		Defs struct {
			TypedError struct {
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"typedError"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("cannot decode the envelope schema: %v", err)
	}

	declared := document.Defs.TypedError.Properties.Kind.Enum
	if len(declared) == 0 {
		t.Fatal("the envelope schema declares no typed error kinds")
	}

	projected := TypedErrorKinds()
	if len(projected) != len(declared) {
		t.Fatalf("projected %d kinds, schema declares %d", len(projected), len(declared))
	}
	for i, kind := range declared {
		if projected[i] != kind {
			t.Errorf("position %d: projected %q, schema declares %q", i, projected[i], kind)
		}
		if !TypedErrorKindAllowed(kind) {
			t.Errorf("schema kind %q is rejected by TypedErrorKindAllowed", kind)
		}
	}
}

// The two kinds the store fold previously omitted. Named explicitly so the
// specific regression in #265 cannot silently return.
func TestPreviouslyOmittedKindsAreAccepted(t *testing.T) {
	for _, kind := range []string{"stale_law_revision", "domain_overlap"} {
		if !TypedErrorKindAllowed(kind) {
			t.Errorf("%q must be accepted; the store fold omitted it while the agent layer accepted it", kind)
		}
	}
}

// Every TS7 kind should be reachable through a named constant rather than a
// bare string literal at the call site. The generated projection stays the
// source of membership; this asserts the constants cover it exactly, so a kind
// added to the schema does not stay spelled as a literal forever.
func TestEveryTypedErrorKindHasANamedConstant(t *testing.T) {
	constants := map[string]FailureKind{
		"unknown_scope": KindUnknownScope, "ambiguous_scope": KindAmbiguousScope,
		"stale_context": KindStaleContext, "unauthorized": KindUnauthorized,
		"approval_required": KindApprovalRequired, "approval_invalid": KindApprovalInvalid,
		"version_conflict": KindVersionConflict, "idempotency_conflict": KindIdempotencyConflict,
		"operation_conflict": KindOperationConflict, "invalid_transition": KindInvalidTransition,
		"invalid_relation": KindInvalidRelation, "invariant_violation": KindInvariantViolation,
		"missing_evidence": KindMissingEvidence, "not_terminal": KindNotTerminal,
		"outcome_mismatch": KindOutcomeMismatch, "stale_requires_review": KindStaleRequiresReview,
		"stale_law_revision": KindStaleLawRevision, "domain_overlap": KindDomainOverlap,
		"degraded_not_allowed": KindDegradedNotAllowed, "unreachable": KindUnreachable,
		"invalid_cursor": KindInvalidCursor, "limit_exceeded": KindLimitExceeded,
		"budget_refused": KindBudgetRefused, "invalid_input": KindInvalidInput,
		"cancelled": KindCancelled, "timeout": KindTimeout,
		"transport_failure": KindTransportFailure, "malformed_response": KindMalformedResponse,
		"internal_error": KindInternalError,
	}
	for _, kind := range TypedErrorKinds() {
		constant, ok := constants[kind]
		if !ok {
			t.Errorf("schema kind %q has no named FailureKind constant", kind)
			continue
		}
		if string(constant) != kind {
			t.Errorf("constant for %q carries value %q", kind, constant)
		}
	}
	for kind := range constants {
		if !TypedErrorKindAllowed(kind) {
			t.Errorf("constant %q is not a member of the closed set", kind)
		}
	}
}

func TestUnknownKindIsRejected(t *testing.T) {
	if TypedErrorKindAllowed("not_a_declared_kind") {
		t.Fatal("an undeclared kind must be rejected")
	}
}
