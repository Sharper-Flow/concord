package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

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
	read := InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}
	response, err := Dispatch(ctx, s, service, read, staleScopeEnvelope(grant))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("stale read outcome=%v error=%+v", response.Outcome, response.Error)
	}
	refreshed := false
	for _, warning := range response.Warnings {
		if warning.Kind == "context_refreshed" {
			refreshed = true
		}
	}
	if !refreshed || response.ResolvedScope == nil || response.ResolvedScope.ScopeVersion != current {
		t.Fatalf("response scope=%+v warnings=%+v", response.ResolvedScope, response.Warnings)
	}
	before := domainEventCount(t, s)
	mutation := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Stale","value_statement":"Stale scope capture","kind":"task","project_ids":["project-1"],"idempotency_key":"stale-scope-capture"}`)}
	response, err = Dispatch(ctx, s, service, mutation, staleScopeEnvelope(grant))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "stale_context" {
		t.Fatalf("stale mutation response=%+v", response)
	}
	if domainEventCount(t, s) != before {
		t.Fatal("stale mutation changed domain events")
	}
}
