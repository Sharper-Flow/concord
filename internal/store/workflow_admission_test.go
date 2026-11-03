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
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, tc.planType, tc.step, []string{}, nil)
			pin, err := ReadWorkPin(t.Context(), f.store, workID)
			if err != nil {
				t.Fatal(err)
			}
			if pin.Step != tc.step || pin.WorkflowType != string(tc.planType) || workPinContainsAction(pin.NextValidIntents, "bind_evidence") != tc.binding {
				t.Fatalf("pin type=%q step=%q intents=%v", pin.WorkflowType, pin.Step, pin.NextValidIntents)
			}
			request := planAdmissionRequest(workID, "bind_evidence", pin.Version, f.owner, json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"artifact:admission"}`))
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
			bound, err := workflowEvidenceReferenceBound(t.Context(), f.store.db, workID, "artifact:admission", "test")
			if err != nil || !bound {
				t.Fatalf("binding was not recorded: bound=%v err=%v", bound, err)
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
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{}, nil)
	version := readWorkVersion(t, f.store, workID)
	valid := json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"artifact:refusal"}`)
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
			first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), tc.kind)
			second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), tc.kind)
			if first.Detail != second.Detail || readWorkVersion(t, f.store, workID) != version {
				t.Fatalf("refusal parity or rollback: preflight=%v dispatch=%v", first, second)
			}
		})
	}
	request := planAdmissionRequest(workID, "bind_evidence", version, f.owner, valid)
	request.PrincipalRef = "principal:someone-else"
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("actor preflight: %v", err)
	}
	planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindUnauthorized)
	if readWorkVersion(t, f.store, workID) != version {
		t.Fatal("principal mismatch changed the work")
	}
}
