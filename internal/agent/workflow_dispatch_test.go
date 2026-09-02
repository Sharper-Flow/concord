package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func seedAgentWorkflow(t *testing.T, s *store.Store, grant Authority) int64 {
	t.Helper()
	ctx := context.Background()
	seedCurrentWorkflowDomainFixture(t, s)
	definition := store.BuiltinWorkflowDefinitions()[0]
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	version, err := workVersionForWorkflow(t, s)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func workVersionForWorkflow(t *testing.T, s *store.Store) (int64, error) {
	t.Helper()
	var version int64
	err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version)
	return version, err
}

func TestWorkflowActionDispatchUsesStrictPreflightAuthApprovalAndReplayPath(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	if version != 4 {
		t.Fatalf("workflow seed version=%d, want 4", version)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	unknown := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"unknown-action","idempotency_key":"wf-unknown"}`)}
	refused, err := Dispatch(context.Background(), s, service, unknown, env)
	if err != nil || refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_transition" {
		t.Fatalf("unknown workflow action response=%+v err=%v", refused, err)
	}
	var completed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("malformed/unknown request reached authority: completed=%d", completed)
	}
	duplicate := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","action_id":"unknown-action","idempotency_key":"wf-duplicate"}`)}
	duplicateResponse, err := Dispatch(context.Background(), s, service, duplicate, env)
	if err != nil || duplicateResponse.Outcome != OutcomeError || duplicateResponse.Error == nil || duplicateResponse.Error.Kind != "invalid_input" {
		t.Fatalf("duplicate JSON response=%+v err=%v", duplicateResponse, err)
	}

	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":[],"idempotency_key":"wf-record-proposal"}`)}
	first, err := Dispatch(context.Background(), s, service, request, env)
	if err != nil || first.Outcome != OutcomeOK || first.Error != nil || len(*first.ChangedRefs) != 1 {
		t.Fatalf("workflow action response=%+v err=%v", first, err)
	}
	if (*first.ChangedRefs)[0].Version != "5" {
		t.Fatalf("workflow action changed version=%s, want 5", (*first.ChangedRefs)[0].Version)
	}
	var operations, records int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id LIKE 'workflow-%'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='workflow_action'`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || records != 1 {
		t.Fatalf("workflow durable state operations=%d idempotency=%d", operations, records)
	}
	var contractVersion string
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_digest FROM durable_operations WHERE op_id LIKE 'workflow-%'`).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	if contractVersion != ManifestDigest {
		t.Fatalf("workflow durable contract digest=%s, want %s", contractVersion, ManifestDigest)
	}

	replay, err := Dispatch(context.Background(), s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed || len(*replay.ChangedRefs) != 1 {
		if replay.Error != nil {
			t.Fatalf("workflow replay response=%+v error=%+v err=%v", replay, *replay.Error, err)
		}
		t.Fatalf("workflow replay response=%+v err=%v", replay, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkflowActionCompleted).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("replay emitted duplicate action events=%d", completed)
	}
}

func TestWorkflowActionAvailabilityPrecedesPayloadAndAuthorityValidation(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "workflow_action",
		// The payload is intentionally incomplete. Strict boundary validation
		// must be reported before pinned-instance or grant validation.
		Input: json.RawMessage(`{"work_id":"missing-work","fields":[]}`),
	}
	response, dispatchErr := Dispatch(context.Background(), s, service, request, env)
	if dispatchErr != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("malformed workflow request response=%+v err=%v", response, dispatchErr)
	}
}

