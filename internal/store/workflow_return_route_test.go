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

func seedWorkflowReturnRouteFixture(t *testing.T, workID, definitionRef, verdictStep string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureRequiring(t, workID, definitionRef, verdictStep, []string{"verification"}, []string{"verification", "review", "artifact"})
}

func seedWorkflowReturnRouteFixtureRequiring(t *testing.T, workID, definitionRef, verdictStep string, requiredEvidence, boundKinds []string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, currentFixtureDefinition(t, definitionRef), verdictStep, requiredEvidence, boundKinds)
}

func seedCurrentWorkflowReturnRouteFixture(t *testing.T, workID, definitionRef, verdictStep string) workflowReturnRouteFixture {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Current(definitionRef)
	if !ok {
		t.Fatalf("workflow definition %s is not registered", definitionRef)
	}
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, verdictStep, []string{"verification"}, []string{"verification", "review", "artifact"})
}

func seedWorkflowReturnRouteFixtureWithDefinition(t *testing.T, workID string, registered RegisteredDefinition, verdictStep string, requiredEvidence, boundKinds []string, predicates ...map[string]any) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithSource(t, workID, registered, verdictStep, requiredEvidence, boundKinds, nil, predicates...)
}

// beforeInitialize models source state already delivered before this item
// starts. The initializer records that state; the hook never edits it.
func seedWorkflowReturnRouteFixtureWithSource(t *testing.T, workID string, registered RegisteredDefinition, verdictStep string, requiredEvidence, boundKinds []string, beforeInitialize func(*Store), predicates ...map[string]any) workflowReturnRouteFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	if beforeInitialize != nil {
		beforeInitialize(s)
	}
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
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	nextEvent := func(event Event) Event {
		version++
		return event
	}
	events := []Event{nextEvent(workflowEventWithActor("return-operator-"+workID, WorkflowActorRecorded, workID, operatorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(operator.ActorClass),
	}))}
	outcomePredicate := map[string]any{
		"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
		"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
	}
	checkAdmitted := false
	for _, kind := range registered.Definition.OutcomeSchema.AllowedKinds {
		if kind == PredicateCheck {
			checkAdmitted = true
		}
	}
	if !checkAdmitted {
		outcomePredicate["outcome_kind"] = "outcome"
		outcomePredicate["outcome_payload"] = map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}
	}
	if len(predicates) == 0 {
		predicates = []map[string]any{outcomePredicate}
	}
	contractPayload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
		"outcome_predicates": predicates,
		"required_evidence":  requiredEvidence, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_boundary_version": 1, "rigor_class": "prototype_internal",
		"consequence_class": "internal_sqlite",
	}
	if registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth {
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
	events = append(events, nextEvent(withPinnedDecisionFact(t, registered.Definition, contract)))
	for _, kind := range boundKinds {
		evidenceRef := "evidence:return-route-" + kind
		seedWorkflowAuthority(t, s, "return-authority-"+kind+"-"+workID, workID, "principal/return-route-"+kind, "request/return-route-"+kind, []string{evidenceRef})
		events = append(events, nextEvent(workflowEventWithActor("return-evidence-"+kind+"-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind,
			"immutable_subject_ref": evidenceRef, "producer_id": "principal/return-route-" + kind, "producer_run_ref": "return-authority-" + kind + "-" + workID,
			"producer_watermark": "request/return-route-" + kind, "observed_at": "2026-09-12T00:00:00Z",
		})))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, verdictStep, ownerRef); err != nil {
		t.Fatal(err)
	}
	return workflowReturnRouteFixture{store: s, owner: owner, operator: operator}
}

func TestPReturnRouteCurrentReviewFixture(t *testing.T) {
	j := pReturnReviewFixture(t, "p-return-current-review", "workflow.break_fix", "correction_required")
	pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
	if err != nil || pin.Review == nil || pin.Review.Accepted == nil || pin.Step != "repair" || !pin.Review.DependenciesCurrent || pin.Review.Candidate == nil {
		t.Fatalf("current accepted Review fixture=%+v err=%v", pin, err)
	}
}

func prepareVerdictCorrectionFixture(t *testing.T, id string) workflowReturnRouteFixture {
	t.Helper()
	j := pReturnReviewFixture(t, id, "workflow.implementation", "correction_required")
	return workflowReturnRouteFixture{store: j.s, owner: j.fixture.owner, operator: j.fixture.operator}
}

func TestVerdictCorrectionSequenceNeedsConjunctiveHealthyVerdicts(t *testing.T) {
	const id = "return-route-conjunctive-sequence"
	f, ref := seedLateCriterionAssociation(t, id)
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:first"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	predicates, err := readWorkflowContractPredicates(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(predicates) != 2 || len(verdicts) != 1 {
		t.Fatalf("partial proof predicates=%v associations=%+v", predicates, verdicts)
	}
	if !issue1013HasIntent(issue1013Pin(t, f.store, id), "record_verdict") {
		t.Fatal("partial proof closed missing-proof route")
	}
	if err := runVerdictActionAs(t, f.store, id, "record_verdict", criterionAssociationPayload(ref, "predicate:second"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err = latestWorkflowVerdicts(t.Context(), f.store.db, id, 1)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("complete conjunction=%+v err=%v", verdicts, err)
	}
}

func countWorkflowCompletionEvents(t *testing.T, s *Store, id string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, id, WorkflowCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
