package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// Confirmation binds to the current contract version. A changed premise needs
// a new confirmation even when the criterion content and its proof are intact.
func TestSupersessionInvalidatesPriorPremiseConfirmation(t *testing.T) {
	t.Parallel()
	const workID = "cd31-work"
	f := qCurrentAcceptance(t, workID)
	s := f.store
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.run(t.Context(), "confirm_premise", "confirm-first", f.version(t.Context(), t), nil, nil); err != nil {
		t.Fatal(err)
	}
	var confirmed int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("first confirmation count = %d, want 1: %v", confirmed, err)
	}
	var outcome string
	if err := s.db.QueryRow(`SELECT outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1`, workID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"contract_version": 2, "premise": "Align the five sibling provider services.", "decision_domains": []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check", "outcome_payload": json.RawMessage(outcome)}},
		"required_evidence":  []string{"verification"}, "route_conventions": []string{workflowCompleteStepCorrectionRoute}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
		"architecture_binding": WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}},
	}
	fields["law_boundary_version"] = 1
	fields["consequence_class"] = "internal_sqlite"
	recordCompleteStepContradictionObservation(t, s, workID, f.owner)
	version := f.version(t.Context(), t)
	supersession := workflowEventWithActor("cd31-supersede", WorkflowContractSuperseded, workID, actor, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 1, "new_contract_version": 2,
		"supersede_reason": "The direction widens the premise to five siblings.", "audit_evidence": []string{"operator-direction"}, "successor_contract": fields,
	})
	supersession.PayloadVersion = workflowContractSupersededLawChangePayloadVersion
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{supersession}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations c WHERE c.work_id=? AND c.contract_version=(SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL)`, workID, workID).Scan(&confirmed); err != nil || confirmed != 0 {
		t.Fatalf("successor borrowed the old confirmation: %d, %v", confirmed, err)
	}
	for _, kind := range []string{"verification", "review", "artifact"} {
		version = f.version(t.Context(), t)
		binding := workflowEventWithActor("successor-contract-evidence-"+kind, WorkflowEvidenceBound, workID, actor, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind,
			"immutable_subject_ref": "evidence:return-route-" + kind, "producer_id": "principal/return-route-" + kind,
			"producer_run_ref": "return-authority-" + kind + "-" + workID, "producer_watermark": "request/return-route-" + kind, "observed_at": "2026-09-12T00:00:00Z",
		})
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{binding}, ExpectedVersions: workVersion(workID, version)}); err != nil {
			t.Fatal(err)
		}
	}
	j := &candidateJourney{fixture: workContextFixture{store: s, workID: workID, owner: f.owner, operator: f.operator}, s: s, workID: workID, repo: f.repo, acceptor: f.owner}
	reviewGateStartStep(t, s, workID, "execution", "start_execution", f.owner)
	run := j.observeRun(t, "successor-proof")
	j.bindVerification(t, run)
	qAcceptCriterionReview(t, j.fixture, run.OperationRef)
	if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "delivery:successor", "delivery_state": "asserted"}, f.owner); err != nil {
		t.Fatal(err)
	}
	run = j.observeRun(t, "successor-refine")
	reviewGateStartStep(t, s, workID, "refine", "checkpoint_refine", f.owner)
	j.bindVerification(t, run)
	qAcceptCriterionReview(t, j.fixture, run.OperationRef)
	if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "delivery:successor", "delivery_state": "asserted"}, f.owner); err != nil {
		t.Fatal(err)
	}
	if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "delivery:successor", "delivery_state": "asserted"}, f.owner); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload(run.OperationRef, "predicate:return-route"), 0, f.owner); err != nil {
		var step string
		_ = s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step)
		t.Fatalf("successor criterion association at %s: %v", step, err)
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, workID, "release", actor); err != nil {
		t.Fatal(err)
	}
	bindCompletionClosure(t, s, workID, f.owner)
	version = f.version(t.Context(), t)
	completion := workflowCompletedFixtureEvent("cd31-terminal", workID, actor, actor, version)
	err = CompleteWorkflow(t.Context(), s, completion)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || failure.Clause != 5 || !strings.Contains(failure.Detail, "premise") {
		t.Fatalf("completion after supersession = %v, want the premise refusal at clause 5", err)
	}
}
