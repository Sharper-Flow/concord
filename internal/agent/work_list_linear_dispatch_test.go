package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
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
