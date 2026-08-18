package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// trunkFirewallFixture returns a service whose resolver reports the given
// worktree topology, plus a signing key for grant requests.
func trunkFirewallFixture(t *testing.T, mainWorktree bool) (*Service, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "trunk-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Trunk Fixture","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "trunk-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Trunk Project"}`)},
		{EventID: "trunk-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"trunk firewall fixture","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(s)
	service.Now = func() time.Time { return fixedTime() }
	service.ProjectResolver = func(context.Context, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1", MainWorktree: mainWorktree}, nil
	}
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read", "work_define", "work_transition", "cross_scope"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	return service, privateKey
}

func signedTrunkGrant(privateKey ed25519.PrivateKey, nonce string, capabilities []Capability) GrantRequest {
	req := grantRequest(privateKey, nonce)
	req.Assertion.RequestedCapabilities = capabilities
	req.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(req.Assertion))
	return req
}

// CD-0008 D1: the main checkout is read-only. Every mutating capability,
// including mutation enablers like cross_scope, is refused there.
func TestIssueGrantRefusesMutationOnMainWorktree(t *testing.T) {
	mutating := [][]Capability{
		{"work_define"},
		{"product_read", "work_transition"},
		{"cross_scope"},
	}
	for _, capabilities := range mutating {
		service, privateKey := trunkFirewallFixture(t, true)
		req := signedTrunkGrant(privateKey, "trunk-firewall-"+strings.Join(capabilityStrings(capabilities), "-"), capabilities)
		_, err := service.IssueGrant(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "linked worktree") {
			t.Fatalf("capabilities=%v expected main-worktree refusal, got err=%v", capabilities, err)
		}
	}
}

func TestIssueGrantAllowsReadsOnMainWorktreeAndMutationOnLinked(t *testing.T) {
	service, privateKey := trunkFirewallFixture(t, true)
	grant, err := service.IssueGrant(context.Background(), signedTrunkGrant(privateKey, "trunk-read-only-nonce-0001", []Capability{"product_read"}))
	if err != nil {
		t.Fatalf("read-only grant on main worktree should pass, got %v", err)
	}
	if len(grant.Capabilities) != 1 || grant.Capabilities[0] != Capability("product_read") {
		t.Fatalf("grant capabilities=%v", grant.Capabilities)
	}

	linked, linkedKey := trunkFirewallFixture(t, false)
	if _, err := linked.IssueGrant(context.Background(), signedTrunkGrant(linkedKey, "linked-mutation-nonce-00001", []Capability{"work_define"})); err != nil {
		t.Fatalf("mutating grant on linked worktree should pass, got %v", err)
	}
}
