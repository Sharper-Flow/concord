package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// The grant-request route is the one place tool arguments may ask for added
// capabilities, so every refusal the mandate names — denial, expiry, altered
// arguments, stale policy, replay — must leave the stored policy byte for
// byte unchanged, and the one approved path must apply the exact derived
// additions and nothing else. These tests dispatch through the runtime the
// adapter uses, so no shell or MCP side door is exercised.

type storedClientPolicy struct {
	PrincipalRef string
	Capabilities []string
	Products     []string
	Projects     []string
	Agents       []string
}

func readStoredPolicy(t *testing.T, s *store.Store, clientRef string) storedClientPolicy {
	t.Helper()
	client, _, err := s.TrustedClientWithKey(context.Background(), clientRef)
	if err != nil {
		t.Fatal(err)
	}
	stored := storedClientPolicy{PrincipalRef: client.PrincipalRef}
	mustDecode := func(raw string, into *[]string) {
		t.Helper()
		if err := json.Unmarshal([]byte(raw), into); err != nil {
			t.Fatal(err)
		}
	}
	mustDecode(client.CapabilitiesJSON, &stored.Capabilities)
	mustDecode(client.ProductScopeJSON, &stored.Products)
	mustDecode(client.ProjectScopeJSON, &stored.Projects)
	mustDecode(client.AgentScopeJSON, &stored.Agents)
	return stored
}

func grantRequestEnvelope(t *testing.T, s *store.Store, grant Authority) CallEnvelope {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return mutationEnvelope(grant, scopeVersion)
}

func grantRequestInput(approvalRef string) string {
	input := `{"capabilities":["cross_scope"],"product_scope":["product-2"],"project_scope":[],"agent_scope":[],"reason":"dependent work claims a cross-Product worktree","idempotency_key":"grant-request-1"`
	if approvalRef != "" {
		input += `,"approval":{"approval_ref":"` + approvalRef + `"}`
	}
	return input + "}"
}

func dispatchGrantRequest(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input string) Envelope {
	t.Helper()
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "client_policy_grant_request", Input: json.RawMessage(input)}, env)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestGrantRequestMintsExactOperatorChallenge(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	before := readStoredPolicy(t, s, "client-1")
	env := grantRequestEnvelope(t, s, grant)
	response := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	if response.Error == nil || response.Error.Kind != "approval_required" {
		t.Fatalf("challenge response = %+v", response)
	}
	summary := response.Error.ConsequenceSummary
	if summary == nil || summary.Tool != "concord_work_relate" || summary.Operation != "client_policy_grant_request" || summary.Consequence != "scope" || summary.OperationDigest == "" || summary.ExpiresAt == "" {
		t.Fatalf("consequence summary = %+v", summary)
	}
	if len(summary.Scope) == 0 {
		t.Fatal("consequence summary binds no scope")
	}
	details := response.Error.Details
	if details["client_ref"] != "client-1" {
		t.Fatalf("challenge names %v, want the calling client", details["client_ref"])
	}
	wantScope := approvalScopeBindings(map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}})
	if !reflect.DeepEqual(summary.Scope, wantScope) {
		t.Fatalf("challenge bindings = %v, want %v", summary.Scope, wantScope)
	}
	for _, field := range []string{"added_capabilities", "added_product_scope", "added_project_scope", "added_agent_scope"} {
		if _, duplicated := details[field]; duplicated {
			t.Fatalf("approval binding %q has a second wire owner in details", field)
		}
	}
	client, _, err := s.TrustedClientWithKey(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if details["policy_version"] != TrustedClientPolicyVersion(client) {
		t.Fatal("challenge policy version does not match the stored policy")
	}
	after := readStoredPolicy(t, s, "client-1")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("challenge changed the stored policy: %+v -> %+v", before, after)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 1 {
		t.Fatal("challenge was not recorded")
	}
}

