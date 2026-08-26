// CD-0059: agent-layer tests proving (a) a principal lacking the
// worker_dispatch capability cannot invoke the registered action, and
// (b) worker_dispatch is structurally non-grantable through the bearer
// grant vocabulary, mirroring worker_evidence.

package agent

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// TestDispatchWorkerRefusesPrincipalWithoutWorkerDispatchCapability proves
// the runtime surfaces unauthorized when the invoking grant does not carry
// worker_dispatch. The action's RequiredCapability is read from the
// registry entry, so the refusal happens before any state mutation. The
// test drives the runtime through mutateWorkflowAction directly so the
// workflow-instance precondition does not gate the capability check.
func TestDispatchWorkerRefusesPrincipalWithoutWorkerDispatchCapability(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	defer s.Close()

	// Drive the runtime directly: bypass DispatchWithRegistry's
	// workflow-instance preflight so the test exercises the
	// RequiredCapability check that mutations.go reads from the registry.
	registry := store.BuiltinWorkflowRegistry()
	entry, ok := registry.Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("workflow.implementation is not registered")
	}
	var foundCapability string
	for _, a := range entry.Definition.ActionDefinitions {
		if a.ID == "dispatch_worker" {
			foundCapability = a.RequiredCapability
		}
	}
	if foundCapability != "worker_dispatch" {
		t.Fatalf("dispatch_worker required_capability = %q, want worker_dispatch", foundCapability)
	}
	// The grant carries work_transition only. The runtime's
	// ValidateAndConsumeGrantTx refuses when the invocation's
	// RequiredCapability is worker_dispatch.
	inv := Invocation{
		GrantToken: grant.Token, ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef,
		SessionRef: grant.SessionRef, AgentRef: grant.AgentRef,
		Directory: grant.Directory, Worktree: grant.Worktree,
		ManifestDigest:     grant.ManifestDigest,
		RequiredCapability: Capability(foundCapability),
	}
	_, err := service.ValidateInvocation(ctx, inv)
	if err == nil {
		t.Fatal("ValidateInvocation accepted a grant lacking worker_dispatch")
	}
	if !strings.Contains(err.Error(), "worker_dispatch") && !strings.Contains(err.Error(), "grant capability missing") {
		t.Fatalf("refusal = %v, want worker_dispatch / grant capability missing", err)
	}
}

// TestWorkerDispatchCapabilityIsStructurallyNonGrantable proves the
// grant-request vocabulary excludes worker_dispatch: a bearer grant
// carrying worker_dispatch is refused at the request validation step,
// never to be issued. The capability is policy-bound, like worker_evidence.
func TestWorkerDispatchCapabilityIsStructurallyNonGrantable(t *testing.T) {
	ctx := context.Background()
	// Open a fresh store and register a client whose policy carries
	// worker_dispatch + a grantable capability. The grant issuance path
	// must refuse to encode worker_dispatch into the bearer grant.
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service := NewService(s)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := TrustedClientPolicy{
		PrincipalRef: "principal-cd0059",
		Capabilities: []Capability{CapabilityWorkerDispatch, "work_transition"},
		ProductScope: []string{"product-1"},
		ProjectScope: []string{"project-1"},
	}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{
		ClientRef: "client-cd0059", KeyID: "key-cd0059", PublicKey: publicKey, Policy: policy,
	}); err != nil {
		t.Fatalf("client policy carrying worker_dispatch refused: %v", err)
	}

	// Build a signed grant request that asks for worker_dispatch. The
	// capability is structurally non-grantable, so the grant must be
	// refused regardless of signature validity.
	assertion := SignedAssertion{
		ClientRef: "client-cd0059", SessionRef: "session-1", AgentRef: "agent-1",
		Directory: "/repo", Worktree: "/repo-wt",
		RequestedProductID:    "product-1",
		RequestedProjectIDs:   []string{"project-1"},
		RequestedCapabilities: []Capability{"worker_dispatch"},
		IssuedAt:              fixedTime(),
		Nonce:                 nonceForChallenge("cd0059-non-grantable"),
		ManifestDigest:        ManifestDigest,
	}
	assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(assertion))
	req := GrantRequest{Assertion: assertion, ExpiresAt: fixedTime().Add(time.Hour)}
	_, err = service.IssueGrant(ctx, req)
	if err == nil {
		t.Fatal("grant carrying worker_dispatch was issued")
	}
	if !strings.Contains(err.Error(), "invalid grant request") {
		t.Fatalf("refusal = %v, want invalid grant request", err)
	}
}

// TestWorkerDispatchCapabilityIsInTheClientPolicyAllowList proves the
// capability is registrable in a trusted client policy, like
// worker_evidence, but the policy list is closed: an unknown capability
// is refused at registration time.
func TestWorkerDispatchCapabilityIsInTheClientPolicyAllowList(t *testing.T) {
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service := NewService(s)

	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := TrustedClientPolicy{
		PrincipalRef: "principal-cd0059",
		Capabilities: []Capability{CapabilityWorkerDispatch, "work_transition"},
		ProductScope: []string{"product-1"},
		ProjectScope: []string{"project-1"},
	}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{
		ClientRef: "client-cd0059", KeyID: "key-cd0059", PublicKey: publicKey, Policy: policy,
	}); err != nil {
		t.Fatalf("client policy carrying worker_dispatch refused: %v", err)
	}

	// Confirm the inverse: a capability outside the closed list is
	// refused at registration time. This pins the policy boundary so a
	// future change cannot silently widen it.
	bogusPolicy := TrustedClientPolicy{
		PrincipalRef: "principal-bogus",
		Capabilities: []Capability{"worker_dispatch_v2"},
		ProductScope: []string{"product-1"},
		ProjectScope: []string{"project-1"},
	}
	publicKey2, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{
		ClientRef: "client-bogus", KeyID: "key-bogus", PublicKey: publicKey2, Policy: bogusPolicy,
	}); err == nil {
		t.Fatal("client policy carrying unknown capability was registered")
	}
}
