package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The completion record must agree with the event log it summarizes (#856).
// The fixture binds evidence, associates current criterion proof, and
// confirms the premise; the completed payload must carry those facts, not
// the literal zero, the absent-fields default, and the constant "ok".
func TestCompletionRecordCarriesTheValuesTheGateComputed(t *testing.T) {
	t.Parallel()
	s, _ := seedCompletionGateCase(t, "record-integrity", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='record-integrity'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	actorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/owner", "session/record-integrity")
	event, err := workflowCompletionEvent(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: "record-integrity", ExpectedVersion: version, OperationID: "record-integrity-complete", Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}, actorRef, json.RawMessage(`{"impact_verdict":"non-breaking"}`))
	if err != nil {
		t.Fatalf("completion event was refused: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got, _ := payload["premise_confirmed"].(bool); !got {
		t.Error("premise_confirmed is false while the log holds an operator premise confirmation for this contract")
	}
	if got, _ := payload["evidence_count"].(float64); got != 6 {
		t.Errorf("evidence_count = %v, want six bindings (three required kinds, captured receipt, spec, and reviewed closure)", got)
	}
	if got, _ := payload["final_verdict_kind"].(string); got != "ok" {
		t.Errorf("final_verdict_kind = %q, want the recorded verdict kind %q", got, "ok")
	}
}

// fields.evidence_ref is a declared key; bind_evidence must honor it (#856
// follow-on). Binding through it is what lets a verdict cite the commit it
// evaluated instead of an opaque operation id.
func TestBindEvidenceHonorsTheDeclaredEvidenceRefField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "record-evidence-ref"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	digest := workflowFixtureDefinition(t, 2).Digest
	setup := []Event{
		workflowEvent("eref-owner-actor", WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("eref-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("eref-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("eref-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("eref-design", workID, ownerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("eref-planning", workID, ownerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("eref-start", WorkflowActionStarted, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "eref:start", "actor_ref": ownerRef}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: 9, ActionID: "bind_evidence", Payload: mustJSONValue(map[string]any{"evidence_ref": "commit:declared", "evidence_kind": "verification"}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: "eref:bind", OperationID: "eref:bind",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "eref:bind", RequestID: "request:eref:bind", ContractDigest: testManifestDigest, Now: time.Unix(2, 0).UTC(),
	}); err != nil {
		t.Fatalf("bind_evidence with a declared evidence_ref was refused: %v", err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	var subject string
	if err := tx.QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref') FROM workflow_evidence_bindings WHERE work_id=? ORDER BY binding_seq DESC LIMIT 1`, workID).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if subject != "commit:declared" {
		t.Errorf("immutable_subject_ref = %q, want the declared evidence_ref %q", subject, "commit:declared")
	}
}
