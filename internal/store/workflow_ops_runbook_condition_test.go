package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOpsRunbookConditionResolutionAcrossHealthDispatch(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	workID := "ops-runbook-condition-dispatch"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:ops-test", ClientRef: "client:ops-test", AgentRef: "agent:ops-test", SessionRef: "session:ops-test", ActorClass: ActorAgent}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.ops_runbook")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range definition.Definition.StepGraph.Steps {
		if !containsString(step.Actions, "resolve_condition") || !containsString(step.Actions, "cancel_condition") {
			t.Fatalf("step %q lacks universal condition actions: %v", step.ID, step.Actions)
		}
	}
	initTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
		_ = initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}

	version := int64(4)
	version = dispatchOpsRunbookAction(t, s, workID, version, "approve_contract", "ops-approve-contract", actor, json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"domain_registry_content_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`))
	version = dispatchOpsRunbookAction(t, s, workID, version, "approve_operation", "ops-approve-operation", actor, json.RawMessage(`{"step_id":"approval"}`))
	version = dispatchOpsRunbookAction(t, s, workID, version, "add_condition", "ops-add-condition", actor, json.RawMessage(`{"step_id":"execute","condition_id":"condition:health","await_type":"ci_result","await_ref":"health:ops","resolution_authority":"durable_operation:ops-add-condition"}`))
	assertOpsRunbookStep(t, s, workID, "health")

	healthPayload := json.RawMessage(`{"step_id":"health","run_id":"run:ops","native_subject_ref":"route:ops","status":"healthy","evidence_ref":"evidence:health","evidence_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`)
	err = WorkflowActionPreflight(ctx, s, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: "health", ActionID: "record_health", Payload: healthPayload, Actor: actor})
	if err == nil || !strings.Contains(err.Error(), "consequential action has unresolved external conditions") {
		t.Fatalf("record_health with an open condition error=%v, want boundary refusal", err)
	}

	version = dispatchOpsRunbookAction(t, s, workID, version, "resolve_condition", "ops-resolve-condition", actor, json.RawMessage(`{"step_id":"health","condition_id":"condition:health","resolution_evidence":["evidence:ops-add-condition"],"resolved_by_event":"ops-condition-resolved"}`))
	if version <= 4 {
		t.Fatalf("resolve_condition resulting version=%d, want an advanced version", version)
	}
	assertOpsRunbookStep(t, s, workID, "rollback_optional")
	var state string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id=?`, workID, "condition:health").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" {
		t.Fatalf("condition state=%q, want resolved", state)
	}
}

func dispatchOpsRunbookAction(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, actor WorkflowActor, payload json.RawMessage) int64 {
	t.Helper()
	result, err := dispatchOpsRunbookActionResult(t, s, workID, version, actionID, operationID, actor, payload)
	if err != nil {
		t.Fatalf("dispatch %s: %v", actionID, err)
	}
	return result.ResultingVersion
}

func dispatchOpsRunbookActionResult(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, actor WorkflowActor, payload json.RawMessage) (WorkflowActionExecutionResult, error) {
	t.Helper()
	payload = testApprovalPayload(actionID, payload)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	result, actionErr := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor,
		AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	})
	_ = leaveFold(ctx, tx)
	if actionErr != nil {
		_ = tx.Rollback()
		return WorkflowActionExecutionResult{}, actionErr
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func assertOpsRunbookStep(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	var got string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("current step=%q, want %q", got, want)
	}
}
