package store

import (
	"context"
	"testing"
	"time"
)

type workflowReturnRouteFixture struct {
	store    *Store
	owner    WorkflowActor
	operator WorkflowActor
}

func seedWorkflowReturnRouteFixture(t *testing.T, workID string, planType WorkflowStepPlanType, frontier string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithPlan(t, workID, planType, frontier, []string{"verification", "review", "artifact"})
}

func seedWorkflowReturnRouteFixtureWithPlan(t *testing.T, workID string, planType WorkflowStepPlanType, frontier string, boundKinds []string) workflowReturnRouteFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, planType, frontier)
	version := readWorkVersion(t, s, workID)
	nextEvent := func(event Event) Event {
		version++
		return event
	}
	events := []Event{nextEvent(workflowEventWithActor("return-operator-"+workID, WorkflowActorRecorded, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(operator.ActorClass),
	}))}
	contractPayload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "legislative_subjects": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_boundary_version": 1, "consequence_class": "internal_sqlite",
	}
	if planType != WorkflowStepPlanResearch {
		// The registry root "product-root:product" is the Product-wide Domain
		// the fixture registry commits; "root" is its child. Root-scope
		// selections (product-wide rationale) name the real root, so the
		// approved affected scope admits both.
		contractPayload["architecture_binding"] = WorkflowArchitectureBinding{
			HomeDomainID: "root", AffectedDomainIDs: []string{"root", "product-root:product"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		}
	}
	contract := workflowEventWithActor("return-contract-"+workID, WorkflowContractApproved, workID, ownerRef, contractPayload)
	events = append(events, nextEvent(withPlanDecisionFact(t, planType, contract)))
	for _, kind := range boundKinds {
		evidenceRef := "evidence:return-route-" + kind
		seedWorkflowAuthority(t, s, "return-authority-"+kind+"-"+workID, workID, "principal/return-route-"+kind, "request/return-route-"+kind, []string{evidenceRef})
		events = append(events, nextEvent(workflowEventWithActor("return-evidence-"+kind+"-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind,
			"immutable_subject_ref": evidenceRef, "producer_id": "principal/return-route-" + kind, "producer_run_ref": "return-authority-" + kind + "-" + workID,
			"producer_watermark": "request/return-route-" + kind, "observed_at": "2026-09-12T00:00:00Z",
		})))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): readWorkVersion(t, s, workID)}}); err != nil {
		t.Fatal(err)
	}
	return workflowReturnRouteFixture{store: s, owner: owner, operator: operator}
}

func countWorkflowCompletionEvents(t *testing.T, s *Store, id string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, id, WorkflowCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
