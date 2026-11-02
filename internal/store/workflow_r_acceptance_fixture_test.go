package store

import (
	"testing"
)

// rAcceptance seeds a workflow.implementation item at acceptance through the
// current owners: a current integrated candidate, a captured receipt, Main's
// accepted Review, Main's criterion association when associate is set, and the
// recorded investigation. domains is the active contract's decision fact; a
// investigation false leaves the investigation unrecorded. A
// non-empty fact revises the seeded engineering contract through the
// ordinary update before the candidate is reviewed, so every proof binds the
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

// rReviseContract updates the active contract with a successor that keeps
// its predicates and declares domains as the decision fact.
func rReviseContract(t *testing.T, s *Store, workID, actorRef string, domains []string) {
	t.Helper()
	var actor WorkflowActor
	if err := s.db.QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&actor.PrincipalRef, &actor.ClientRef, &actor.AgentRef, &actor.SessionRef, &actor.ActorClass); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	successor := pContractSuccessor(t, s, workID, "deliver the checked change under its revised decision fact")
	successor["decision_domains"] = domains
	// The same Domain write authority the seeded contract carries, plus
	// the committed registry's Product root for root-scope selections.
	successor["architecture_binding"] = WorkflowArchitectureBinding{
		HomeDomainID: "root", AffectedDomainIDs: []string{"root", "product-root:product"},
		DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
		VerificationObligations: []WorkflowVerificationObligation{},
	}
	if err := reviseContractRecord(t, s, workID, actor, successor, version, nil); err != nil {
		t.Fatal(err)
	}
}

// rComplete runs the ordinary public completion as the executing coordinator.
func rComplete(t *testing.T, f acceptanceRecoveryFixture) error {
	t.Helper()
	return runCoordinatorActionTx(t, f.store, BuiltinWorkflowRegistry(), f.workID, "complete", f.owner, `{"impact_verdict":"non-breaking"}`)
}

// rAcceptanceModel is the abstract admission state of an rAcceptance item:
// one active contract, the recorded Review of the current candidate with its
// criterion association
// (the model's ok verdict), and the recorded investigation.
func rAcceptanceModel() admissionModelState {
	return admissionModelState{
		step: "acceptance", contracts: 1, verdict: "ok", artifact: true,
		review: admissionModelReview{accepted: "ready_for_final_proof", candidate: true, current: true},
	}
}
