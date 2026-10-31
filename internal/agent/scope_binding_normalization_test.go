package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A scope list that names the same work item twice must not mask a
// typed outcome behind an Envelope.MarshalJSON failure. Scope-list
// normalization is the shared owner: bindings render strictly sorted and
// unique however often a path repeats an identity, resolved scopes stay
// unique, and the strict envelope check that rejects duplicate bindings
// stays exactly as strict as it was.

func TestNormalizeScopeListDeduplicatesStringAndJSONArrayLists(t *testing.T) {
	t.Parallel()
	inOrder := []string{"work-b", "work-a", "work-b", "work-c", "work-a"}
	want := []string{"work-b", "work-a", "work-c"}
	if got := normalizeScopeList(inOrder); !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeScopeList(%v)=%v want %v", inOrder, got, want)
	}
	// A JSON-decoded list (stored challenge, approval, or idempotency
	// snapshot) carries []any with non-string members possible.
	decoded := []any{"work-b", "work-a", 7, nil, "work-c", "work-b", "work-a"}
	if got := scopeListValues(decoded); !reflect.DeepEqual(got, want) {
		t.Fatalf("scopeListValues(%v)=%v want %v", decoded, got, want)
	}
	if got := scopeListValues("work-a"); got != nil {
		t.Fatalf("scalar scope value decoded to %v want nil", got)
	}
	if got := scopeListValues([]any{1.5, true}); len(got) != 0 {
		t.Fatalf("non-string members decoded to %v want none", got)
	}
}

func TestNormalizeScopeListPreservesUniqueOrderBytesAndOwnership(t *testing.T) {
	t.Parallel()
	unique := []string{"work-c", "work-a", "work-b"}
	before, _ := json.Marshal(unique)
	normalized := normalizeScopeList(unique)
	after, _ := json.Marshal(normalized)
	if string(before) != string(after) {
		t.Fatalf("already unique list changed bytes: %s became %s", before, after)
	}
	// Non-aliasing: the normalized copy never shares a backing array with
	// the caller's slice, so neither can observe the other change.
	if len(normalized) > 0 && &normalized[0] == &unique[0] {
		t.Fatal("normalized list aliases the caller's slice")
	}
	normalized[0] = "work-mutated"
	if unique[0] != "work-c" {
		t.Fatalf("caller-owned slice mutated: %v", unique)
	}
	// scopeFromMap resolves the same contract for the wire scope, for both
	// builder-set and JSON-decoded lists.
	scope := map[string]any{"work_ids": []string{"w-1", "w-1", "w-2"}, "project_ids": []any{"p-2", "p-1", "p-2"}}
	resolved := scopeFromMap(scope)
	if !reflect.DeepEqual(resolved.WorkIDs, []string{"w-1", "w-2"}) || !reflect.DeepEqual(resolved.ProjectIDs, []string{"p-2", "p-1"}) {
		t.Fatalf("resolved scope work=%v project=%v want unique first-occurrence order", resolved.WorkIDs, resolved.ProjectIDs)
	}
	refusal := agentcontract.NewCoreError(agentcontract.NewBase("scope-binding", "concord_work_relate", "message_send"), agentcontract.TypedError{Kind: "unauthorized", RecoveryAction: agentcontract.RecoveryAction{Kind: "contact_operator"}, EffectState: agentcontract.EffectNone})
	refusal.ResolvedScope = resolved
	if err := refusal.Validate(); err != nil {
		t.Fatalf("resolved scope with repeated identities rejected: %v", err)
	}
}

func TestScopeListValuesPreservesUniqueJSONBytesAndOwnership(t *testing.T) {
	t.Parallel()
	for _, input := range []any{[]string(nil), []string{}, []any(nil), []any{}, []any{"work-c", "work-a", "work-b"}} {
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		got := scopeListValues(input)
		after, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("unique %T list changed JSON bytes: %s became %s", input, before, after)
		}
		if !reflect.DeepEqual(scopeListValues(got), got) {
			t.Errorf("normalization is not idempotent for %v", input)
		}
		if len(got) > 0 {
			got[0] = "work-mutated"
			unchanged, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(unchanged) {
				t.Errorf("normalization aliases input: %s became %s", before, unchanged)
			}
		}
	}
}

// assertSelfSendInvariantRefusal holds the shared regression shape:
// an authorized direct self-addressed message_send answers with an
// encodable typed invariant_violation, effect state none, naming the store
// invariant's two sources, and leaves no message, version, or approval
// challenge effect behind.
func assertSelfSendInvariantRefusal(t *testing.T, s *store.Store, response agentcontract.Envelope, workID string, dispatchErr error) {
	t.Helper()
	if dispatchErr != nil {
		t.Fatalf("self-addressed send returned err=%v", dispatchErr)
	}
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invariant_violation" {
		t.Fatalf("self-addressed send wants invariant_violation: outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if response.Error.EffectState != agentcontract.EffectNone {
		t.Fatalf("self-addressed send effect_state=%s want none", response.Error.EffectState)
	}
	if response.Error.RetrySafe {
		t.Fatal("self-addressed send must not be retry-safe")
	}
	if response.Error.RecoveryAction.Kind != "reread_entities" {
		t.Fatalf("self-addressed send recovery=%s want reread_entities (the envelope schema couples an options-less invariant_violation to it)", response.Error.RecoveryAction.Kind)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("the refusal envelope does not marshal: %v", err)
	}
	for _, source := range []string{
		"internal/store/work_messages.go foldMessageSent",
		"internal/store/schema.go work_messages CHECK(recipient_work_id != sender_work_id)",
	} {
		if !strings.Contains(string(encoded), source) {
			t.Fatalf("refusal does not name invariant source %q: %s", source, encoded)
		}
	}
	sources, ok := response.Error.Details["invariant_sources"].([]string)
	if !ok || len(sources) != 2 {
		t.Fatalf("invariant provenance missing: %+v", response.Error.Details)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_messages`); got != 0 {
		t.Fatalf("self-addressed send left %d messages", got)
	}
	if version := workVersion(t, s, workID); version != 2 {
		t.Fatalf("self-addressed send changed the sender version to %d", version)
	}
}

// TestMessageSendSelfRecipientRefusedAsInvariantSingleProduct holds the
// single-Product case: the refusal precedes any effect, so the store fold
// and its CHECK constraint are never reached with a self-addressed event.
func TestMessageSendSelfRecipientRefusedAsInvariantSingleProduct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := messagesFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"work_id": "work-sender", "recipient_work_id": "work-sender", "body": "a coordinator cannot hand off to itself", "expected_version": 2, "idempotency_key": "msg-self-1"})
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "message_send", Input: raw}, mutationEnvelope(grant, scopeVersion))
	assertSelfSendInvariantRefusal(t, s, response, "work-sender", dispatchErr)
}

// TestMessageSendSelfRecipientRefusedAsInvariantCrossProduct holds the
// observed failure shape: on shared cross-Product work the unfixed core
// bound the shared work twice into the refusal, and the duplicate binding
// made the refusal envelope unmarshalable, so the operator never received
// the typed outcome at all.
func TestMessageSendSelfRecipientRefusedAsInvariantCrossProduct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := crossProductDispatchFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"work_id": "work-1", "recipient_work_id": "work-1", "body": "a coordinator cannot leave a durable handoff on its own shared work item", "expected_version": 2, "idempotency_key": "msg-self-cross-1"})
	response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "message_send", Input: raw}, mutationEnvelope(grant, scopeVersion))
	assertSelfSendInvariantRefusal(t, s, response, "work-1", dispatchErr)
}
