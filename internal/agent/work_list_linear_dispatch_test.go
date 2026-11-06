package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkListLinearKeyDispatch(t *testing.T) {
	s, service, grant, scope := captureTaskFixture(t)
	var ids []string
	for i, title := range []string{"ZZ-7001 in title", "Unrelated title"} {
		key := fmt.Sprintf("ZZ-700%d", i+1)
		input := fmt.Sprintf(`{"title":%q,"kind":"task","project_ids":["project-1"],"human_key":%q,"remote_issue_uuid":%q,"url":"https://example.com/issue/%s","idempotency_key":%q}`, title, key, "uuid-"+key, key, "capture-"+key)
		response := dispatchCapture(t, s, service, grant, scope, "request-"+key, input)
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("capture: %+v", response.Error)
		}
		ids = append(ids, (*response.ChangedRefs)[0].ID)
	}
	if err := pm1fixture.SeedWorkItem(context.Background(), s, "project-1", "work-unlinked", "Unlinked", 1); err != nil {
		t.Fatal(err)
	}
	read := func(input string) agentcontract.Envelope {
		t.Helper()
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: json.RawMessage(input)}, mutationEnvelope(grant, scope))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	for i, key := range []string{"ZZ-7001", "ZZ-7002"} {
		response := read(fmt.Sprintf(`{"linear_issue_key":%q}`, key))
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if response.Outcome != agentcontract.OutcomeOK || json.Unmarshal(response.Result, &page) != nil || len(page.Items) != 1 || page.Items[0]["id"] != ids[i] || page.Items[0]["linear_issue_key"] != key {
			t.Fatalf("filter %s: result=%s error=%+v", key, response.Result, response.Error)
		}
	}
	response := read(`{}`)
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if response.Outcome != agentcontract.OutcomeOK || json.Unmarshal(response.Result, &page) != nil {
		t.Fatalf("list: %+v", response.Error)
	}
	found := false
	for _, item := range page.Items {
		if item["id"] == "work-unlinked" {
			found = true
			if _, ok := item["linear_issue_key"]; ok {
				t.Fatalf("unlinked key present: %v", item)
			}
		}
	}
	if !found {
		t.Fatal("missing unlinked item")
	}
	bad := read(`{"linear_issue_key":"con-123"}`)
	if bad.Error == nil || bad.Error.Kind != "invalid_input" {
		t.Fatalf("malformed key: %+v", bad)
	}
	empty := read(`{"linear_issue_key":"ZZ-9999"}`)
	page.Items = nil
	if empty.Outcome != agentcontract.OutcomeOK || json.Unmarshal(empty.Result, &page) != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty: result=%s error=%+v", empty.Result, empty.Error)
	}
}

// A terminal linked item stays findable by its key beside the live item that
// recaptured the same issue: the filter matches every holder, and at most one
// match is live (TS3 2.2).
func TestWorkListLinearKeyMatchesTerminalHolder(t *testing.T) {
	s, _, _, _ := captureTaskFixture(t)
	ctx := context.Background()
	service, _, grant := newAuthorizedService(t, s, "terminal-client", "terminal-human", []agentcontract.Capability{"work_define", "work_transition", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scope, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	first := `{"title":"First","kind":"task","project_ids":["project-1"],"human_key":"ZZ-7100","remote_issue_uuid":"uuid-terminal-key","url":"https://example.com/issue/ZZ-7100","idempotency_key":"terminal-first"}`
	holder := dispatchCapture(t, s, service, grant, scope, "terminal-first", first)
	if holder.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("holder: %+v", holder.Error)
	}
	holderID := (*holder.ChangedRefs)[0].ID
	env := mutationEnvelope(grant, scope)
	env.RequestID = "terminal-intake"
	intakeInput, err := json.Marshal(map[string]any{"work_id": holderID, "expected_version": 3, "action_id": "record_intake", "idempotency_key": "terminal-intake", "fields": map[string]any{"outcome": "decline"}})
	if err != nil {
		t.Fatal(err)
	}
	intake, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: intakeInput}, env)
	if err != nil || intake.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("intake decline: %+v err=%v", intake.Error, err)
	}
	cancelInput, err := json.Marshal(map[string]any{"work_id": holderID, "expected_version": 4, "target": "cancelled", "reason": "superseded", "evidence": []map[string]string{{"kind": "verification", "authority": "test", "locator_kind": "file", "locator": "README.md"}}, "idempotency_key": "terminal-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: cancelInput}, env)
	if err != nil || cancelled.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("cancel: %+v err=%v", cancelled, err)
	}
	recapture := `{"title":"Second","kind":"task","project_ids":["project-1"],"human_key":"ZZ-7100","remote_issue_uuid":"uuid-terminal-key","url":"https://example.com/issue/ZZ-7100","idempotency_key":"terminal-second"}`
	fresh := dispatchCapture(t, s, service, grant, scope, "terminal-second", recapture)
	if fresh.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("recapture after terminal holder: %+v", fresh.Error)
	}
	list, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: json.RawMessage(`{"linear_issue_key":"ZZ-7100"}`)}, mutationEnvelope(grant, scope))
	if err != nil || list.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(list.Result, &page) != nil || len(page.Items) != 2 {
		t.Fatalf("terminal holder must stay findable beside its live recapture: %s", list.Result)
	}
	live := 0
	for _, item := range page.Items {
		if item["lifecycle"] == "in_progress" || item["lifecycle"] == "needed" {
			live++
		}
		if _, ok := item["linear_issue_key"]; !ok {
			t.Fatalf("linked item without key: %v", item)
		}
	}
	if live != 1 {
		t.Fatalf("exactly one holder is live: %s", list.Result)
	}
}
