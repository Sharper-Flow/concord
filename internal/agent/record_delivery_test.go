package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestMigratedTerminalDeliveryReplacementReplay(t *testing.T) {
	f := newSpineFixture(t)
	ctx := t.Context()
	db := f.s.DatabaseForTesting()
	_, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES(?,1,'historical-delivery','actor:historical','2026-01-01T00:00:00Z',8,'{"delivery_artifact":"artifact:old","delivery_state":"asserted"}');
UPDATE workflow_instances SET workflow_type=NULL,step_plan=NULL,instance_state='completed' WHERE work_id=?;
UPDATE work_items SET lifecycle='completed' WHERE id=?; DELETE FROM write_guard`, f.work, f.work, f.work)
	if err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, f.s, f.work)
	input := map[string]any{"work_id": f.work, "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:new", "delivery_state": "asserted"}, "idempotency_key": "migrated-delivery-replacement"}
	first := spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
	var artifact, state, lifecycle string
	var eventCount, operationCount, idemCount int
	query := `SELECT json_extract(d.payload,'$.delivery_artifact'),i.instance_state,w.lifecycle,(SELECT count(*) FROM effect_receipts WHERE subject_id=w.id),(SELECT count(*) FROM durable_operations WHERE work_id=w.id),(SELECT count(*) FROM idempotency_records) FROM workflow_delivery_state d JOIN workflow_instances i ON i.work_id=d.work_id JOIN work_items w ON w.id=d.work_id WHERE d.work_id=?`
	if err := db.QueryRowContext(ctx, query, f.work).Scan(&artifact, &state, &lifecycle, &eventCount, &operationCount, &idemCount); err != nil {
		t.Fatal(err)
	}
	if artifact != "artifact:new" || state != "completed" || lifecycle != "completed" {
		t.Fatalf("replacement %q %q %q", artifact, state, lifecycle)
	}
	resultVersion := workVersion(t, f.s, f.work)
	replay := spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
	if !reflect.DeepEqual(first.Result, replay.Result) {
		t.Fatalf("replay result: %s != %s", first.Result, replay.Result)
	}
	var afterArtifact, afterState, afterLifecycle string
	var afterEvents, afterOps, afterIdem int
	if err := db.QueryRowContext(ctx, query, f.work).Scan(&afterArtifact, &afterState, &afterLifecycle, &afterEvents, &afterOps, &afterIdem); err != nil {
		t.Fatal(err)
	}
	if artifact != afterArtifact || state != afterState || lifecycle != afterLifecycle || eventCount != afterEvents || operationCount != afterOps || idemCount != afterIdem || resultVersion != workVersion(t, f.s, f.work) {
		t.Fatal("replay changed durable state")
	}
}

// Summary, review and delivery do not complete a plan step.
func TestRecordDeliveryExitsTheStepTheSessionExecuted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	f.verify(t)
	f.implement(t)
	f.proof = f.verify(t)
	s := f.s
	execActor := store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent}
	currentStep := func() string {
		t.Helper()
		pin, err := store.ReadWorkPin(ctx, s, f.work)
		if err != nil {
			t.Fatal(err)
		}
		return pin.Step
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
		return store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, preflight, nil, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, request)
			return err
		})
	}

	if step := currentStep(); step != "implement" {
		t.Fatalf("fixture step=%q, want implement", step)
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
	if step := currentStep(); step != "implement" {
		t.Fatalf("cross_context_boundary moved the step to %q", step)
	}

	delivery := map[string]any{"delivery_artifact": "artifact:repair", "delivery_state": "asserted"}
	// complete_step keeps its reviewed-attempt interface beside record_review;
	// both write the same step attempt.
	if err := try("complete_step", map[string]any{"step_id": "implement", "note": "Implementation reviewed.", "reviewer": "concord-review", "result": "pass", "commit": gitRun(t, f.path, "rev-parse", "HEAD"), "link": "https://example.com/review/repair"}); err != nil {
		t.Fatalf("complete implement refused: %v", err)
	}
	if step := currentStep(); step != "review" {
		t.Fatalf("implementation completion left the step at %q, want review", step)
	}
	if err := try("record_delivery", delivery); err != nil {
		t.Fatalf("record_delivery at delivery gate refused: %v", err)
	}
	if step := currentStep(); step != "review" {
		t.Fatalf("delivery moved the step to %q, want review", step)
	}
}

// An unreviewed implementation cannot claim delivery on an inserted frontier.
func TestRecordDeliveryRequiresAcceptedReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, _ := ekbWorkAtExecute(t, ctx, s, service, env, "delivery-unreviewed")
	var plan store.WorkflowStepPlan
	if err := json.Unmarshal([]byte(agentPlanFixtureJSON(t, store.WorkflowStepPlanChange, "implement")), &plan); err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.ID == "implement" && len(step.Attempts) != 0 {
			t.Fatal("fixture has a completed implementation attempt")
		}
	}
	var started any
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&started); err != nil || started != nil {
		t.Fatalf("fixture has execution start %v: %v", started, err)
	}
	plan.Steps = append(plan.Steps[:1], append([]store.WorkflowPlanStep{{ID: "delivery-probe", After: []string{"plan"}}}, plan.Steps[1:]...)...)
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET step_plan=? WHERE work_id=?; DELETE FROM write_guard`, string(retryJSON(plan)), workID); err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, workID)
	before := agentCurrentEffects(t, s)
	var receipts int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"work_id": workID, "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:missing-start", "delivery_state": "asserted"}, "idempotency_key": "delivery-no-start-action"})
	env.RequestID = "request:delivery-no-start"
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "missing_evidence" {
		t.Fatalf("record_delivery without review outcome=%q error=%+v, want missing evidence", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "Review") {
		t.Fatalf("refusal message=%q does not name the missing review", response.Error.Message)
	}
	var afterReceipts int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM idempotency_records`).Scan(&afterReceipts); err != nil {
		t.Fatal(err)
	}
	if agentCurrentEffects(t, s) != before || workVersion(t, s, workID) != version || receipts != afterReceipts {
		t.Fatal("refused delivery changed durable state")
	}
}
