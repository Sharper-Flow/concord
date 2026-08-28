package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func staleScopeEnvelope(authority Authority) CallEnvelope {
	env := mutationEnvelope(authority, "stale-scope-version")
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

func TestInvalidSelectedProductFailsAsContextNotAuthorization(t *testing.T) {
	ctx := context.Background()
	s, service, authority, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	version, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}

	invocation := Invocation{
		ClientRef: authority.ClientRef, PrincipalRef: authority.PrincipalRef, SessionRef: authority.SessionRef,
		AgentRef: authority.AgentRef, Directory: authority.Directory, Worktree: authority.Worktree,
		ManifestDigest: authority.ManifestDigest, RequiredCapability: "product_read", ProjectID: "project-1",
	}
	authorized, err := service.Authorize(ctx, invocation)
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(authorized, version)
	env.SelectedProductID = "product-granted"
	failure := validateRuntimeScope(ctx, s, env, authorized, OperationRead)
	if failure == nil {
		t.Fatal("invalid selected Product was accepted")
	}
	var runtimeErr *runtimeFailure
	if !errors.As(failure, &runtimeErr) {
		t.Fatalf("context failure type = %T, want runtime failure", failure)
	}
	if runtimeErr.kind != "stale_context" {
		t.Fatalf("context failure kind = %q, want stale_context", runtimeErr.kind)
	}
	if runtimeErr.kind == "unauthorized" {
		t.Fatal("invalid selected Product was reported as authorization")
	}
	if runtimeErr.recovery != "refresh_context" || len(runtimeErr.Candidates) != 1 || runtimeErr.Candidates[0] != "product-1" {
		t.Fatalf("context failure = %+v", runtimeErr)
	}
}

func TestInvalidSelectedProductUnderAmbiguousScopeResolvesByCandidate(t *testing.T) {
	ctx := context.Background()
	s, service, authority, _ := mutationDispatchFixture(t, []Capability{"product_read"})
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

	invocation := Invocation{
		ClientRef: authority.ClientRef, PrincipalRef: authority.PrincipalRef, SessionRef: authority.SessionRef,
		AgentRef: authority.AgentRef, Directory: authority.Directory, Worktree: authority.Worktree,
		ManifestDigest: authority.ManifestDigest, RequiredCapability: "product_read", ProjectID: "project-1",
	}
	authorized, err := service.Authorize(ctx, invocation)
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(authorized, version)
	env.SelectedProductID = "product-granted"
	failure := validateRuntimeScope(ctx, s, env, authorized, OperationRead)
	if failure == nil {
		t.Fatal("invalid selected Product was accepted")
	}
	var runtimeErr *runtimeFailure
	if !errors.As(failure, &runtimeErr) {
		t.Fatalf("context failure type = %T, want runtime failure", failure)
	}
	if runtimeErr.kind != "ambiguous_scope" || runtimeErr.recovery != "resolve_ambiguity" {
		t.Fatalf("context failure = %+v", runtimeErr)
	}
	if !equalStrings(runtimeErr.Candidates, candidates) {
		t.Fatalf("candidates=%v, want %v", runtimeErr.Candidates, candidates)
	}
}
