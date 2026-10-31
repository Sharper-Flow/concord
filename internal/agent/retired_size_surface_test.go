package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestWorkScopeOmitsRetiredSize(t *testing.T) {
	s, service, grant, scopeVersion := captureTaskFixture(t)
	capture := dispatchCapture(t, s, service, grant, scopeVersion, "size-absence-capture",
		`{"title":"Need","value_statement":"Why","kind":"task","project_ids":["project-1"],"idempotency_key":"size-absence"}`)
	if capture.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("capture: %+v", capture)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "size-absence-read"
	input, err := json.Marshal(map[string]string{"product_id": "product-1", "work_id": (*capture.ChangedRefs)[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: input}, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("scope: %+v, %v", response, err)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Work map[string]json.RawMessage `json:"work"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Work == nil {
		t.Fatalf("missing work in %s", raw)
	}
	if _, found := result.Work["size"]; found {
		t.Fatalf("retired size in %s", raw)
	}
}
