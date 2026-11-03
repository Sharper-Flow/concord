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
// accepted Review, and Main's criterion association when associate is set.
// domains is the active contract's decision fact; a non-empty fact revises the seeded engineering contract through the
// ordinary update before the candidate is reviewed, so every proof binds the
// decision-bearing successor.
func rAcceptance(t *testing.T, workID string, domains []string, associate bool) acceptanceRecoveryFixture {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{"verification"}, []string{"verification", "review", "artifact"})
	t.Cleanup(func() { _ = route.store.Close() })
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	if len(domains) > 0 {
		rReviseContract(t, f.store, workID, ownerRefForTest(t, f.owner), domains)
	}
	return qAssociateCurrentProof(t, f, "", associate)
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
	ctx, err := f.store.PrepareWorkflowActionProof(t.Context(), "complete", f.workID)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, f.store, f.workID)
	id := fmt.Sprintf("complete-%s-%d", f.workID, version)
	request := WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: "complete", Actor: f.owner,
		Payload:              json.RawMessage(`{"impact_verdict":"non-breaking"}`),
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64), IdempotencyIdentity: id, OperationID: id,
		PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id,
		RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Unix(13, 0).UTC(),
	}
	return f.store.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, request)
		return err
	})
}
