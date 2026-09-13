package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The session that executed an external-effect step leaves it through
// record_delivery, a hand-off summary never moves the step, and delivery needs
// the step's fenced start. Repository changes then enter independent review.
func TestRecordDeliveryExitsTheStepTheSessionExecuted(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, _ := workflowEngineFixture(t, "")
	execActor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-session-exec-aaaa", AgentRef: "agent-exec", SessionRef: "session-exec-aaaa", ActorClass: store.ActorAgent}
	currentStep := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, "work-1").Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	version := func() int64 {
		t.Helper()
		var v int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, "work-1").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	attempt := 0
	try := func(actionID string, payload map[string]any) error {
		t.Helper()
		attempt++
		identity := fmt.Sprintf("delivery-%s-%d", actionID, attempt)
		raw, _ := json.Marshal(payload)
		v := version()
		request := store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: v, ActionID: actionID, Payload: raw,
			Actor: execActor, AcceptedInputsDigest: "sha256:" + strings.Repeat("3", 64), IdempotencyIdentity: identity,
			OperationID: "delivery-op-" + identity, PrincipalRef: "human-1", Tool: "concord-test", IdempotencyKey: "delivery-key-" + identity,
			RequestID: "delivery-request-" + identity, AcceptedScope: `{"project":"project-1"}`, ContractDigest: ManifestDigest, Now: fixedTime(),
		}
		preflight := store.WorkflowActionPreflightRequest{WorkID: "work-1", ExpectedVersion: v, ActionID: actionID, Payload: raw, Actor: execActor}
		return store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, store.BuiltinWorkflowRegistry(), preflight, nil, fixedTime(), nil, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
			return err
		})
	}

	if step := currentStep(); step != "repair" {
		t.Fatalf("fixture step=%q, want repair", step)
	}

	// A hand-off records a boundary and holds the step.
	if err := try("checkpoint_context", map[string]any{
		"active_unit": "unit:repair", "hypothesis": "hypothesis:one", "diagnosis": "diagnosis:one", "strategy": "strategy:one",
		"touched_refs": []string{"internal/store/workflow_registry.go"}, "evidence_refs": []string{"evidence:one"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}); err != nil {
		t.Fatalf("checkpoint_context refused: %v", err)
	}
	if err := try("cross_context_boundary", map[string]any{"boundary_kind": "summary", "mode": "summary", "checkpoint_id": "delivery-op-delivery-checkpoint_context-1:context-checkpoint", "summary": "handed off"}); err != nil {
		t.Fatalf("cross_context_boundary refused: %v", err)
	}
	if step := currentStep(); step != "repair" {
		t.Fatalf("cross_context_boundary moved the step to %q", step)
	}

	// Delivery after the fenced start advances to review.
	if err := try("record_delivery", map[string]any{}); err != nil {
		t.Fatalf("record_delivery after start_repair refused: %v", err)
	}
	if step := currentStep(); step != "review" {
		t.Fatalf("record_delivery left the step at %q, want review", step)
	}
}

// Delivery on a step that never started has nothing to deliver.
func TestRecordDeliveryRequiresTheFencedStart(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Delivery without start", "task", "workflow.generic_one_off", "delivery-no-start")
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='execute' WHERE work_id=?; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	version := agentCompositionWorkVersion(t, s, workID)
	raw, _ := json.Marshal(map[string]any{"work_id": workID, "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{}, "idempotency_key": "delivery-no-start-action"})
	env.RequestID = "request:delivery-no-start"
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("record_delivery without a start outcome=%q error=%+v, want a refusal", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "fenced start") {
		t.Fatalf("refusal message=%q does not name the missing start", response.Error.Message)
	}
}
