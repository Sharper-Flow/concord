package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestContractReassessmentPreservesDesignUntilExplicitReplacement(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, action := range []string{"record_proposal", "record_alignment", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, action, version, "design-correction-"+action)
		version = workOneVersion(t, s)
	}
	fields := workflowContractFieldsFixture()
	fields["required_evidence"] = []string{"verification"}
	fields["route_conventions"] = []string{}
	fields["rigor_class"] = "prototype_internal"
	approved, version := approvedWorkflowContract(t, s, service, grant, privateKey, env, version, fields, "design-correction-approve")
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approve contract: %+v", approved.Error)
	}
	before, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if before.DesignRecord == nil {
		t.Fatal("initial approved contract has no design record")
	}
	fields["contract_version"] = 2
	fields["supersede_reason"] = "reassess the unchanged approved outcome and architecture"
	fields["audit_evidence"] = []string{"evidence:design-correction"}
	corrected, version := approvedWorkflowActionWithVersions(t, s, service, grant, privateKey, env, version, "supersede_contract", fields, "design-correction-supersede", map[string]any{"work": version, "contract": before.Contract.Version})
	if corrected.Outcome != OutcomeOK {
		t.Fatalf("supersede contract: %+v", corrected.Error)
	}
	after, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Contract == nil || after.Contract.Version != 2 {
		t.Fatalf("corrected contract not current: %+v", after.Contract)
	}
	if !reflect.DeepEqual(after.DesignRecord, before.DesignRecord) {
		t.Fatalf("contract-only reassessment changed the design: before=%+v after=%+v", before.DesignRecord, after.DesignRecord)
	}
	fields["contract_version"] = 3
	fields["premise"] = "Use a replacement approach rather than the original design"
	fields["supersede_reason"] = "replace the architecture explicitly with the corrected objective"
	fields["design_record"] = map[string]any{
		"approach":     "Use the replacement approach",
		"decisions":    []map[string]any{{"id": "decision:replacement", "question": "Which approach?", "choice": "replacement", "rationale": "Matches the corrected objective", "rejected": []string{"original"}}},
		"touched_refs": []string{"internal/store/workflow_design.go"},
	}
	replaced, version := approvedWorkflowActionWithVersions(t, s, service, grant, privateKey, env, version, "supersede_contract", fields, "design-correction-replace", map[string]any{"work": version, "contract": 2})
	if replaced.Outcome != OutcomeOK {
		t.Fatalf("replace design with contract: %+v", replaced.Error)
	}
	current, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if current.Contract == nil || current.Contract.Version != 3 || current.DesignRecord == nil || current.DesignRecord.Approach != "Use the replacement approach" {
		t.Fatalf("replacement pair not current: contract=%+v design=%+v", current.Contract, current.DesignRecord)
	}
	wantDecisions := []store.WorkflowDesignDecision{{ID: "decision:replacement", Question: "Which approach?", Choice: "replacement", Rationale: "Matches the corrected objective", Rejected: []string{"original"}}}
	if !reflect.DeepEqual(current.DesignRecord.Decisions, wantDecisions) || !reflect.DeepEqual(current.DesignRecord.TouchedRefs, []string{"internal/store/workflow_design.go"}) || current.DesignRecord.WorkVersion <= after.DesignRecord.WorkVersion {
		t.Fatalf("replacement content was not recorded: %+v", current.DesignRecord)
	}
	eventsBefore := workEventCount(t, s, "work-1")
	designsBefore := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`)
	fields["contract_version"] = 4
	fields["design_record"].(map[string]any)["decisions"].([]map[string]any)[0]["choice"] = strings.Repeat("x", 1025)
	raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": fields, "idempotency_key": "design-correction-invalid"})
	if err != nil {
		t.Fatal(err)
	}
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" {
		t.Fatalf("oversized replacement accepted: %+v", refused.Error)
	}
	if workOneVersion(t, s) != version {
		t.Fatal("invalid replacement changed work version")
	}
	unchanged, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unchanged.Contract, current.Contract) || !reflect.DeepEqual(unchanged.DesignRecord, current.DesignRecord) {
		t.Fatal("invalid replacement changed the current contract or design")
	}
	if workEventCount(t, s, "work-1") != eventsBefore || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`) != designsBefore {
		t.Fatal("invalid replacement wrote an event or design")
	}
}
