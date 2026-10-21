package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A recorded settlement must be readable through the public
// concord_work_trace.history surface as an optional typed conflict_application
// payload on its work.conflict_applied event, carrying the recorded pair and
// the frozen binding, with the authenticated principal as the actor. Every
// assertion reads the serialized public page.

func TestConflictSettlementHistoryPublishesThePublicTrio(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	settlement, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{
		"work_id":         "work-a",
		"idempotency_key": "conflict-history-1",
		"conflict":        map[string]any{"rule_id": "R1", "outcome": "settled: rule R1 applied, the slice stays out of scope"},
	})
	if err != nil || settlement.Error != nil || settlement.Outcome != OutcomeOK {
		t.Fatalf("conflict settlement = %+v err = %v, want a recorded settlement before reading history", settlement, err)
	}
	history := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "history",
		Input:     json.RawMessage(`{"work_id":"work-a","event_kinds":["` + store.WorkConflictApplied + `"]}`),
	}, mutationEnvelope(grant, scopeVersion))
	if history.Outcome != OutcomeOK || history.Error != nil {
		t.Fatalf("conflict history = %+v, want an ok page; a refusal here also means the generated result schema does not accept the page", history.Error)
	}
	var page struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(history.Result, &page); err != nil {
		t.Fatalf("history result is not a page: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0]["kind"] != store.WorkConflictApplied {
		t.Fatalf("filtered history page = %v, want exactly the work.conflict_applied event", page.Events)
	}
	event := page.Events[0]
	payload, ok := event["conflict_application"].(map[string]any)
	if !ok {
		t.Fatalf("public history event %v carries no conflict_application payload; the persisted trio is hidden from readers", event)
	}
	if len(payload) != 5 {
		t.Fatalf("conflict_application payload carries %d fields; the closed shape has exactly 5: %v", len(payload), payload)
	}
	if got := payload["rule_id"]; got != "R1" {
		t.Fatalf("published rule_id %v, want R1", got)
	}
	if got := payload["outcome"]; got != "settled: rule R1 applied, the slice stays out of scope" {
		t.Fatalf("published outcome %v, want the dispatched outcome", got)
	}
	ref, ok := payload["settlement_ref"].(string)
	if !ok || ref == "" || len(ref) > 256 {
		t.Fatalf("published settlement_ref %v, want a core-computed value inside its 256-byte bound", payload["settlement_ref"])
	}
	hash, ok := payload["rule_source_hash"].(string)
	if !ok || len(hash) != len("sha256:")+64 || !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("published rule_source_hash %v, want a sha256 digest", payload["rule_source_hash"])
	}
	generation, ok := payload["catalog_generation"].(float64)
	if !ok || generation < 1 {
		t.Fatalf("published catalog_generation %v, want the live table generation", payload["catalog_generation"])
	}
	if got := event["actor"]; got != grant.PrincipalRef {
		t.Fatalf("published actor %v, want the authenticated principal %q", got, grant.PrincipalRef)
	}
	// Closed published shape: no arbitrary payload leak beside the ordinary
	// event fields and the one typed conflict_application payload.
	for key := range event {
		switch key {
		case "event_id", "kind", "version", "occurred_at", "actor", "reason", "evidence", "contract_version", "previous_contract_version", "conflict_application":
		default:
			t.Fatalf("public history event leaks key %q: %v", key, event)
		}
	}
}

func TestConflictSettlementLeavesOrdinaryHistoryPagesUnchanged(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	settlement, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{
		"work_id":         "work-a",
		"idempotency_key": "conflict-ordinary-1",
		"conflict":        map[string]any{"rule_id": "R1", "outcome": "settled"},
	})
	if err != nil || settlement.Error != nil || settlement.Outcome != OutcomeOK {
		t.Fatalf("conflict settlement = %+v err = %v, want ok", settlement, err)
	}
	statement, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-ordinary-statement", "statement": "ordinary progress note"})
	if err != nil || statement.Error != nil || statement.Outcome != OutcomeOK {
		t.Fatalf("statement observation = %+v err = %v, want ok", statement, err)
	}
	defaults := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "history",
		Input:     json.RawMessage(`{"work_id":"work-a"}`),
	}, mutationEnvelope(grant, scopeVersion))
	if defaults.Outcome != OutcomeOK || defaults.Error != nil {
		t.Fatalf("default history = %+v, want an unchanged ok page", defaults.Error)
	}
	var defaultPage struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(defaults.Result, &defaultPage); err != nil {
		t.Fatalf("default history result is not a page: %v", err)
	}
	if len(defaultPage.Events) == 0 {
		t.Fatal("default history returned no events")
	}
	for _, event := range defaultPage.Events {
		if event["kind"] == store.WorkConflictApplied {
			t.Fatalf("default history includes the conflict event %v; the published defaults must stay unchanged", event)
		}
		if _, ok := event["conflict_application"]; ok {
			t.Fatalf("ordinary event %v carries a conflict_application payload", event)
		}
	}
	created := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "history",
		Input:     json.RawMessage(`{"work_id":"work-a","event_kinds":["work.created"]}`),
	}, mutationEnvelope(grant, scopeVersion))
	if created.Outcome != OutcomeOK || created.Error != nil {
		t.Fatalf("ordinary filtered history = %+v, want an ok page", created.Error)
	}
	var createdPage struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(created.Result, &createdPage); err != nil {
		t.Fatalf("ordinary filtered history result is not a page: %v", err)
	}
	if len(createdPage.Events) != 1 || createdPage.Events[0]["kind"] != "work.created" {
		t.Fatalf("ordinary filtered history = %v, want exactly the work.created event", createdPage.Events)
	}
	if _, ok := createdPage.Events[0]["conflict_application"]; ok {
		t.Fatalf("ordinary event %v carries a conflict_application payload", createdPage.Events[0])
	}
}
