package agent

import (
	"context"
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
	for _, action := range []string{"record_proposal", "record_alignment"} {
		invokeWorkflowIssue31Action(t, s, service, env, action, version, "design-correction-"+action)
		version = workOneVersion(t, s)
	}
	fields := workflowContractFieldsForSeededRegistry(t, s)
	fields["required_evidence"] = []string{"verification"}
	fields["route_conventions"] = []string{}
	fields["rigor_class"] = "prototype_internal"
	approved, _ := approvedWorkflowContract(t, s, service, env, version, fields, "design-correction-approve")
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve contract: %+v", approved.Error)
	}
	version = recordApprovedWorkDesign(t, s, service, env, "work-1", "design-correction-design")
	before, err := store.ReadWorkflowContinuity(ctx, s, store.ContinuityRequest{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if before.DesignRecord == nil {
		t.Fatal("initial approved contract has no design record")
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
	if !reflect.DeepEqual(after.DesignRecord, before.DesignRecord) {
		t.Fatalf("contract-only reassessment changed the design: before=%+v after=%+v", before.DesignRecord, after.DesignRecord)
	}
	fields["contract_version"] = 3
	fields["premise"] = "Use a replacement approach rather than the original design"
	fields["design_record"] = map[string]any{
		"approach":     "Use the replacement approach",
		"decisions":    []map[string]any{{"id": "decision:replacement", "question": "Which approach?", "choice": "replacement", "rationale": "Matches the corrected objective", "rejected": []string{"original"}}},
		"touched_refs": []string{"internal/store/workflow_design.go"},
	}
	eventsBefore := workEventCount(t, s, "work-1")
	designsBefore := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`)
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
	if !reflect.DeepEqual(unchanged.Contract, after.Contract) || !reflect.DeepEqual(unchanged.DesignRecord, after.DesignRecord) {
		t.Fatal("invalid replacement changed the current contract or design")
	}
	if workEventCount(t, s, "work-1") != eventsBefore || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`) != designsBefore {
		t.Fatal("invalid replacement wrote an event or design")
	}
}
