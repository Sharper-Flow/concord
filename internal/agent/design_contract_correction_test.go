package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestContractCorrectionDoesNotPinAnObsoleteDesign(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version := seedAgentWorkflow(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, action := range []string{"record_proposal", "record_alignment", "record_discovery", "record_design"} {
		invokeWorkflowIssue31Action(t, s, service, env, "work-1", action, version, "design-correction-"+action)
		version = workflowIssue31Version(t, s)
	}
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingFixture()
	approved, version := approvedOpsAction(t, s, service, grant, privateKey, env, version, "approve_contract", fields, "design-correction-approve")
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
	fields["premise"] = "Use a replacement approach rather than the original design"
	fields["required_evidence"] = []string{"verification"}
	fields["route_conventions"] = []string{}
	fields["rigor_class"] = "prototype_internal"
	fields["supersede_reason"] = "the operator rejected the earlier approach"
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
	if after.DesignRecord != nil {
		t.Fatalf("contract v2 still pins the obsolete design from work version %d", after.DesignRecord.WorkVersion)
	}
	if after.WorkPin == nil {
		t.Fatal("continuity has no work pin")
	}
	for _, intent := range after.WorkPin.NextValidIntents {
		if intent.ActionID == "dispatch_worker" {
			t.Fatal("work pin advertises dispatch with an obsolete design")
		}
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`); got != 1 {
		t.Fatalf("historical design records = %d, want 1", got)
	}
	dispatchEnv := workflowDispatchWorktreeFixture(t, s, env)
	dispatch := func(version int64, key string) Envelope {
		t.Helper()
		var lane store.LaneDefinition
		for _, candidate := range store.BuiltinLaneDefinitions() {
			if candidate.ID == "verify" {
				lane = candidate
			}
		}
		payload, err := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": key,
			"fields": map[string]any{"attempt_id": "attempt:" + key, "worker_packet": map[string]any{
				"schema_version": "1.0", "attempt_id": "attempt:" + key, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
				"work_id": "work-1", "step_id": "execution", "inputs": map[string]any{"task": "Verify the corrected design"},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: payload}, dispatchEnv)
	}
	staleDispatch := dispatch(version, "design-stale-dispatch")
	if staleDispatch.Outcome != OutcomeError || staleDispatch.Error == nil || staleDispatch.Error.Kind != "missing_evidence" || !strings.Contains(staleDispatch.Error.Message, "design") {
		t.Fatalf("obsolete design did not block dispatch: %+v", staleDispatch.Error)
	}
	if workflowIssue31Version(t, s) != version {
		t.Fatal("refused dispatch changed work")
	}
	definition, err := store.VerifyWorkflowInstanceDefinition(ctx, s, store.BuiltinWorkflowRegistry(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	fields["contract_version"] = 3
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
	if current.WorkPin == nil {
		t.Fatal("corrected continuity has no work pin")
	}
	dispatchVisible := false
	for _, intent := range current.WorkPin.NextValidIntents {
		dispatchVisible = dispatchVisible || intent.ActionID == "dispatch_worker"
	}
	if !dispatchVisible {
		t.Fatal("current design did not restore dispatch discovery")
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`); got != 2 {
		t.Fatalf("replacement lost design history: got %d rows", got)
	}
	afterDefinition, err := store.VerifyWorkflowInstanceDefinition(ctx, s, store.BuiltinWorkflowRegistry(), "work-1")
	if err != nil || afterDefinition.Digest != definition.Digest {
		t.Fatalf("correction changed the pinned definition: %+v, %v", afterDefinition, err)
	}
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
	if workflowIssue31Version(t, s) != version {
		t.Fatal("invalid replacement changed work version")
	}
	currentDispatch := dispatch(version, "design-current-dispatch")
	if currentDispatch.Outcome != OutcomeOK {
		t.Fatalf("current replacement did not restore dispatch admission: %+v", currentDispatch.Error)
	}
}
