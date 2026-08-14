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
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM domain_events`).Scan(&count); err != nil {
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
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM work_items WHERE title=?`, "Stale").Scan(&captured); err != nil {
			t.Fatal(err)
		}
		if captured != 0 {
			t.Fatalf("rejected mutation created %d work items", captured)
		}
	})
}
