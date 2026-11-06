package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Premise confirmation records the authenticated non-worker coordinator, with
// no operator substitution. Every confirmation stays bound to its contract
// version.

// engineeringConfirmationSeed parks a workflow.implementation item at
// acceptance on the live journey and records the routine actors.
func engineeringConfirmationSeed(t *testing.T, workID string) (s *Store, operator, coordinator WorkflowActor) {
	t.Helper()
	f := rAcceptance(t, workID, []string{}, true)
	actors := recordApprovalActors(t, f.store, workID)
	return f.store, actors.operator, actors.coordinator
}

// seedRoutineReviewersForSelection records the reviewer/operator/coordinator
// triple on a delivered seed (selection variants use their own store shape).
func seedRoutineReviewersForSelection(t *testing.T, s *Store, workID string) (reviewer, operator, coordinator WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	reviewer = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/independent-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	operator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	coordinator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
	version := readWorkVersion(t, s, workID)
	events := []Event{}
	for index, actor := range []WorkflowActor{reviewer, operator, coordinator} {
		ref, err := WorkflowActorRef(actor)
		if err != nil {
			t.Fatal(err)
		}
		classes := map[WorkflowActor]string{reviewer: "agent", operator: "operator", coordinator: "agent"}
		events = append(events, workflowEventWithActor(fmt.Sprintf("selection-actor-%d-%s", index, workID), WorkflowActorRecorded, workID, ref, map[string]any{"work_id": workID, "expected_version": version + int64(index), "resulting_version": version + int64(index) + 1, "actor_ref": ref, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": classes[actor]}))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return reviewer, operator, coordinator
}

// runEngineeringConfirmation invokes the confirm_premise constructor directly
// on the seeded store. The proof the constructor's law and Domain
// reads revalidate is prepared before the transaction opens, the same
// pre-transaction preparation the owning boundary runs
// (PrepareWorkflowActionProof).
func runEngineeringConfirmation(t *testing.T, s *Store, workID, invokerRef string, fields map[string]json.RawMessage) ([]Event, error) {
	t.Helper()
	ctx, err := s.PrepareWorkflowActionProof(context.Background(), "confirm_premise", workID)
	if err != nil {
		return nil, err
	}
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return workflowConfirmPremiseEvents(ctx, tx, WorkflowStepPlanChange, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "confirm_premise", OperationID: "confirm-" + workID,
	}, invokerRef, fields, "confirm-"+workID, version)
}

func TestEngineeringConfirmation(t *testing.T) {
	t.Parallel()

	t.Run("engineering coordinator confirmation succeeds", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-coordinator"
		s, _, coordinator := engineeringConfirmationSeed(t, workID)
		events, err := runEngineeringConfirmation(t, s, workID, ownerRefForTest(t, coordinator), nil)
		if err != nil {
			t.Fatalf("engineering coordinator confirmation refused: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("confirmation produced %d events, want 1", len(events))
		}
		var payload struct {
			ContractVersion       int64  `json:"contract_version"`
			ConfirmingActorRef    string `json:"confirming_actor_ref"`
			ConfirmationAuthority string `json:"confirmation_authority"`
			DecisionContextDigest string `json:"decision_context_digest"`
		}
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		coordinatorRef := ownerRefForTest(t, coordinator)
		if payload.ConfirmingActorRef != coordinatorRef || payload.ConfirmationAuthority != "coordinator" {
			t.Fatalf("confirmation recorded %q/%q, want the invoking coordinator with coordinator authority", payload.ConfirmingActorRef, payload.ConfirmationAuthority)
		}
		if payload.DecisionContextDigest != "" {
			t.Fatalf("engineering confirmation carried a human decision-context digest %q", payload.DecisionContextDigest)
		}
		if events[0].PayloadVersion != 2 {
			t.Fatalf("confirmation payload version = %d, want 2", events[0].PayloadVersion)
		}
	})

	t.Run("engineering records the agent-class coordinator only", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-agent-class"
		s, operator, _ := engineeringConfirmationSeed(t, workID)
		_, err := runEngineeringConfirmation(t, s, workID, ownerRefForTest(t, operator), nil)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("operator-class invoker confirmation returned %v, want the coordinator-typed refusal", err)
		}
	})
}

func TestEngineeringConfirmationLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("class upgrade invalidates the engineering confirmation", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-upgrade-lifecycle"
		route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{})
		s := route.store
		defer s.Close()
		coordinator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
		coordinatorRef := ownerRefForTest(t, coordinator)
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
		operatorRef := ownerRefForTest(t, operator)
		version := readWorkVersion(t, s, workID)
		recorded := workflowEventWithActor("engineering-coordinator-"+workID, WorkflowActorRecorded, workID, coordinatorRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": coordinatorRef, "principal_ref": coordinator.PrincipalRef, "client_ref": coordinator.ClientRef, "agent_ref": coordinator.AgentRef, "session_ref": coordinator.SessionRef, "actor_class": "agent"})
		operatorRecorded := workflowEventWithActor("engineering-operator-"+workID, WorkflowActorRecorded, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{recorded, operatorRecorded}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatalf("record confirmation actors: %v", err)
		}
		version = readWorkVersion(t, s, workID)
		expectedV, resultingV := version, version+1
		// Write the engineering confirmation row for v1 through the fold body.
		confirmed := workflowEventWithActor("engineering-confirm-v1-"+workID, WorkflowPremiseConfirmed, workID, coordinatorRef, map[string]any{"work_id": workID, "expected_version": expectedV, "resulting_version": resultingV, "contract_version": 1, "confirming_actor_ref": coordinatorRef, "confirmation_authority": "coordinator"})
		confirmed.PayloadVersion = 2
		writeTx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterWrite(context.Background(), writeTx); err != nil {
			t.Fatal(err)
		}
		writeErr := workflowPremiseConfirmedWriteTx(context.Background(), writeTx, confirmed, workflowPremiseConfirmedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &expectedV, ResultingVersion: &resultingV}, ContractVersion: 1, ConfirmingActorRef: coordinatorRef, ConfirmationAuthority: "coordinator"})
		if leaveErr := leaveWrite(context.Background(), writeTx); writeErr == nil {
			writeErr = leaveErr
		}
		if writeErr != nil {
			_ = writeTx.Rollback()
			t.Fatalf("engineering confirmation fold body refused: %v", writeErr)
		}
		if err := writeTx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&coordinatorRef); err != nil {
			t.Fatalf("engineering confirmation row genuinely missing: %v", err)
		}
		successor := pContractSuccessor(t, s, workID)
		currentVersion := readWorkVersion(t, s, workID)
		if err := reviseContractRecord(t, s, workID, coordinator, successor, currentVersion); err != nil {
			t.Fatalf("class upgrade revision refused: %v", err)
		}
		// The v1 confirmation row survives as history, but the active
		// contract is now v2 and the stale v1 confirmation cannot satisfy it:
		// a live re-confirmation of v1 refuses as stale.
		staleCurrent := readWorkVersion(t, s, workID)
		staleTx, staleErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if staleErr != nil {
			t.Fatal(staleErr)
		}
		defer staleTx.Rollback()
		staleWrite := workflowPremiseConfirmedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &staleCurrent, ResultingVersion: new(int64)}, ContractVersion: 1, ConfirmingActorRef: coordinatorRef, ConfirmationAuthority: "coordinator"}
		confirmErr := workflowPremiseConfirmedWriteTx(context.Background(), staleTx, confirmed, staleWrite)
		var failure *Failure
		if confirmErr == nil || !failureAs(confirmErr, &failure) || !strings.Contains(failure.Detail, "supersession invalidated") {
			t.Fatalf("stale v1 confirmation write returned %v, want the supersession-invalidated refusal", confirmErr)
		}
		if err := staleTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		staleFields := map[string]json.RawMessage{}
		staleFields["contract_version"] = json.RawMessage(`1`)
		_, constructorErr := runEngineeringConfirmation(t, s, workID, coordinatorRef, staleFields)
		var constructorFailure *Failure
		if constructorErr == nil || !failureAs(constructorErr, &constructorFailure) || constructorFailure.Kind != KindInvalidPayload || !strings.Contains(constructorFailure.Detail, "supersession invalidated") {
			t.Fatalf("constructor stale v1 confirmation returned %v, want the supersession-invalidated refusal", constructorErr)
		}
		forgedTx, forgedErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if forgedErr != nil {
			t.Fatal(forgedErr)
		}
		if err := workflowPremiseConfirmedAuthorityTx(context.Background(), forgedTx, workID, 2, coordinatorRef, "coordinator"); err != nil {
			t.Fatalf("coordinator authority refused on the active successor: %v", err)
		}
		// A named contract version other than the active one refuses even
		// under the right authority: supersession invalidates the stale
		// confirmation.
		if err := workflowPremiseConfirmedAuthorityTx(context.Background(), forgedTx, workID, 1, coordinatorRef, "coordinator"); err == nil {
			t.Fatal("coordinator authority accepted a superseded contract version")
		}
		// Wrong-direction authority: an operator-typed claim refuses — the
		// confirmation records the coordinator authority alone.
		if err := workflowPremiseConfirmedAuthorityTx(context.Background(), forgedTx, workID, 1, operatorRef, "operator"); err == nil {
			t.Fatal("operator authority accepted")
		}
		if err := forgedTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT 1 FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=2`, workID).Scan(new(any)); err == nil {
			t.Fatal("the successor inherited a confirmation the supersession invalidated")
		}
	})
}
