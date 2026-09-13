package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
)

// concord_work_browse.blocked answers with the blocked work in `items` and the
// unresolved blockers in `nodes`. Two defects met in that answer.
//
// The blocker graph query never selected the blocker's version, so every
// blocker summary carried version 0 against a schema whose version minimum is
// 1. Any answer holding a blocker failed its own result validation.
//
// The payload builder then wrote both slices over one backing array:
// `nodes := items` copies the slice header, not the elements, so the first
// append to nodes overwrote items[0], the second overwrote items[1], and the
// two slices corrupted each other element for element.
//
// Together they made the read unusable exactly when it had something to
// report, and its failure looked like a schema violation rather than either
// cause.
func TestBlockedReadKeepsItemsAndBlockerNodesSeparateAndVersioned(t *testing.T) {
	ctx := context.Background()
	s, service, grant, corpus := agentJobsPM1Fixture(t)
	if _, err := pm1fixture.SeedKnowledge(ctx, s, corpus, t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedKnowledge: %v", err)
	}
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")

	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_browse",
		Operation: "blocked",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","page":{"cursor":null,"limit":10}}`),
	}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("blocked read refused: %+v", resp.Error)
	}

	var payload struct {
		Items []struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"items"`
		Nodes []struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode blocked payload: %v", err)
	}
	if len(payload.Edges) == 0 {
		t.Fatal("the fixture produced no blocker edge, so this test proves nothing about the graph")
	}

	// A version below 1 fails the result schema, so the read refuses its own
	// answer. Assert it on the decoded payload as well, because the schema
	// reports only the first offending path.
	for i, item := range payload.Items {
		if item.Version < 1 {
			t.Errorf("items[%d] (%s) carries version %d; the result schema requires at least 1", i, item.ID, item.Version)
		}
	}
	for i, node := range payload.Nodes {
		if node.Version < 1 {
			t.Errorf("nodes[%d] (%s) carries version %d; the result schema requires at least 1", i, node.ID, node.Version)
		}
	}

	// items holds the blocked work and nodes holds its blockers. An id in both
	// means one slice overwrote the other.
	inItems := make(map[string]bool, len(payload.Items))
	for _, item := range payload.Items {
		inItems[item.ID] = true
	}
	for _, node := range payload.Nodes {
		if inItems[node.ID] {
			t.Errorf("%s appears in both items and nodes; the two slices share a backing array", node.ID)
		}
	}

	// Every edge runs from blocked work to one of the blocker nodes. An edge
	// whose endpoint is in neither slice means the payload lost an element.
	inNodes := make(map[string]bool, len(payload.Nodes))
	for _, node := range payload.Nodes {
		inNodes[node.ID] = true
	}
	for _, edge := range payload.Edges {
		if !inItems[edge.From] {
			t.Errorf("edge from %s has no entry in items", edge.From)
		}
		if !inNodes[edge.To] && !inItems[edge.To] {
			t.Errorf("edge to %s has no entry in nodes", edge.To)
		}
	}
}
