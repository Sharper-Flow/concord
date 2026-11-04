package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func workEventCount(t *testing.T, s *store.Store, workID string) int {
	t.Helper()
	count := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&count); err != nil {
		t.Fatalf("count events for %s: %v", workID, err)
	}
	return count
}

func approvedWorkflowContract(t *testing.T, s *store.Store, service *Service, env CallEnvelope, version int64, fields map[string]any, key string) (agentcontract.Envelope, int64) {
	t.Helper()
	return dispatchWorkflowActionResult(t, s, service, env, version, "approve_contract", fields, key)
}

func dispatchWorkflowActionResult(t *testing.T, s *store.Store, service *Service, env CallEnvelope, version int64, action string, fields map[string]any, key string) (agentcontract.Envelope, int64) {
	t.Helper()
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "idempotency_key": key}
	if fields != nil {
		input["fields"] = fields
	} else {
		input["fields"] = map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	var newVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&newVersion); err != nil {
		t.Fatal(err)
	}
	return resp, newVersion
}

// AJ8 checks retired actions refuse on an intake-parked item with no effects.
// Workflow-engine-contract §§2.1, 5.1, 11 and 12 retire definition pins and
// admit actions from the current plan instead.
func bindAJ8HealthFailureRollback(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	ctx := agentWorkflowSourceProof(t, context.Background(), s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize the instance: %v", err)
	}
	version := workVersion(t, s, "work-1")
	intake, version := dispatchWorkflowActionResult(t, s, service, env, version, "record_intake", map[string]any{"outcome": "proceed", "open_questions_found": false}, "aj8-intake")
	if intake.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record intake: %+v", intake.Error)
	}
	var stateName string
	var workflowType *string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state,workflow_type FROM workflow_instances WHERE work_id='work-1'`).Scan(&stateName, &workflowType); err != nil || stateName != "intake" || workflowType != nil {
		t.Fatalf("expected untyped intake instance: state=%s type=%v err=%v", stateName, workflowType, err)
	}

	const runID = "run-routing-1"
	state := func() (int64, int64) {
		t.Helper()
		var events, contracts int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id='work-1'`).Scan(&contracts); err != nil {
			t.Fatal(err)
		}
		return events, contracts
	}
	nativeFields := func(phase, status string) map[string]any {
		digit := map[string]string{"start": "1", "health": "2", "rollback": "3"}[phase]
		return map[string]any{"run_id": runID, "native_subject_ref": "routing-provider:prod-edge", "status": status, "evidence_ref": "https://evidence.invalid/runs/" + runID + "/" + phase, "evidence_digest": "sha256:" + strings.Repeat(digit, 64), "asserted_at": fixedTime().Format("2006-01-02T15:04:05Z")}
	}

	// Contract approval and the retired native actions refuse without effects.
	baselineEvents, baselineContracts := state()
	baselineEffects := agentCurrentEffects(t, s)
	refuse := func(actionID string, fields map[string]any, key string) {
		t.Helper()
		input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": actionID, "idempotency_key": key}
		if fields != nil {
			input["fields"] = fields
		}
		challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if challenge.Outcome != agentcontract.OutcomeError || challenge.Error == nil {
			t.Fatalf("%s on intake = %+v, want a refusal", actionID, challenge.Error)
		}
		if actionID == "approve_contract" && challenge.Error.Kind != "invalid_transition" {
			t.Fatalf("approve_contract on intake = %+v, want invalid_transition", challenge.Error)
		}
		events, contracts := state()
		if events != baselineEvents || contracts != baselineContracts {
			t.Fatalf("%s refusal wrote state: events=%d/%d contracts=%d/%d", actionID, baselineEvents, events, baselineContracts, contracts)
		}
		if agentCurrentEffects(t, s) != baselineEffects {
			t.Fatalf("%s refusal changed current rows or receipts", actionID)
		}
	}
	refuse("approve_contract", workflowContractFieldsFixture(), "aj8-rollback-contract")
	refuse("approve_operation", nil, "aj8-rollback-operation")
	refuse("start_run", nativeFields("start", "started"), "aj8-rollback-start")
	refuse("record_health", nativeFields("health", "failed"), "aj8-rollback-health")
	refuse("rollback_run", nativeFields("rollback", "rolled_back"), "aj8-rollback-run")

	obs := envelopeToObservation(agentcontract.Envelope{Outcome: agentcontract.OutcomeOK})
	obs.State = map[string]any{"refused_actions": "approve_contract,approve_operation,start_run,record_health,rollback_run", "zero_effects": true}
	obs.Effects["adapter_domain_logic"] = probedAbsent{
		Evidence: "the retired authoring refuses before any write; no adapter inference path exists to probe because no native action is authorable",
	}
	return obs
}
