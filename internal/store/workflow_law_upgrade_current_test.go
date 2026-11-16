package store

import (
	"fmt"
	"testing"
	"time"
)

func currentLawUpgradeFixture(t *testing.T, contract, boundary int) (*Store, WorkflowActor) {
	t.Helper()
	const workID = "upgrade-law"
	var s *Store
	var owner WorkflowActor
	if contract == 0 {
		s = openTemp(t)
		owner = seedWorkflowLawFixture(t, s, workID)
	} else {
		fixture := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "frame", nil)
		s, owner = fixture.store, fixture.owner
		if boundary == 0 {
			terminalCleanupSQL(t, s, `UPDATE workflow_contracts SET law_boundary_version=0 WHERE work_id='upgrade-law'`)
		}
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, workTransitionEvent("start-"+workID, workID, "needed", "in_progress", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	return s, owner
}

func currentLawUpgradeAction(t *testing.T, s *Store, owner WorkflowActor, action string, fields map[string]any) error {
	t.Helper()
	const id = "upgrade-law"
	ctx, err := prepareTerminalTestContext(t.Context(), s, id, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, id)
	op := fmt.Sprintf("upgrade:%s:%d", action, version)
	payload := mustJSONValue(fields)
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{
			WorkID: id, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: testManifestDigest,
			IdempotencyIdentity: op, OperationID: op, PrincipalRef: owner.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: op, RequestID: "request:" + op,
			ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
		})
		return err
	})
}

func TestWorkflowLawCurrentContractRoutes(t *testing.T) {
	for _, action := range []string{"approve_contract", "revise_intent"} {
		t.Run(action, func(t *testing.T) {
			contract := 0
			if action == "revise_intent" {
				contract = 1
			}
			s, owner := currentLawUpgradeFixture(t, contract, 1)
			if contract == 0 {
				seedWorkflowPlanFrontier(t, s, "upgrade-law", WorkflowStepPlanResearch, firstCallApprovalFrontier(t, WorkflowStepPlanResearch))
			}
			fields := map[string]any{"legislative_subjects": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}}
			if action == "revise_intent" {
				fields["contract_version"] = 2
				if err := reviseContractRecord(t, s, "upgrade-law", owner, fields, readWorkVersion(t, s, "upgrade-law")); err != nil {
					t.Fatalf("contract update: %v", err)
				}
			} else if err := currentLawUpgradeAction(t, s, owner, action, fields); err != nil {
				t.Fatalf("contract approval: %v", err)
			}
			contractData, err := readWorkflowContractVersion(t.Context(), s.db, "upgrade-law", int64(contract+1))
			if err != nil || contractData.Version != int64(contract+1) {
				t.Fatalf("current contract=%+v error=%v", contractData, err)
			}
		})
	}
}

func TestWorkflowLawCurrentCompletionRoutes(t *testing.T) {
	for _, boundary := range []int{0, 1} {
		t.Run(fmt.Sprintf("boundary_%d", boundary), func(t *testing.T) {
			s, owner := currentLawUpgradeFixture(t, 1, boundary)
			payload := mustJSONValue(map[string]any{"impact_verdict": "non-breaking"})
			request := WorkflowActionPreflightRequest{WorkID: "upgrade-law", ExpectedVersion: readWorkVersion(t, s, "upgrade-law"), ActionID: "complete", Payload: payload, Actor: owner}
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, request); err == nil {
				t.Fatal("completion admitted with unfinished steps")
			}
			completeWorkflowPlanFixture(t, s, "upgrade-law")
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, request); err != nil {
				t.Fatalf("completion preflight after plan: %v", err)
			}
			stale := request
			stale.ExpectedVersion--
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, stale); err == nil {
				t.Fatal("completion admitted a stale work version")
			}
			invalidActor := request
			invalidActor.Actor = WorkflowActor{}
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, invalidActor); err == nil {
				t.Fatal("completion admitted an invalid actor")
			}
			if err := currentLawUpgradeAction(t, s, owner, "complete", map[string]any{"impact_verdict": "non-breaking"}); err != nil {
				t.Fatalf("completion: %v", err)
			}
			pin, err := ReadWorkPin(t.Context(), s, "upgrade-law")
			if err != nil || pin.Lifecycle != "completed" {
				t.Fatalf("completed pin=%+v error=%v", pin, err)
			}
		})
	}
}

func TestWorkflowLawCompletionRequiresActiveContract(t *testing.T) {
	s, owner := currentLawUpgradeFixture(t, 0, 0)
	plan := currentWorkflowPlanFixture(t, WorkflowStepPlanResearch)
	seedWorkflowPlanFrontier(t, s, "upgrade-law", WorkflowStepPlanResearch, plan.Steps[0].ID)
	completeWorkflowPlanFixture(t, s, "upgrade-law")
	if err := currentLawUpgradeAction(t, s, owner, "complete", map[string]any{"impact_verdict": "non-breaking"}); err == nil {
		t.Fatal("completion without an active contract succeeded")
	}
	pin, err := ReadWorkPin(t.Context(), s, "upgrade-law")
	if err != nil || pin.Lifecycle != "in_progress" {
		t.Fatalf("work changed after refused completion: pin=%+v error=%v", pin, err)
	}
}
