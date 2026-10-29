package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestContractApprovalRefusesDuplicateActiveProjection(t *testing.T) {
	t.Parallel()
	const workID = "duplicate-active-approval"
	s, owner, _ := seedItemAtAcceptance(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
DELETE FROM write_guard`, workID, workID); err != nil {
		t.Fatal(err)
	}

	version := readWorkVersion(t, s, workID)
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

func TestDuplicateActiveContractsRecoverWithExactPredecessorSet(t *testing.T) {
	t.Parallel()
	const workID = "duplicate-active-recovery"
	s, owner, _ := seedItemAtAcceptance(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	seedDuplicateContractProjection(t, s, workID, ownerRef)
	version := readWorkVersion(t, s, workID)
	if _, action, err := WorkflowActionDefinitionFor(context.Background(), s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil || action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("duplicate recovery action = %q, error = %v", action.ID, err)
	}
	recoveryFields := map[string]any{
		"contract_version": 3, "predecessor_contract_versions": []int64{1, 2}, "premise": "recovered premise",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:recovery", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}}, "required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
		"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:duplicate-recovery"},
	}
	recoveryPayload, err := json.Marshal(recoveryFields)
	if err != nil {
		t.Fatal(err)
	}
	makeRecoveryEventFields := func(expectedVersion int64) map[string]any {
		return map[string]any{
			"work_id": workID, "expected_version": expectedVersion, "resulting_version": expectedVersion + 1,
			"previous_contract_version": int64(1), "predecessor_contract_versions": []int64{1, 2}, "new_contract_version": int64(3),
			"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:duplicate-recovery"},
			"approval_ref":              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"approval_operation_digest": "sha256:" + strings.Repeat("c", 64),
			"approval_scope_json":       `{}`,
			"approval_versions_json":    `{"work_id":"` + workID + `"}`,
			"approval_consequence":      "recovery",
			"successor_contract": map[string]any{
				"contract_version": 3, "premise": "recovered premise", "outcome_predicates": recoveryFields["outcome_predicates"],
				"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
				"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
				"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			},
		}
	}
	if err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: recoveryPayload, Actor: owner,
	}); err != nil {
		t.Fatalf("duplicate recovery preflight refused the reachable route: %v", err)
	}
	unauthorized := workflowEventWithActor("duplicate-recovery-agent", WorkflowContractSuperseded, workID, ownerRef, makeRecoveryEventFields(version))
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{unauthorized}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err == nil || !strings.Contains(err.Error(), "recorded operator approval actor") {
		t.Fatalf("agent duplicate recovery error = %v, want operator authorization refusal", err)
	}
	approvalRef := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		approvalRef, "sha256:"+strings.Repeat("c", 64), `{}`, `{"work_id":"`+workID+`"}`, "recovery", "principal/operator", "client/concord-1", "session/"+workID+"-recovery", "2026-09-10T00:00:00Z", "2026-09-11T00:00:00Z", 1, 1, "approval-evidence", "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:" + approvalRef, SessionRef: "session/" + workID + "-recovery", ActorClass: ActorOperator}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	operatorVersion := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("duplicate-recovery-operator", WorkflowActorRecorded, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": operatorVersion, "resulting_version": operatorVersion + 1,
		"actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(ActorOperator),
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): operatorVersion}}); err != nil {
		t.Fatal(err)
	}
	version = readWorkVersion(t, s, workID)
	// The fold owns the binding's self-consistency: the payload must name the
	// approval the recorded operator actor asserts, and every binding field
	// admission checked must be present. A mismatched digest is admission's
	// refusal, not the fold's, because the fold reads no approval row.
	unbound := makeRecoveryEventFields(version)
	unbound["approval_ref"] = "b0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("duplicate-recovery-unbound", WorkflowContractSuperseded, workID, operatorRef, unbound)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err == nil || !strings.Contains(err.Error(), "requires a recorded operator approval actor") {
		t.Fatalf("recovery binding outside the recorded operator actor was accepted: %v", err)
	}
	incomplete := makeRecoveryEventFields(version)
	incomplete["approval_consequence"] = ""
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("duplicate-recovery-incomplete", WorkflowContractSuperseded, workID, operatorRef, incomplete)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err == nil || !strings.Contains(err.Error(), "carries no complete operator approval binding") {
		t.Fatalf("incomplete recovery binding was accepted: %v", err)
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("rejected recovery approval changed work version from %d to %d", version, got)
	}
	event := workflowEventWithActor("duplicate-recovery", WorkflowContractSuperseded, workID, operatorRef, makeRecoveryEventFields(version))
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
