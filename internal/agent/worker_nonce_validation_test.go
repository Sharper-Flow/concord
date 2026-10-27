package agent

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

type workerNonceValidationCase struct {
	name      string
	nonce     string
	wantValid bool
}

func workerNonceValidationCases() []workerNonceValidationCase {
	ascii := func(count int) string { return strings.Repeat("n", count) }
	twoByte := func(count int) string { return strings.Repeat("é", count) }
	fourByte := func(count int) string { return strings.Repeat("😀", count) }
	return []workerNonceValidationCase{
		{name: "ascii-15-refused", nonce: ascii(15), wantValid: false},
		{name: "ascii-16-authenticates", nonce: ascii(16), wantValid: true},
		{name: "ascii-256-authenticates", nonce: ascii(256), wantValid: true},
		{name: "ascii-257-refused", nonce: ascii(257), wantValid: false},
		{name: "eight-two-byte-characters-refused", nonce: twoByte(8), wantValid: false},
		{name: "two-byte-16-code-points-authenticates", nonce: twoByte(16), wantValid: true},
		{name: "two-byte-256-code-points-authenticates", nonce: twoByte(256), wantValid: true},
		{name: "two-byte-257-code-points-refused", nonce: twoByte(257), wantValid: false},
		{name: "four-byte-256-code-points-authenticates", nonce: fourByte(256), wantValid: true},
		{name: "four-byte-257-code-points-refused", nonce: fourByte(257), wantValid: false},
		{name: "nul-before-16-refused", nonce: "\x00" + ascii(15), wantValid: false},
		{name: "nul-after-valid-16-character-prefix-refused", nonce: ascii(16) + "\x00", wantValid: false},
		{name: "invalid-utf8-after-valid-ascii-prefix-refused", nonce: ascii(16) + "\xff", wantValid: false},
	}
}

func TestWorkerEvidenceNonceValidationUsesTheStoreCodePointUnit(t *testing.T) {
	t.Parallel()
	for _, testCase := range workerNonceValidationCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			service, control, key := workerControlProofFixture(t, []agentcontract.Capability{CapabilityWorkerEvidence})
			env := workerEvidenceV2Envelope(t, "nonce-unit-placeholder")
			env.ClientRef = control.ClientRef
			var assertion WorkerEvidenceAssertion
			decodeWorkerEvidenceEnvelope(t, env, &assertion)
			// Set raw bytes after JSON decoding so invalid UTF-8 is not repaired.
			assertion.Nonce = testCase.nonce
			assertion.Signature = ed25519.Sign(key, CanonicalWorkerEvidenceAssertion(assertion))
			binding := decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, ""))
			principal, err := validateWorkerEvidenceNonce(t, service, assertion, binding)
			if !testCase.wantValid {
				const malformed = "worker evidence assertion is malformed"
				if err == nil || err.Error() != malformed {
					t.Fatalf("refusal = %v, want exactly %q", err, malformed)
				}
				if principal != "" {
					t.Fatalf("refused principal = %q, want empty", principal)
				}
				var failure *store.Failure
				if errors.As(err, &failure) {
					t.Fatalf("refusal = %+v, want the plain validation error, not a store failure", failure)
				}
				if got := countWorkerEvidenceNonceRows(t, service, testCase.nonce); got != 0 {
					t.Fatalf("refused nonce rows = %d, want none recorded", got)
				}
				return
			}
			if err != nil || principal != "human-1" {
				t.Fatalf("accepted principal = %q, error = %v", principal, err)
			}
			if got := countWorkerEvidenceNonceRows(t, service, testCase.nonce); got != 1 {
				t.Fatalf("accepted nonce rows = %d, want one recorded row", got)
			}
		})
	}
}

func TestWorkerControlNonceValidationUsesTheStoreCodePointUnit(t *testing.T) {
	t.Parallel()
	for _, testCase := range workerNonceValidationCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			service, assertion, key := workerControlProofFixture(t, []agentcontract.Capability{"work_transition"})
			assertion.Nonce = testCase.nonce
			assertion.Signature = ed25519.Sign(key, CanonicalWorkerControlAssertion(assertion))
			authority, err := checkWorkerControlProof(t, service, assertion)
			if !testCase.wantValid {
				want := authorityRefusal("worker control assertion is malformed").Error()
				if err == nil || err.Error() != want {
					t.Fatalf("refusal = %v, want exactly %q", err, want)
				}
				var failure *store.Failure
				if errors.As(err, &failure) {
					t.Fatalf("refusal = %+v, want the typed authority refusal, not a store failure", failure)
				}
				return
			}
			if err != nil || authority.AgentRef != assertion.AgentRef || authority.SessionRef != assertion.SessionRef || authority.PrincipalRef != "human-1" {
				t.Fatalf("accepted authority = %+v, error = %v", authority, err)
			}
		})
	}
}
