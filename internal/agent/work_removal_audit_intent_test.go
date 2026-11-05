package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// TestRemovedWorkServesNoUnusableIntent follows the intent list a successful
// removal serves. The work projection is deleted, so every read that
// resolves live work scope refuses the removed identity; serving such a
// read as the next intent advertises a route the caller cannot use. The
// result serves no intent instead, and its retained acknowledgement stays
// reachable through retained authority: replaying the same removal re-serves
// the acknowledgement from the idempotency record before scope validation.
func TestRemovedWorkServesNoUnusableIntent(t *testing.T) {
	s, service, env, in := defaultRemovalFixture(t)
	out := removalDispatch(t, s, service, env, in)
	if out.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("removal: %+v", out.Error)
	}
	var result struct {
		Intents []agentcontract.NextIntent `json:"next_valid_intents"`
	}
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Intents) != 0 {
		t.Fatalf("removal advertised %d intents, want none: %+v", len(result.Intents), result.Intents)
	}
	if out.NextValidIntents == nil || len(*out.NextValidIntents) != 0 {
		t.Fatalf("envelope next_valid_intents=%v, want an explicit empty list", out.NextValidIntents)
	}
	replay := removalDispatch(t, s, service, env, in)
	if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("removal replay refused for the removed identity: outcome=%s replayed=%t error=%+v", replay.Outcome, replay.Replayed, replay.Error)
	}
	var replayed struct {
		Intents []agentcontract.NextIntent `json:"next_valid_intents"`
		Changed []struct {
			EntityKind string `json:"entity_kind"`
			ID         string `json:"id"`
		} `json:"changed_refs"`
	}
	if err := json.Unmarshal(replay.Result, &replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed.Changed) != 1 || replayed.Changed[0].EntityKind != "work_item" || replayed.Changed[0].ID != "work-1" {
		t.Fatalf("replay lost the retained acknowledgement's changed refs: %+v", replayed.Changed)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 {
		t.Fatal("replaying the removal recreated the removed work identity")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='shelving-removal' AND state='committed'`) != 1 {
		t.Fatal("replaying the removal changed the retained removal receipt")
	}
}

// TestRemovedWorkAcknowledgementRefusesChangedCallerOrRequest proves the
// retained acknowledgement stays partitioned: a caller whose idempotency
// partition holds no record falls through to live scope validation, which
// the removed identity cannot satisfy, and the same caller reusing the key
// with a changed canonical request gets an idempotency conflict.
func TestRemovedWorkAcknowledgementRefusesChangedCallerOrRequest(t *testing.T) {
	s, service, env, in := defaultRemovalFixture(t)
	out := removalDispatch(t, s, service, env, in)
	if out.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("removal: %+v", out.Error)
	}
	if err := service.RegisterTrustedClient(t.Context(), testClientRegistration("client-2", "human-2", []agentcontract.Capability{"work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	scope, _, err := s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	peerEnv := mutationEnvelope(mustAuthorize(t, service, "client-2", "human-2", agentcontract.Capability("work_transition"), "remove"), scope)
	peerEnv.Directory, peerEnv.Worktree, peerEnv.HostAssertionDigest = env.Directory, env.Worktree, env.HostAssertionDigest
	peer := removalDispatch(t, s, service, peerEnv, in)
	if peer.Error == nil || peer.Error.Kind != "unknown_scope" {
		t.Fatalf("changed caller gained acknowledgement access: %+v", peer.Error)
	}
	if !strings.Contains(peer.Error.Message, "work reference is not in Product scope") {
		t.Fatalf("changed caller refusal message %q does not name the live-scope requirement", peer.Error.Message)
	}
	changed := map[string]any{}
	for k, v := range in {
		changed[k] = v
	}
	changed["reason"] = "cancelled"
	conflict := removalDispatch(t, s, service, env, changed)
	if conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("changed canonical request gained acknowledgement access: %+v", conflict.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 {
		t.Fatal("refused changed-caller replay recreated the removed work identity")
	}
}

func mustAuthorize(t *testing.T, service *Service, client, principal string, capability agentcontract.Capability, operation string) Authority {
	t.Helper()
	authority, err := service.Authorize(t.Context(), Invocation{ClientRef: client, PrincipalRef: principal, SessionRef: "session-2", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: capability, RequiredOperation: operation})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}
