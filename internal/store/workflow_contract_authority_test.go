package store

import (
	"context"
	"strings"
	"testing"
)

func TestContractApprovalRefusesDuplicateActiveProjection(t *testing.T) {
	t.Parallel()
	const workID = "duplicate-active-approval"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
DELETE FROM fold_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}

	version := verdictItemVersion(t, s, workID)
	event := workflowEventWithActor("duplicate-approval", WorkflowContractApproved, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 3, "premise": "must refuse", "outcome_kind": "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:duplicate", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	err = foldWorkflowContractApproved(context.Background(), tx, event)
	_ = leaveFold(context.Background(), tx)
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

func TestDuplicateActiveContractsRecoverWithExactPredecessorSet(t *testing.T) {
	t.Parallel()
	const workID = "duplicate-active-recovery"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
DELETE FROM fold_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}
	if _, action, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil || action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("duplicate recovery action = %q, error = %v", action.ID, err)
	}
	version := verdictItemVersion(t, s, workID)
	event := workflowEventWithActor("duplicate-recovery", WorkflowContractSuperseded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 1, "predecessor_contract_versions": []int64{1, 2}, "new_contract_version": 3,
		"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:duplicate-recovery"},
		"successor_contract": map[string]any{
			"contract_version": 3, "premise": "recovered premise", "outcome_predicates": []map[string]any{{
				"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
				"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:recovery", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			}}, "required_evidence": []string{"verification"}, "route_conventions": []string{},
			"spec_mandate": []string{}, "law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{},
			"law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		},
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	var active, successor, superseded int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=3`, workID).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by=3`, workID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if active != 1 || successor != 1 || superseded != 2 {
		t.Fatalf("recovery projection active=%d successor=%d superseded=%d", active, successor, superseded)
	}
}