func TestWorkflowActionReplayVectorsUseInvokeAndAuthoritativeDurableResults(t *testing.T) {
	for _, vector := range []struct {
		name           string
		resultKind     string
		want           Outcome
		operationState OperationState
		retrySafe      bool
	}{
		{name: "current success", resultKind: "completed", want: OutcomeOK},
		{name: "current pending", resultKind: "pending", want: OutcomePending, operationState: OperationPending},
		{name: "partial", resultKind: "partial", want: OutcomePartial, operationState: OperationPartial, retrySafe: true},
		{name: "current failure", resultKind: "failed", want: OutcomePartial, operationState: OperationFailed},
		{name: "failed stale", resultKind: "failed_stale", want: OutcomePartial, operationState: OperationFailed},
	} {
		t.Run(vector.name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
			if got := seedAgentWorkflow(t, s, grant); got != 4 {
				t.Fatalf("workflow seed version=%d, want 4", got)
			}
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			input := json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":[],"idempotency_key":"legacy-replay-` + strings.ReplaceAll(vector.name, " ", "-") + `"}`)
			opID := seedCurrentWorkflowActionReplay(t, s, env, input, vector.resultKind)
			beforeEvents := countWorkflowEvents(t, s)
			beforeVersion := workflowReplayWorkVersion(t, s)
			beforeChallenges := countWorkflowApprovalChallenges(t, s)

			data, marshalErr := json.Marshal(struct {
				CallEnvelope CallEnvelope    `json:"call_envelope"`
				Tool         string          `json:"tool"`
				Operation    string          `json:"operation"`
				Input        json.RawMessage `json:"input"`
			}{CallEnvelope: env, Tool: "concord_work_transition", Operation: "workflow_action", Input: input})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			response, invokeErr := Invoke(context.Background(), s, service, data)
			if invokeErr != nil {
				t.Fatal(invokeErr)
			}
			if response.Outcome != vector.want || !response.Replayed || (vector.want != OutcomeOK && response.OperationRef == nil) {
				if response.Error != nil {
					t.Fatalf("%s replay outcome=%s replayed=%t error=%s message=%s", vector.name, response.Outcome, response.Replayed, response.Error.Kind, response.Error.Message)
				}
				t.Fatalf("%s replay response=%+v", vector.name, response)
			}
			if vector.want == OutcomeOK && response.Error != nil {
				t.Fatalf("successful replay returned error=%+v", response.Error)
			}
			if vector.want == OutcomePending && (response.OperationRef == nil || response.OperationRef.State != OperationPending) {
				t.Fatalf("pending replay operation_ref=%+v", response.OperationRef)
			}
			if vector.want == OutcomePartial && (response.OperationRef == nil || response.OperationRef.State != vector.operationState || response.Error == nil || response.Error.Kind != "operation_conflict" || response.Error.RetrySafe != vector.retrySafe || response.Error.RecoveryAction.Kind != "reconcile_operation") {
				t.Fatalf("failed replay operation_ref=%+v error=%+v", response.OperationRef, response.Error)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("replay envelope validation: %v", err)
			}
			var durableResultKind string
			if err := s.DatabaseForTesting().QueryRow(`SELECT result_kind FROM durable_operations WHERE op_id=?`, opID).Scan(&durableResultKind); err != nil {
				t.Fatal(err)
			}
			if durableResultKind != vector.resultKind {
				t.Fatalf("replay changed durable result classification from %q to %q", vector.resultKind, durableResultKind)
			}
			if got := countWorkflowEvents(t, s); got != beforeEvents {
				t.Fatalf("replay changed event count from %d to %d (op=%s)", beforeEvents, got, opID)
			}
			if got := workflowReplayWorkVersion(t, s); got != beforeVersion {
				t.Fatalf("replay changed work version from %d to %d", beforeVersion, got)
			}
			if got := countWorkflowApprovalChallenges(t, s); got != beforeChallenges {
				t.Fatalf("replay changed approval challenges from %d to %d", beforeChallenges, got)
			}
		})
	}

}

