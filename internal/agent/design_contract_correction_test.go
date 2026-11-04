package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestContractReassessmentPreservesDesignAndRefusesInlineReplacement(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	plan := agentPlanFields()
	spineOK(t, dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_plan", "fields": plan, "idempotency_key": "design-plan"})}, env))
	version = workOneVersion(t, s)
	fields := workflowContractFieldsForSeededRegistry(t, s)
	fields["route_conventions"] = []string{}
	fields["rigor_class"] = "prototype_internal"
	approved, _ := approvedWorkflowContract(t, s, service, env, version, fields, "design-correction-approve")
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve contract: %+v", approved.Error)
	}
	version = workOneVersion(t, s)
	plan["approach"] = "A revised implementation boundary"
	plan["reason"] = "Clarify the implementation boundary"
	spineOK(t, dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_plan", "fields": plan, "idempotency_key": "design-plan-revision"})}, env))
	version = workOneVersion(t, s)
	var beforePlan string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_plan_records WHERE work_id='work-1'`).Scan(&beforePlan); err != nil {
		t.Fatal(err)
	}
	var revisedPlan map[string]any
	if err := json.Unmarshal([]byte(beforePlan), &revisedPlan); err != nil || revisedPlan["approach"] != plan["approach"] || revisedPlan["reason"] != plan["reason"] {
		t.Fatalf("plan revision not retained: %s, err=%v", beforePlan, err)
	}
	fields["contract_version"] = 2
	corrected, version := dispatchContractRevisionResult(t, s, service, env, "work-1", version, fields, "design-correction-revise")
	if corrected.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("revise contract: %+v", corrected.Error)
	}
	after, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Contract == nil || after.Contract.Version != 2 {
		t.Fatalf("corrected contract not current: %+v", after.Contract)
	}
	var afterPlan string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_plan_records WHERE work_id='work-1'`).Scan(&afterPlan); err != nil || afterPlan != beforePlan {
		t.Fatalf("contract-only reassessment changed the plan: before=%s after=%s err=%v", beforePlan, afterPlan, err)
	}
	fields["contract_version"] = 3
	fields["premise"] = "Use a replacement approach rather than the original design"
	fields["design_record"] = map[string]any{
		"approach":     "Use the replacement approach",
		"decisions":    []map[string]any{{"id": "decision:replacement", "question": "Which approach?", "choice": "replacement", "rationale": "Matches the corrected objective", "rejected": []string{"original"}}},
		"touched_refs": []string{"internal/store/workflow_design.go"},
	}
	eventsBefore := workEventCount(t, s, "work-1")
	refused, _ := dispatchContractRevisionResult(t, s, service, env, "work-1", version, fields, "design-correction-invalid")
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" {
		t.Fatalf("inline design replacement accepted: %+v", refused.Error)
	}
	if workOneVersion(t, s) != version {
		t.Fatal("invalid replacement changed work version")
	}
	unchanged, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unchanged.Contract, after.Contract) {
		t.Fatal("invalid replacement changed the current contract or design")
	}
	var unchangedPlan string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_plan_records WHERE work_id='work-1'`).Scan(&unchangedPlan); err != nil || unchangedPlan != afterPlan || workEventCount(t, s, "work-1") != eventsBefore {
		t.Fatal("invalid replacement wrote an event or design")
	}
}
