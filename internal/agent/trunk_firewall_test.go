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

// CD-0008 D1 (amended by CD-0092 D2): the main checkout refuses every
// implementation-bearing capability, including mutation enablers like
// cross_scope, until a linked worktree claims it. work_define is allowed
// because its writes land in the store, not a checkout path.
func TestAuthorizeRefusesMutationOnMainWorktree(t *testing.T) {
	mutating := []Capability{"work_transition", "work_relate", "work_compact", "work_initiative", "cross_scope"}
	for _, capability := range mutating {
		service := trunkFirewallFixture(t, true)
		_, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: capability, ProductID: "product-1", ProjectID: "project-1"})
		if err == nil || !strings.Contains(err.Error(), "linked worktree") {
			t.Fatalf("capability=%v expected main-worktree refusal, got err=%v", capability, err)
		}
	}
}

// CD-0092 D2/D3: work_define is a Product-state-only capability and resolves
// from the main checkout. Each entry in the allowlist must grant there.
func TestAuthorizeAllowsWorkDefineOnMainWorktree(t *testing.T) {
	for _, capability := range []Capability{"product_read", "work_define"} {
		service := trunkFirewallFixture(t, true)
		invocation := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: capability, ProductID: "product-1", ProjectID: "project-1"}
		authority, err := service.Authorize(context.Background(), invocation)
		if err != nil {
			t.Fatalf("capability=%v expected grant on main checkout, got err=%v", capability, err)
		}
		if !containsCapability(authority.Capabilities, capability) {
			t.Fatalf("capability=%v authority capabilities=%v", capability, authority.Capabilities)
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

func TestLifecycleTransitionAllowsMainCheckout(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "lifecycle",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"in_progress","reason":"start work","idempotency_key":"main-lifecycle"}`),
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("main-checkout lifecycle response=%+v err=%v", response, err)
	}
	if lifecycle := workLifecycle(t, s, "work-1"); lifecycle != "in_progress" {
		t.Fatalf("work lifecycle=%q, want in_progress", lifecycle)
	}
}

func TestMainCheckoutLifecyclePreservesTerminalGates(t *testing.T) {
	t.Run("missing evidence", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
		service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
			return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
		}
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(ctx, s, service, InvokeRequest{
			Tool:      "concord_work_transition",
			Operation: "lifecycle",
			Input:     json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"completed","reason":"finish work","idempotency_key":"main-terminal-no-evidence"}`),
		}, mutationEnvelope(grant, scopeVersion))
		if err != nil || response.Outcome != OutcomeError || response.Error == nil {
			t.Fatalf("terminal response=%+v err=%v", response, err)
		}
		if response.Error.Kind != "missing_evidence" {
			t.Fatalf("terminal error.kind=%q, want missing_evidence", response.Error.Kind)
		}
	})

	t.Run("approved evidence", func(t *testing.T) {
		ctx := context.Background()
		s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
		service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
			return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
		}
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		env := mutationEnvelope(grant, scopeVersion)
		request := InvokeRequest{
			Tool:      "concord_work_transition",
			Operation: "lifecycle",
			Input:     json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"completed","reason":"finish work","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"main-checkout-verification"}],"idempotency_key":"main-terminal-approved"}`),
		}
		challenge, err := Dispatch(ctx, s, service, request, env)
		if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
			t.Fatalf("challenge response=%+v err=%v", challenge, err)
		}
		approvalRef, ok := challenge.Error.Details["approval_ref"].(string)
		if !ok || len(approvalRef) != 64 {
			t.Fatalf("approval_ref=%v", challenge.Error.Details["approval_ref"])
		}
		digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
		scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
		versions := map[string]any{"work": int64(2)}
		env.HostApproval = signedHostApproval(privateKey, approvalRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", fixedTime(), "main-terminal-approval")
		request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"completed","reason":"finish work","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"main-checkout-verification"}],"idempotency_key":"main-terminal-approved","approval":{"approval_ref":"` + approvalRef + `"}}`)
		approved, err := Dispatch(ctx, s, service, request, env)
		if err != nil || approved.Outcome != OutcomeOK {
			t.Fatalf("approved response=%+v err=%v", approved, err)
		}
		if lifecycle := workLifecycle(t, s, "work-1"); lifecycle != "completed" {
			t.Fatalf("work lifecycle=%q, want completed", lifecycle)
		}
	})
}

func TestMainCheckoutRefusesImplementationOperations(t *testing.T) {
	cases := []struct {
		operation string
		input     string
	}{
		{operation: "worktree_claim", input: `{"work_id":"work-1","project_id":"project-1","branch":"work/main-boundary","base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"/tmp/main-boundary","expected_version":2,"idempotency_key":"main-claim"}`},
		{operation: "worktree_reclaim", input: `{"work_id":"work-1","project_id":"project-1","default_ref":"main","expected_version":2,"idempotency_key":"main-reclaim"}`},
		{operation: "workflow_action", input: `{"work_id":"work-1","expected_version":2,"action_id":"approve_contract","fields":[],"idempotency_key":"main-action"}`},
	}
	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
			service.ProjectResolver = func(context.Context, *store.Transaction, string, string) (store.ProjectResolution, error) {
				return store.ProjectResolution{ProjectID: "project-1", MainWorktree: true}, nil
			}
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: tc.operation, Input: json.RawMessage(tc.input)}, mutationEnvelope(grant, scopeVersion))
			if err != nil || response.Outcome != OutcomeError || response.Error == nil {
				t.Fatalf("operation=%s response=%+v err=%v", tc.operation, response, err)
			}
			if response.Error.Kind != "unauthorized" {
				t.Fatalf("operation=%s error.kind=%q, want unauthorized", tc.operation, response.Error.Kind)
			}
			if !strings.Contains(response.Error.Message, "CD-0092 D2") {
				t.Fatalf("operation=%s error.message=%q, want CD-0092 D2 refusal", tc.operation, response.Error.Message)
			}
		})
	}
}

func TestMainCheckoutAllowlistDeclaresBothSides(t *testing.T) {
	for _, capability := range []Capability{"product_read", "work_define"} {
		if _, ok := mainCheckoutAllowedCapabilities[capability]; !ok {
			t.Fatalf("capability %q is missing from the main-checkout allowlist", capability)
		}
	}
	if len(mainCheckoutAllowedCapabilities) != 2 {
		t.Fatalf("main-checkout capability allowlist=%v, want exactly two entries", mainCheckoutAllowedCapabilities)
	}
	operations, ok := mainCheckoutAllowedOperations[Capability("work_transition")]
	if !ok {
		t.Fatal("work_transition operation allowlist is missing")
	}
	if _, ok := operations["lifecycle"]; !ok || len(operations) != 1 {
		t.Fatalf("work_transition operation allowlist=%v, want lifecycle only", operations)
	}
}
