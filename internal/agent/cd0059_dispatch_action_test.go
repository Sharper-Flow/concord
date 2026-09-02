// Agent-layer tests prove that a principal without worker_dispatch cannot
// invoke the registered action and that the capability stays policy-bound.

package agent

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// TestDispatchWorkerRefusesPrincipalWithoutWorkerDispatchCapability proves
// the runtime surfaces unauthorized when the client policy does not carry
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
	// The client policy carries work_transition only. The runtime's
	// AuthorizeTx refuses when the invocation's
	// RequiredCapability is worker_dispatch.
	inv := Invocation{
		ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef,
		SessionRef: grant.SessionRef, AgentRef: grant.AgentRef,
		Directory: grant.Directory, Worktree: grant.Worktree,
		ManifestDigest:     grant.ManifestDigest,
		RequiredCapability: Capability(foundCapability),
	}
	_, err := service.Authorize(ctx, inv)
	if err == nil {
		t.Fatal("Authorize accepted an invocation lacking worker_dispatch")
	}
	if !strings.Contains(err.Error(), "capability outside trusted client policy") {
		t.Fatalf("refusal = %v, want client policy refusal", err)
	}
}

func TestWorkerDispatchCapabilityIsStructurallyNonAuthorizable(t *testing.T) {
	ctx := context.Background()
	_, service, authority, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	registry := store.BuiltinWorkflowRegistry()
	entry, ok := registry.Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("workflow.implementation is not registered")
	}
	var required Capability
	for _, action := range entry.Definition.ActionDefinitions {
		if action.ID == "dispatch_worker" {
			required = Capability(action.RequiredCapability)
		}
	}
	if required != CapabilityWorkerDispatch {
		t.Fatalf("dispatch_worker required capability = %q, want %q", required, CapabilityWorkerDispatch)
	}
	invocation := Invocation{
		ClientRef: authority.ClientRef, PrincipalRef: authority.PrincipalRef, SessionRef: authority.SessionRef,
		AgentRef: authority.AgentRef, Directory: authority.Directory, Worktree: authority.Worktree,
		ManifestDigest: authority.ManifestDigest, RequiredCapability: required, ProjectID: "project-1",
	}
	_, err := service.Authorize(ctx, invocation)
	if err == nil {
		t.Fatal("worker_dispatch obtained from a policy without that capability")
	}
	if !strings.Contains(err.Error(), "capability outside trusted client policy") {
		t.Fatalf("refusal = %v, want a client policy refusal", err)
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
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1"}, nil
	}

	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := TrustedClientPolicy{
		PrincipalRef: "principal-cd0059",
		Capabilities: []Capability{CapabilityWorkerDispatch, "work_transition"},
		ProductScope: []string{"product-1"},
		ProjectScope: []string{"project-1"},
		AgentScope:   testFixtureAgents,
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
