package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0068: Domain observations through the tool surface. Recording is cheap
// and unapproved; the window is bounded; dismissal is the operator's act and
// carries an approval challenge; the rows read back through the Domain surface.

func TestDomainObservationDispatchRecordsReadsBackAndGatesDismissal(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_define", "product_read"})
	if err := pm1fixture.SeedCurrentProductDomain(ctx, s, "product-1", "project-1"); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	domain := pm1fixture.FixtureRootDomainID
	env := mutationEnvelope(grant, scopeVersion)

	// Recording is unapproved (CD-0068 D3) and needs no work item.
	recordInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"statement":"This Domain has no owner for the scanner failure path.","refs":["docs/core-architecture.md"],"tags":["gap"],"idempotency_key":"domain-obs-1"}`, domain))
	recorded, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "observation_record", Input: recordInput}, env)
	if err != nil || recorded.Outcome != OutcomeOK {
		t.Fatalf("observation_record response=%+v err=%v", recorded.Error, err)
	}
	var observationID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT observation_id FROM domain_observations WHERE product_id='product-1' AND domain_id=?`, domain).Scan(&observationID); err != nil {
		t.Fatal(err)
	}

	// CD-0068 D6: the observation reads back through the Domain surface.
	detailInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q}`, domain))
	detail, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "detail", Input: detailInput}, env)
	if err != nil || detail.Outcome != OutcomeOK {
		t.Fatalf("domain detail response=%+v err=%v", detail.Error, err)
	}
	if got := detailObservationIDs(t, detail); len(got) != 1 || got[0] != observationID {
		t.Fatalf("detail observations = %v, want [%s]", got, observationID)
	}

	// CD-0068 D3: dismissal is approval-gated, so an agent cannot drain the
	// window it fills.
	dismissInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"observation_id":%q,"idempotency_key":"domain-dismiss-1"}`, domain, observationID))
	request := InvokeRequest{Tool: "concord_domain", Operation: "observation_dismiss", Input: dismissInput}
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Outcome != OutcomeError || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("unapproved dismissal response=%+v err=%v", missing, err)
	}
	challengeRef, ok := missing.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge ref = %v", missing.Error.Details["approval_ref"])
	}
	// The refused dismissal left the observation open.
	if state := observationState(t, s, observationID); state != "open" {
		t.Fatalf("state after refused dismissal = %q, want open", state)
	}

	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{}, "session-1", "agent-1", "/repo-wt", fixedTime(), "domain-obs-nonce-01")
	approvedInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"observation_id":%q,"idempotency_key":"domain-dismiss-1","approval":{"approval_ref":%q}}`, domain, observationID, challengeRef))
	approved, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "observation_dismiss", Input: approvedInput}, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved dismissal response=%+v err=%v", approved.Error, err)
	}

	// The row persists for audit and leaves the open window.
	if state := observationState(t, s, observationID); state != "dismissed" {
		t.Fatalf("state after dismissal = %q, want dismissed", state)
	}
	env.HostApproval = nil
	afterDetail, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "detail", Input: detailInput}, env)
	if err != nil || afterDetail.Outcome != OutcomeOK {
		t.Fatalf("domain detail after dismissal response=%+v err=%v", afterDetail.Error, err)
	}
	if got := detailObservationIDs(t, afterDetail); len(got) != 0 {
		t.Fatalf("dismissed observation still open in detail: %v", got)
	}
}

// CD-0068 D2: a full window refuses and names the Domain rather than evicting.
func TestDomainObservationDispatchRefusesWhenWindowIsFull(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "product_read"})
	if err := pm1fixture.SeedCurrentProductDomain(ctx, s, "product-1", "project-1"); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	domain := pm1fixture.FixtureRootDomainID
	env := mutationEnvelope(grant, scopeVersion)
	for i := 0; i < 64; i++ {
		input := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"statement":"observation %d","idempotency_key":"fill-%d"}`, domain, i, i))
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "observation_record", Input: input}, env)
		if err != nil || response.Outcome != OutcomeOK {
			t.Fatalf("fill %d response=%+v err=%v", i, response.Error, err)
		}
	}
	overflowInput := json.RawMessage(fmt.Sprintf(`{"product_id":"product-1","domain_id":%q,"statement":"one too many","idempotency_key":"overflow-1"}`, domain))
	overflow, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_domain", Operation: "observation_record", Input: overflowInput}, env)
	if err != nil {
		t.Fatal(err)
	}
	if overflow.Outcome != OutcomeError || overflow.Error == nil || overflow.Error.Kind != "invariant_violation" {
		t.Fatalf("full window response=%+v", overflow.Error)
	}
	if overflow.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("recovery action = %q, want contact_operator", overflow.Error.RecoveryAction.Kind)
	}
	var total int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_observations WHERE product_id='product-1' AND domain_id=?`, domain).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 64 {
		t.Fatalf("rows after refusal = %d, want 64", total)
	}
}

func detailObservationIDs(t *testing.T, response Envelope) []string {
	t.Helper()
	var payload struct {
		Observations []struct {
			ObservationID string `json:"observation_id"`
		} `json:"observations"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatalf("decode domain detail payload: %v", err)
	}
	out := []string{}
	for _, observation := range payload.Observations {
		out = append(out, observation.ObservationID)
	}
	return out
}

func observationState(t *testing.T, s *store.Store, observationID string) string {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM domain_observations WHERE observation_id=?`, observationID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
