package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func planAdmissionRequest(workID, action string, version int64, actor WorkflowActor, payload json.RawMessage) WorkflowActionExecutionRequest {
	key := fmt.Sprintf("plan-admission-%s-%s-%d", workID, action, version)
	return WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor,
		PrincipalRef: actor.PrincipalRef, OperationID: key, IdempotencyIdentity: key,
		IdempotencyKey: key, RequestID: "request:" + key, Tool: "concord_work_transition",
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		ContractDigest:       "sha256:" + strings.Repeat("b", 64), Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
	}
}

func planAdmissionPreflight(t *testing.T, s *Store, request WorkflowActionExecutionRequest) error {
	t.Helper()
	_, err := WorkflowActionDefinitionFor(t.Context(), s, WorkflowActionPreflightRequest{
		WorkID: request.WorkID, ExpectedVersion: request.ExpectedVersion, ActionID: request.ActionID,
		Payload: request.Payload, Actor: request.Actor,
	})
	return err
}

func planAdmissionDispatch(t *testing.T, s *Store, request WorkflowActionExecutionRequest) error {
	t.Helper()
	ctx, err := s.PrepareWorkflowActionProof(context.Background(), request.ActionID, request.WorkID)
	if err != nil {
		return err
	}
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, request)
		return err
	})
}

func planAdmissionFailure(t *testing.T, err error, kind FailureKind) *Failure {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != kind {
		t.Fatalf("failure = %v, want %s", err, kind)
	}
	return failure
}

func TestWorkflowPlanAdmissionFrontierParity(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		planType   WorkflowStepPlanType
		binding    bool
	}{
		{"change-plan", "plan", WorkflowStepPlanChange, false},
		{"change-implement", "implement", WorkflowStepPlanChange, true},
		{"fix-review", "review", WorkflowStepPlanFix, true},
		{"research-investigate", "investigate", WorkflowStepPlanResearch, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "admission-frontier-" + tc.name
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, tc.planType, tc.step, nil)
			pin, err := ReadWorkPin(t.Context(), f.store, workID)
			if err != nil {
				t.Fatal(err)
			}
			if pin.Step != tc.step || pin.WorkflowType != string(tc.planType) || workPinContainsAction(pin.NextValidIntents, "bind_evidence") != tc.binding {
				t.Fatalf("pin type=%q step=%q intents=%v", pin.WorkflowType, pin.Step, pin.NextValidIntents)
			}
			seedWorkflowAuthority(t, f.store, "admission-producer", workID, "principal/admission", "request/admission", []string{"artifact:admission"})
			request := planAdmissionRequest(workID, "bind_evidence", pin.Version, f.owner, json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"artifact:admission","producer_id":"principal/admission","producer_run_ref":"admission-producer","producer_watermark":"request/admission"}`))
			request.EvidenceRefs = []string{"artifact:admission"}
			preflight := planAdmissionPreflight(t, f.store, request)
			if !tc.binding {
				first := planAdmissionFailure(t, preflight, KindIllegalLifecycleTransition)
				second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindIllegalLifecycleTransition)
				if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != pin.Version {
					t.Fatalf("off-step parity: preflight=%v dispatch=%v", first, second)
				}
				return
			}
			if preflight != nil {
				t.Fatalf("declared binding preflight: %v", preflight)
			}
			if err := planAdmissionDispatch(t, f.store, request); err != nil {
				t.Fatalf("declared binding dispatch: %v", err)
			}
			after, err := ReadWorkPin(t.Context(), f.store, workID)
			if err != nil || after.Step != tc.step || after.Version <= pin.Version {
				t.Fatalf("binding changed plan frontier or did not record: pin=%+v err=%v", after, err)
			}
			actorRef, err := WorkflowActorRef(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			var recordedActor string
			if err := f.store.db.QueryRow(`SELECT actor FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')='artifact:admission'`, workID).Scan(&recordedActor); err != nil || recordedActor != actorRef {
				t.Fatalf("binding actor=%q, want %q: %v", recordedActor, actorRef, err)
			}
		})
	}
}

func TestWorkflowPlanAdmissionRefusalParity(t *testing.T) {
	const workID = "plan-admission-refusals"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", nil)
	version := readWorkVersion(t, f.store, workID)
	seedWorkflowAuthority(t, f.store, "refusal-producer", workID, "principal/refusal", "request/refusal", []string{"artifact:refusal"})
	valid := json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"artifact:refusal","producer_id":"principal/refusal","producer_run_ref":"refusal-producer","producer_watermark":"request/refusal"}`)
	// bindRefusalEffectSnapshot captures the no-effect state a refused
	// bind_evidence must preserve: work version, evidence bindings and
	// effect receipts (CD-0124 D3). It takes the caller's t so a failing
	// query fails the subtest that observed it.
	bindRefusalEffectSnapshot := func(t *testing.T) string {
		t.Helper()
		var version, bindings, receipts int64
		if err := f.store.db.QueryRow(`SELECT (SELECT version FROM work_items WHERE id=?1),(SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?1),(SELECT count(*) FROM effect_receipts WHERE subject_id=?1)`, workID).Scan(&version, &bindings, &receipts); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("version=%d bindings=%d receipts=%d", version, bindings, receipts)
	}
	for _, tc := range []struct {
		name string
		edit func(*WorkflowActionExecutionRequest)
		kind FailureKind
	}{
		{"stale", func(r *WorkflowActionExecutionRequest) { r.ExpectedVersion-- }, KindVersionConflict},
		{"invalid-payload", func(r *WorkflowActionExecutionRequest) { r.Payload = json.RawMessage(`{"undeclared":true}`) }, KindInvalidPayload},
		{"missing-actor", func(r *WorkflowActionExecutionRequest) { r.Actor = WorkflowActor{} }, KindUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := planAdmissionRequest(workID, "bind_evidence", version, f.owner, valid)
			tc.edit(&request)
			before := bindRefusalEffectSnapshot(t)
			first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), tc.kind)
			second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), tc.kind)
			if first.Detail != second.Detail || bindRefusalEffectSnapshot(t) != before {
				t.Fatalf("refusal parity or rollback: preflight=%v dispatch=%v", first, second)
			}
		})
	}
	request := planAdmissionRequest(workID, "bind_evidence", version, f.owner, valid)
	request.PrincipalRef = "principal:someone-else"
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("actor preflight: %v", err)
	}
	before := bindRefusalEffectSnapshot(t)
	dispatchErr := planAdmissionDispatch(t, f.store, request)
	_ = planAdmissionFailure(t, dispatchErr, KindUnauthorized)
	if after := bindRefusalEffectSnapshot(t); after != before {
		t.Fatalf("principal mismatch changed the work: %s -> %s", before, after)
	}
}
