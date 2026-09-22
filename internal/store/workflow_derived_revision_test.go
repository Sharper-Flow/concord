package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// seedDerivedLawExtension adds a legislated law next to the derived spec:one
// that seedWorkflowLaw seeds. Migration 98 defaults every law_subjects row to
// derived, so the legislated row is stated explicitly.
func seedDerivedLawExtension(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier) VALUES('project','workflow-law-locator','spec:legislated','spec','accepted','docs/decisions/spec-legislated.md','Synthetic legislated test law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test','legislated'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// runDerivedLawApproval drives one approve_contract on the generic one-off
// workflow and reports the action error instead of failing on it, because
// both the admitted and the refused path are the point of the test.
func runDerivedLawApproval(t *testing.T, s *Store, workID string, version int64, actor WorkflowActor, payload json.RawMessage) (int64, error) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: testApprovalPayload("approve_contract", payload), Actor: actor,
		AcceptedInputsDigest: "sha256:derived-revision", IdempotencyIdentity: "approve-derived-" + workID, OperationID: "approve-derived-" + workID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "approve-derived-" + workID,
		RequestID: "request:approve-derived-" + workID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result.ResultingVersion, nil
}

func seedGenericOneOffWorkflow(t *testing.T, s *Store, workID string, actor WorkflowActor) int64 {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.generic_one_off")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return 4
}

// TestDerivedLawRevisionOnNonProductChangingContract holds the carve-out
// CD-0041 D5 states and the CON-336 decision enacted: a contract that does
// not change Product truth may carry law_modifies naming only derived
// records, the modification stays visible on the approved contract, and a
// legislated name is still refused. The architecture binding refusal is
// untouched.
func TestDerivedLawRevisionOnNonProductChangingContract(t *testing.T) {
	t.Parallel()
	actor := WorkflowActor{PrincipalRef: "principal:derived-revision", ClientRef: "client:derived-revision", AgentRef: "agent:derived-revision", SessionRef: "session:derived-revision", ActorClass: ActorAgent}
	docBytes, err := os.ReadFile("../../docs/decisions/CD-0041-architecture-bound-product-law.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docBytes), "revise `derived` law in-contract") {
		t.Fatal("CD-0041 D5 does not state the derived carve-out")
	}

	t.Run("derived only modification is admitted and recorded", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		workID := "derived-revision-admitted"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		version := seedGenericOneOffWorkflow(t, s, workID, actor)
		payload := json.RawMessage(`{"premise":"Revise the derived record inside this contract.","spec_mandate":["spec:one"],"law_modifies":["spec:one"],"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:derived-revision","immutable_subject_ref":"commit:derived-revision","expected_result":"pass"}}]}`)
		if _, err := runDerivedLawApproval(t, s, workID, version, actor, payload); err != nil {
			t.Fatalf("a derived-only law_modifies was refused: %v", err)
		}
		var stored string
		if err := s.DatabaseForTesting().QueryRow(`SELECT law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stored, "spec:one") {
			t.Fatalf("the approved contract dropped the derived revision line: %s", stored)
		}
	})

	t.Run("a legislated modification stays refused", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		workID := "derived-revision-refused"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		version := seedGenericOneOffWorkflow(t, s, workID, actor)
		payload := json.RawMessage(`{"premise":"Attempt to revise legislated law without a Product-changing workflow.","spec_mandate":["spec:legislated"],"law_modifies":["spec:legislated"],"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:derived-revision","immutable_subject_ref":"commit:derived-revision","expected_result":"pass"}}]}`)
		_, err := runDerivedLawApproval(t, s, workID, version, actor, payload)
		if err == nil {
			t.Fatal("a legislated law_modifies was admitted on a non-Product-changing contract")
		}
		var failure *Failure
		if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "may revise only derived law") {
			t.Fatalf("refusal does not name the tier boundary: %v", err)
		}
	})

	t.Run("the architecture binding refusal is unchanged", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		workID := "derived-revision-binding"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		seedDerivedLawExtension(t, s)
		version := seedGenericOneOffWorkflow(t, s, workID, actor)
		payload := json.RawMessage(`{"premise":"Attempt to carry a binding without Product truth.","spec_mandate":[],"law_modifies":[],"architecture_binding":{"domain_registry_content_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]},"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:derived-revision","immutable_subject_ref":"commit:derived-revision","expected_result":"pass"}}]}`)
		_, err := runDerivedLawApproval(t, s, workID, version, actor, payload)
		if err == nil {
			t.Fatal("an architecture binding was admitted on a non-Product-changing contract")
		}
		var failure *Failure
		if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "cannot carry architecture_binding") {
			t.Fatalf("refusal does not name the binding boundary: %v", err)
		}
	})
}
