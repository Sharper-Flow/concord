package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func runIssue933OperatorAction(t *testing.T, s *Store, workID, action string, payload json.RawMessage, owner WorkflowActor, operator WorkflowActor) error {
	t.Helper()
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		return err
	}
	operationID := fmt.Sprintf("issue933-%s-%s-%d", action, workID, version)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner, OperatorActor: &operator,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC(),
	})
	if err != nil {
		_ = leaveFold(context.Background(), tx)
		return err
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		return err
	}
	return tx.Commit()
}

func TestIssue933LateVerdictRecoveryHoldsStepAndRefusesHealthyReplacement(t *testing.T) {
	const workID = "issue933-late-verdict"
	s, owner := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record mismatch verdict: %v", err)
	}
	operator := operatorVerdictActor(t, workID)
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), owner, operator); err != nil {
		t.Fatalf("confirm premise: %v", err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step after premise confirmation = %q, want release", step)
	}

	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("late healthy verdict recovery: %v", err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step after late verdict = %q, want release", step)
	}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err == nil {
		t.Fatal("healthy comparable verdict replacement succeeded")
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("refused healthy replacement changed verdict count from %d to %d", before, after)
	}
}

func TestIssue933PremiseRevisionUsesTypedContractSupersession(t *testing.T) {
	const workID = "issue933-contract-correction"
	s, owner := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}
	// The operator question gate requires an investigation observation naming
	// a current Domain and another work item before it offers the question.
	seedComparisonObservation(t, s, workID)
	question, err := ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.ActionID != "confirm_premise" {
		t.Fatalf("operator question = %+v, want confirm_premise", question)
	}
	var reviseAction string
	for _, choice := range question.Choices {
		if choice.ID == "revise" {
			reviseAction = choice.ActionID
		}
	}
	if reviseAction != "supersede_contract" {
		t.Fatalf("revise choice action = %q, want supersede_contract", reviseAction)
	}

	operator := operatorVerdictActor(t, workID)
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:issue933-correction"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, operator); err != nil {
		t.Fatalf("supersede contract at premise checkpoint: %v", err)
	}
	var currentVersion, activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&currentVersion); err != nil {
		t.Fatal(err)
	}
	if currentVersion != 1 {
		t.Fatalf("active contract count = %d, want 1", currentVersion)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version = %d, want 2", activeVersion)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "acceptance" {
		t.Fatalf("step after contract correction = %q, want acceptance", step)
	}
	var verdicts []workflowVerdictRecordedPayload
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err = latestWorkflowVerdicts(context.Background(), tx, workID, 2)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].PredicateID != "predicate:primary" {
		t.Fatalf("compatible verdicts after correction = %+v, want the preserved primary verdict", verdicts)
	}
	question, err = ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.PremiseSummary != "corrected premise" {
		t.Fatalf("post-correction question = %+v, want corrected premise", question)
	}
}

func TestWorkflowActionPreflightResolvesContractCorrectionAtCheckpoint(t *testing.T) {
	const workID = "preflight-supersede-at-checkpoint"
	s, owner := seedItemAtAcceptance(t, workID, false)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, verdictReviewer(t, workID)); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}

	version := verdictItemVersion(t, s, workID)
	payload := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:preflight"]}`)
	request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: "acceptance", ActionID: "supersede_contract", Payload: payload, Actor: owner}
	if err := testWorkflowActionPreflight(context.Background(), s, request); err != nil {
		t.Fatalf("contract correction preflight: %v", err)
	}

	request.ActionID = "undeclared_action"
	request.Payload = json.RawMessage(`{}`)
	if err := testWorkflowActionPreflight(context.Background(), s, request); err == nil {
		t.Fatal("undeclared action passed preflight")
	}
}
