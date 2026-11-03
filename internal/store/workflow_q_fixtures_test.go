package store

import (
	"encoding/json"
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
	return qBuildAcceptancePredicates(t, workID, artifact, associate, extraRequiredKinds, true)
}

func qBuildAcceptancePredicates(t *testing.T, workID, artifact string, associate bool, extraRequiredKinds []string, confirm bool, predicates ...map[string]any) acceptanceRecoveryFixture {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", append([]string{"verification"}, extraRequiredKinds...), []string{"verification", "review", "artifact"}, predicates...)
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	return qAssociateCurrentProofAt(t, f, "review", artifact, associate, confirm && len(extraRequiredKinds) == 0)
}

// qAssociateCurrentProof keeps the seeded store and contract. The item must
// declare the current Review and criterion-association routes. It adds a
// clean candidate repository before capturing the command receipt.
func qAssociateCurrentProof(t *testing.T, f workContextFixture, artifact string, associate bool) acceptanceRecoveryFixture {
	t.Helper()
	return qAssociateCurrentProofAt(t, f, "review", artifact, associate)
}

// qAssociateCurrentProofAt associates the proof at the family's named
// verdict step.
func qAssociateCurrentProofAt(t *testing.T, f workContextFixture, verdictStep, artifact string, associate bool, confirm ...bool) acceptanceRecoveryFixture {
	t.Helper()
	workID := f.workID
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}
	if artifact != "" {
		if err := j.action(t, "start_execution", map[string]any{}, f.owner); err != nil {
			t.Fatalf("start candidate production: %v", err)
		}
	}
	run := j.observeRun(t, "q-proof")
	j.bindVerification(t, run)
	qAcceptCriterionReview(t, f, run.OperationRef)
	moveWorkflowPlanFixtureFrontier(t, f.store, workID, verdictStep)
	if artifact != "" {
		j.bindVerification(t, j.observeRun(t, "q-delivery"))
		if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"}, f.owner); err != nil {
			t.Fatalf("record candidate delivery: %v", err)
		}
	}
	fixture := acceptanceRecoveryFixture{store: f.store, workID: workID, owner: f.owner, operator: f.operator, repo: repo, receipt: run.OperationRef}
	if associate {
		if err := runVerdictActionAs(t, f.store, workID, "record_verdict", qAssociationPayload(t, fixture), 0, f.owner); err != nil {
			t.Fatalf("associate captured current proof: %v", err)
		}
		if len(confirm) == 0 || confirm[0] {
			if err := runVerdictActionAs(t, f.store, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
				t.Fatalf("confirm the fixture premise: %v", err)
			}
		}
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

func qAcceptCriterionReview(t *testing.T, f workContextFixture, receipt string) {
	t.Helper()
	completeCurrentReviewForTest(t, f, "ready_for_final_proof", func(report *ReviewBlock) {
		for _, criterion := range report.Dependencies.Criteria {
			report.Assessments = append(report.Assessments, ReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{receipt}})
		}
	})
}

func TestQAssociateCurrentProofKeepsSeededContract(t *testing.T) {
	t.Parallel()
	const workID = "q-seeded-association"
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", []string{"verification"}, []string{"verification", "review", "artifact"})
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

// Contract-notice folds do not evaluate or complete a candidate.
func qContractRevisionFixture(t *testing.T, workID string) (*Store, string) {
	t.Helper()
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "plan", []string{}, []string{})
	return route.store, mustActorRef(t, route.owner)
}
