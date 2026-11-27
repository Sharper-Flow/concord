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

// blockWorkflowInstance reaches the blocked state the way production does:
// the owner starts the implement step and its attempt fails nonrecoverably.
func blockWorkflowInstance(t *testing.T, s *Store, workID string, owner WorkflowActor) {
	t.Helper()
	dispatchWorkflowAction(t, s, workID, "start_execution", json.RawMessage(`{}`), owner)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	failed := actionFailedFixture("blocked-"+workID, workID, ownerRef, ownerRef, "implement", 1, version, false)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{failed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("nonrecoverable action failure: %v", err)
	}
	assertWorkflowInstanceState(t, s, workID, "blocked")
}

func assertWorkflowInstanceState(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != want {
		t.Fatalf("instance_state = %q, want %q", state, want)
	}
}

func dispatchWorkflowAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, actor WorkflowActor) {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), s, workID, action)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	id := fmt.Sprintf("%s-%s-%d", workID, action, version)
	request := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: actor,
		AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: id, OperationID: id,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id,
		RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Unix(13, 0).UTC(),
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, request)
		return err
	}); err != nil {
		t.Fatalf("%s: %v", action, err)
	}
}

func TestWorkflowStartExecutionMovesBlockedInstanceToRunning(t *testing.T) {
	t.Parallel()
	const workID = "blocked-start-execution"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "implement")
	blockWorkflowInstance(t, f.store, workID, f.owner)
	dispatchWorkflowAction(t, f.store, workID, "start_execution", json.RawMessage(`{}`), f.owner)
	assertWorkflowInstanceState(t, f.store, workID, "running")
}

func TestWorkflowCheckpointExecutionMovesBlockedInstanceToRunning(t *testing.T) {
	t.Parallel()
	const workID = "blocked-checkpoint-execution"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "implement")
	blockWorkflowInstance(t, f.store, workID, f.owner)
	dispatchWorkflowAction(t, f.store, workID, "checkpoint_execution", json.RawMessage(`{}`), f.owner)
	assertWorkflowInstanceState(t, f.store, workID, "running")
}

// Delivery and premise actions admitted while the instance is blocked leave
// it blocked; the complete action then closes it.
func TestWorkflowCompleteMovesBlockedInstanceToCompleted(t *testing.T) {
	t.Parallel()
	const workID = "blocked-complete"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "implement")
	s := f.store
	blockWorkflowInstance(t, s, workID, f.owner)
	moveWorkflowPlanFixtureFrontier(t, s, workID, "review")
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:blocked-complete","delivery_state":"asserted"}`), 0, f.owner); err != nil {
		t.Fatalf("record delivery: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	completeWorkflowPlanFixture(t, s, workID)
	assertWorkflowInstanceState(t, s, workID, "blocked")
	dispatchWorkflowAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), f.owner)
	assertWorkflowInstanceState(t, s, workID, "completed")
}

// Cancelling a work item closes its workflow instance in the same fold.
func TestTerminalLifecycleClosesTheWorkflowInstance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		terminal      func(t *testing.T, s *Store, workID string)
		instanceState string
	}{
		{name: "cancelled", instanceState: "cancelled", terminal: func(t *testing.T, s *Store, workID string) {
			if err := applyWorkEvent(t, s, workTransitionEvent(workID+"-cancel", workID, "needed", "cancelled", 3, 4), workVersion(workID, 3)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workID := "terminal-" + tc.name
			s := openTemp(t)
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/terminal", AgentRef: "agent/terminal", SessionRef: "session/" + workID, ActorClass: ActorAgent}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner})
			}); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if before != "intake" {
				t.Fatalf("instance_state before the terminal fold = %q, want intake", before)
			}

			tc.terminal(t, s, workID)

			var lifecycle, state string
			var completedAt *string
			if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state, i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &completedAt); err != nil {
				t.Fatal(err)
			}
			if !isTerminalLifecycle(lifecycle) {
				t.Fatalf("lifecycle = %q, want a terminal lifecycle", lifecycle)
			}
			if state != tc.instanceState {
				t.Errorf("instance_state = %q, want %q after the item reached %q", state, tc.instanceState, lifecycle)
			}
			if completedAt == nil || *completedAt == "" {
				t.Errorf("completed_at is empty; the terminal fold must stamp the instance")
			}
		})
	}
}

// CD-0183 D4: a completed lifecycle target whose workflow instance has not
// reached a terminal state is refused with a typed refusal whose remedy names
// the workflow completion action.
func TestTerminalLifecycleRefusesCompletedWhileWorkflowLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "terminal-completed-live"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/terminal", AgentRef: "agent/terminal", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner})
	}); err != nil {
		t.Fatal(err)
	}
	err := applyWorkEvent(t, s, workTransitionEvent(workID+"-complete", workID, "needed", "completed", 3, 4), workVersion(workID, 3))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindNotTerminal {
		t.Fatalf("completed on external evidence error=%v, want %s", err, KindNotTerminal)
	}
	if !strings.Contains(failure.RecoveryAction, "workflow_action complete") {
		t.Fatalf("recovery action = %q, want the workflow completion action", failure.RecoveryAction)
	}
	var lifecycle, state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" || state != "intake" {
		t.Fatalf("refused transition changed state: lifecycle=%q instance=%q, want needed/intake", lifecycle, state)
	}
}

// The workflow completion's own lifecycle move keeps the instance record the
// completion fold wrote: the terminal-lifecycle close admits an item whose
// instance is already terminal, and stamps nothing (CD-0183 D3).
func TestTerminalLifecycleLeavesACompletedInstanceAlone(t *testing.T) {
	t.Parallel()
	workID := "completed-then-lifecycle"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{includeSpec: true, includePremise: true})
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("workflow completion refused: %v", err)
	}
	var lifecycle, state string
	var completedAt *string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state, i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &completedAt); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("lifecycle = %q after the workflow completion, want completed", lifecycle)
	}
	if state != "completed" {
		t.Fatalf("instance_state = %q after the workflow completion, want completed", state)
	}
	if completedAt == nil || *completedAt == "" {
		t.Fatal("the completion left no instance stamp")
	}
}
