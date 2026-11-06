package store

import (
	"context"
	"strings"
	"testing"
)

func TestContractApprovalRefusesDuplicateActiveProjection(t *testing.T) {
	t.Parallel()
	const workID = "duplicate-active-approval"
	s, owner := seedItemAtAcceptance(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version)
SELECT work_id,2,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version FROM workflow_contracts WHERE work_id=? AND contract_version=1;
DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}

	version := readWorkVersion(t, s, workID)
	event := workflowEventWithActor("duplicate-approval", WorkflowContractApproved, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 3, "legislative_subjects": []string{},
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	err = foldWorkflowContractApproved(context.Background(), tx, event, nil)
	_ = leaveWrite(context.Background(), tx)
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "requires no active workflow contract") {
		t.Fatalf("duplicate active approval error = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("refused approval changed contract count to %d", count)
	}
}

func TestContractRecordUpdateRefusesDuplicateActiveProjection(t *testing.T) {
	t.Parallel()
	const workID = "duplicate-active-recovery"
	s, owner := seedItemAtAcceptance(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	contract := pContractSuccessor(t, s, workID)
	seedDuplicateContractProjection(t, s, workID, ownerRef)
	version := readWorkVersion(t, s, workID)
	before := contractRecordEffectSnapshot(t, s, workID)
	err = reviseContractRecord(t, s, workID, owner, contract, version)
	if !hasFailureKind(err, KindInvariantViolation) || !strings.Contains(err.Error(), "multiple active contracts") {
		t.Fatalf("duplicate active revision = %v", err)
	}
	if after := contractRecordEffectSnapshot(t, s, workID); after != before {
		t.Fatal("refused duplicate revision changed the record")
	}
	var active int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("refused revision changed duplicate contracts: active=%d", active)
	}
}
