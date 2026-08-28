package agent

import (
	"context"
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

	_ = execKey
	grant, err := service.Authorize(ctx, Invocation{ClientRef: "client-session-exec-aaaa", PrincipalRef: "human-1", SessionRef: "session-exec-aaaa", AgentRef: "agent-exec", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
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
