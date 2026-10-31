package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// qCurrentAcceptance records a current implementation candidate, a captured
// command receipt, and an accepted Review assessment before associating proof.
// It leaves extra evidence kinds outstanding without changing frozen scenarios.
func qCurrentAcceptance(t *testing.T, workID string, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	return qCurrentAcceptanceWithDelivery(t, workID, "", extraRequiredKinds...)
}

func qCurrentAcceptanceWithDelivery(t *testing.T, workID, artifact string, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	return qBuildAcceptance(t, workID, artifact, true, extraRequiredKinds...)
}

func qBuildAcceptance(t *testing.T, workID, artifact string, associate bool, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	return qBuildAcceptancePredicates(t, workID, artifact, associate, extraRequiredKinds)
}

func qBuildAcceptancePredicates(t *testing.T, workID, artifact string, associate bool, extraRequiredKinds []string, predicates ...map[string]any) acceptanceRecoveryFixture {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "execution", append([]string{"verification"}, extraRequiredKinds...), []string{"verification", "review", "artifact"}, predicates...)
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	return qAssociateCurrentProof(t, f, artifact, associate)
}

// qAssociateCurrentProof keeps the seeded store and contract. The item must
// declare the current Review and criterion-association routes. It adds a
// managed claim and clean candidate before capturing the command receipt.
// Investigation is seeded by default; pass false to leave that gate unmet.
func qAssociateCurrentProof(t *testing.T, f workContextFixture, artifact string, associate bool, investigation ...bool) acceptanceRecoveryFixture {
	t.Helper()
	return qAssociateCurrentProofAt(t, f, "acceptance", artifact, associate, investigation...)
}

// qAssociateCurrentProofAt associates the proof at the family's named
// verdict step.
func qAssociateCurrentProofAt(t *testing.T, f workContextFixture, verdictStep, artifact string, associate bool, investigation ...bool) acceptanceRecoveryFixture {
	t.Helper()
	workID := f.workID
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}
	if artifact != "" {
		if err := j.action(t, "start_execution", map[string]any{}, f.owner); err != nil {
			t.Fatalf("start candidate production: %v", err)
		}
	}
	run := j.observeRun(t, "q-proof")
	j.bindVerification(t, run)
	qAcceptCriterionReview(t, f, repo, run.OperationRef)
	if artifact != "" {
		j.bindVerification(t, j.observeRun(t, "q-delivery"))
		if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"}, f.owner); err != nil {
			t.Fatalf("record candidate delivery: %v", err)
		}
	}
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := f.store.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, f.store, workID, verdictStep, actor); err != nil {
		t.Fatal(err)
	}
	fixture := acceptanceRecoveryFixture{store: f.store, workID: workID, owner: f.owner, operator: f.operator, repo: repo, receipt: run.OperationRef}
	if associate {
		if err := runVerdictActionAs(t, f.store, workID, "record_verdict", qAssociationPayload(t, fixture), 0, f.owner); err != nil {
			t.Fatalf("associate captured current proof: %v", err)
		}
	}
	if len(investigation) == 0 || investigation[0] {
		seedPremiseDeliverablesInvestigation(t, f.store, workID)
	}
	return fixture
}

// qAssociationPayload associates the fixture's captured receipt with every
// current Review criterion.
func qAssociationPayload(t *testing.T, f acceptanceRecoveryFixture) json.RawMessage {
	t.Helper()
	dependencies, err := workflowReviewDependencies(t.Context(), f.store.db, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, criterion := range dependencies.Criteria {
		ids = append(ids, criterion.PredicateID)
	}
	return criterionAssociationPayload(f.receipt, ids...)
}

func qAcceptCriterionReview(t *testing.T, f workContextFixture, repo, receipt string) {
	t.Helper()
	completeCurrentReviewForTest(t, f, repo, "ready_for_final_proof", func(report *ReviewBlock) {
		for _, criterion := range report.Dependencies.Criteria {
			report.Assessments = append(report.Assessments, ReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{receipt}})
		}
	})
}

func TestQAssociateCurrentProofKeepsSeededContract(t *testing.T) {
	t.Parallel()
	const workID = "q-seeded-association"
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
	s := route.store
	var before string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowContractApproved).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f := qAssociateCurrentProof(t, workContextFixture{store: s, workID: workID, owner: route.owner, operator: route.operator}, "", true)
	if f.store != s || f.receipt == "" {
		t.Fatal("association replaced the seeded store or returned no receipt")
	}
	var after string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowContractApproved).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("association changed the seeded contract assertion")
	}
	var contracts, associations int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&associations); err != nil {
		t.Fatal(err)
	}
	if contracts != 1 || associations != 1 {
		t.Fatalf("seeded contract count=%d, proof association count=%d, want one each", contracts, associations)
	}
}

func TestQAssociateCurrentProofCanLeaveInvestigationMissing(t *testing.T) {
	t.Parallel()
	const workID = "q-uninvestigated-association"
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
	f := qAssociateCurrentProof(t, workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}, "", true, false)
	err = WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: "confirm_premise", Actor: f.owner,
		Payload: json.RawMessage(`{}`),
	})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "investigation") {
		t.Fatalf("confirmation without an investigation = %v, want missing_evidence naming investigation", err)
	}
}

// Contract-notice folds do not evaluate or complete a candidate.
func qContractRevisionFixture(t *testing.T, workID string) (*Store, string) {
	t.Helper()
	s, owner, version := issue31ActorSetup(t, workID)
	seedWorkflowLaw(t, s)
	for _, action := range []string{"record_proposal", "record_discovery", "record_design", "approve_contract"} {
		version = issue31WorkflowAction(t, s, workID, version, action, workID+":"+action, owner)
	}
	return s, mustActorRef(t, owner)
}
