package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// bindAJ4CurrentLifecycleAfterContractRevision checks retained lifecycle rows
// after an ordinary contract correction.
func bindAJ4CurrentLifecycleAfterContractRevision(t *testing.T, _ jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition", "product_read"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	plan, version := dispatchWorkflowActionResult(t, s, service, env, version, "record_plan", agentPlanFields(), "aj4-history-plan")
	if plan.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record_plan: %+v", plan.Error)
	}
	approved, _ := approvedWorkflowContract(t, s, service, env, version, workflowContractFieldsForSeededRegistry(t, s), "aj4-history-approve")
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract = %+v", approved.Error)
	}
	version = workOneVersion(t, s)
	revised, _ := dispatchContractRevisionResult(t, s, service, env, "work-1", version, map[string]any{
		"contract_version":     2,
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
		"legislative_subjects": []string{},
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
	}, "aj4-history-revise")
	if revised.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("revise_intent = %+v", revised.Error)
	}

	revisionBefore, err := s.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: json.RawMessage(`{"work_id":"work-1","record_kinds":["lifecycle"]}`)}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("history = %+v", resp.Error)
	}
	var page struct {
		Entries []struct {
			Kind string `json:"entry_kind"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal history page: %v", err)
	}

	obs := envelopeToObservation(resp)
	kinds := []any{}
	for _, entry := range page.Entries {
		kinds = append(kinds, entry.Kind)
	}
	obs.Result = map[string]any{"entry_kinds": kinds}
	obs.Effects = map[string]any{"history_mutation": probedAbsent{}}
	after, err := s.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after != revisionBefore {
		obs.Effects["history_mutation"] = after - revisionBefore
	}
	return obs
}