func TestWorkflowActionRejectsLegacyEventReplay(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool: "concord_work_transition", Operation: "workflow_action",
		Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"replay","fields":[],"idempotency_key":"legacy-event-replay"}`),
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_transition" {
		t.Fatalf("legacy event replay response=%+v", response)
	}
}

func TestWorkflowActionReplayRejectsOldSurfaceResultShapeAndDigest(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":[],"idempotency_key":"current-replay-shape"}`)
	opID := seedCurrentWorkflowActionReplay(t, s, env, input, "completed")
	if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_payload=? WHERE op_id=?`, `{"changed_refs":["work-1"],"next_valid_intents":[],"operation_id":"`+opID+`"}`, opID); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{CallEnvelope: env, Tool: "concord_work_transition", Operation: "workflow_action", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, data)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" {
		t.Fatalf("old result shape replay response=%+v", response)
	}
}

func seedCurrentWorkflowActionReplay(t *testing.T, s *store.Store, env CallEnvelope, input json.RawMessage, resultKind string) string {
	t.Helper()
	digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
	opID := "workflow-" + digest[7:31]
	resultPayload := `{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":5}],"next_valid_intents":[],"operation_id":"` + opID + `"}`
	idempotencyChanged := "[]"
	changedRef := `{"entity_kind":"work_item","id":"work-1","version":5}`
	changedRefs := `[` + strconv.Quote(changedRef) + `]`
	scope := `{"product_id":"product-1","project_ids":["project-1"],"work_ids":["work-1"],"scope_version":"` + env.ScopeVersion + `"}`
	definition := store.BuiltinWorkflowDefinitions()[0]
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, opID, 1, "work-1", definition.Ref, definition.Version, "proposal", "internal_sqlite", "sha256:legacy-input", scope, "completed", resultPayload, "[]", changedRefs, env.PrincipalRef, env.RequestID, fixedTime().Format(time.RFC3339Nano), fixedTime().Format(time.RFC3339Nano), ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if resultKind != "completed" {
		if _, err := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_kind=? WHERE op_id=?`, resultKind, opID); err != nil {
			if resultKind == "future_result" {
				if _, pragmaErr := s.DatabaseForTesting().Exec(`PRAGMA ignore_check_constraints=ON`); pragmaErr != nil {
					t.Fatal(pragmaErr)
				}
				if _, updateErr := s.DatabaseForTesting().Exec(`UPDATE durable_operations SET result_kind=? WHERE op_id=?`, resultKind, opID); updateErr != nil {
					t.Fatal(updateErr)
				}
			} else {
				t.Fatal(err)
			}
		}
	}
	_, err = s.DatabaseForTesting().Exec(`INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at,last_observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, env.PrincipalRef, "concord_work_transition", "workflow_action", idempotencyKey(input), digest, opID, "[]", `{"changed_refs":[],"next_valid_intents":[]}`, idempotencyChanged, scope, fixedTime().Format(time.RFC3339Nano), fixedTime().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	return opID
}

func workflowReplayWorkVersion(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func countWorkflowEvents(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countWorkflowApprovalChallenges(t *testing.T, s *store.Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approval_challenges`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkflowActionDispatchUsesDefinitionApprovalChallenge(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for i, action := range []string{"record_proposal", "record_discovery", "record_design"} {
		input := json.RawMessage(`{"work_id":"work-1","expected_version":` + string(rune('4'+i)) + `,"action_id":"` + action + `","idempotency_key":"wf-advance-` + action + `"}`)
		response, dispatchErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
		if dispatchErr != nil || response.Outcome != OutcomeOK {
			t.Fatalf("advance action=%s response=%+v err=%v", action, response, dispatchErr)
		}
	}
	input := workflowContractActionInput(t, "work-1", 7, "wf-approve-contract", "")
	challenge, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("approval challenge response=%+v err=%v", challenge, err)
	}
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest("concord_work_transition", "workflow_action", env, input)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 7}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "workflow-approval-0001")
	var durableBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE workflow_type_ref LIKE 'workflow.%'`).Scan(&durableBefore); err != nil {
		t.Fatal(err)
	}
	if durableBefore != 3 {
		t.Fatalf("approval challenge durable operation count=%d, want 3 prior actions", durableBefore)
	}
	approvedInput := workflowContractActionInput(t, "work-1", 7, "wf-approve-contract", challengeRef)
	approved, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedInput}, env)
	if err != nil || approved.Outcome != OutcomeOK {
		if approved.Error != nil {
			t.Fatalf("approved workflow action response=%+v error=%+v err=%v", approved, *approved.Error, err)
		}
		t.Fatalf("approved workflow action response=%+v err=%v", approved, err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "execution" {
		t.Fatalf("approved action current step=%s, want execution", step)
	}
	projection, err := store.ReadWorkflowProjection(context.Background(), s, store.WorkflowReadRequest{WorkID: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if projection.ArchitectureBinding == nil || projection.ArchitectureBinding.DomainRegistryContentHash != pm1fixture.FixtureDomainRegistryContentHash || projection.ArchitectureBinding.HomeDomainID != pm1fixture.FixtureRootDomainID {
		t.Fatalf("agent approval did not persist readable architecture binding: %+v", projection.ArchitectureBinding)
	}
}
