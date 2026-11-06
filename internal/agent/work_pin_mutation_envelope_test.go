package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestMutationEnvelopePublishesCompleteWorkPin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	actor := store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef)
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version) VALUES('work-1',1,'internal_sqlite','now',?,'[]','[]',1);
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
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", response.Result, true); err != nil {
		t.Fatalf("mutation result did not satisfy its published schema: %v", err)
	}
	current, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.WorkPins) != 1 || payload.WorkPins[0].WorkID != "work-1" || payload.WorkPins[0].WorkflowType != "change" || !reflect.DeepEqual(payload.WorkPins[0], current) {
		t.Fatalf("mutation work pins=%+v, want current complete work pin %+v", payload.WorkPins, current)
	}
	var raw struct {
		WorkPins []map[string]json.RawMessage `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &raw); err != nil || len(raw.WorkPins) != 1 {
		t.Fatalf("raw mutation work pins=%+v err=%v", raw.WorkPins, err)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &expected); err != nil || !reflect.DeepEqual(raw.WorkPins[0], expected) {
		t.Fatalf("wire work pin=%v, want complete current pin %s: %v", raw.WorkPins[0], encoded, err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_verification_obligations WHERE work_id='work-1' AND contract_version=1 AND law_id='law:fixture' AND obligation_id IN ('review','verification')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("active contract verification obligations count=%d, want 2: %v", count, err)
	}
}
