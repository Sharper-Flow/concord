package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func runDeliveryAdmissionAction(t *testing.T, s *Store, workID string, payload json.RawMessage, owner WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), s, workID, "record_delivery")
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, workID)
	id := fmt.Sprintf("record-delivery-%s-%d", workID, version)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{EvidenceRefs: actionEvidenceRefs("record_delivery", payload), WorkID: workID, ExpectedVersion: version, ActionID: "record_delivery", Payload: payload, Actor: owner, AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: id, OperationID: id, PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC()})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func workflowLatestDeliveryAssertionSeq(ctx context.Context, s *Store, workID string) (int64, error) {
	var seq int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(MAX(assertion_seq),0) FROM workflow_delivery_state WHERE work_id=?`, workID).Scan(&seq)
	return seq, err
}

func TestDeliveryAssertionReaders(t *testing.T) {
	const workID = "delivery-assertion-readers"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{})
	s := f.store
	if err := runDeliveryAdmissionAction(t, s, workID, json.RawMessage(`{"delivery_artifact":"artifact:delivery","delivery_state":"asserted"}`), f.owner); err != nil {
		t.Fatal(err)
	}
	seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), s, workID)
	if err != nil || seq == 0 {
		t.Fatalf("delivery assertion sequence = %d, %v", seq, err)
	}
	assertion, err := workflowDeliveryAssertionRead(t.Context(), s.db, workID)
	if err != nil || assertion == nil || assertion.Artifact != "artifact:delivery" || assertion.Seq != seq {
		t.Fatalf("assertion read = %+v, %v", assertion, err)
	}
	var completions int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=? AND delivery_assertion=1`, workID, WorkflowActionCompleted).Scan(&completions); err != nil || completions != 1 {
		t.Fatalf("asserted delivery completions = %d, %v", completions, err)
	}
	if got := workflowPlanFixtureFrontier(t, s, workID); got != "review" {
		t.Fatalf("delivery advanced frontier to %q", got)
	}
	pin, err := ReadWorkPin(t.Context(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Step != "review" || !workPinContainsAction(pin.NextValidIntents, "record_review") || !workPinContainsAction(pin.NextValidIntents, "record_delivery") {
		t.Fatalf("delivery pin step=%q intents=%+v", pin.Step, pin.NextValidIntents)
	}
}
