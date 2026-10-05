package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CON-835 regression. Bounded summary and preview reads stay bounded: the
// recorded intent detail (task, value statement, tags, workflow type
// reference) rides only the authoritative single-work scope read and the
// full-detail list read. Snapshot previews, summary-detail listings, and the
// ready listing must not carry it. Urgency is not intent detail: CD-0018
// already declares it on work_summary, so every read that carries a work
// summary carries the declared band.
func TestBoundedWorkReadsOmitIntentDetailButCarryUrgency(t *testing.T) {
	t.Parallel()
	s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
		"title": "Bounded reads stay bounded", "value_statement": "Intent detail belongs to the authoritative reads",
		"task": "Recorded instruction", "kind": "task", "priority": 5, "urgency": "expedite",
		"tags": []string{"agent-plane"}, "workflow_type_ref": "workflow.break_fix",
		"idempotency_key": "bounds-capture-1",
	})
	intentOnly := []string{"task", "value_statement", "tags", "workflow_type_ref"}

	previews := roundtripReadItems(t, ctx, s, service, env, "concord_product_view", "snapshot", map[string]any{"product_id": "product-1"}, "previews")
	assertBoundedSummaries(t, "snapshot previews", previews, intentOnly, workID)

	summaryItems := roundtripReadItems(t, ctx, s, service, env, "concord_work_browse", "list", map[string]any{"product_id": "product-1", "work_ids": []string{workID}}, "items")
	assertBoundedSummaries(t, "summary-detail list", summaryItems, intentOnly, workID)

	readyItems := roundtripReadItems(t, ctx, s, service, env, "concord_work_browse", "ready", map[string]any{"product_id": "product-1"}, "items")
	assertBoundedSummaries(t, "ready listing", readyItems, intentOnly, workID)
}

func roundtripReadItems(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, tool, operation string, input map[string]any, field string) []map[string]any {
	t.Helper()
	response := roundtripDispatchOK(t, ctx, s, service, env, "bounds-"+operation, tool, operation, input)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	raw, ok := payload[field]
	if !ok {
		t.Fatalf("%s.%s result carries no %q: %s", tool, operation, field, response.Result)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatalf("%s.%s returned no items to bound", tool, operation)
	}
	return items
}

func assertBoundedSummaries(t *testing.T, surface string, items []map[string]any, intentOnly []string, workID string) {
	t.Helper()
	found := false
	for _, item := range items {
		if item["id"] != workID {
			continue
		}
		found = true
		for _, field := range intentOnly {
			if _, ok := item[field]; ok {
				t.Fatalf("%s carried intent field %q for %s; bounded reads omit intent detail", surface, field, workID)
			}
		}
		if item["urgency"] != "expedite" {
			t.Fatalf("%s urgency = %#v, want the declared CD-0018 band carried on work_summary", surface, item["urgency"])
		}
	}
	if !found {
		t.Fatalf("%s did not include the captured item %s", surface, workID)
	}
}
