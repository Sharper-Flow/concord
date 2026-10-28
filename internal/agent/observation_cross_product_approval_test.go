package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// CD-0243 D3/D7 retire ordinary cross-Product observation consent.
func TestCrossProductObservationRecordNeedsNoConsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := crossProductPolicyFixture(t, []agentcontract.Capability{"product_read", "work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-2","statement":"progress note from the other Product","idempotency_key":"cross-observation-1"}`)}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if challenge.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("cross-Product observation_record = %+v", challenge.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_observations WHERE work_id='work-2'`); got != 1 {
		t.Fatalf("recorded %d work observations, want one", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
		t.Fatalf("ordinary observation minted %d challenges", got)
	}
}

// CD-0243 D3/D7 preserve exact replay after removing ordinary consent.
func TestCrossProductObservationRecordReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant := crossProductPolicyFixture(t, []agentcontract.Capability{"product_read", "work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-2","statement":"progress note from the other Product","idempotency_key":"cross-observation-2"}`)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: input}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || challenge.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("cross-Product observation_record = %+v, err = %v", challenge.Error, dispatchErr)
	}
	approved, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || approved.Error != nil || approved.Outcome != agentcontract.OutcomeOK || !approved.Replayed {
		t.Fatalf("approved cross-Product observation_record = %s / %v, err = %v", approved.Outcome, approved.Error, dispatchErr)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_observations WHERE work_id='work-2'`); got != 1 {
		t.Fatalf("approved observation_record recorded %d work observations, want 1", got)
	}
}
