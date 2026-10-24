package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// rAcceptance seeds a workflow.implementation item at acceptance through the
// current owners: a current integrated candidate, a captured receipt, Main's
// accepted Review, Main's criterion association when associate is set, and the
// recorded investigation. domains is the active contract's decision fact; a
// investigation false leaves the investigation unrecorded. A
// non-empty fact supersedes the seeded engineering contract through the
// registered fold before the candidate is reviewed, so every proof binds the
// decision-bearing successor.
func rAcceptance(t *testing.T, workID string, domains []string, associate, investigation bool) acceptanceRecoveryFixture {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
	t.Cleanup(func() { _ = route.store.Close() })
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	if len(domains) > 0 {
		rReviseContract(t, f.store, workID, ownerRefForTest(t, f.owner), domains)
	}
	return qAssociateCurrentProof(t, f, "", associate, investigation)
}

// rReviseContract supersedes the active contract with a successor that keeps
// its predicates and declares domains as the decision fact.
func rReviseContract(t *testing.T, s *Store, workID, actorRef string, domains []string) {
	t.Helper()
	var active int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	predicates, err := readWorkflowContractPredicates(t.Context(), s.db, workID, active)
	if err != nil {
		t.Fatal(err)
	}
	successorPredicates := make([]map[string]any, 0, len(predicates))
	for ordinal, predicate := range predicates {
		successorPredicates = append(successorPredicates, map[string]any{"predicate_id": predicate.PredicateID, "ordinal": ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload)})
	}
	version := readWorkVersion(t, s, workID)
	successor := map[string]any{
		"contract_version": active + 1, "premise": "deliver the checked change under its revised decision fact",
		"outcome_predicates": successorPredicates,
		"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"decision_domains": domains,
		// The same Domain write authority the seeded contract carries.
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
	}
	id := fmt.Sprintf("r-revise-%s-%d", workID, active+1)
	event := workflowTypedEvent(id, WorkflowContractSuperseded, workID, actorRef, time.Unix(14, 0).UTC(), version, map[string]any{
		"previous_contract_version": active, "predecessor_contract_versions": []int64{active}, "new_contract_version": active + 1,
		"supersede_reason": "revise the decision the delivered work carries", "audit_evidence": []string{"audit:" + id},
		"successor_contract": successor,
	})
	appendWorkflowEventsWithRegistry(t, s, BuiltinWorkflowRegistry(), Operation{
		Events:           []Event{withPinnedDecisionFact(t, mustBuiltinDefinition(t, "workflow.implementation").Definition, event)},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
}

// rConfirmPremise answers confirm_premise through the public action route.
// operator and digest are set for decision-bearing work only.
func rConfirmPremise(t *testing.T, f acceptanceRecoveryFixture, actor WorkflowActor, operator *WorkflowActor, digest string) error {
	t.Helper()
	return runApprovalActionTx(t, f.store, BuiltinWorkflowRegistry(), f.workID, "confirm_premise", actor, operator, "confirm", digest, `{"selected_choice":"confirm"}`)
}

// rComplete runs the ordinary public completion as the executing coordinator.
func rComplete(t *testing.T, f acceptanceRecoveryFixture) error {
	t.Helper()
	return runApprovalActionTx(t, f.store, BuiltinWorkflowRegistry(), f.workID, "complete", f.owner, nil, "", "", `{"impact_verdict":"non-breaking"}`)
}

// rAcceptanceModel is the abstract admission state of an rAcceptance item:
// one active contract, the satisfied worker job in the Project, Main's
// accepted Review of the current candidate with its criterion association
// (the model's ok verdict), and the recorded investigation.
func rAcceptanceModel(engineering bool) admissionModelState {
	return admissionModelState{
		step: "acceptance", contracts: 1, verdict: "ok", artifact: true, engineering: engineering,
		jobs: "satisfied", jobsScope: "project",
		review: admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true},
	}
}
