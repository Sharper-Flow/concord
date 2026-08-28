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
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func trunkFirewallFixture(t *testing.T, mainWorktree bool) *Service {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
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
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1", MainWorktree: mainWorktree}, nil
	}
	publicKey, _, _ := ed25519.GenerateKey(cryptorand.Reader)
	policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read", "work_define", "work_transition", "cross_scope"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	return service
}

// CD-0008 D1: the main checkout is read-only. Every mutating capability,
// including mutation enablers like cross_scope, is refused there.
func TestAuthorizeRefusesMutationOnMainWorktree(t *testing.T) {
	mutating := []Capability{"work_define", "work_transition", "cross_scope"}
	for _, capability := range mutating {
		service := trunkFirewallFixture(t, true)
		_, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: capability, ProductID: "product-1", ProjectID: "project-1"})
		if err == nil || !strings.Contains(err.Error(), "linked worktree") {
			t.Fatalf("capability=%v expected main-worktree refusal, got err=%v", capability, err)
		}
	}
}

func TestAuthorizeAllowsReadsOnMainWorktreeAndMutationOnLinked(t *testing.T) {
	service := trunkFirewallFixture(t, true)
	invocation := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"}
	authority, err := service.Authorize(context.Background(), invocation)
	if err != nil {
		t.Fatalf("read-only authority on main worktree should pass, got %v", err)
	}
	if !containsCapability(authority.Capabilities, "product_read") {
		t.Fatalf("authority capabilities=%v", authority.Capabilities)
	}

	linked := trunkFirewallFixture(t, false)
	invocation.RequiredCapability = "work_define"
	if _, err := linked.Authorize(context.Background(), invocation); err != nil {
		t.Fatalf("mutating authority on linked worktree should pass, got %v", err)
	}
}
