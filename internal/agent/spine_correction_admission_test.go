package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSpineCurrentReviewCorrectionAdmission(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.implement(t)
	before := f.read(t)
	delivery := map[string]any{"delivery_artifact": "artifact:spine-corrected-value", "delivery_state": "asserted"}
	effects := premiseConsentSnapshot(t, f.s)
	actor := store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent}
	err := store.AuthorizeWorkflowActionAtBoundaryTx(t.Context(), f.s, store.WorkflowActionPreflightRequest{
		WorkID: f.work, ExpectedVersion: before.Pinned.Pin.Version, ActionID: "record_delivery", Payload: retryJSON(delivery), Actor: actor,
	}, nil, func(*store.Transaction) error { return nil })
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.RecoveryAction == "" {
		t.Fatalf("preflight did not name the required recovery: %v", err)
	}
	refused := f.action(t, "record_delivery", delivery)
	after := f.read(t)
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != mapFailureKind(failure.Kind) || refused.Error.Message != failure.Detail || refused.Error.EffectState != agentcontract.EffectNone ||
		!reflect.DeepEqual(effects, premiseConsentSnapshot(t, f.s)) || !reflect.DeepEqual(before.Pinned.Contract, after.Pinned.Contract) || before.Pinned.Pin.Version != after.Pinned.Pin.Version {
		t.Fatalf("delivery refusal changed authority or disagreed with preflight: preflight=%v mutation=%+v", err, refused.Error)
	}
	f.review(t, "fixed")
	if step := f.read(t).Pinned.Step; step != "review" {
		t.Fatalf("intermediate fixed review did not complete implement: %q", step)
	}
	spineOK(t, f.action(t, "record_delivery", delivery))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise", "idempotency_key": "correction-confirm-premise",
	}))
	f.review(t, "fixed")
	if step := f.read(t).Pinned.Step; step != "review" {
		t.Fatalf("fixed final review unexpectedly completed the final step: %q", step)
	}
	held := f.read(t).Pinned.Pin.Version
	if early := f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}); early.Outcome != agentcontract.OutcomeError || early.Error == nil ||
		early.Error.EffectState != agentcontract.EffectNone || f.read(t).Pinned.Pin.Version != held {
		t.Fatalf("a fixed final review admitted completion: %+v", early.Error)
	}
	f.completeReview(t)
	var raw string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, f.work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan store.WorkflowStepPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.ID == "review" {
			if len(step.Attempts) != 2 || step.Attempts[0].Result != "fixed" || step.Attempts[1].Result != "pass" || step.Attempts[1].Commit != gitRun(t, f.path, "rev-parse", "HEAD") || step.Attempts[1].Reviewer != "concord-review" || step.Attempts[1].Link != "https://example.com/review/spine" {
				t.Fatalf("final review did not retain both attempts: %+v", step.Attempts)
			}
			return
		}
	}
	t.Fatal("final review step missing")
}
