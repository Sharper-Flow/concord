package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// A hand-off summary holds the producer step. Ready Review advances its
// checked candidate to refinement, whose own start and delivery remain required.
func TestRecordDeliveryExitsTheStepTheSessionExecuted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	f.verify(t)
	f.prepareControl(t)
	f.implement(t)
	f.proof = f.verify(t)
	f.prepareControl(t)
	s := f.s
	execActor := store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent}
	currentStep := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, f.work).Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	version := func() int64 {
		t.Helper()
		var v int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, f.work).Scan(&v); err != nil {
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
			WorkID: f.work, ExpectedVersion: v, ActionID: actionID, Payload: raw,
			Actor: execActor, AcceptedInputsDigest: "sha256:" + strings.Repeat("3", 64), IdempotencyIdentity: identity,
			OperationID: "delivery-op-" + identity, PrincipalRef: "human-1", Tool: "concord-test", IdempotencyKey: "delivery-key-" + identity,
			RequestID: "delivery-request-" + identity, AcceptedScope: `{"project":"project-1"}`, ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
		}
		preflight := store.WorkflowActionPreflightRequest{WorkID: f.work, ExpectedVersion: v, ActionID: actionID, Payload: raw, Actor: execActor}
		return store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, store.BuiltinWorkflowRegistry(), preflight, nil, func(tx *store.Transaction) error {
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

	f.review(t, "ready_for_final_proof", f.proof)
	delivery := map[string]any{"delivery_artifact": "artifact:repair", "delivery_state": "asserted"}
	if step := currentStep(); step != "refine" {
		t.Fatalf("ready Review left the step at %q, want refine", step)
	}
	if err := try("checkpoint_refine", map[string]any{}); err != nil {
		t.Fatalf("checkpoint_refine refused: %v", err)
	}
	if err := try("bind_evidence", map[string]any{"evidence_kind": "artifact"}); err != nil {
		t.Fatalf("refine artifact binding refused: %v", err)
	}
	// The unchanged candidate reuses its passing producer receipt.
	proofRef := f.proof
	if err := try("bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": proofRef}); err != nil {
		t.Fatalf("refine verification binding refused: %v", err)
	}
	if err := try("record_delivery", delivery); err != nil {
		t.Fatalf("record_delivery after checkpoint_refine refused: %v", err)
	}
	if step := currentStep(); step != "delivery" {
		t.Fatalf("refine record_delivery left the step at %q, want delivery", step)
	}
	if err := try("record_delivery", delivery); err != nil {
		t.Fatalf("record_delivery at delivery gate refused: %v", err)
	}
	if step := currentStep(); step != "verify" {
		t.Fatalf("delivery gate left the step at %q, want verify", step)
	}
}

// Delivery on a step that never started has nothing to deliver.
func TestRecordDeliveryRequiresTheFencedStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newSpineFixture(t)
	s, service, env, workID := f.s, f.service, f.env, f.work
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='refine' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	version := agentCompositionWorkVersion(t, s, workID)
	raw, _ := json.Marshal(map[string]any{"work_id": workID, "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:missing-start", "delivery_state": "asserted"}, "idempotency_key": "delivery-no-start-action"})
	env.RequestID = "request:delivery-no-start"
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("record_delivery without a start outcome=%q error=%+v, want a refusal", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "fenced start") {
		t.Fatalf("refusal message=%q does not name the missing start", response.Error.Message)
	}
}