func TestApprovedGrantRequestAppliesExactUnion(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("challenge response = %+v", challenge)
	}
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "grant-approval")
	approved := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved response = %+v", approved)
	}
	var result struct {
		ClientRef     string   `json:"client_ref"`
		PolicyVersion string   `json:"policy_version"`
		AddedCaps     []string `json:"added_capabilities"`
		AddedProducts []string `json:"added_product_scope"`
		AddedProjects []string `json:"added_project_scope"`
		AddedAgents   []string `json:"added_agent_scope"`
	}
	if err := json.Unmarshal(approved.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ClientRef != "client-1" || !reflect.DeepEqual(result.AddedCaps, []string{"cross_scope"}) || !reflect.DeepEqual(result.AddedProducts, []string{"product-2"}) || len(result.AddedProjects) != 0 || len(result.AddedAgents) != 0 {
		t.Fatalf("applied result = %+v", result)
	}
	stored := readStoredPolicy(t, s, "client-1")
	if stored.PrincipalRef != "human-1" {
		t.Fatalf("expansion changed the stored principal to %q", stored.PrincipalRef)
	}
	// Every prior grant survives, and the union holds exactly the requested
	// additions — the grants a dependent cross-Product worktree claim needs.
	for _, capability := range []string{"work_relate", "cross_scope"} {
		if !contains(stored.Capabilities, capability) {
			t.Fatalf("stored capabilities %v lack %q", stored.Capabilities, capability)
		}
	}
	if !contains(stored.Products, "product-1") || !contains(stored.Products, "product-2") || len(stored.Products) != 2 {
		t.Fatalf("stored Product scope = %v", stored.Products)
	}
	if !contains(stored.Projects, "project-1") || !contains(stored.Agents, "agent-1") {
		t.Fatalf("stored scopes = %+v", stored)
	}
	client, _, err := s.TrustedClientWithKey(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.PolicyVersion != TrustedClientPolicyVersion(client) {
		t.Fatal("result policy version does not match the stored policy")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals WHERE used_count=1`) != 1 {
		t.Fatal("the operator approval was not consumed exactly once")
	}
}

func TestAlteredArgumentsRefuseAndLeaveAuthorityUnchanged(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "altered-approval")
	// The approval was asserted for the challenged arguments; the second call
	// widens the additions, so the digest no longer matches the assertion.
	altered := `{"capabilities":["cross_scope","work_transition"],"product_scope":["product-2"],"project_scope":[],"agent_scope":[],"reason":"dependent work claims a cross-Product worktree","idempotency_key":"grant-request-1","approval":{"approval_ref":"` + ref + `"}}`
	response := dispatchGrantRequest(t, s, service, env, altered)
	if response.Error == nil || response.Error.Kind != "approval_invalid" {
		t.Fatalf("altered response = %+v", response)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges WHERE status='active'`) != 1 {
		t.Fatal("the refused attempt consumed the challenge")
	}
	stored := readStoredPolicy(t, s, "client-1")
	if contains(stored.Capabilities, "work_transition") || contains(stored.Capabilities, "cross_scope") {
		t.Fatalf("refusal widened the stored policy: %+v", stored)
	}
}

// assertStalePolicyVersionConflict pins the typed stale-policy refusal: the
// route answers version_conflict, the envelope validates against the
// generated envelope schema, and current_versions names the calling client at
// its live policy version — the value a fresh approval request must bind.
func assertStalePolicyVersionConflict(t *testing.T, s *store.Store, response Envelope) {
	t.Helper()
	if response.Error == nil || response.Error.Kind != "version_conflict" {
		t.Fatalf("stale response kind = %v, want version_conflict (envelope: %+v)", response.Error.Kind, *response.Error)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("version_conflict envelope fails the generated envelope schema: %v", err)
	}
	if len(response.Error.CurrentVersions) != 1 {
		t.Fatalf("current_versions len=%d, want one trusted_client carrier", len(response.Error.CurrentVersions))
	}
	current := response.Error.CurrentVersions[0]
	if current.EntityKind != "trusted_client" || current.ID != "client-1" {
		t.Fatalf("current_versions[0]=%+v, want trusted_client/client-1", current)
	}
	live, _, err := s.TrustedClientWithKey(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != TrustedClientPolicyVersion(live) {
		t.Fatalf("current_versions carries %q, want the live policy version %q", current.Version, TrustedClientPolicyVersion(live))
	}
}

func TestStalePolicyRefusesApproval(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	// The operator widens the policy between the challenge and the approval.
	// The expansion goes through the untouched operator route, so the stored
	// version the challenge bound no longer exists.
	if err := service.ExpandTrustedClientPolicy(context.Background(), "client-1", TrustedClientPolicy{AgentScope: []string{"agent-extra"}}); err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "stale-approval")
	response := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	assertStalePolicyVersionConflict(t, s, response)
	stored := readStoredPolicy(t, s, "client-1")
	if contains(stored.Capabilities, "cross_scope") || contains(stored.Products, "product-2") {
		t.Fatalf("stale approval widened the stored policy: %+v", stored)
	}
	if !contains(stored.Agents, "agent-extra") {
		t.Fatalf("operator expansion was lost: %+v", stored)
	}
}

func TestExpiredChallengeRefusesAndLeavesAuthorityUnchanged(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	// Advance the clock past the challenge expiry, then assert the approval
	// against the timestamp the expired challenge cannot satisfy.
	later := fixedTime().Add(11 * time.Minute)
	service.Now = func() time.Time { return later }
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, later, "expired-approval")
	response := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	if response.Error == nil || response.Error.Kind != "approval_invalid" {
		t.Fatalf("expired response = %+v", response)
	}
	stored := readStoredPolicy(t, s, "client-1")
	if contains(stored.Capabilities, "cross_scope") || contains(stored.Products, "product-2") {
		t.Fatalf("expired approval widened the stored policy: %+v", stored)
	}
}

