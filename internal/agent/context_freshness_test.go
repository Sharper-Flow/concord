package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// TS5 §3 requires an outdated but semantically unchanged scope to split by
// operation kind: a read executes and returns the refreshed scope version with an
// explicit context_refreshed notice, while a mutation fails stale_context before
// any effect. The two halves are one rule, so they are proven against the same
// stale envelope rather than in isolation.

func staleScopeEnvelope(grant Grant) CallEnvelope {
	env := mutationEnvelope(grant, "stale-scope-version")
	env.RequestID = "context-freshness-request"
	return env
}

func domainEventCount(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestStaleUnchangedScopePermitsReadAndRejectsMutationBeforeAnyEffect(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_define"})
	current, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if current == "stale-scope-version" {
		t.Fatal("fixture scope version collides with the stale sentinel")
	}

	t.Run("read refreshes context", func(t *testing.T) {
		before := domainEventCount(t, s)
		request := InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}
		response, err := Dispatch(ctx, s, service, request, staleScopeEnvelope(grant))
		if err != nil {
			t.Fatal(err)
		}
		if response.Outcome != OutcomeOK {
			t.Fatalf("stale read outcome=%v error=%+v, want ok", response.Outcome, response.Error)
		}
		var refreshed bool
		for _, notice := range response.Warnings {
			if notice.Kind == "context_refreshed" {
				refreshed = true
			}
		}
		if !refreshed {
			t.Fatalf("stale read warnings=%+v, want a context_refreshed notice", response.Warnings)
		}
		if response.ResolvedScope == nil || response.ResolvedScope.ScopeVersion != current {
			t.Fatalf("resolved scope=%+v, want the current scope version %q", response.ResolvedScope, current)
		}
		if after := domainEventCount(t, s); after != before {
			t.Fatalf("read recorded %d domain events", after-before)
		}
	})

	t.Run("mutation fails before any effect", func(t *testing.T) {
		before := domainEventCount(t, s)
		request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Stale","value_statement":"Stale scope capture","kind":"task","project_ids":["project-1"],"idempotency_key":"stale-scope-capture"}`)}
		response, err := Dispatch(ctx, s, service, request, staleScopeEnvelope(grant))
		if err != nil {
			t.Fatal(err)
		}
		if response.Outcome != OutcomeError || response.Error == nil {
			t.Fatalf("stale mutation outcome=%v error=%+v, want a typed failure", response.Outcome, response.Error)
		}
		if response.Error.Kind != "stale_context" {
			t.Fatalf("stale mutation error kind=%q, want stale_context", response.Error.Kind)
		}
		if response.Error.RecoveryAction.Kind != "refresh_context" {
			t.Fatalf("stale mutation recovery=%q, want refresh_context", response.Error.RecoveryAction.Kind)
		}
		if response.Error.EffectState != EffectNone {
			t.Fatalf("stale mutation effect state=%q, want none", response.Error.EffectState)
		}
		if after := domainEventCount(t, s); after != before {
			t.Fatalf("rejected mutation recorded %d domain events", after-before)
		}
		var captured int
		if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM work_items WHERE title=?`, "Stale").Scan(&captured); err != nil {
			t.Fatal(err)
		}
		if captured != 0 {
			t.Fatalf("rejected mutation created %d work items", captured)
		}
	})
}

// TS5 §3's third branch — changed ownership, a selected Product that is no longer
// valid, or newly ambiguous scope — must fail with a context kind carrying bounded
// candidates and mechanical recovery. Only a Product outside the grant is an
// authorization failure. The two were previously conflated, so an agent whose
// membership moved underneath it was told to contact the operator for a condition
// it could re-resolve itself.
func TestInvalidSelectedProductFailsAsContextNotAuthorization(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})

	// The selected Product must be inside the grant so the context branch is the
	// one under test: grant authorization runs first and would otherwise mask it.
	if _, err := s.DatabaseForTesting().Exec(`UPDATE agent_grants SET product_scope_json=?`, `["product-1","product-granted"]`); err != nil {
		t.Fatal(err)
	}

	read := func(t *testing.T, selected string) Envelope {
		t.Helper()
		version, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		env := mutationEnvelope(grant, version)
		env.RequestID = "selected-product-request"
		env.SelectedProductID = selected
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}, env)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	t.Run("single candidate is stale context", func(t *testing.T) {
		response := read(t, "product-granted")
		if response.Outcome != OutcomeError || response.Error == nil {
			t.Fatalf("outcome=%v error=%+v, want a typed failure", response.Outcome, response.Error)
		}
		if response.Error.Kind != "stale_context" {
			t.Fatalf("error kind=%q, want stale_context", response.Error.Kind)
		}
		if response.Error.RecoveryAction.Kind != "refresh_context" {
			t.Fatalf("recovery=%q, want refresh_context", response.Error.RecoveryAction.Kind)
		}
		if len(response.Error.Candidates) != 1 || response.Error.Candidates[0] != "product-1" {
			t.Fatalf("candidates=%v, want the resolved owner", response.Error.Candidates)
		}
	})

	t.Run("grant violation stays unauthorized", func(t *testing.T) {
		// product-1 is a genuine candidate but outside this grant's Product scope
		// once the scope is narrowed, so authorization still owns this failure.
		narrow, narrowService, narrowGrant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
		if _, err := narrow.DatabaseForTesting().Exec(`UPDATE agent_grants SET product_scope_json=?`, `["product-other"]`); err != nil {
			t.Fatal(err)
		}
		version, _, err := narrow.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		env := mutationEnvelope(narrowGrant, version)
		env.RequestID = "grant-violation-request"
		response, err := Dispatch(ctx, narrow, narrowService, InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}, env)
		if err != nil {
			t.Fatal(err)
		}
		if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unauthorized" {
			t.Fatalf("outcome=%v error=%+v, want unauthorized", response.Outcome, response.Error)
		}
	})

	t.Run("stale selection is never refreshed through", func(t *testing.T) {
		// A read may proceed through a stale scope VERSION, but never through a
		// selection the resolved scope rejects: refreshing the version alone would
		// carry the invalid Product forward into the query.
		response := read(t, "product-granted")
		for _, notice := range response.Warnings {
			if notice.Kind == "context_refreshed" {
				t.Fatal("an invalid selected Product was refreshed through instead of refused")
			}
		}
		if response.Outcome == OutcomeOK {
			t.Fatal("read executed with a selected Product outside the resolved scope")
		}
	})
}

// The same branch must report newly ambiguous scope as ambiguous_scope rather than
// stale_context, because the recovery differs: the caller selects from candidates
// instead of refreshing a version.
func TestInvalidSelectedProductUnderAmbiguousScopeResolvesByCandidate(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	if _, err := s.DatabaseForTesting().Exec(`UPDATE agent_grants SET product_scope_json=?`, `["product-1","product-2","product-granted"]`); err != nil {
		t.Fatal(err)
	}
	events := []store.Event{
		{EventID: "ambiguity-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Second","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "ambiguity-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-1","role":"secondary","reason":"ambiguity","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	version, candidates, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%v, want two owning Products", candidates)
	}
	env := mutationEnvelope(grant, version)
	env.RequestID = "ambiguous-selection-request"
	env.SelectedProductID = "product-granted"
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "ambiguous_scope" {
		t.Fatalf("outcome=%v error=%+v, want ambiguous_scope", response.Outcome, response.Error)
	}
	if response.Error.RecoveryAction.Kind != "resolve_ambiguity" {
		t.Fatalf("recovery=%q, want resolve_ambiguity", response.Error.RecoveryAction.Kind)
	}
	if !equalStrings(response.Error.Candidates, candidates) {
		t.Fatalf("candidates=%v, want the resolved owners %v", response.Error.Candidates, candidates)
	}
}
