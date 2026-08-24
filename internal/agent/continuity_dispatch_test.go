package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"
)

// TestContinuityDispatchReadsAPinnedContract dispatches the real read against a
// work item whose contract is approved through the engine. A continuity
// snapshot with no contract validates against a strictly smaller schema, so
// only a pinned contract exercises the workflow_contract shape end to end.
//
// The fixture is shared with the verdict-scope tests because it is the one
// place that drives the engine to an approved contract through
// AuthorizeWorkflowActionAtBoundaryTx rather than seeding rows.
func TestContinuityDispatchReadsAPinnedContract(t *testing.T) {
	ctx := context.Background()
	s, service, execKey, _ := verdictScopeFixture(t)

	request := grantRequest(execKey, "continuity-pinned-nonce-01")
	request.Assertion.SessionRef = "session-exec-aaaa"
	request.Assertion.AgentRef = "agent-exec"
	request.Assertion.ClientRef = "client-session-exec-aaaa"
	request.Assertion.Signature = ed25519.Sign(execKey, CanonicalAssertion(request.Assertion))
	grant, err := service.IssueGrant(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}

	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "continuity", Input: json.RawMessage(`{"work_id":"work-1","page":{"cursor":null,"limit":10}}`)}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("continuity with a pinned contract failed: %+v", response.Error)
	}

	var payload struct {
		Pinned struct {
			Contract *struct {
				Version             int64 `json:"version"`
				ChangesProductTruth bool  `json:"changes_product_truth"`
			} `json:"contract"`
		} `json:"pinned"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Pinned.Contract == nil {
		t.Fatalf("continuity carried no contract: %s", response.Result)
	}
	if payload.Pinned.Contract.Version < 1 {
		t.Fatalf("pinned contract version = %d", payload.Pinned.Contract.Version)
	}
}
