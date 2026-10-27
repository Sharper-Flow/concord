package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkerControlProofSeparatesClientRefusalsFromStoreFaults(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"active", "missing client", "inactive key", "bad signature", "read fault"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			service, assertion, key := workerControlProofFixture(t, []agentcontract.Capability{"work_transition"})
			ctx := context.Background()
			switch scenario {
			case "missing client":
				assertion.ClientRef = "unregistered-client"
				assertion.Signature = ed25519.Sign(key, CanonicalWorkerControlAssertion(assertion))
			case "inactive key":
				revokeSoleClientKey(t, service.Store)
			case "bad signature":
				assertion.Signature[0] ^= 1
			case "read fault":
				ctx = cancelledContext()
			}
			var authority Authority
			var verifyErr error
			// BEGIN succeeds; only the verifier's query observes cancellation.
			err := service.Store.Transact(context.Background(), func(tx *store.Transaction) error {
				authority, verifyErr = service.verifyWorkerControlAssertionTx(ctx, tx, probedHost(), assertion)
				return verifyErr
			})
			if err != verifyErr {
				t.Fatalf("transaction returned %v, want verifier error %v", err, verifyErr)
			}
			switch scenario {
			case "active":
				if err != nil || authority.ClientRef != "client-1" || authority.PrincipalRef != "human-1" {
					t.Fatalf("active authority = %+v, error = %v", authority, err)
				}
			case "read fault":
				assertUnavailableClientReadFault(t, verifyErr)
			default:
				const message = "worker control assertion signature or client invalid"
				if err == nil || err.Error() != message {
					t.Fatalf("refusal = %v, want %q", err, message)
				}
				out := failureEnvelope(NewBase("worker-control-refusal", "concord_work_transition", "worktree_verify"), err)
				if out.Error == nil || out.Error.Kind != "unauthorized" || out.Error.RetrySafe || out.Error.RecoveryAction.Kind != "contact_operator" {
					t.Fatalf("refusal envelope = %+v, want non-retryable unauthorized", out.Error)
				}
			}
		})
	}
}

func TestWorkerEvidenceAssertionSeparatesClientRefusalsFromStoreFaults(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"active", "missing client", "inactive key", "bad signature", "read fault"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			service, control, key := workerControlProofFixture(t, []agentcontract.Capability{CapabilityWorkerEvidence})
			env := workerEvidenceV2Envelope(t, "worker-evidence-read-nonce")
			env.ClientRef = control.ClientRef
			var assertion WorkerEvidenceAssertion
			decodeWorkerEvidenceEnvelope(t, env, &assertion)
			binding := decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, ""))
			assertion.Signature = ed25519.Sign(key, CanonicalWorkerEvidenceAssertion(assertion))
			ctx := context.Background()
			switch scenario {
			case "missing client":
				assertion.ClientRef = "unregistered-client"
				assertion.Signature = ed25519.Sign(key, CanonicalWorkerEvidenceAssertion(assertion))
			case "inactive key":
				revokeSoleClientKey(t, service.Store)
			case "bad signature":
				assertion.Signature[0] ^= 1
			case "read fault":
				ctx = cancelledContext()
			}
			var principal string
			var verifyErr error
			err := service.Store.Transact(context.Background(), func(tx *store.Transaction) error {
				principal, verifyErr = service.ValidateWorkerEvidenceAssertionTx(ctx, tx, assertion, binding)
				return verifyErr
			})
			if err != verifyErr {
				t.Fatalf("transaction returned %v, want verifier error %v", err, verifyErr)
			}
			switch scenario {
			case "active":
				if err != nil || principal != "human-1" {
					t.Fatalf("active principal = %q, error = %v", principal, err)
				}
			case "read fault":
				assertUnavailableClientReadFault(t, verifyErr)
			default:
				message := "trusted client unavailable"
				if scenario == "bad signature" {
					message = "worker evidence assertion signature invalid"
				}
				if err == nil || err.Error() != message {
					t.Fatalf("refusal = %v, want %q", err, message)
				}
				var failure *store.Failure
				if errors.As(err, &failure) {
					t.Fatalf("refusal = %+v, want the existing non-store refusal", failure)
				}
			}
		})
	}
}
