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
	var bindings int
	if err := tx.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id='record-integrity'`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if got, _ := payload["evidence_count"].(float64); got != float64(bindings) || bindings != 6 {
		t.Errorf("evidence_count = %v, want the six recorded bindings (got %d)", got, bindings)
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
	fixture := qCurrentAcceptance(t, workID)
	s, owner := fixture.store, fixture.owner
	t.Cleanup(func() { _ = s.Close() })
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "bind_evidence", Payload: mustJSONValue(map[string]any{"evidence_ref": "commit:declared", "evidence_kind": "verification"}), Actor: owner,
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
