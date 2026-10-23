package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// rAcceptance seeds a workflow.implementation item at acceptance through the
// current owners: a current integrated candidate, a captured receipt, Main's
// accepted Review, Main's criterion association when associate is set, and the
// recorded investigation. domains is the active contract's decision fact; a
// non-empty fact supersedes the seeded engineering contract through the
// registered fold before the candidate is reviewed, so every proof binds the
// decision-bearing successor.
func rAcceptance(t *testing.T, workID string, domains []string, associate bool) acceptanceRecoveryFixture {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
	t.Cleanup(func() { _ = route.store.Close() })
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	if len(domains) > 0 {
		rReclassifyContract(t, f, domains)
	}
	return qAssociateCurrentProof(t, f, "", associate)
}

// rReclassifyContract supersedes contract 1 with a successor that keeps its
// predicates and declares domains as the decision fact.
func rReclassifyContract(t *testing.T, f workContextFixture, domains []string) {
	t.Helper()
	predicates, err := readWorkflowContractPredicates(t.Context(), f.store.db, f.workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	successorPredicates := make([]map[string]any, 0, len(predicates))
	for ordinal, predicate := range predicates {
		successorPredicates = append(successorPredicates, map[string]any{"predicate_id": predicate.PredicateID, "ordinal": ordinal, "outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload)})
	}
	version := readWorkVersion(t, f.store, f.workID)
	successor := map[string]any{
		"contract_version": 2, "premise": "deliver the checked decision-bearing change",
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
	event := workflowTypedEvent("r-reclassify-"+f.workID, WorkflowContractSuperseded, f.workID, ownerRefForTest(t, f.owner), time.Unix(14, 0).UTC(), version, map[string]any{
		"previous_contract_version": 1, "predecessor_contract_versions": []int64{1}, "new_contract_version": 2,
		"supersede_reason": "declare the decision the delivered work carries", "audit_evidence": []string{"audit:r-reclassify-" + f.workID},
		"successor_contract": successor,
	})
	appendWorkflowEventsWithRegistry(t, f.store, BuiltinWorkflowRegistry(), Operation{
		Events:           []Event{withPinnedDecisionFact(t, mustBuiltinDefinition(t, "workflow.implementation").Definition, event)},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): version},
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
