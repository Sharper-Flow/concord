package agent

import (
	"context"
	"encoding/json"
	"errors"
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

// AJ8-health-failure-rollback: an approved production routing change is
// applied, its health check fails, and the declared rollback runs. Concord
// never calls the provider (CD-0039 D9): the native authority reports each
// phase through typed workflow-action fields, Concord folds one attributed
// native-run event per phase, and the logical operation completes partial —
// the native steps succeeded, the approved change did not.
// bindAJ8HealthFailureRollback carries the AJ8 corpus row's retired
// ops-runbook authoring into its CD-0218 disposition: the native
// start/health/rollback choreography left the live catalog with its family,
// so the corpus witness becomes the archived-family refusal — the nonterminal
// retired instance a serving release leaves behind refuses every public
// workflow action with zero effects, and the retained native-run reader keeps
// the recorded-history shape the frozen journals cover.
func bindAJ8HealthFailureRollback(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// No archived family registers a definition (CD-0242 D14), so the
	// nonterminal archived instance is the recorded projection a pre-cutover
	// store carries: a current instance whose pin names the archived family.
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentWorkflowSourceProof(t, context.Background(), s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize the instance: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET definition_ref='workflow.ops_runbook' WHERE work_id='work-1';
		DELETE FROM write_guard`); err != nil {
		t.Fatalf("pin the archived family: %v", err)
	}
	version := int64(0)
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
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

	// Every public action on the nonterminal archived instance refuses with
	// the archived-family answer and commits nothing — the approval-gated
	// contract first, then the retired native actions by id.
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
			t.Fatalf("%s on the archived instance = %+v, want a refusal", actionID, challenge.Error)
		}
		// A live action id reaches the archived-family gate and names it; a
		// retired id refuses at the closed union before any authority read.
		if actionID == "approve_contract" && !strings.Contains(challenge.Error.Message, "workflow.ops_runbook is archived") {
			t.Fatalf("approve_contract on the archived instance = %+v, want the archived-family refusal", challenge.Error)
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

	// No replayed journal reader survives (CD-0242 D2), and no archived
	// definition is registered (D14): the workflow read names the archived
	// family and its migration route instead of an unexplained absence (D13).
	_, err = store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1", Limit: 20})
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindInvariantViolation || !strings.Contains(failure.Detail, "workflow.ops_runbook is archived") {
		t.Fatalf("continuity read on the archived instance = %v, want the archived-family answer", err)
	}

	obs := envelopeToObservation(agentcontract.Envelope{Outcome: agentcontract.OutcomeOK})
	obs.State = map[string]any{"archived_family": "workflow.ops_runbook", "refused_actions": "approve_contract,approve_operation,start_run,record_health,rollback_run", "zero_effects": true}
	obs.Effects["adapter_domain_logic"] = probedAbsent{
		Evidence: "the retired authoring refuses before any write; no adapter inference path exists to probe because no native action is authorable",
	}
	return obs
}