func TestApprovedGrantRequestReplaysWithoutReapplying(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "replay-approval")
	approved := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved response = %+v", approved)
	}
	version := readStoredPolicy(t, s, "client-1")
	replayed := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	if replayed.Outcome != OutcomeOK || !replayed.Replayed {
		t.Fatalf("replayed response = %+v", replayed)
	}
	if !reflect.DeepEqual(version, readStoredPolicy(t, s, "client-1")) {
		t.Fatal("replay changed the stored policy")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals WHERE used_count=1`) != 1 {
		t.Fatal("replay consumed the approval a second time")
	}
}

func TestGrantRequestRefusesBearerForbiddenCapabilities(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	input := `{"capabilities":["worker_dispatch"],"product_scope":[],"project_scope":[],"agent_scope":[],"reason":"dispatch a nested worker","idempotency_key":"grant-request-worker"}`
	_, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "client_policy_grant_request", Input: json.RawMessage(input)}, env)
	// The published schema carries the closed bearer-safe vocabulary, so the
	// core's own payload validation refuses before any derivation runs.
	if err == nil || !strings.Contains(err.Error(), "enum mismatch") {
		t.Fatalf("worker capability dispatch = %v, want the schema vocabulary refusal", err)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
		t.Fatal("a refused request minted a challenge")
	}
	stored := readStoredPolicy(t, s, "client-1")
	if contains(stored.Capabilities, "worker_dispatch") {
		t.Fatalf("refusal widened the stored policy: %+v", stored)
	}
	// The derivation carries the same closed vocabulary, so a request that
	// reaches the handler from a stale published schema still refuses typed
	// and names the operator route (CD-0044, CD-0071 D1).
	_, deriveErr := DeriveTrustedClientPolicyExpansion(store.TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "human-1", CapabilitiesJSON: `["work_relate"]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `[]`}, TrustedClientPolicy{Capabilities: []Capability{"worker_dispatch"}})
	if deriveErr == nil {
		t.Fatal("derivation accepted a bearer-forbidden capability")
	}
	if !strings.Contains(deriveErr.Error(), "client-policy-expand") {
		t.Fatalf("derivation refusal names no operator route: %q", deriveErr.Error())
	}
}

func TestGrantRequestTargetsOnlyTheCallingClient(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	// A second registered client stays untouched by client-1's request: the
	// input schema carries no client field, so the caller cannot name one.
	otherKey, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterTrustedClient(context.Background(), ClientRegistration{ClientRef: "client-2", KeyID: "key-client-2", PublicKey: otherKey, Policy: TrustedClientPolicy{PrincipalRef: "human-2", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}, AgentScope: testFixtureAgents}}); err != nil {
		t.Fatal(err)
	}
	before := readStoredPolicy(t, s, "client-2")
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	if challenge.Error == nil || challenge.Error.Details["client_ref"] != "client-1" {
		t.Fatalf("challenge target = %+v", challenge.Error)
	}
	if !reflect.DeepEqual(before, readStoredPolicy(t, s, "client-2")) {
		t.Fatal("the challenge touched a client other than the caller")
	}
}

func TestGrantRequestWithoutNewGrantsRefuses(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	input := `{"capabilities":["work_relate"],"product_scope":["product-1"],"project_scope":[],"agent_scope":[],"reason":"everything is already granted","idempotency_key":"grant-request-empty"}`
	response := dispatchGrantRequest(t, s, service, env, input)
	if response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("empty-diff response = %+v", response)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
		t.Fatal("a no-op request minted a challenge")
	}
}

