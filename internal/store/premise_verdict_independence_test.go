package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A verdict whose actor executes a delivery pass after recording it no
// longer evaluates independently, and completion refuses it. confirm_premise
// is the last step that declares record_verdict, so it applies completion's
// own actor check and refuses while the operator re-verdict is still
// declared; afterwards the item could not complete and no step would let it
// re-record the verdict.
func TestConfirmPremiseRefusesVerdictWhoseActorExecutedAfterIt(t *testing.T) {
	t.Parallel()
	const workID = "premise-verdict-independence"
	s, owner := seedDeliveredItemAtAcceptance(t, workID)
	operator := operatorVerdictActor(t, workID)
	reviewer := WorkflowActor{PrincipalRef: owner.PrincipalRef, ClientRef: owner.ClientRef, AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, err := WorkflowActorRef(reviewer)
	if err != nil {
		t.Fatal(err)
	}

	// The reviewer has executed no step, so its verdict is independent.
	verdict := json.RawMessage(`{"predicate_id":"predicate:primary","verdict_kind":"ok","evaluation_evidence":["evidence:operator-verdict"]}`)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", verdict, 0, reviewer); err != nil {
		t.Fatalf("independent reviewer verdict refused: %v", err)
	}

	// The item is forced back to execution, and the reviewer runs the next
	// delivery pass itself.
	setWorkflowStepForOperatorVerdictTest(t, s, workID, "execution")
	if err := runVerdictActionAs(t, s, workID, "start_execution", json.RawMessage(`{}`), 0, reviewer); err != nil {
		t.Fatalf("reviewer start_execution refused: %v", err)
	}
	version := readWorkVersion(t, s, workID)
	delivery := workflowEventWithActor("redelivery-"+workID, WorkflowActionCompleted, workID, reviewerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": "execution", "action_id": "record_delivery", "attempt_epoch": 2,
		"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": reviewerRef,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{delivery}}); err != nil {
		t.Fatalf("reviewer record_delivery: %v", err)
	}
	if step := readInstanceStep(t, s, workID); step != "acceptance" {
		t.Fatalf("step after redelivery = %q, want acceptance", step)
	}

	err = runOperatorPremiseAction(t, s, workID, "confirm_premise", nil, owner, operator)
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized ||
		!strings.Contains(failure.Detail, "predicate:primary") || !strings.Contains(failure.Detail, "evaluate its own delivery") ||
		!strings.Contains(failure.RecoveryAction, "CD-0116 D1") {
		t.Fatalf("confirm_premise over the reviewer's verdict err=%v, want the clause-4 refusal naming the predicate and the CD-0116 D1 re-verdict", err)
	}

	// The operator re-verdict is declared at acceptance; it replaces the
	// reviewer's verdict, and the item confirms and completes.
	if err := runOperatorVerdict(t, s, workID, owner, operator); err != nil {
		t.Fatalf("operator re-verdict refused: %v", err)
	}
	if err := runOperatorPremiseAction(t, s, workID, "confirm_premise", nil, owner, operator); err != nil {
		t.Fatalf("confirm_premise after the operator re-verdict refused: %v", err)
	}
	if err := runOperatorPremiseAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), owner, operator); err != nil {
		t.Fatalf("complete after the operator re-verdict refused: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
}

func runOperatorPremiseAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, owner, operator WorkflowActor) error {
	t.Helper()
	ctx := context.Background()
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := action + "-" + workID
	if _, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + workID, IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}
