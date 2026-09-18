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
	t.Parallel()
	const workID = "issue933-late-verdict"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
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
	t.Parallel()
	const workID = "issue933-contract-correction"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
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
	if binding, err := WorkflowFailedWorkerRetryBinding(context.Background(), s, workID); err != nil {
		t.Fatalf("public correction path after contract supersession: %v", err)
	} else if binding != nil {
		t.Fatalf("correction path returned a retry binding without a failed worker: %#v", binding)
	}
	question, err = ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.PremiseSummary != "corrected premise" {
		t.Fatalf("post-correction question = %+v, want corrected premise", question)
	}
}

func TestIssue933CorrectionPreflightAfterContractSupersession(t *testing.T) {
	t.Parallel()
	const workID = "issue933-correction-preflight"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatalf("record return-route mismatch verdict: %v", err)
	}
	seedComparisonObservation(t, s, workID)
	question, err := ReadWorkflowOperatorQuestion(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if question == nil || question.ActionID != "confirm_premise" {
		t.Fatalf("operator question = %+v, want confirm_premise", question)
	}
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:return-route","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:return-route","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","architecture_binding":{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]},"supersede_reason":"correct the accepted premise","audit_evidence":["evidence:issue933-correction"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, fixture.operator); err != nil {
		t.Fatalf("supersede contract at premise checkpoint: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := json.RawMessage(`{"diagnosis":"the delivered subject still fails","strategy":"repeat the external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := InspectWorkflowActionAdmission(ctx, s, WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: "request_correction", Payload: payload, Actor: owner,
	}); err != nil {
		t.Fatalf("public correction preflight after contract supersession: %v", err)
	}
	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "request_correction")
	if err != nil {
		t.Fatalf("public correction action after contract supersession: %v", err)
	}
	if action.ID != "request_correction" {
		t.Fatalf("public correction action = %q, want request_correction", action.ID)
	}
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, owner, fixture.operator); err != nil {
		t.Fatalf("public correction path after contract supersession: %v", err)
	}
}

func TestWorkflowActionPreflightResolvesContractCorrectionAtCheckpoint(t *testing.T) {
	t.Parallel()
	const workID = "preflight-supersede-at-checkpoint"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, verdictReviewer(t, s, workID)); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}

	version := verdictItemVersion(t, s, workID)
	payload := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:preflight"]}`)
	request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: "acceptance", ActionID: "supersede_contract", Payload: payload, Actor: owner}
	if err := InspectWorkflowActionAdmission(context.Background(), s, request); err != nil {
		t.Fatalf("contract correction preflight: %v", err)
	}

	request.ActionID = "undeclared_action"
	request.Payload = json.RawMessage(`{}`)
	if err := InspectWorkflowActionAdmission(context.Background(), s, request); err == nil {
		t.Fatal("undeclared action passed preflight")
	}
}

// A verdict that omits contract_version must resolve the active contract,
// not contract 1. After any supersession a caller that does not name a
// version records against the successor, so verdict and confirmation read
// one approval set instead of deadlocking between versions.
func TestVerdictOmittingContractVersionResolvesActiveContract(t *testing.T) {
	t.Parallel()
	const workID = "verdict-version-default-supersede"
	s, owner, _ := seedItemAtAcceptance(t, workID, false)
	reviewer := verdictReviewer(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("record compatible verdict: %v", err)
	}
	seedComparisonObservation(t, s, workID)
	operator := operatorVerdictActor(t, workID)
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}},{"predicate_id":"predicate:added","ordinal":1,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:added","immutable_subject_ref":"commit:` + workID + `-added","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"add a successor predicate","audit_evidence":["evidence:verdict-default"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, owner, operator); err != nil {
		t.Fatalf("supersede contract: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"predicate_id":"predicate:added","verdict_kind":"ok"}`), 0, reviewer); err != nil {
		t.Fatalf("verdict without contract_version must resolve the active contract: %v", err)
	}
	var recorded int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.contract_version') FROM domain_events WHERE subject_id=? AND kind='workflow.verdict_recorded' ORDER BY seq DESC LIMIT 1`, workID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("verdict contract_version=%d, want 2", recorded)
	}
}