func TestRestoredPolicyRefusesApprovalMintedBeforeRestore(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	// The operator changes the policy and then restores its exact prior
	// content while the challenge is still valid. A content-only policy
	// version would match the minted challenge again; the policy revision
	// the store bumps on every write keeps the restored policy a different
	// version, so the approval minted before the round trip refuses.
	original, _, err := s.TrustedClientWithKey(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ExpandTrustedClientPolicy(context.Background(), "client-1", TrustedClientPolicy{AgentScope: []string{"agent-extra"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTrustedClientPolicy(context.Background(), "client-1", store.TrustedClientRecord{PrincipalRef: original.PrincipalRef, CapabilitiesJSON: original.CapabilitiesJSON, ProductScopeJSON: original.ProductScopeJSON, ProjectScopeJSON: original.ProjectScopeJSON, AgentScopeJSON: original.AgentScopeJSON}, fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	restored, _, err := s.TrustedClientWithKey(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	if restored.CapabilitiesJSON != original.CapabilitiesJSON || restored.ProductScopeJSON != original.ProductScopeJSON || restored.ProjectScopeJSON != original.ProjectScopeJSON || restored.AgentScopeJSON != original.AgentScopeJSON {
		t.Fatal("the restore did not reproduce the minted policy content")
	}
	if restored.PolicyRevision <= original.PolicyRevision {
		t.Fatalf("restoring identical content left the policy revision at %d, mint revision %d", restored.PolicyRevision, original.PolicyRevision)
	}
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "restored-approval")
	response := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	assertStalePolicyVersionConflict(t, s, response)
	stored := readStoredPolicy(t, s, "client-1")
	if contains(stored.Capabilities, "cross_scope") || contains(stored.Products, "product-2") || contains(stored.Agents, "agent-extra") {
		t.Fatalf("the refused approval changed the stored policy: %+v", stored)
	}
}

func TestGrantRequestAcknowledgementIsDurable(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	// The challenge mint acknowledges client and approval authority, so the
	// response may return only after a durable commit (CD-0050 D3).
	before := s.DurableCommits()
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("challenge response = %+v", challenge)
	}
	assertDurableCommit(t, s, before)
	afterChallenge := s.DurableCommits()
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "wal-approval")
	approved := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved response = %+v", approved)
	}
	// The applied union acknowledges the same authority family, so the apply
	// path commits durably too.
	assertDurableCommit(t, s, afterChallenge)
}

func assertDurableCommit(t *testing.T, s *store.Store, before uint64) {
	t.Helper()
	if got := s.DurableCommits(); got <= before {
		t.Fatalf("durable commits = %d after an acknowledged grant request, want more than %d", got, before)
	}
}

// grantRequestInputGrants builds a grant request whose additions are the given
// capability, Product, and Project lists, under a distinct idempotency key.
func grantRequestInputGrants(idempotencyKey string, capabilities, products, projects []string) string {
	mustMarshal := func(values []string) string {
		raw, err := json.Marshal(values)
		if err != nil {
			panic(err)
		}
		return string(raw)
	}
	return `{"capabilities":` + mustMarshal(capabilities) +
		`,"product_scope":` + mustMarshal(products) +
		`,"project_scope":` + mustMarshal(projects) +
		`,"agent_scope":[],"reason":"dependent work claims a cross-Product worktree","idempotency_key":"` + idempotencyKey + `"}`
}

// totalBoundCapabilities names the seven bearer-safe capabilities a fresh
// work_relate client does not hold yet, so the total-additions boundary tests
// below can count the derived diff exactly.
func totalBoundCapabilities() []string {
	return []string{"cross_scope", "product_read", "research", "work_compact", "work_define", "work_initiative", "work_transition"}
}

func totalBoundProducts() []string {
	products := make([]string, 0, 24)
	for i := 2; i <= 25; i++ {
		products = append(products, fmt.Sprintf("product-%d", i))
	}
	return products
}

// Every addition and both client identity bindings must fit the typed summary
// before the core writes a challenge or policy change.
func TestGrantRequestOverTheTotalBoundRefuses(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	before := readStoredPolicy(t, s, "client-1")
	env := grantRequestEnvelope(t, s, grant)
	// 7 + 24 additions and 2 identity bindings exceed the 32-binding summary.
	input := grantRequestInputGrants("grant-request-over", totalBoundCapabilities(), totalBoundProducts(), []string{})
	response := dispatchGrantRequest(t, s, service, env, input)
	if response.Error == nil || response.Error.Kind != "limit_exceeded" || response.Error.EffectState != EffectNone || response.Error.RecoveryAction.Kind != "reduce_limit" {
		t.Fatalf("oversized grant response = %+v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("oversized refusal envelope fails the generated envelope schema: %v", err)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
		t.Fatal("an oversized grant request minted a challenge")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
		t.Fatal("an oversized grant request recorded an approval")
	}
	if !reflect.DeepEqual(before, readStoredPolicy(t, s, "client-1")) {
		t.Fatal("an oversized grant refusal changed the stored policy")
	}
}

// A full typed summary must deliver and authorize every requested addition,
// including a single dimension with more than twenty additions.
func TestGrantRequestAtTheTotalBoundMints(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	before := readStoredPolicy(t, s, "client-1")
	env := grantRequestEnvelope(t, s, grant)
	// 7 + 23 additions and 2 identity bindings fill the 32-binding summary.
	products := totalBoundProducts()[:23]
	input := grantRequestInputGrants("grant-request-bound", totalBoundCapabilities(), products, []string{})
	response := dispatchGrantRequest(t, s, service, env, input)
	if response.Error == nil || response.Error.Kind != "approval_required" {
		t.Fatalf("boundary grant response = %+v", response)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("at-bound approval_required envelope fails the generated envelope schema: %v", err)
	}
	scope := map[string]any{"client_ref": "client-1", "policy_version": response.Error.Details["policy_version"], "capabilities": totalBoundCapabilities(), "product_scope": normalizeStrings(products)}
	if summary := response.Error.ConsequenceSummary; summary == nil || len(summary.Scope) != 32 || !reflect.DeepEqual(summary.Scope, approvalScopeBindings(scope)) {
		t.Fatalf("boundary consequence summary = %+v, want all 32 scope bindings (2 identity + 30 additions)", summary)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 1 {
		t.Fatal("the boundary grant request did not record its challenge")
	}
	if !reflect.DeepEqual(before, readStoredPolicy(t, s, "client-1")) {
		t.Fatal("the boundary challenge changed the stored policy")
	}
	ref := response.Error.Details["approval_ref"].(string)
	digest := response.Error.Details["operation_digest"].(string)
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "grant-boundary-approval")
	var approvedInput map[string]any
	if err := json.Unmarshal([]byte(input), &approvedInput); err != nil {
		t.Fatal(err)
	}
	approvedInput["approval"] = map[string]string{"approval_ref": ref}
	raw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	approved := dispatchGrantRequest(t, s, service, env, string(raw))
	if approved.Outcome != OutcomeOK {
		t.Fatalf("boundary approval = %+v", approved)
	}
	stored := readStoredPolicy(t, s, "client-1")
	if stored.PrincipalRef != before.PrincipalRef || !reflect.DeepEqual(stored.Products, normalizeStrings(append(before.Products, products...))) || !reflect.DeepEqual(stored.Capabilities, normalizeStrings(append(before.Capabilities, totalBoundCapabilities()...))) {
		t.Fatalf("boundary approval did not preserve and apply the exact union: %+v", stored)
	}
}

// The grant and its replay acknowledge durably under a pinned reader,
// without applying the policy union again.
func TestGrantRequestAcknowledgesUnderPinnedReader(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	env := grantRequestEnvelope(t, s, grant)
	challenge := dispatchGrantRequest(t, s, service, env, grantRequestInput(""))
	details := challenge.Error.Details
	ref := details["approval_ref"].(string)
	digest := details["operation_digest"].(string)
	scope := map[string]any{"client_ref": "client-1", "policy_version": details["policy_version"], "capabilities": []string{"cross_scope"}, "product_scope": []string{"product-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "barrier-approval")

	// The reader holds an open snapshot for the whole request.
	reader, err := sql.Open("sqlite", "file:"+s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readTx.Rollback() }()
	var probe int
	if err := readTx.QueryRowContext(context.Background(), `SELECT count(*) FROM agent_clients`).Scan(&probe); err != nil {
		t.Fatalf("reader snapshot: %v", err)
	}

	before := s.DurableCommits()
	response := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
	if response.Outcome != OutcomeOK || response.Error != nil {
		t.Fatalf("grant under a pinned reader = %+v, want ok", response)
	}
	assertDurableCommit(t, s, before)
	stored := readStoredPolicy(t, s, "client-1")
	if !contains(stored.Capabilities, "cross_scope") || !contains(stored.Products, "product-2") {
		t.Fatalf("the acknowledged grant was not applied: %+v", stored)
	}

	for _, pinned := range []bool{true, false} {
		if !pinned {
			if err := readTx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
		before = s.DurableCommits()
		metadataBefore := replayMetadataCount(t, s, "grant-request-1")
		replay := dispatchGrantRequest(t, s, service, env, grantRequestInput(ref))
		if replay.Outcome != OutcomeOK || replay.Error != nil || !replay.Replayed {
			t.Fatalf("grant replay pinned=%t: response=%+v error=%+v", pinned, replay, replay.Error)
		}
		requireReplayCommit(t, s, "grant-request-1", before, metadataBefore, true)
		if !reflect.DeepEqual(stored, readStoredPolicy(t, s, "client-1")) {
			t.Fatal("replay changed the committed policy")
		}
	}
}
