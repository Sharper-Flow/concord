package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Domain identities of the committed fixture registry. The contract approves
// the Product root and child-alpha; child-unaffected is current but outside
// the approved affected scope; deprecated-domain is not current. Registry and
// Domain staleness are structurally impossible beside the one same-commit
// snapshot.
const (
	workContextTestRoot       = "product-root:product"
	workContextTestChild      = "child-alpha"
	workContextTestUnaffected = "child-unaffected"
	workContextTestDeprecated = "deprecated-domain"
)

// seedWorkContextDomainRegistry commits the Domain registry fixture into the
// workflow law home's repo: the Product root, the affected child, a current
// child outside the approved affected scope, a deprecated child, and the
// committed spec fixture's home Domain. The retired SQL Domain projection
// stays empty — the validator reads the committed registry.
func seedWorkContextDomainRegistry(t *testing.T, s *Store) {
	t.Helper()
	registry := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "product", RootDomainID: "product-root:product",
		Domains: []KnowledgeDomain{
			{DomainID: "product-root:product", Name: "Root", Purpose: "Governs the synthetic Product.", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: "root", Name: "Root", Purpose: "Fixture root child Domain.", Status: "current", ParentDomainID: "product-root:product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: "child", Name: "Child", Purpose: "Fixture child Domain.", Status: "current", ParentDomainID: "product-root:product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: workContextTestChild, Name: "Affected child", Purpose: "The approved affected child.", Status: "current", ParentDomainID: "product-root:product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: workContextTestUnaffected, Name: "Unaffected", Purpose: "Current outside the approved affected scope.", Status: "current", ParentDomainID: "product-root:product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: workContextTestDeprecated, Name: "Deprecated", Purpose: "Retired scope.", Status: "deprecated", ParentDomainID: "product-root:product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: "product-root:fixture", Name: "Law fixture home", Purpose: "The committed spec fixture's home Domain.", Status: "current", ParentDomainID: "product-root:product", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		},
	}
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	var lawRepo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE project_id='project' AND locator_id='workflow-law-locator'`).Scan(&lawRepo); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, lawRepo, knowledgeRegistryPath, string(encoded)+"\n")
	commitKnowledgeRepo(t, lawRepo, "work context registry fixture")
}

// workContextFixture is one seeded workflow at a plan frontier with an
// approved Product-changing contract and a committed Domain registry.
type workContextFixture struct {
	store    *Store
	workID   string
	owner    WorkflowActor
	operator WorkflowActor
}

// seedWorkContextFixture seeds one fix work item whose approved contract
// affects root and child-alpha, advanced to implement, with the registry
// fixture above installed.
func seedWorkContextFixture(t *testing.T, workID string) workContextFixture {
	t.Helper()
	return seedWorkContextFixtureWithPlan(t, workID, WorkflowStepPlanFix, "implement")
}

func seedWorkContextFixtureWithPlan(t *testing.T, workID string, planType WorkflowStepPlanType, frontier string) workContextFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedWorkContextDomainRegistry(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	plan := currentWorkflowPlanFixture(t, planType)
	seedWorkflowPlanFrontier(t, s, workID, plan.Type, frontier)
	initialVersion := readWorkVersion(t, s, workID)
	version := initialVersion
	events := []Event{workflowEventWithActor("ctx-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef,
		"agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": string(owner.ActorClass),
	})}
	version++
	contract := map[string]any{
		"contract_version": 1, "premise": "deliver the checked change", "decision_domains": []string{},
		"legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"architecture_binding": WorkflowArchitectureBinding{
			HomeDomainID: workContextTestRoot, AffectedDomainIDs: []string{workContextTestRoot, workContextTestChild},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
		},
	}
	if planType == WorkflowStepPlanResearch {
		delete(contract, "architecture_binding")
	}
	events = append(events, withPlanDecisionFact(t, planType, workflowTypedEvent("ctx-contract-"+workID, WorkflowContractApproved, workID, ownerRef, time.Unix(5, 0).UTC(), version, contract)))
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): initialVersion}}); err != nil {
		t.Fatal(err)
	}
	return workContextFixture{store: s, workID: workID, owner: owner, operator: operator}
}

// workContextCheckpointPayload is one valid checkpoint_context declaration
// other tests use as the hold action that advances the work version without
// moving the plan frontier.
func workContextCheckpointPayload(id string) json.RawMessage {
	payload := map[string]any{
		"checkpoint_id": id, "active_unit": "implementation",
		"hypothesis": "the checked change holds", "diagnosis": "the fixture holds the step",
		"strategy":     "hold the step and advance the version",
		"touched_refs": []string{"internal/store/workflow.go"}, "evidence_refs": []string{"internal/store/workflow_test.go"},
		"pending_questions": []string{}, "pending_decisions": []string{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return raw
}

func contextFindingFixture() WorkContextFinding {
	return WorkContextFinding{
		Kind: "observation", Statement: "the bounded read is the only admission route",
		SubjectRef: "internal/store/work_context.go", EvidenceRefs: []string{"internal/store/work_context_test.go"},
		DomainID: workContextTestChild,
	}
}
