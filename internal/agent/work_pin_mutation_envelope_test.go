package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestMutationEnvelopePublishesActiveContractObligations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	actor := store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef)
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'fixture premise','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal');
		INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES('work-1',1,'product-1','root','sha256:' || printf('%064d',0));
		INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES('work-1',1,'law:fixture','verification'),('work-1',1,'law:fixture','review');
		DELETE FROM write_guard`, actor)
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	input := retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_plan", "idempotency_key": "obligation-envelope", "fields": map[string]any{
		"outcomes": "Working outcome", "approach": "Bounded change", "decisions": []map[string]any{{"id": "decision:one", "question": "Which approach?", "choice": "One approach", "rationale": "It fits", "rejected": []string{}}}, "touches": []string{"file:one"}, "merge_split": "One item", "risks": "Bounded risk",
	}})
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, mutationEnvelope(grant, scopeVersion))
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record_plan: %+v", response.Error)
	}
	var payload struct {
		WorkPins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	want := []string{"review", "verification"}
	if len(payload.WorkPins) != 1 || payload.WorkPins[0].WorkID != "work-1" || payload.WorkPins[0].WorkflowType != "change" || payload.WorkPins[0].Obligations == nil || !reflect.DeepEqual(*payload.WorkPins[0].Obligations, want) {
		t.Fatalf("mutation work pins=%+v, want change plan and obligations %v", payload.WorkPins, want)
	}
	var raw struct {
		WorkPins []map[string]json.RawMessage `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &raw); err != nil || len(raw.WorkPins) != 1 {
		t.Fatalf("raw mutation work pins=%+v err=%v", raw.WorkPins, err)
	}
	var obligations []string
	if err := json.Unmarshal(raw.WorkPins[0]["obligations"], &obligations); err != nil || !reflect.DeepEqual(obligations, want) {
		t.Fatalf("raw obligations=%v err=%v, want %v", obligations, err, want)
	}
}
